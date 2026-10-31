#!/usr/bin/env python3
"""Exercise the scheduler/hub contracts: queue instead of error, blocking
next, sender-side wait, capability gating and steering.

python3 scripts/scheduler_smoke.py --binary bin/wtable --output .dev/test-results/scheduler.json
No external models, credentials or user sessions are used.
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
    root = Path(tempfile.mkdtemp(prefix="wt-p1-", dir="/tmp"))
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

        def spawn(name, command, **cfg):
            return rpc.call("pane.spawn", {"name": name, "backend": "pty", "cwd": str(root),
                                           "command": command, "delivery_config": cfg,
                                           "cols": 120, "rows": 20})

        peers = {}

        def peer(name, **cfg):
            inbox, outbox = root / (name + "-in"), root / (name + "-out")
            inbox.touch()
            wrapper = ("import sys,threading,runpy\ndef drain():\n while sys.stdin.buffer.read(1): pass\n"
                       "threading.Thread(target=drain,daemon=True).start()\nscript=sys.argv.pop(1)\n"
                       "runpy.run_path(script,run_name='__main__')")
            p = spawn(name, [sys.executable, "-c", wrapper, str(base / "smoke.py"),
                             "--peer", "--inbox", str(inbox), "--outbox", str(outbox)], **cfg)
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

        worker = spawn("worker", [sys.executable, "-u", "-c",
                                  "import sys,time\n"
                                  "for line in sys.stdin:\n"
                                  "    print('got:'+line.strip(), flush=True)\n"],
                       settle_ms=10, verify_ms=100, ack_timeout_ms=5000)
        time.sleep(.3)

        def get(task):
            tid = task["id"] if isinstance(task, dict) else task
            return rpc.call("task.get", {"id": tid})

        # 1. busy is a queue reason, not a send failure; it drains on completion.
        first = rpc.call("task.send", {"to": worker["id"], "prompt": "first"})
        assert first["state"] == "delivered", first
        second = rpc.call("task.send", {"to": worker["id"], "prompt": "second"})
        assert second["state"] == "queued" and second["queue_reason"].startswith("busy:"), second
        rpc.call("task.done", {"id": first["id"], "result": "one"})
        wait_until(lambda: get(second)["state"] == "delivered", "queued task drains after completion")
        record("busy_queues_with_reason_and_drains_after_completion")

        # 2. blocking next: only a pull pane may call it, and it blocks.
        puller = peer("puller", settle_ms=10)
        rpc.call("pane.caps.set", {"id": puller["id"], "caps": {
            "receive_tasks": True, "return_results": True, "push_idle": False,
            "push_steer": False, "prompt": False, "pull": True}})
        started = time.monotonic()
        empty = call_peer("puller", "task.next", {"timeout_ms": 300})
        elapsed = (time.monotonic() - started) * 1000
        assert empty.get("error") is None and empty["result"] is None, empty
        assert 250 <= elapsed <= 2000, elapsed
        record("next_blocks_until_timeout_and_returns_null")

        pull = rpc.call("task.send", {"to": puller["id"], "prompt": "pull work",
                                      "delivery_mode": "pull"})
        assert pull["state"] == "queued", pull
        started = time.monotonic()
        claimed = call_peer("puller", "task.next", {"timeout_ms": 5000})["result"]
        assert claimed and claimed["id"] == pull["id"] and claimed["state"] == "running", claimed
        assert (time.monotonic() - started) < 1.5
        assert claimed["delivered_via"] == "pull"
        record("next_returns_a_pull_task_immediately")

        # 3. sender-side wait; only the sender may wait.
        other = peer("other", settle_ms=10)
        denied = call_peer("other", "task.wait", {"id": claimed["id"], "timeout_ms": 50})
        assert "error" in denied, denied
        waiting = rpc.call("task.wait", {"id": claimed["id"], "timeout_ms": 50})
        assert waiting["state"] in ("running", "delivered", "accepted"), waiting
        call_peer("puller", "task.result", {"id": claimed["id"], "result": "pulled"})
        done = rpc.call("task.wait", {"id": claimed["id"], "timeout_ms": 1500})
        assert done["state"] == "completed" and done["result"] == "pulled" and done["result_pushed"], done
        record("wait_blocks_returns_result_and_rejects_non_sender")

        # 4. capability gating.
        closed = spawn("closed", [sys.executable, "-u", "-c", "import time;time.sleep(30)"], settle_ms=10)
        rpc.call("pane.caps.set", {"id": closed["id"], "caps": {
            "receive_tasks": False, "return_results": False, "push_idle": False,
            "push_steer": False, "prompt": False, "pull": False}})
        expect_error(lambda: rpc.call("task.send", {"to": closed["id"], "prompt": "nope"}), "does not accept")
        assert not [t for t in rpc.call("task.list") if t["to"] == closed["id"]]
        record("capabilities_reject_send_without_creating_a_task")

        # 5. steering reaches a busy pane when the capability is enabled.
        busy = rpc.call("task.send", {"to": worker["id"], "prompt": "long work", "on_complete": "none"})
        wait_until(lambda: get(busy)["state"] in ("delivered", "accepted"), "busy task in flight")
        expect_error(lambda: rpc.call("task.send", {"to": worker["id"], "prompt": "steer me",
                                                    "delivery_mode": "steer"}), "steering")
        rpc.call("pane.caps.set", {"id": worker["id"], "caps": {
            "receive_tasks": True, "return_results": True, "push_idle": True,
            "push_steer": True, "prompt": False, "pull": True}})
        steer = rpc.call("task.send", {"to": worker["id"], "prompt": "ADD-CONSTRAINT",
                                       "delivery_mode": "steer"})
        assert steer["state"] == "delivered" and steer["delivered_via"] == "steer", steer
        wait_until(lambda: "WTABLE STEER" in screen_text(rpc.call("pane.screen", {"id": worker["id"]})),
                   "steer envelope on screen")
        rpc.call("task.done", {"id": steer["id"], "result": "steer accepted"})
        record("steer_reaches_a_busy_pane_only_with_the_capability")

        rpc.call("task.done", {"id": busy["id"], "result": "long done"})
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
            daemon.terminate()
            try:
                daemon.wait(timeout=5)
            except subprocess.TimeoutExpired:
                daemon.kill()
        log.close()
        shutil.rmtree(root)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default="./bin/wtable")
    parser.add_argument("--output", default=".dev/test-results/scheduler.json")
    run(parser.parse_args())
