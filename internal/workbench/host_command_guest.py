"""Local Claude MCP adapter and SSH stdio relay for Cage's Windows host commands."""
import asyncio
import fcntl
import json
import os
from pathlib import Path, PureWindowsPath
import socket
import sys
import tempfile
import uuid

SERVER = "cage-host"
TOOL = "run_host_command"
PROTOCOLS = ("2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05")
REQUEST_LIMIT = 128 * 1024
RESPONSE_LIMIT = 8 * 1024 * 1024
MAX_TIMEOUT = 3600
DEFAULT_TIMEOUT = 300
TOOL_SCHEMA = {
    "name": TOOL,
    "description": (
        "Execute a non-interactive Windows PowerShell command on the Windows host running Cage, "
        "in its real user and network environment. Use an absolute Windows working directory. "
        "For native executables use 'exit $LASTEXITCODE' when the exact program exit code is needed."
    ),
    "inputSchema": {
        "type": "object",
        "properties": {
            "command": {"type": "string", "description": "Windows PowerShell command or script."},
            "cwd": {"type": "string", "description": "Absolute host Windows directory, e.g. C:\\Projects."},
            "timeout_seconds": {"type": "integer", "minimum": 1, "maximum": MAX_TIMEOUT, "default": DEFAULT_TIMEOUT},
        },
        "required": ["command", "cwd"],
        "additionalProperties": False,
    },
}


def wire(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8") + b"\n"


def private_dir():
    home = Path.home()
    directory = home / ".local/state/cage-host-command"
    if directory.resolve() != directory:
        raise ValueError("Cage guest state must be stored in the guest home directory")
    directory.mkdir(parents=True, mode=0o700, exist_ok=True)
    info = directory.stat()
    if info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError("Cage guest state directory must be private (mode 0700)")
    return directory


def setup():
    home = Path.home()
    config = home / ".config/Claude/claude_desktop_config.json"
    if config.resolve() != config:
        raise ValueError("Claude Desktop settings contain a symbolic link")
    config.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    entry = {"command": "/usr/bin/python3", "args": ["-I", str(Path(__file__).resolve()), "mcp"]}
    with (private_dir() / "setup.lock").open("a") as guard:
        fcntl.flock(guard, fcntl.LOCK_EX)
        previous = config.read_bytes() if config.exists() else None
        settings = json.loads(previous) if previous is not None else {}
        if not isinstance(settings, dict):
            raise ValueError("Invalid Claude Desktop settings")
        servers = settings.setdefault("mcpServers", {})
        if not isinstance(servers, dict):
            raise ValueError("Invalid Claude Desktop mcpServers settings")
        if SERVER in servers:
            if servers[SERVER] != entry:
                raise ValueError("Existing 'cage-host' MCP entry belongs to another program")
            return
        servers[SERVER] = entry
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=config.parent,
                                         prefix=".cage-host-", delete=False) as tmp:
            temp_path = Path(tmp.name)
            try:
                json.dump(settings, tmp, ensure_ascii=False, indent=2)
                tmp.write("\n")
                tmp.flush()
                os.fsync(tmp.fileno())
                if (config.read_bytes() if config.exists() else None) != previous:
                    raise ValueError("Claude Desktop settings changed during registration. Retry.")
                os.replace(temp_path, config)
            finally:
                temp_path.unlink(missing_ok=True)


def validate(arguments):
    if not isinstance(arguments, dict) or arguments.keys() - {"command", "cwd", "timeout_seconds"}:
        raise ValueError("Expected command, cwd and optional timeout_seconds")
    command, cwd = arguments.get("command"), arguments.get("cwd")
    timeout = arguments.get("timeout_seconds", DEFAULT_TIMEOUT)
    if (not isinstance(command, str) or not command.strip() or "\0" in command
            or len(command.encode("utf-8")) > 64 * 1024):
        raise ValueError("command must be a non-empty Windows PowerShell script of up to 64 KiB")
    if not isinstance(cwd, str) or "\0" in cwd or not PureWindowsPath(cwd).is_absolute():
        raise ValueError("cwd must be an absolute Windows path such as C:\\Projects")
    if type(timeout) is not int or not 1 <= timeout <= MAX_TIMEOUT:
        raise ValueError(f"timeout_seconds must be an integer between 1 and {MAX_TIMEOUT}")
    return {"command": command, "cwd": cwd, "timeout_seconds": timeout}


async def bridge():
    directory = private_dir()
    endpoint = directory / "command.sock"
    waiting = {}
    clients = set()
    with (directory / "bridge.lock").open("a") as guard:
        fcntl.flock(guard, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if endpoint.is_socket():
            endpoint.unlink()
        async def forward(reader, writer):
            identifier = uuid.uuid4().hex
            clients.add(writer)
            try:
                raw = await asyncio.wait_for(reader.readline(), 10)
                if not raw.endswith(b"\n") or len(raw) > REQUEST_LIMIT:
                    raise ValueError("Invalid host command frame")
                arguments = validate(json.loads(raw))
                if len(waiting) >= 8:
                    raise ValueError("Cage is already running eight host commands")
                reply = asyncio.get_running_loop().create_future()
                waiting[identifier] = reply
                frame = wire({"id": identifier, **arguments})
                if len(frame) > REQUEST_LIMIT:
                    raise ValueError("Encoded request exceeds the host command limit")
                sys.stdout.buffer.write(frame)
                sys.stdout.buffer.flush()
                result = await asyncio.wait_for(reply, arguments["timeout_seconds"] + 15)
                writer.write(result)
            except (ValueError, OSError, asyncio.TimeoutError) as exc:
                writer.write(wire({"error": str(exc) or "Timed out waiting for the host command"}))
            finally:
                waiting.pop(identifier, None)
                try:
                    await writer.drain()
                except (ConnectionError, OSError):
                    pass
                writer.close()
                clients.discard(writer)

        async with await asyncio.start_unix_server(forward, path=endpoint, limit=REQUEST_LIMIT):
            try:
                os.chmod(endpoint, 0o600)
                host_reader = asyncio.StreamReader(limit=RESPONSE_LIMIT)
                transport, _ = await asyncio.get_running_loop().connect_read_pipe(
                    lambda: asyncio.StreamReaderProtocol(host_reader), sys.stdin.buffer)
                try:
                    sys.stdout.buffer.write(wire({"ready": 1}))
                    sys.stdout.buffer.flush()
                    while raw := await host_reader.readline():
                        response = json.loads(raw)
                        reply = waiting.get(response["id"])
                        if reply is not None and not reply.done():
                            reply.set_result(raw)
                finally:
                    transport.close()
            finally:
                endpoint.unlink(missing_ok=True)
                for reply in waiting.values():
                    reply.cancel()
                for writer in clients:
                    writer.close()


def tool_result(result):
    return {"content": [{"type": "text", "text": json.dumps(result, ensure_ascii=False)}],
            "isError": bool(result.get("error") or result.get("exit_code", 0) != 0)}


def run_command(params):
    if not isinstance(params, dict) or params.get("name") != TOOL:
        raise ValueError("Unknown Cage tool")
    try:
        arguments = validate(params.get("arguments"))
        frame = wire(arguments)
        if len(frame) > REQUEST_LIMIT:
            raise ValueError("Encoded request exceeds the host command limit")
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
            client.settimeout(arguments["timeout_seconds"] + 20)
            client.connect(str(private_dir() / "command.sock"))
            client.sendall(frame)
            with client.makefile("rb") as stream:
                line = stream.readline(RESPONSE_LIMIT + 1)
        if not line.endswith(b"\n") or len(line) > RESPONSE_LIMIT:
            raise ValueError("Windows host disconnected. Check the command outcome before retrying.")
        result = json.loads(line)
        if not isinstance(result, dict):
            raise ValueError("Invalid response from Windows host")
        result.pop("id", None)
        return tool_result(result)
    except (FileNotFoundError, ConnectionRefusedError):
        return tool_result({"error": "Host commands are offline. Enable host commands in Cage on Windows."})
    except (OSError, ValueError) as exc:
        return tool_result({"error": str(exc)})


def handle(request):
    if not isinstance(request, dict) or request.get("jsonrpc") != "2.0" or not isinstance(request.get("method"), str):
        return {"jsonrpc": "2.0", "id": None, "error": {"code": -32600, "message": "Invalid Request"}}
    if "id" not in request:
        return None
    response = {"jsonrpc": "2.0", "id": request["id"]}
    params = request.get("params", {})
    method = request["method"]
    try:
        if method == "initialize":
            version = params.get("protocolVersion") if isinstance(params, dict) else None
            response["result"] = {"protocolVersion": version if version in PROTOCOLS else PROTOCOLS[0],
                                  "capabilities": {"tools": {}},
                                  "serverInfo": {"name": SERVER, "version": "1.0.0"}}
        elif method == "ping":
            response["result"] = {}
        elif method == "tools/list":
            response["result"] = {"tools": [TOOL_SCHEMA]}
        elif method == "tools/call":
            response["result"] = run_command(params)
        else:
            response["error"] = {"code": -32601, "message": "Method not found"}
    except ValueError as exc:
        response["error"] = {"code": -32602, "message": str(exc)}
    return response


def mcp():
    for raw in sys.stdin.buffer:
        try:
            response = handle(json.loads(raw))
        except (ValueError, UnicodeError):
            response = {"jsonrpc": "2.0", "id": None, "error": {"code": -32700, "message": "Parse error"}}
        if response is not None:
            sys.stdout.buffer.write(wire(response))
            sys.stdout.buffer.flush()


if __name__ == "__main__":
    os.umask(0o077)
    mode = sys.argv[1] if len(sys.argv) == 2 else ""
    if mode == "mcp":
        mcp()
    elif mode == "setup":
        setup()
    elif mode == "bridge":
        asyncio.run(bridge())
    else:
        sys.exit("Expected setup, bridge, or mcp")
