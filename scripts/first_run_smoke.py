#!/usr/bin/env python3
"""First-run and worker-creation acceptance for the rail/canvas model.

Run after building: python3 scripts/first_run_smoke.py --binary bin/wtable
Uses only local fixtures and an isolated WTABLE_HOME.
"""
import argparse
import fcntl
import json
import os
from pathlib import Path
import pty
import shutil
import struct
import subprocess
import sys
import tempfile
import termios
import threading
import time

from p1_smoke import FRAME_HELPER
from smoke import RPC, wait_until


class Session:
    def __init__(self, binary, script_dir, presets):
        self.root = Path(tempfile.mkdtemp(prefix="wt-first-", dir="/tmp"))
        self.home = self.root / "state"
        self.home.mkdir()
        self.empty_path = self.root / "empty-path"
        self.empty_path.mkdir()
        (self.home / "presets.json").write_text(json.dumps(presets))
        self.env = dict(os.environ, WTABLE_HOME=str(self.home), TERM="xterm-256color", PATH=str(self.empty_path))
        self.log = (self.root / "daemon.log").open("wb")
        self.daemon = subprocess.Popen([binary, "daemon"], env=self.env, stdout=self.log, stderr=self.log)
        self.rpc = wait_until(self.connect, "daemon")
        frame_src = self.root / "frame.go"
        frame_src.write_text(FRAME_HELPER)
        self.frame_bin = self.root / "frame"
        subprocess.run(["go", "build", "-o", str(self.frame_bin), str(frame_src)], check=True, capture_output=True)
        self.transcript = bytearray()
        self.master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))

        def controlling_tty():
            os.setsid()
            fcntl.ioctl(0, termios.TIOCSCTTY, 0)
        self.tui = subprocess.Popen([binary], cwd=self.root, env=self.env, stdin=slave, stdout=slave, stderr=slave, preexec_fn=controlling_tty)
        os.close(slave)

        def drain():
            try:
                while True:
                    self.transcript.extend(os.read(self.master, 65536))
            except OSError:
                pass
        threading.Thread(target=drain, daemon=True).start()

    def connect(self):
        try:
            return RPC(self.home, (self.home / "controller.token").read_text().strip())
        except (FileNotFoundError, ConnectionRefusedError):
            return None

    def screen(self):
        return subprocess.check_output([str(self.frame_bin)], input=bytes(self.transcript)).decode()

    def shown(self, text):
        return text in self.screen()

    def choose_workspace(self):
        wait_until(lambda: self.shown("选择工作区"), "workspace picker")
        assert self.shown("当前目录"), self.screen()
        self.send(b"\r")

    def send(self, data):
        os.write(self.master, data.encode() if isinstance(data, str) else data)
        time.sleep(.15)

    def prefix(self, key):
        self.send(b"\x01" + key.encode())

    def panes(self):
        return self.rpc.call("state.get", {"frames": False})["panes"]

    def close(self):
        try:
            if self.tui.poll() is None:
                self.prefix("q")
                self.tui.wait(timeout=5)
        except Exception:
            self.tui.terminate()
        try:
            self.rpc.call("shutdown")
        except Exception:
            pass
        self.rpc.close()
        try:
            self.daemon.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.daemon.terminate()
        os.close(self.master)
        self.log.close()
        shutil.rmtree(self.root)


def run(args):
    binary = str(Path(args.binary).resolve())
    script_dir = Path(__file__).resolve().parent
    output = Path(args.output).resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    report = {"status": "running", "provider_calls": 0, "checks": []}

    def record(name):
        report["checks"].append({"name": name, "passed": True})
        print("PASS", name, flush=True)

    session = None
    fixture_root = None
    try:
        fixture_root = Path(tempfile.mkdtemp(prefix="wt-acp-root-", dir="/tmp"))
        acp = {"id": "acp-fixture", "name": "ACP fixture", "backend": "acp",
               "command": [sys.executable, str(script_dir / "fixtures/acp_agent.py"), "--root", str(fixture_root)]}
        pty_preset = {"id": "pty-fixture", "name": "PTY fixture", "backend": "pty", "command": ["/bin/cat"]}
        session = Session(binary, script_dir, [pty_preset, acp])
        session.choose_workspace()

        wait_until(lambda: session.shown("新建前台 agent"), "first-run picker")
        assert session.shown("PTY fixture"), session.screen()
        assert not session.shown("ACP fixture"), "ACP must not appear in the foreground preset list"
        session.send(b"\r")
        pane = wait_until(lambda: next((p for p in session.panes() if p["state"] == "running"), None), "foreground agent")
        assert pane["kind"] == "foreground" and pane["backend"] == "pty", pane
        assert pane["name"] == "pty-fixture", pane
        assert pane.get("workspace") == str(session.root.resolve()), pane
        assert len(session.panes()) == 1
        record("first_run_creates_one_pty_foreground_with_a_single_enter_and_hides_acp")

        session.prefix(" ")
        wait_until(lambda: session.shown("命令面板"), "command palette")
        session.send("新建 worker")
        session.send(b"\r")
        wait_until(lambda: session.shown("新建 worker"), "worker picker")
        assert session.shown("PTY fixture"), session.screen()
        assert not session.shown("ACP fixture"), "ACP must not appear in the worker preset list"
        session.send(b"\x1b[B")  # shell, pty fixture
        session.send(b"\r")
        worker = wait_until(lambda: next((p for p in session.panes()
            if p.get("preset") == "pty-fixture" and p["kind"] == "worker"), None), "PTY worker")
        wait_until(lambda: next(p for p in session.panes() if p["id"] == worker["id"])["state"] == "running", "PTY worker running")
        assert worker["kind"] == "worker" and worker["backend"] == "pty", worker
        assert next(p for p in session.panes() if p["id"] == pane["id"])["kind"] == "foreground"
        record("worker_flow_hides_acp_and_does_not_replace_the_foreground")
        session.close()
        session = None

        broken = {"id": "broken-pty", "name": "Broken PTY", "backend": "pty", "command": ["/nonexistent/agent"]}
        session = Session(binary, script_dir, [broken, acp])
        session.choose_workspace()
        wait_until(lambda: session.shown("新建前台 agent"), "fallback picker")
        wait_until(lambda: session.shown("重新检测") and session.shown("创建 shell"), "fallback actions")
        session.send("s")
        pane = wait_until(lambda: next((p for p in session.panes() if p["state"] == "running"), None), "temporary shell")
        assert pane["kind"] == "foreground" and pane["backend"] == "pty", pane
        record("missing_pty_presets_offer_recheck_and_temporary_shell")
        report["status"] = "passed"
    except Exception as exc:
        report["status"] = "failed"
        report["error"] = repr(exc)
        raise
    finally:
        if session:
            session.close()
        if fixture_root:
            shutil.rmtree(fixture_root, ignore_errors=True)
        output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default="bin/wtable")
    parser.add_argument("--output", default=".dev/test-results/first-run.json")
    run(parser.parse_args())
