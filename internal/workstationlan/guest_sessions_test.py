import fcntl
import json
from pathlib import Path
import tempfile
import unittest
from uuid import uuid4

import guest_sessions as storage


class GuestSessionsTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        storage.HOME = Path(temp.name)
        storage.WORK = storage.HOME / ".cc-remote"
        storage.CONFIG = storage.HOME / ".claude"
        self.device = "a" * 64
        self.query = dict(device=self.device, name="Workstation", action="list", receipts=[])

    def transcript(self, owner, project="C/Project", id=None):
        id = id or str(uuid4())
        proof = str(uuid4())
        cwd = storage.WORK / "ws" / owner / project
        folder = storage.CONFIG / "projects" / storage.re.sub(r"[^a-zA-Z0-9]", "-", str(cwd))
        folder.mkdir(parents=True, exist_ok=True)
        file = folder / (id + ".jsonl")
        file.write_text(json.dumps(dict(type="user", uuid=proof, cwd=str(cwd))) + "\n")
        return file, proof

    def test_ownership_batch_busy_retry(self):
        ours, proof = self.transcript("Workstation")
        hidden, _ = self.transcript("Workstation")
        scoped, _ = self.transcript(self.device)
        busy, _ = self.transcript(self.device)
        foreign, _ = self.transcript("b" * 64)
        self.query["receipts"] = [dict(id=ours.stem, cwd="C:\\Project", proof=proof, title="Old", archived=True),
                                  dict(id=hidden.stem, cwd="C:\\Project", proof=proof)]
        sidecar = scoped.parent / scoped.stem / "subagents" / "agent.jsonl"
        sidecar.parent.mkdir(parents=True)
        sidecar.write_bytes(b"a" * 512)
        backup = storage.CONFIG / "file-history" / ours.stem / "snapshot"
        backup.parent.mkdir(parents=True)
        backup.write_bytes(b"b" * 512)
        rows = {r["id"]: r for r in storage.main(self.query)["sessions"]}
        self.assertEqual(set(rows), {ours.stem, scoped.stem, busy.stem})
        self.assertTrue(rows[ours.stem]["archived"])
        lockdir = storage.managed_dir("locks", self.device)
        with (lockdir / busy.stem).open("w") as lock:
            fcntl.flock(lock, fcntl.LOCK_SH)
            reply = storage.main(dict(self.query, action="delete", ids=[ours.stem, scoped.stem, busy.stem, foreign.stem]))
        self.assertEqual(sum(r["bytes"] for r in reply["results"]), rows[ours.stem]["bytes"] + rows[scoped.stem]["bytes"])
        self.assertTrue(all("error" in r for r in reply["results"][2:]))
        self.assertTrue(busy.exists() and hidden.exists() and foreign.exists())
        self.assertFalse(ours.exists() or scoped.exists() or backup.exists() or sidecar.exists())
        again = storage.main(dict(self.query, action="delete", ids=[ours.stem]))
        self.assertEqual(again["results"], [dict(id=ours.stem, bytes=0)])

    def test_links_and_duplicate_ids(self):
        ours, _ = self.transcript(self.device)
        other, _ = self.transcript("b" * 64, id=ours.stem)
        result = storage.main(dict(self.query, action="delete", ids=[ours.stem]))
        self.assertIn("error", result["results"][0])
        other.unlink()
        dest = storage.HOME / "outside"
        dest.mkdir()
        (dest / "keep").touch()
        (ours.parent / ours.stem).symlink_to(dest, target_is_directory=True)
        safe, _ = self.transcript(self.device)
        self.assertEqual(len(storage.main(self.query)["sessions"]), 2)
        reply = storage.main(dict(self.query, action="delete", ids=[ours.stem, safe.stem]))
        self.assertIn("error", reply["results"][0])
        self.assertFalse(safe.exists())
        self.assertTrue((dest / "keep").exists() and ours.exists())


if __name__ == "__main__":
    unittest.main()
