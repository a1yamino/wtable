#!/usr/bin/env python3
"""P2 Graph acceptance with a real daemon, TUI and PTYs; no model calls.

Run after building: python3 scripts/p2_smoke.py --binary bin/wtable
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
from smoke import RPC, screen_text, wait_until


def run(args):
    binary = str(Path(args.binary).resolve())
    script_dir = Path(__file__).resolve().parent
    root = Path(tempfile.mkdtemp(prefix="wt-p2-", dir="/tmp"))
    home = root / "state"; home.mkdir()
    output = Path(args.output).resolve(); output.parent.mkdir(parents=True, exist_ok=True)
    report = {"status": "running", "provider_calls": 0, "checks": [], "artifacts": {}}
    transcript = bytearray()
    rpc = daemon = tui = master = None
    log = (root / "daemon.log").open("wb")
    env = dict(os.environ, WTABLE_HOME=str(home), TERM="xterm-256color")

    def record(name):
        report["checks"].append({"name": name, "passed": True})
        print("PASS", name, flush=True)

    def save(name, value):
        path = output.with_name(output.stem + "-" + name)
        path.write_text(value)
        report["artifacts"][name] = str(path)

    try:
        frame_src = root / "frame.go"; frame_src.write_text(FRAME_HELPER)
        frame_bin = root / "frame"
        subprocess.run(["go", "build", "-o", str(frame_bin), str(frame_src)], check=True, capture_output=True)
        daemon = subprocess.Popen([binary, "daemon"], env=env, stdout=log, stderr=log)

        def connect():
            try:
                return RPC(home, (home / "controller.token").read_text().strip())
            except (FileNotFoundError, ConnectionRefusedError):
                return None
        rpc = wait_until(connect, "daemon")

        def state():
            return rpc.call("state.get", {"frames": False, "graph": True})

        def task(task_id):
            return rpc.call("task.get", {"id": task_id})

        def pane_text(pane_id):
            return screen_text(rpc.call("pane.screen", {"id": pane_id}))

        peers = {}
        for name in "ABCD":
            inbox, outbox = root / (name + "-in.jsonl"), root / (name + "-out.jsonl")
            inbox.touch()
            pane = rpc.call("pane.spawn", {"name": name, "backend": "pty", "cwd": str(root),
                "command": [sys.executable, str(script_dir / "smoke.py"), "--peer", "--inbox", str(inbox), "--outbox", str(outbox)]})
            wait_until(lambda: "PEER_READY" in pane_text(pane["id"]), name + " peer ready")
            peers[name] = {"pane": pane, "inbox": inbox, "outbox": outbox, "sequence": 0}

        def peer_call(name, method, params):
            peer = peers[name]; peer["sequence"] += 1
            sequence = peer["sequence"]
            with peer["inbox"].open("a") as commands:
                commands.write(json.dumps({"id": sequence, "method": method, "params": params}) + "\n")

            def response():
                if not peer["outbox"].exists():
                    return None
                return next((item for line in peer["outbox"].read_text().splitlines() if line
                             and (item := json.loads(line))["id"] == sequence), None)
            value = wait_until(response, name + " peer response")
            assert "error" not in value, value
            return value["result"]

        ids = {name: peer["pane"]["id"] for name, peer in peers.items()}
        grants = {(ids[a], ids[b]) for a, b in [("A", "B"), ("B", "C"), ("C", "A"), ("C", "D")]}
        for source, target in grants:
            rpc.call("edge.add", {"from": source, "to": target})
        completed = peer_call("B", "task.send", {"to": ids["C"], "prompt": "GRAPH_UNREAD_TASK", "delivery_mode": "claim"})
        assert peer_call("C", "task.next", {})["id"] == completed["id"]
        peer_call("C", "task.result", {"id": completed["id"], "result": "Graph unread result"})
        running = peer_call("C", "task.send", {"to": ids["D"], "prompt": "GRAPH_RUNNING_TASK", "delivery_mode": "claim"})
        assert peer_call("D", "task.next", {})["id"] == running["id"]
        queued = peer_call("A", "task.send", {"to": ids["B"], "prompt": "GRAPH_QUEUED_TASK", "delivery_mode": "claim"})
        preserved = peer_call("C", "task.send", {"to": ids["D"], "prompt": "GRAPH_PRESERVED_TASK", "delivery_mode": "claim"})
        controller_task = rpc.call("task.send", {"to": ids["A"], "prompt": "GRAPH_HUMAN_TASK", "delivery_mode": "claim"})
        assert queued["state"] == preserved["state"] == controller_task["state"] == "queued"
        assert "graph" not in rpc.call("state.get", {"frames": False}), "graph must be opt-in"
        snapshot = state(); graph = snapshot["graph"]
        nodes = {n["id"]: n for n in graph["nodes"]}
        assert set(nodes) == {"you", *ids.values()}
        required = {"id", "name", "preset", "backend", "layer", "state", "attention", "unread"}
        assert all(required <= n.keys() for n in nodes.values())
        for name, pane_id in ids.items():
            original = next(p for p in snapshot["panes"] if p["id"] == pane_id)
            assert nodes[pane_id]["name"] == name
            for field in ["preset", "backend", "layer", "state"]:
                assert nodes[pane_id][field] == original.get(field, "")
        assert {(e["from"], e["to"]) for e in graph["edges"] if e["kind"] == "grant"} == grants
        assert {(e["from"], e["to"]) for e in graph["edges"] if e["kind"] == "virtual"} == {("you", pane_id) for pane_id in ids.values()}
        active = {(e["from"], e["to"]): e["active"] for e in graph["edges"]}
        assert active[(ids["A"], ids["B"])] == active[("you", ids["A"])] == 0
        assert active[(ids["C"], ids["D"])] == 1
        assert all(nodes[ids[name]]["unread"] == 1 and "bell" in nodes[ids[name]]["attention"] for name in "BC")
        save("initial-state.json", json.dumps(snapshot, ensure_ascii=False, indent=2) + "\n")
        record("opt_in_graph_projection_matches_real_nodes_grants_virtual_edges_and_active_tasks")

        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))

        def controlling_tty():
            os.setsid(); fcntl.ioctl(0, termios.TIOCSCTTY, 0)
        tui = subprocess.Popen([binary], cwd=root, env=env, stdin=slave, stdout=slave, stderr=slave, preexec_fn=controlling_tty)
        os.close(slave)

        def drain():
            try:
                while True:
                    transcript.extend(os.read(master, 65536))
            except OSError:
                pass
        threading.Thread(target=drain, daemon=True).start()

        def screen():
            return subprocess.check_output([str(frame_bin)], input=bytes(transcript)).decode()

        def shown(value):
            return value in screen()

        def send(data):
            os.write(master, data.encode() if isinstance(data, str) else data)
            time.sleep(.15)

        def prefix(key):
            send(b"\x01" + key.encode())

        def screenshot(name):
            save(name + "-screen.txt", screen())

        def select_node(name):
            for _ in range(6):
                if shown("关联任务 · " + name + " ·"):
                    return
                send("j")
            raise AssertionError("cannot select node: " + name)

        def select_edge(source, target):
            for _ in range(5):
                if shown("选中边：" + source + " → " + target + " ·"):
                    return
                send("j")
            raise AssertionError("cannot select edge: " + source + " -> " + target)

        wait_until(lambda: shown("PEER_READY"), "initial TUI")
        prefix("G")
        wait_until(lambda: shown("Graph"), "Graph opened")
        screenshot("cycle")
        rows = screen().splitlines()
        assert any(arrow in "\n".join(rows[2:32]) for arrow in ["▶", "▼", "▲", "◀"]), "diagram direction arrows missing"
        positions = {name: next(i for i, row in enumerate(rows) if "│" + name + " · " in row) for name in "ABCD"}
        assert positions["A"] == positions["B"] == positions["C"] < positions["D"], positions
        record("graph_opens_from_prefix_G_and_renders_directed_cycle_and_downstream_node")

        baseline = {pane_id: pane_text(pane_id) for pane_id in ids.values()}
        send(b"\x1b[200~GRAPH_PASTE_MUST_NOT_REACH_PANE\x1b[201~")
        send("jk")
        assert {pane_id: pane_text(pane_id) for pane_id in ids.values()} == baseline, "Graph keys or paste reached a pane"
        record("graph_navigation_and_bracketed_paste_are_isolated_from_panes")

        select_node("you")
        screenshot("you")
        assert controller_task["id"] in screen(), "selected you node must show its related task"
        send("x")
        assert len(state()["edges"]) == 4
        assert not shown("按 y 确认"), "virtual edges must not offer deletion"
        select_node("A")
        send("j"); assert shown("关联任务 · B ·"), "j must select the next node"
        send("k"); assert shown("关联任务 · A ·"), "k must select the previous node"
        wait_until(lambda: queued["id"] in screen(), "A related tasks")
        screenshot("related-tasks")
        assert "queued" in screen() and "A → B" in screen(), "related task direction or state missing"
        select_node("B")
        assert completed["id"] in screen() and "[completed] · 未读" in screen(), "related unread result missing"
        screenshot("unread-task")
        record("selected_node_lists_related_task_id_direction_state_and_you_cannot_delete_virtual_edges")

        send("e")
        screenshot("edge-selection")
        select_edge("A", "B")
        send("k"); assert not shown("选中边：A → B ·"), "k must select the previous real edge"
        send("j"); assert shown("选中边：A → B ·"), "j must return to the selected real edge"
        send("x")
        wait_until(lambda: shown("按 y 确认"), "edge deletion confirmation")
        send(b"\r")
        assert len(state()["edges"]) == 4 and task(queued["id"])["state"] == "queued"
        send("n")
        assert len(state()["edges"]) == 4
        send("x"); wait_until(lambda: shown("按 y 确认"), "second deletion confirmation")
        send(b"\x1b")
        assert len(state()["edges"]) == 4
        send("x"); wait_until(lambda: shown("按 y 确认"), "confirmed deletion prompt")
        screenshot("delete-confirm")
        send("y")
        wait_until(lambda: len(state()["edges"]) == 3, "grant removed")
        assert (ids["A"], ids["B"]) not in {(e["from"], e["to"]) for e in state()["edges"]}
        assert task(queued["id"])["state"] == "cancelled"
        assert task(preserved["id"])["state"] == task(controller_task["id"])["state"] == "queued"
        assert task(running["id"])["state"] == "running"
        assert len([e for e in state()["graph"]["edges"] if e["kind"] == "virtual"]) == 4
        record("edge_deletion_requires_y_n_and_Escape_cancel_Enter_does_not_confirm_and_only_direction_queue_is_cancelled")

        # Exit then reopen to return to node mode with deterministic selection.
        send(b"\x1b")
        wait_until(lambda: not shown("Graph"), "Escape exits Graph")
        prefix("G"); wait_until(lambda: shown("Graph"), "Graph reopened")
        select_node("C")
        wait_until(lambda: preserved["id"] in screen(), "C selected")
        send(b"\r")
        wait_until(lambda: not shown("Graph"), "node jump exits Graph")
        marker = "GRAPH_JUMP_TARGET_C"
        send(marker + "\r")
        wait_until(lambda: marker in pane_text(ids["C"]), "jumped pane receives input")
        assert all(marker not in pane_text(ids[name]) for name in "ABD")
        screenshot("jump-zoom")
        assert screen().count("PEER_READY") == 1, "Enter should zoom to the selected pane"
        record("Escape_exits_Graph_and_Enter_jumps_to_selected_pane_in_single_pane_zoom")
        for edge in state()["edges"]:
            rpc.call("edge.remove", edge)
        prefix("G"); wait_until(lambda: shown("Graph"), "virtual-only Graph opened")
        send("e"); wait_until(lambda: shown("没有真实授权边"), "virtual edges excluded from selection")
        send("x")
        assert not shown("按 y 确认") and not state()["edges"]
        assert len(state()["graph"]["edges"]) == 4
        screenshot("virtual-only")
        record("virtual_only_graph_has_no_selectable_or_deletable_edges")
        send(b"\x1b")
        save("final-state.json", json.dumps(state(), ensure_ascii=False, indent=2) + "\n")
        prefix("d"); assert tui.wait(timeout=5) == 0
        report["status"] = "passed"
    except Exception as exc:
        report["status"] = "failed"; report["error"] = repr(exc)
        try:
            if master is not None:
                save("failure-screen.txt", screen())
            if rpc is not None:
                save("failure-state.json", json.dumps(state(), ensure_ascii=False, indent=2) + "\n")
        except Exception:
            pass
        raise
    finally:
        output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        output.with_suffix(".ansi").write_bytes(transcript)
        if tui and tui.poll() is None:
            tui.terminate(); tui.wait(timeout=5)
        if rpc:
            try:
                rpc.call("shutdown")
            except Exception:
                pass
            rpc.close()
        if daemon:
            try:
                daemon.wait(timeout=10)
            except subprocess.TimeoutExpired:
                daemon.terminate(); daemon.wait(timeout=10)
        if master is not None:
            os.close(master)
        log.close(); shutil.rmtree(root)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default="bin/wtable")
    parser.add_argument("--output", default=".dev/test-results/p2.json")
    run(parser.parse_args())
