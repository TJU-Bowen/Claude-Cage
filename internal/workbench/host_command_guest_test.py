"""Exercise the Linux guest adapter with disposable config and a fake Windows peer."""
import json
import os
from pathlib import Path
import select
import subprocess
import sys
import tempfile
import unittest


RUNNER = Path(__file__).with_name("host_command_guest.py").resolve()


class HostCommands(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="cage-guest-")
        self.addCleanup(temporary.cleanup)
        self.home = Path(temporary.name)
        self.environment = {**os.environ, "HOME": str(self.home)}
        self.config = self.home / ".config/Claude/claude_desktop_config.json"

    def setup_config(self):
        return subprocess.run([sys.executable, "-I", str(RUNNER), "setup"], env=self.environment,
                              capture_output=True, timeout=5)

    def test_registration(self):
        self.config.parent.mkdir(parents=True)
        original = {"preferences": {"theme": "light"}, "mcpServers": {"existing": {"command": "existing"}}}
        self.config.write_text(json.dumps(original))
        self.assertEqual(self.setup_config().returncode, 0)
        saved = self.config.read_bytes()
        edited = json.loads(saved)
        entry = edited["mcpServers"].pop("cage-host")
        self.assertEqual(edited, original)
        self.assertEqual(entry["args"], ["-I", str(RUNNER), "mcp"])
        self.assertEqual(self.setup_config().returncode, 0)
        self.assertEqual(self.config.read_bytes(), saved)
        self.assertEqual(self.config.stat().st_mode & 0o777, 0o600)

    def test_invalid_or_conflicting_settings_preserved(self):
        self.config.parent.mkdir(parents=True)
        for original in ('invalid', 'null', '{"mcpServers":{"cage-host":{"command":"other"}}}'):
            with self.subTest(original=original):
                self.config.write_text(original)
                self.assertNotEqual(self.setup_config().returncode, 0)
                self.assertEqual(self.config.read_text(), original)

    def start(self, mode):
        proc = subprocess.Popen([sys.executable, "-I", str(RUNNER), mode], env=self.environment,
                                stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        def finish():
            if proc.poll() is None:
                proc.kill()
            proc.wait(timeout=5)
            for stream in (proc.stdin, proc.stdout, proc.stderr):
                stream.close()
        self.addCleanup(finish)
        return proc

    def read(self, proc):
        self.assertTrue(select.select([proc.stdout], [], [], 5)[0], "missing protocol response")
        line = proc.stdout.readline()
        self.assertTrue(line, "unexpected end of protocol stream")
        return json.loads(line)

    def send(self, proc, message):
        proc.stdin.write(json.dumps(message).encode() + b"\n")
        proc.stdin.flush()

    def call(self, proc, cwd=r"C:\Projects", request_id=1):
        self.send(proc, {"jsonrpc": "2.0", "id": request_id, "method": "tools/call",
                         "params": {"name": "run_host_command", "arguments":
                                    {"cwd": cwd, "command": "Write-Output '测试'", "timeout_seconds": 60}}})

    def test_protocol_and_offline(self):
        proc = self.start("mcp")
        self.send(proc, {"jsonrpc": "2.0", "id": 1, "method": "initialize",
                         "params": {"protocolVersion": "2025-06-18"}})
        self.assertEqual(self.read(proc)["result"]["protocolVersion"], "2025-06-18")
        self.send(proc, {"jsonrpc": "2.0", "method": "notifications/initialized"})
        self.send(proc, {"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
        self.assertEqual(self.read(proc)["result"]["tools"][0]["name"], "run_host_command")
        self.call(proc, request_id=3)
        self.assertIn("offline", self.read(proc)["result"]["content"][0]["text"])
        self.call(proc, cwd="project-relative")
        self.assertIn("absolute Windows path", self.read(proc)["result"]["content"][0]["text"])

    def test_independent_calls_then_disconnect(self):
        bridge = self.start("bridge")
        self.assertEqual(self.read(bridge), {"ready": 1})
        path = self.home / ".local/state/cage-host-command/command.sock"
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        clients = [self.start("mcp"), self.start("mcp")]
        pending = []
        for index, client in enumerate(clients):
            self.call(client, request_id=index)
            frame = self.read(bridge)
            self.assertEqual(frame["command"], "Write-Output '测试'")
            pending.append((frame["id"], index))
        self.assertNotEqual(pending[0][0], pending[1][0])
        for identifier, index in reversed(pending):
            self.send(bridge, {"id": identifier, "exit_code": index, "stdout": f"✅ {index}",
                               "stderr": "", "truncated": False})
        for index, client in enumerate(clients):
            reply = self.read(client)
            self.assertEqual(reply["id"], index)
            self.assertEqual(reply["result"]["isError"], bool(index))
            output = json.loads(reply["result"]["content"][0]["text"])
            self.assertEqual(output["stdout"], f"✅ {index}")
            self.assertNotIn("id", output)
        self.call(clients[0])
        self.read(bridge)
        bridge.stdin.close()
        self.assertEqual(bridge.wait(timeout=5), 0)
        self.assertTrue(self.read(clients[0])["result"]["isError"])
        self.assertFalse(path.exists())
        replacement = self.start("bridge")
        self.assertEqual(self.read(replacement), {"ready": 1})


if __name__ == "__main__":
    unittest.main()
