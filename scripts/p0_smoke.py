#!/usr/bin/env python3
"""Exercise P0 delivery/report contracts against real isolated daemon and PTYs.

python3 scripts/p0_smoke.py --binary bin/wtable --output .dev/test-results/p0.json
No external models, credentials copied to logs, or user sessions are used.
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
from smoke import RPC, RPCError, screen_text, wait_until


def run(args):
    binary = str(Path(args.binary).resolve())
    base = Path(__file__).resolve().parent
    root = Path(tempfile.mkdtemp(prefix="wt-p0-", dir="/tmp"))
    home = root / "state"
    home.mkdir()
    report = {"status": "running", "provider_calls": 0, "checks": []}
    output = Path(args.output).resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    log = (root / "daemon.log").open("wb")
    env = dict(os.environ, WTABLE_HOME=str(home))
    daemon = subprocess.Popen([binary, "daemon"], env=env, stdout=log, stderr=log)
    rpc = None

    def connect():
        return RPC(home, (home / "controller.token").read_text().strip())

    def ready():
        try:
            return connect()
        except (FileNotFoundError, ConnectionRefusedError):
            return None

    def record(name):
        report["checks"].append({"name": name, "passed": True})
        print("PASS", name, flush=True)

    def expect_error(fn, contains=""):
        try:
            fn()
        except RPCError as exc:
            assert contains in str(exc), str(exc)
        else:
            raise AssertionError("operation unexpectedly succeeded")

    try:
        rpc = wait_until(ready, "daemon")
        peers = {}

        def spawn(name, command, backend="pty", **cfg):
            return rpc.call("pane.spawn", {"name": name, "backend": backend, "cwd": str(root),
                                           "command": command, "delivery_config": cfg, "cols": 160, "rows": 30})

        def peer(name, **cfg):
            inbox, outbox = root / (name + "-in"), root / (name + "-out")
            inbox.touch()
            # Consume PTY bytes without interpreting them; kernel echo remains visible.
            # Otherwise repeated injections eventually fill an unread canonical input queue.
            wrapper = "import sys,threading,runpy\ndef drain():\n while sys.stdin.buffer.read(1): pass\nthreading.Thread(target=drain,daemon=True).start()\nscript=sys.argv.pop(1)\nrunpy.run_path(script,run_name='__main__')"
            p = spawn(name, [sys.executable, "-c", wrapper, str(base / "smoke.py"), "--peer", "--inbox", str(inbox), "--outbox", str(outbox)], **cfg)
            wait_until(lambda: "PEER_READY" in screen_text(rpc.call("pane.screen", {"id": p["id"]})), "peer ready")
            peers[name] = [inbox, outbox, 0]
            return p

        def call_peer(name, method, params):
            meta = peers[name]
            meta[2] += 1
            seq = meta[2]
            with meta[0].open("a") as f:
                f.write(json.dumps({"id": seq, "method": method, "params": params}) + "\n")
            def response():
                if not meta[1].exists():
                    return None
                for line in meta[1].read_text().splitlines():
                    if line and (item := json.loads(line))["id"] == seq:
                        return item
                return None
            return wait_until(response, "peer response")

        def peer_ok(name, method, params):
            value = call_peer(name, method, params)
            assert "error" not in value, value
            return value["result"]

        def get(t):
            return rpc.call("task.get", {"id": t["id"]})

        def send(p, text, **options):
            return rpc.call("task.send", {"to": p["id"], "prompt": text, **options})

        receiver = peer("receiver", settle_ms=10, verify_ms=150, ack_timeout_ms=700)
        observer = peer("observer", settle_ms=10, verify_ms=150, ack_timeout_ms=700)
        sender = peer("sender", settle_ms=10)
        time.sleep(.05)
        task = send(receiver, "hello\nsecond line")
        assert task["state"] == "delivered" and task["delivery_mode"] == "inject"
        wait_until(lambda: task["id"] in screen_text(rpc.call("pane.screen", {"id": receiver["id"]})), "injected envelope")
        assert peer_ok("receiver", "task.ack", {"id": task["id"]})["state"] == "accepted"
        peer_ok("receiver", "task.result", {"id": task["id"], "result": "injection complete"})
        assert get(task)["state"] == "completed"
        record("inject_ack_result")

        blocked = spawn("blocked", [sys.executable, "-u", "-c", "import time;print('Log in');time.sleep(60)"], blocked_regex="Log in", settle_ms=10)
        wait_until(lambda: "Log in" in screen_text(rpc.call("pane.screen", {"id": blocked["id"]})), "blocked marker")
        before = len(rpc.call("task.list"))
        expect_error(lambda: send(blocked, "MUST_NOT_BE_WRITTEN"), "blocked:")
        assert len(rpc.call("task.list")) == before
        assert "MUST_NOT_BE_WRITTEN" not in screen_text(rpc.call("pane.screen", {"id": blocked["id"]}))
        record("blocked_gate_rejects_without_task_or_input")

        time.sleep(.05)
        unconfirmed = send(receiver, "echo-only")
        wait_until(lambda: get(unconfirmed).get("delivery_status") == "write-unconfirmed", "unconfirmed echo")
        expect_error(lambda: send(receiver, "busy rejection"), "busy:")
        retry = rpc.call("task.retry", {"id": unconfirmed["id"]})
        assert retry["retry_of"] == unconfirmed["id"] and retry["state"] == "delivered"
        assert get(unconfirmed)["state"] == "unknown"
        peer_ok("receiver", "task.result", {"id": retry["id"], "result": "retry complete"})
        peer_ok("receiver", "task.result", {"id": unconfirmed["id"], "result": "late original result"})
        record("write_unconfirmed_explicit_retry_and_late_result")

        time.sleep(.05)
        unknown = send(receiver, "timeout")
        wait_until(lambda: get(unknown)["state"] == "unknown", "ack timeout")
        peer_ok("receiver", "task.result", {"id": unknown["id"], "result": "late result"})
        assert get(unknown)["state"] == "completed"
        record("ack_timeout_unknown_accepts_late_result")

        tasks_before = len(rpc.call("task.list"))
        screen_before = screen_text(rpc.call("pane.screen", {"id": observer["id"]}))
        notification = send(receiver, "notify only", delivery_mode="claim", on_complete="notify", report_to=observer["id"])
        assert peer_ok("receiver", "task.next", {})["id"] == notification["id"]
        rpc.call("task.done", {"id": notification["id"], "result": "report text " + "文" * 3000})
        duplicate = call_peer("receiver", "task.result", {"id": notification["id"], "result": "late duplicate"})
        assert "error" in duplicate
        reports = [r for r in rpc.call("report.list") if r["parent_task"] == notification["id"]]
        assert len(reports) == 1 and reports[0]["state"] == "delivered" and not reports[0]["seen"]
        assert len(reports[0]["summary"].encode()) <= 4096
        assert len(rpc.call("task.list")) == tasks_before + 1
        assert screen_text(rpc.call("pane.screen", {"id": observer["id"]})) == screen_before
        inbox = peer_ok("observer", "report.list", {})
        assert any(r["id"] == reports[0]["id"] for r in inbox)
        assert "error" in call_peer("observer", "task.get", {"id": notification["id"]})
        peer_ok("observer", "report.seen", {"id": reports[0]["id"]})
        assert rpc.call("report.get", {"id": reports[0]["id"]})["seen"]
        record("notify_inbox_no_execution_byte_limit_authorized_access_dedup")

        for target in [receiver, observer]:
            rpc.call("edge.add", {"from": sender["id"], "to": target["id"]})
        handoff = peer_ok("sender", "task.send", {"to": receiver["id"], "prompt": "handoff", "on_complete": "handoff", "report_to": observer["id"]})
        peer_ok("receiver", "task.next", {})
        peer_ok("receiver", "task.result", {"id": handoff["id"], "result": "follow-up input"})
        children = [t for t in rpc.call("task.list") if t.get("parent_task") == handoff["id"]]
        assert len(children) == 1 and children[0]["state"] == "queued" and children[0]["from"] == sender["id"]
        assert children[0]["to"] == observer["id"]
        record("handoff_new_queued_child_preserves_sender")

        revoked = peer_ok("sender", "task.send", {"to": receiver["id"], "prompt": "revoke", "on_complete": "handoff", "report_to": observer["id"]})
        peer_ok("receiver", "task.next", {})
        rpc.call("edge.remove", {"from": sender["id"], "to": observer["id"]})
        rpc.call("task.done", {"id": revoked["id"], "result": "manual done cannot elevate sender"})
        revoked_report = next(r for r in rpc.call("report.list") if r["parent_task"] == revoked["id"])
        assert revoked_report["state"] == "undeliverable"
        assert not any(t.get("parent_task") == revoked["id"] for t in rpc.call("task.list"))
        assert peer_ok("sender", "report.get", {"id": revoked_report["id"]})["summary"]
        assert "error" in call_peer("observer", "report.get", {"id": revoked_report["id"]})
        assert "error" in call_peer("observer", "report.seen", {"id": revoked_report["id"]})
        assert not any(r["id"] == revoked_report["id"] for r in peer_ok("observer", "report.list", {}))
        record("revoked_report_undeliverable_even_after_controller_done")

        acp = spawn("acp", [sys.executable, str(base / "fixtures/acp_agent.py"), "--root", str(root)], backend="acp")
        acp_task = send(acp, "stream")
        wait_until(lambda: get(acp_task)["state"] == "completed", "ACP completion")
        assert get(acp_task)["result"] == "fixture stream result" and get(acp_task).get("accepted_at")
        held = send(acp, "hold:" + str(root / "hold-marker"))
        wait_until(lambda: get(held)["state"] == "accepted", "ACP accepted update")
        rpc.call("task.cancel", {"id": held["id"]})
        wait_until(lambda: get(held)["state"] == "cancelled", "ACP cancellation")
        record("acp_update_acceptance_auto_result_and_confirmed_cancel")

        # Stop confirmation is a distinct human action, not inferred from Ctrl-C.
        time.sleep(.05)
        cancelling = send(receiver, "cancel injected")
        value = rpc.call("task.cancel", {"id": cancelling["id"]})
        assert value["state"] == "cancelling"
        assert rpc.call("task.cancel", {"id": cancelling["id"], "confirmed": True})["state"] == "cancelled"
        record("pty_cancel_requires_explicit_stop_confirmation")

        # Completion/report persistence survives daemon replacement with no replay.
        persisted_ids = [r["id"] for r in rpc.call("report.list")]
        rpc.call("shutdown"); rpc.close(); rpc = None
        daemon.wait(timeout=10)
        daemon = subprocess.Popen([binary, "daemon"], env=env, stdout=log, stderr=log)
        rpc = wait_until(ready, "restarted daemon")
        assert [r["id"] for r in rpc.call("report.list")] == persisted_ids
        assert get(notification)["state"] == "completed"
        record("reports_and_completed_results_survive_restart_without_duplicates")
        report["status"] = "passed"
    except Exception as exc:
        report["status"] = "failed"
        report["error"] = repr(exc)
        raise
    finally:
        output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        if rpc:
            try:
                rpc.call("shutdown")
            except Exception:
                pass
            rpc.close()
        try:
            daemon.wait(timeout=10)
        except subprocess.TimeoutExpired:
            daemon.terminate(); daemon.wait(timeout=10)
        log.close()
        shutil.rmtree(root)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default="bin/wtable")
    parser.add_argument("--output", default=".dev/test-results/p0.json")
    run(parser.parse_args())
