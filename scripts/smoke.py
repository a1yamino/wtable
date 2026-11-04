#!/usr/bin/env python3
"""Exercise a real wtable daemon in an isolated temporary directory.

Run: python3 scripts/smoke.py --binary bin/wtable
Only Python's standard library is required. The --peer mode runs inside a PTY
pane and intentionally obtains that pane's credentials from its environment.
"""

import argparse
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import traceback
import sqlite3


class RPCError(Exception):
    pass


class RPC:
    def __init__(self, home, token):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(10)
        self.sock.connect(str(Path(home) / "daemon.sock"))
        self.stream = self.sock.makefile("rb")
        self.sequence = 0
        self.call("auth", {"token": token})

    def call(self, method, params=None):
        self.sequence += 1
        self.sock.sendall((json.dumps({"jsonrpc": "2.0", "id": self.sequence,
                                      "method": method, "params": params or {}}) + "\n").encode())
        line = self.stream.readline(8 << 20)
        if not line:
            raise RPCError("daemon disconnected")
        result = json.loads(line)
        if result.get("id") != self.sequence or result.get("jsonrpc") != "2.0":
            raise RPCError("invalid response envelope")
        if "error" in result:
            raise RPCError(result["error"]["message"])
        return result.get("result")

    def close(self):
        self.stream.close()
        self.sock.close()


def wait_until(check, description, timeout=12):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = check()
        if result:
            return result
        time.sleep(0.05)
    raise AssertionError("timed out: " + description)


def peer_main(inbox, outbox):
    rpc = RPC(os.environ["WTABLE_HOME"], os.environ["WTABLE_PEER_TOKEN"])
    Path(outbox).with_suffix(".pid").write_text(str(os.getpid()))
    print("PEER_READY", flush=True)
    with open(inbox) as commands, open(outbox, "a", buffering=1) as results:
        while True:
            line = commands.readline()
            if not line:
                time.sleep(0.05)
                continue
            command = json.loads(line)
            try:
                result = {"id": command["id"], "result": rpc.call(command["method"], command.get("params"))}
            except RPCError as exc:
                result = {"id": command["id"], "error": str(exc)}
            results.write(json.dumps(result, ensure_ascii=False) + "\n")


def screen_text(frame):
    return "\n".join("".join(cell.get("t", "") for cell in row) for row in frame.get("cells", []))


def run(args):
    binary = Path(args.binary).resolve()
    script = Path(__file__).resolve()
    fixture = script.parent / "fixtures" / "pty_echo.py"
    report = {"status": "running", "binary": str(binary), "provider_calls": 0,
              "assertions": [], "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}
    output = Path(args.output).resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    daemon = None
    rpc = None
    log_file = None
    root = Path(tempfile.mkdtemp(prefix="wtable-smoke-", dir="/tmp"))
    home = root / "state"
    home.mkdir()
    acp_marker = root / "acp-started"
    acp_command = [sys.executable, "-c", "from pathlib import Path; Path(" + repr(str(acp_marker)) + ").touch()"]
    (home / "presets.json").write_text(json.dumps([
        {"id": "disabled-acp", "name": "Disabled ACP", "backend": "acp", "command": acp_command},
    ]))
    started = time.monotonic()
    own_pids = set()

    def record(name, **details):
        report["assertions"].append({"name": name, "passed": True, **details})
        print("PASS " + name, flush=True)

    def connect():
        token = (home / "controller.token").read_text().strip()
        return RPC(home, token)

    def launch():
        nonlocal daemon, log_file, rpc
        log_file = (root / "daemon.log").open("ab")
        env = dict(os.environ, WTABLE_HOME=str(home))
        daemon = subprocess.Popen([str(binary), "daemon"], env=env, stdout=log_file,
                                  stderr=log_file, start_new_session=True)

        def ready():
            if daemon.poll() is not None:
                raise AssertionError("daemon exited before accepting RPC")
            try:
                return connect()
            except (FileNotFoundError, ConnectionRefusedError, OSError):
                return None

        rpc = wait_until(ready, "daemon ready")

    def spawn(name, backend, command):
        return rpc.call("pane.spawn", {"name": name, "backend": backend,
                                      "command": command, "cwd": str(root), "cols": 100, "rows": 24})

    def task(task_id):
        return rpc.call("task.get", {"id": task_id})

    def completed(task_id):
        value = wait_until(lambda: (value if (value := task(task_id))["state"] in
                                    ("completed", "failed", "unknown", "cancelled") else None), "task terminal state")
        assert value["state"] == "completed", "task did not complete: " + value["state"]
        return value

    def send(to, text, key):
        return rpc.call("task.send", {"to": to, "prompt": text, "dedupe_key": key})

    def acp_rejected(method, params):
        try:
            rpc.call(method, params)
        except RPCError as exc:
            assert "ACP 已停用" in str(exc), str(exc)
        else:
            raise AssertionError(method + " unexpectedly accepted ACP")

    try:
        if not binary.is_file():
            raise RuntimeError("binary does not exist: " + str(binary))
        launch()
        shell = spawn("persistent-shell", "pty", ["/bin/sh"])
        pid_file = root / "shell.pid"
        rpc.call("pane.input", {"id": shell["id"], "data": "echo $$ > " + str(pid_file) + "; printf 'PTY_%s\\n' FIRST_MARKER\n"})
        wait_until(lambda: "PTY_FIRST_MARKER" in screen_text(rpc.call("pane.screen", {"id": shell["id"]})), "PTY output")
        shell_pid = int(wait_until(lambda: pid_file.read_text().strip() if pid_file.exists() else None, "PTY PID"))
        own_pids.add(shell_pid)
        rpc.close()
        rpc = None
        os.kill(shell_pid, 0)
        rpc = connect()
        assert "PTY_FIRST_MARKER" in screen_text(rpc.call("pane.screen", {"id": shell["id"]}))
        rpc.call("pane.input", {"id": shell["id"], "data": "printf 'PTY_%s\\n' REATTACHED_MARKER\n"})
        wait_until(lambda: "PTY_REATTACHED_MARKER" in screen_text(rpc.call("pane.screen", {"id": shell["id"]})), "PTY reattach output")
        record("pty_survives_rpc_disconnect_and_reconnect", pane_id=shell["id"])

        panes_before = rpc.call("state.get", {"frames": False})["panes"]
        acp_rejected("pane.spawn", {"name": "direct-acp", "kind": "worker", "backend": "acp",
                                    "command": acp_command, "cwd": str(root)})
        acp_rejected("pane.spawn", {"name": "preset-acp", "kind": "worker", "preset": "disabled-acp",
                                    "cwd": str(root)})
        presets = rpc.call("preset.list")["presets"]
        assert all(p["backend"] == "pty" for p in presets)
        assert not any(p["id"] in ("disabled-acp", "codex-acp") for p in presets)
        panes_after = rpc.call("state.get", {"frames": False})["panes"]
        # Readiness is derived from elapsed time since PTY output; it can
        # settle while the rejected requests and catalog probe run.
        for pane in panes_before + panes_after:
            pane.pop("readiness", None)
            pane.pop("readiness_reason", None)
        assert panes_after == panes_before
        assert not acp_marker.exists(), "rejected ACP command was executed"
        record("acp_spawn_rejected_and_preset_catalog_hidden")

        agent = spawn("fixture-agent", "pty", [sys.executable, str(fixture), "--root", str(root), "--mode", "auto"])
        first = send(agent["id"], "stream", "duplicate-key")
        duplicate = send(agent["id"], "stream", "duplicate-key")
        assert duplicate["id"] == first["id"]
        try:
            send(agent["id"], "different prompt", "duplicate-key")
        except RPCError:
            pass
        else:
            raise AssertionError("conflicting dedupe key accepted")
        tasks = rpc.call("task.list")
        assert sum(value.get("dedupe_key") == "duplicate-key" for value in tasks) == 1
        result = completed(first["id"])
        assert result["result"] == "[dev-fixture] handled: stream"
        record("task_deduplication_and_conflict_rejection", task_id=first["id"])
        record("pty_auto_ack_and_result", task_id=first["id"], result=result["result"])

        inbox, outbox = root / "peer-inbox.jsonl", root / "peer-outbox.jsonl"
        inbox.touch()
        peer = spawn("credential-peer", "pty", [sys.executable, str(script), "--peer", "--inbox", str(inbox), "--outbox", str(outbox)])
        wait_until(lambda: "PEER_READY" in screen_text(rpc.call("pane.screen", {"id": peer["id"]})), "peer authentication")
        own_pids.add(int(outbox.with_suffix(".pid").read_text()))
        peer_sequence = 0

        def peer_call(method, params):
            nonlocal peer_sequence
            peer_sequence += 1
            with inbox.open("a") as commands:
                commands.write(json.dumps({"id": peer_sequence, "method": method, "params": params}) + "\n")

            def response():
                if not outbox.exists():
                    return None
                return next((item for line in outbox.read_text().splitlines() if line
                             and (item := json.loads(line))["id"] == peer_sequence), None)

            return wait_until(response, "peer RPC response")

        response = peer_call("task.send", {"to": agent["id"], "prompt": "stream", "dedupe_key": "peer-before-edge"})
        assert "error" in response, "unconnected peer sent a task"
        response = peer_call("edge.add", {"from": peer["id"], "to": agent["id"]})
        assert "error" in response, "pane escalated to controller edge permissions"
        rpc.call("edge.add", {"from": peer["id"], "to": agent["id"]})
        response = peer_call("task.send", {"to": agent["id"], "prompt": "stream", "dedupe_key": "peer-after-edge"})
        assert "error" not in response, "connected peer task was rejected"
        delivered = completed(response["result"]["id"])
        assert delivered["from"] == peer["id"] and delivered["result"] == "[dev-fixture] handled: stream"
        record("pane_credentials_edge_authorization_and_delivery", pane_id=peer["id"], task_id=delivered["id"])

        queued = rpc.call("task.send", {"to": peer["id"], "prompt": "bridge task", "dedupe_key": "pty-bridge", "delivery_mode": "claim"})
        claimed = peer_call("task.next", {})
        assert claimed["result"]["id"] == queued["id"]
        assert claimed["result"]["state"] == "running"
        submitted = peer_call("task.result", {"id": queued["id"], "result": "bridge result"})
        assert "error" not in submitted
        assert completed(queued["id"])["result"] == "bridge result"
        record("pty_task_claim_and_result")

        repo = root / "repo"
        repo.mkdir()

        def git(cwd, *command):
            result = subprocess.run(["git", "-C", str(cwd), *command], capture_output=True,
                                    text=True, timeout=15, check=True)
            return result.stdout.strip()

        git(repo, "-c", "init.templateDir=", "init", "-b", "main")
        git(repo, "config", "core.hooksPath", "/dev/null")
        (repo / "tracked.txt").write_text("source baseline\n")
        git(repo, "add", "tracked.txt")
        git(repo, "-c", "user.name=Smoke Fixture", "-c", "user.email=smoke@example.invalid",
            "-c", "commit.gpgsign=false", "commit", "-m", "test: seed temporary repository")
        layer = rpc.call("layer.create", {"name": "smoke", "cwd": str(repo), "base": "HEAD"})
        worktree = Path(layer["path"])
        assert worktree.is_dir() and worktree.resolve() != repo.resolve()
        assert git(worktree, "branch", "--show-current") == layer["branch"]
        assert git(worktree, "rev-parse", "HEAD") == git(repo, "rev-parse", "HEAD")
        assert str(worktree.resolve()) in git(repo, "worktree", "list", "--porcelain")
        (worktree / "tracked.txt").write_text("isolated layer change\n")
        assert (repo / "tracked.txt").read_text() == "source baseline\n"
        assert "tracked.txt" in rpc.call("git.changes", {"cwd": str(worktree)})
        diff = rpc.call("git.diff", {"cwd": str(worktree), "path": "tracked.txt"})
        assert "-source baseline" in diff and "+isolated layer change" in diff
        record("git_worktree_isolation_and_diff", branch=layer["branch"])

        held = send(peer["id"], "crash-running", "crash-running")
        wait_until(lambda: task(held["id"])["state"] == "delivered", "PTY task before crash")
        acknowledged = peer_call("task.ack", {"id": held["id"]})
        assert "error" not in acknowledged and acknowledged["result"]["state"] == "accepted"
        rpc.close()
        rpc = None
        daemon.kill()
        daemon.wait(timeout=5)
        log_file.close()

        # Reintroduce a persisted legacy pane solely to exercise restart rejection.
        db = sqlite3.connect(home / "state.db")
        try:
            persisted = json.loads(db.execute("SELECT body FROM state WHERE id=1").fetchone()[0])
            persisted["panes"].append({"id": "legacy-acp", "name": "legacy-acp", "kind": "worker",
                "backend": "acp", "command": acp_command, "cwd": str(root), "workspace": str(root),
                "state": "exited", "created_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())})
            db.execute("UPDATE state SET body=? WHERE id=1", (json.dumps(persisted),))
            db.commit()
        finally:
            db.close()
        launch()
        recovered = task(held["id"])
        assert recovered["state"] == "unknown", "running task was not marked unknown after restart"
        assert task(first["id"])["result"] == "[dev-fixture] handled: stream"
        assert task(first["id"])["state"] == "completed"
        assert any(value["name"] == "smoke" for value in rpc.call("state.get", {"frames": False})["layers"])
        acp_rejected("pane.restart", {"id": "legacy-acp"})
        assert not acp_marker.exists(), "legacy ACP restart executed its command"
        record("legacy_acp_restart_rejected")

        # A restarted peer must not replay old inbox commands, especially a
        # blocking task.next from the preceding claim/result check.
        inbox.write_text("")
        outbox.write_text("")
        rpc.call("pane.restart", {"id": peer["id"]})
        wait_until(lambda: "PEER_READY" in screen_text(rpc.call("pane.screen", {"id": peer["id"]})), "restarted peer")
        own_pids.add(int(outbox.with_suffix(".pid").read_text()))
        assert "crash-running" not in screen_text(rpc.call("pane.screen", {"id": peer["id"]})), "unknown task replayed"
        fresh = send(peer["id"], "post-restart-new-task", "post-restart-new-task")
        wait_until(lambda: task(fresh["id"])["state"] == "delivered", "post-restart PTY delivery")
        assert "error" not in peer_call("task.ack", {"id": fresh["id"]})
        assert "error" not in peer_call("task.result", {"id": fresh["id"], "result": "post restart result"})
        assert completed(fresh["id"])["result"] == "post restart result"
        assert task(held["id"])["state"] == "unknown"
        record("daemon_sigkill_recovery_without_replay", unknown_task=held["id"], preserved_task=first["id"])
        report["status"] = "passed"
    except Exception as exc:
        report["status"] = "failed"
        report["error"] = str(exc)
        report["traceback"] = traceback.format_exc()
    finally:
        if rpc is not None:
            try:
                rpc.call("shutdown")
            except Exception:
                pass
            rpc.close()
        if daemon is not None and daemon.poll() is None:
            try:
                daemon.wait(timeout=5)
            except subprocess.TimeoutExpired:
                daemon.kill()
                daemon.wait(timeout=5)
        if log_file is not None:
            log_file.close()
        if report["status"] == "passed" and daemon is not None and daemon.returncode != 0:
            report["status"] = "failed"
            report["error"] = "daemon did not exit cleanly after shutdown"
        daemon_log = root / "daemon.log"
        if daemon_log.exists() and "WARNING: DATA RACE" in daemon_log.read_text(errors="replace"):
            report["status"] = "failed"
            report["error"] = "Go race detector reported a data race"
        # Only PIDs written by this run's own fixture are considered for cleanup.
        # Pipe EOF normally exits fixtures; this also handles an interrupted run.
        fixture_pids = root / "fixture-pids.jsonl"
        if fixture_pids.exists():
            for line in fixture_pids.read_text().splitlines():
                own_pids.add(int(line))
        for pid in own_pids:
            try:
                os.kill(pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
        shutil.rmtree(root)
        report["elapsed_seconds"] = round(time.monotonic() - started, 3)
        report["temporary_state_removed"] = not root.exists()
        output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        print(str(output), flush=True)
    return 0 if report["status"] == "passed" else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="bin/wtable")
    parser.add_argument("--output", default=".dev/test-results/smoke.json")
    parser.add_argument("--peer", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--inbox", help=argparse.SUPPRESS)
    parser.add_argument("--outbox", help=argparse.SUPPRESS)
    args = parser.parse_args()
    if args.peer:
        peer_main(args.inbox, args.outbox)
        return 0
    return run(args)


if __name__ == "__main__":
    sys.exit(main())
