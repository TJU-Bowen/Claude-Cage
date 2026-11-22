"""Inventory and remove a paired workstation's Claude files. Request and reply are JSON on stdio."""
import collections
import fcntl
import json
import os
from pathlib import Path
import re
import shutil
import stat
import sys

UUID = re.compile(r"[0-9a-fA-F]{8}-(?:[0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}\Z")
DEVICE = re.compile(r"[0-9a-f]{64}\Z")
HOME = Path.home()
CONFIG = HOME / ".claude"
WORK = HOME / ".cc-remote"


def plain(path, mode):
    try:
        return mode(path.lstat().st_mode)
    except FileNotFoundError:
        return False


def bounded_rows(file):
    # Large tool results can occupy megabytes on a single line. Read just the two ends for metadata.
    with file.open("rb") as stream:
        head = stream.read(256 * 1024)
        regions = [head]
        size = file.stat().st_size
        if size > len(head):
            stream.seek(max(len(head), size - 128 * 1024))
            regions.append(stream.read().partition(b"\n")[2])
        for region in regions:
            for line in region.splitlines():
                try:
                    row = json.loads(line)
                    if isinstance(row, dict):
                        yield row
                except (ValueError, UnicodeError):
                    continue


def legacy_proof(file, receipt, expected):
    proof = receipt.get("proof", "")
    if not UUID.fullmatch(proof):
        return False
    # Compare a message present in the workstation mirror, including its unabridged guest cwd.
    with file.open("rb") as stream:
        for line in stream:
            if proof.encode("ascii") not in line:
                continue
            try:
                row = json.loads(line)
            except (ValueError, UnicodeError):
                continue
            if (isinstance(row, dict) and row.get("type") in ("user", "assistant") and
                    row.get("uuid") == proof and row.get("cwd") == str(expected)):
                return True
    return False


def windows_project(cwd):
    cwd = cwd.replace("\\", "/")
    if re.match(r"^[A-Za-z]:/", cwd):
        cwd = "/" + cwd[0] + cwd[2:]
    if not cwd.startswith("/") or ".." in Path(cwd).parts:
        return None
    return cwd.lstrip("/")


def active_sessions():
    active = set()
    folder = CONFIG / "sessions"
    if plain(folder, stat.S_ISDIR):
        for file in folder.glob("*.json"):
            if not file.stem.isdigit() or not plain(file, stat.S_ISREG):
                continue
            try:
                record = json.loads(file.read_bytes())
                pid = int(file.stem)
                if record.get("pid", pid) != pid or pid <= 1:
                    continue
                os.kill(pid, 0)
                active.add(record.get("sessionId"))
            except (OSError, ValueError, AttributeError, TypeError):
                continue
    return active


def owned_parts(file, shared_id=False):
    parts = [file, file.parent / file.stem]
    if not shared_id:
        for folder in ("file-history", "session-env", "tasks"):
            parent = CONFIG / folder
            if plain(parent, stat.S_ISDIR):
                parts.append(parent / file.stem)
    for path in parts:
        if path.is_symlink() or not path.resolve().is_relative_to(CONFIG):
            raise ValueError("会话路径包含链接，已停止清理")
    return parts


def used_bytes(path):
    if plain(path, stat.S_ISREG):
        return path.stat().st_size
    if plain(path, stat.S_ISDIR):
        return sum(used_bytes(entry) for entry in path.iterdir())
    return 0


def list_sessions(request):
    device, name = request["device"], request["name"]
    if not DEVICE.fullmatch(device):
        raise ValueError("工作机身份无效")
    project_root = CONFIG / "projects"
    if not plain(CONFIG, stat.S_ISDIR) or not plain(project_root, stat.S_ISDIR):
        raise ValueError("尚未找到客机 Claude 会话目录")
    receipts = {entry["id"]: entry for entry in request.get("receipts") or []}
    owners = WORK / "ws"
    scoped = owners / device
    safe_name = bool(name and name not in (".", "..") and "/" not in name and "\\" not in name)
    legacy = owners / name if safe_name else None
    key = lambda value: re.sub(r"[^a-zA-Z0-9]", "-", str(value))
    prefixes = (key(scoped) + "-", key(legacy) + "-" if legacy else "\0")
    found, counts = {}, collections.Counter()
    live = active_sessions()
    selected = set(request.get("ids") or [])
    for folder in project_root.iterdir():
        if not plain(folder, stat.S_ISDIR):
            continue
        for file in folder.glob("*.jsonl"):
            sid = file.stem
            if not UUID.fullmatch(sid) or not plain(file, stat.S_ISREG):
                continue
            counts[sid] += 1
            if selected and sid not in selected or not folder.name.startswith(prefixes):
                continue
            receipt = receipts.get(sid, {})
            new = folder.name.startswith(prefixes[0])
            if not new:
                project = windows_project(receipt.get("cwd", ""))
                if not project or not legacy_proof(file, receipt, legacy / project):
                    continue
            recent = list(bounded_rows(file))
            cwd = next((r["cwd"] for r in recent if isinstance(r.get("cwd"), str)), "")
            if new and cwd and not Path(cwd).is_relative_to(scoped):
                continue
            project = receipt.get("cwd") or (str(Path(cwd).relative_to(scoped)) if new and cwd else "")
            title = receipt.get("title") or next((r.get("customTitle") or r.get("aiTitle") for r in reversed(recent)
                                                if r.get("customTitle") or r.get("aiTitle")), "") or sid
            row = dict(id=sid, title=str(title)[:250], project=project, updated=file.stat().st_mtime,
                       running=sid in live, archived=bool(receipt.get("archived")))
            found[sid] = (row, file)
    for sid, (row, file) in found.items():
        row["ambiguous"] = counts[sid] > 1
        try:
            row["bytes"] = sum(used_bytes(p) for p in owned_parts(file, row["ambiguous"]))
        except (OSError, ValueError) as err:
            row["bytes"], row["blocked"] = 0, str(err)
    return found


def managed_dir(branch, device):
    path = WORK
    for part in ("", branch, device):
        if part:
            path = path / part
        path.mkdir(mode=0o700, exist_ok=True)
        if not plain(path, stat.S_ISDIR) or not path.resolve().is_relative_to(HOME):
            raise ValueError("客机会话管理目录无效")
    return path


def purge(request, found):
    device = request["device"]
    locks, tombs = managed_dir("locks", device), managed_dir("deleted", device)
    results = []
    for sid in dict.fromkeys(request["ids"]):
        result = {"id": sid, "bytes": 0}
        try:
            if not UUID.fullmatch(sid):
                raise ValueError("会话标识无效")
            with os.fdopen(os.open(locks / sid, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600), "r+b") as lock:
                try:
                    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                except BlockingIOError:
                    raise ValueError("会话正在运行，请结束后重试")
                if sid not in found:
                    if not plain(tombs / sid, stat.S_ISREG):
                        raise ValueError("未找到属于这台工作机的会话")
                else:
                    row, file = found[sid]
                    if sid in active_sessions() or row["running"]:
                        raise ValueError("会话正在运行，请结束后重试")
                    if row["ambiguous"]:
                        raise ValueError("存在重复会话标识，已跳过")
                    if row.get("blocked"):
                        raise ValueError(row["blocked"])
                    parts = owned_parts(file)
                    # The adapter checks this marker under the same lock before running or uploading a resume.
                    with os.fdopen(os.open(tombs / sid, os.O_CREAT | os.O_WRONLY | os.O_NOFOLLOW, 0o600), "wb"):
                        pass
                    for part in [*parts[1:], parts[0]]:
                        size = used_bytes(part)
                        if plain(part, stat.S_ISDIR):
                            shutil.rmtree(part)
                        elif plain(part, stat.S_ISREG):
                            part.unlink()
                        result["bytes"] += size
        except (OSError, ValueError) as err:
            result["error"] = str(err)
        results.append(result)
    return {"results": results}


def main(request):
    found = list_sessions(request)
    if request["action"] == "list":
        return {"sessions": sorted((r for r, _ in found.values()), key=lambda r: r["updated"], reverse=True)}
    if request["action"] == "delete":
        return purge(request, found)
    raise ValueError("无法识别会话操作")


if __name__ == "__main__":
    try:
        print(json.dumps(main(json.load(sys.stdin)), ensure_ascii=False))
    except (OSError, ValueError, KeyError, TypeError) as err:
        print(json.dumps({"error": str(err)}, ensure_ascii=False))
