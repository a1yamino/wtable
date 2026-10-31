#!/usr/bin/env python3
"""Real-daemon/TTY acceptance for session-local views; no model calls.

Run after building: python3 scripts/view_smoke.py --binary bin/wtable
"""
import argparse
import fcntl
import json
import os
from pathlib import Path
import pty
import shutil
import socket
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
    root = Path(tempfile.mkdtemp(prefix="wt-view-", dir="/tmp"))
    home, tui_home = root / "state", root / "ui"
    home.mkdir(); tui_home.mkdir()
    output = Path(args.output).resolve(); output.parent.mkdir(parents=True, exist_ok=True)
    report = {"status": "running", "provider_calls": 0, "checks": [], "artifacts": {}}
    transcript, requests = bytearray(), []
    daemon = tui = rpc = master = listener = None
    connections = []
    log = (root / "daemon.log").open("wb")
    env = dict(os.environ, WTABLE_HOME=str(home), TERM="xterm-256color")

    def record(name):
        report["checks"].append({"name": name, "passed": True})
        print("PASS", name, flush=True)

    def save(name, text):
        path = output.with_name(output.stem + "-" + name); path.write_text(text)
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
        peers = {}
        for name in "ABC":
            inbox, outbox = root / (name + "-in"), root / (name + "-out")
            inbox.touch()
            pane = rpc.call("pane.spawn", {"name": name, "backend": "pty", "cwd": str(root),
                "delivery_config": {"blocked_regex": "TRUST_THIS_FIXTURE"},
                "command": [sys.executable, str(Path(__file__).with_name("smoke.py")), "--peer", "--inbox", str(inbox), "--outbox", str(outbox)]})
            wait_until(lambda: "PEER_READY" in screen_text(rpc.call("pane.screen", {"id": pane["id"]})), name + " ready")
            peers[name] = pane
        ids = {name: pane["id"] for name, pane in peers.items()}
        rpc.call("pane.input", {"id": ids["B"], "data": "".join("B_OUTPUT_%02d\r" % i for i in range(35))})
        wait_until(lambda: "B_OUTPUT_34" in screen_text(rpc.call("pane.screen", {"id": ids["B"]})), "B scrollback output")

        def state(**params):
            return rpc.call("state.get", {"frames": False, "graph": True, **params})

        def size(name):
            frame = rpc.call("pane.screen", {"id": ids[name]})
            return frame["cols"], frame["rows"]

        # Observe the actual TUI requests without logging auth or pane payloads.
        shutil.copyfile(home / "controller.token", tui_home / "controller.token")
        listener = socket.socket(socket.AF_UNIX); listener.bind(str(tui_home / "daemon.sock")); listener.listen()

        def relay(source, target, observe=False):
            pending = b""
            try:
                while data := source.recv(65536):
                    if observe:
                        pending += data
                        while b"\n" in pending:
                            line, pending = pending.split(b"\n", 1)
                            message = json.loads(line)
                            if message.get("method") == "state.get":
                                requests.append(message.get("params", {}))
                    target.sendall(data)
            except (OSError, ValueError):
                pass
            finally:
                try:
                    target.shutdown(socket.SHUT_WR)
                except OSError:
                    pass

        def accept():
            try:
                while True:
                    client, _ = listener.accept()
                    upstream = socket.socket(socket.AF_UNIX); upstream.connect(str(home / "daemon.sock"))
                    connections.extend([client, upstream])
                    threading.Thread(target=relay, args=(client, upstream, True), daemon=True).start()
                    threading.Thread(target=relay, args=(upstream, client), daemon=True).start()
            except OSError:
                pass
        threading.Thread(target=accept, daemon=True).start()
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))

        def controlling_tty():
            os.setsid(); fcntl.ioctl(0, termios.TIOCSCTTY, 0)
        tui = subprocess.Popen([binary], cwd=root, env=dict(env, WTABLE_HOME=str(tui_home)), stdin=slave, stdout=slave, stderr=slave, preexec_fn=controlling_tty)
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

        def shown(text):
            return text in screen()

        def panel_ready(name="Agents"):
            def ready():
                current = screen()
                return "侧栏/内容" in current and "处理中" not in current and name in current
            return wait_until(ready, "panel ready " + name)

        def send(data):
            os.write(master, data.encode() if isinstance(data, str) else data); time.sleep(.15)

        def prefix(key):
            send(b"\x01" + key.encode())

        def screenshot(name):
            save(name + "-screen.txt", screen())

        def visible(names):
            expected = {ids[name] for name in names}
            wait_until(lambda: requests and requests[-1].get("frames") is True and set(requests[-1].get("ids", [])) == expected, "visible frames " + names)

        def resize(cols, rows):
            fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0)); time.sleep(.3)

        def picker(name, action="f"):
            prefix(action); wait_until(lambda: shown("选择 pane") or shown("分屏目标"), "pane picker")
            send(b"\x1b[H" + b"\x1b[B" * "ABC".index(name) + b"\r")

        wait_until(lambda: shown("PEER_READY"), "initial TUI")
        visible("A")
        assert size("B") == size("C") == (80, 24)
        screenshot("single")
        record("initial_focus_requests_only_visible_pane_frames_and_keeps_background_nominal_size")

        picker("B", "|"); visible("AB")
        a_width, b_width = size("A")[0], size("B")[0]
        assert 1 < a_width < 120 and 1 < b_width < 120
        assert size("C") == (80, 24)
        divider_col = a_width + 1
        send("\x1b[<0;%d;10M\x1b[<32;%d;10M\x1b[<0;%d;10m" % (divider_col, 81, 81))
        wait_until(lambda: size("A")[0] >= 78, "drag divider")
        ratio = size("A")[0] / (size("A")[0] + size("B")[0])
        resize(140, 40)
        wait_until(lambda: size("A")[0] + size("B")[0] == 139, "split resizes")
        assert abs(size("A")[0] / 139 - ratio) < .03
        assert size("C") == (80, 24)
        screenshot("split")
        record("split_chooses_target_drag_preserves_ratio_on_resize_and_background_never_resizes")

        tab1_sizes = size("A"), size("B")
        prefix("T"); picker("C"); visible("C")
        assert shown("tab-2 2/2")
        prefix(","); send(b"\x15work\r"); wait_until(lambda: shown("work 2/2"), "rename tab")
        assert size("A") == tab1_sizes[0]
        prefix("{"); visible("AB")
        assert (size("A"), size("B")) == tab1_sizes
        prefix("}"); visible("C")
        prefix("W"); visible("AB")
        screenshot("tabs")
        record("tabs_keep_independent_layouts_focus_and_ratios_and_support_rename_switch_close")

        prefix("a"); panel_ready("Agents")
        send(b"\x1b[H\x1b[Bp"); visible("A")
        frozen_b = size("B")
        resize(120, 40); assert size("B") == frozen_b
        send("v"); wait_until(lambda: shown("最近 20 行") and shown("B_OUTPUT_34"), "background peek")
        assert not shown("B_OUTPUT_00") and size("B") == frozen_b
        screenshot("peek")
        send(b"\x1b")  # close the peek overlay
        send(b"\r"); wait_until(lambda: shown("对 B 做什么"), "pane action list")
        send(b"\r"); visible("B")  # 打开 is the first entry
        assert size("B")[0] == 120
        record("Agents_park_and_scrollback_peek_keep_size_and_Enter_unparks_in_focus")

        prefix("a"); panel_ready("Agents")
        send(b"\x1b[H\x1b[Bp\x1b"); visible("")
        frozen_b = size("B"); resize(120, 40)
        assert size("B") == frozen_b
        blank = state(frames=True, ids=[])
        assert all("frame" not in pane for pane in blank["panes"]), "ids [] must mean metadata only"
        all_frames = state(frames=True)
        assert all("frame" in pane for pane in all_frames["panes"]), "omitted ids must retain all frames"
        screenshot("all-parked")
        before_input = {name: screen_text(rpc.call("pane.screen", {"id": pane_id})) for name, pane_id in ids.items()}
        send("VIEW_EMPTY_MUST_NOT_REACH_OLD_PANE\r")
        send(b"\x1b[200~VIEW_EMPTY_PASTE_MUST_NOT_REACH_OLD_PANE\x1b[201~")
        assert {name: screen_text(rpc.call("pane.screen", {"id": pane_id})) for name, pane_id in ids.items()} == before_input
        record("all_parked_requests_empty_ids_without_frames_and_omitted_ids_retains_legacy_behavior")

        prefix("G"); wait_until(lambda: shown("协作 Graph"), "Graph opens with all parked")
        wait_until(lambda: requests and requests[-1].get("frames") is False, "Graph metadata-only polling")
        for _ in range(5):
            if shown("关联任务 · C ·"):
                break
            send("j")
        assert shown("关联任务 · C ·")
        send(b"\r"); wait_until(lambda: shown("对 C 做什么"), "graph node action list")
        send(b"\r"); visible("C")  # 打开 is the first entry
        assert screen().count("PEER_READY") == 1
        screenshot("graph-jump")
        record("Graph_uses_no_frames_and_Enter_unparks_selected_pane_into_focus")

        attention_task = rpc.call("task.send", {"to": ids["A"], "prompt": "VIEW_ATTENTION", "delivery_mode": "claim"})
        with (root / "A-in").open("a") as commands:
            commands.write(json.dumps({"id": 1, "method": "task.next", "params": {}}) + "\n")
        wait_until(lambda: (root / "A-out").exists() and '"id": 1' in (root / "A-out").read_text(), "attention task claimed")
        with (root / "A-in").open("a") as commands:
            commands.write(json.dumps({"id": 2, "method": "task.result", "params": {"id": attention_task["id"], "result": "Needs a look"}}) + "\n")
        wait_until(lambda: rpc.call("task.get", {"id": attention_task["id"]})["state"] == "completed", "attention task completed")
        wait_until(lambda: shown("1 waiting"), "attention status")
        prefix("!"); visible("A")
        wait_until(lambda: next(n for n in state()["graph"]["nodes"] if n["id"] == ids["A"])["unread"] == 0, "visible clears unread")
        record("attention_status_and_jump_unpark_recipient_and_clear_its_unread")
        screenshot("attention")
        picker("B"); send("TRUST_THIS_FIXTURE\r"); picker("A")
        wait_until(lambda: next(p for p in state()["panes"] if p["id"] == ids["B"])["readiness"] == "blocked", "blocked readiness metadata")
        wait_until(lambda: shown("1 waiting"), "blocked readiness counted")
        prefix("!"); visible("B")
        screenshot("blocked-attention")
        record("blocked_readiness_counts_as_waiting_and_attention_jump_opens_it")

        prefix("a"); panel_ready("Agents")
        send("O"); wait_until(lambda: shown("角色模板库"), "role library")
        send("a"); wait_until(lambda: shown("编辑角色模板"), "role editor")
        send("view-role\tRole instructions v1\tshell\t\t" + str(root) + "\r")
        def role():
            return next((r for r in rpc.call("preset.list")["roles"] if r["name"] == "view-role"), None)
        wait_until(lambda: role() and role()["instructions"] == "Role instructions v1", "role created")
        wait_until(lambda: shown("角色模板库") and not shown("处理中"), "role library refreshed")
        # Enter advances fields; tab to the last one (cwd) so it submits.
        send("e\t\x15Role instructions v2\t\t\t\r")
        wait_until(lambda: role() and role()["instructions"] == "Role instructions v2", "role updated")
        wait_until(lambda: shown("角色模板库") and not shown("处理中"), "updated role library")
        screenshot("roles")
        send("d"); wait_until(lambda: shown("y 确认 · n/Esc 取消"), "role delete prompt")
        send(b"\r"); assert role() is not None
        send("n"); assert role() is not None
        send("d"); wait_until(lambda: shown("y 确认 · n/Esc 取消"), "second role delete prompt")
        send("y"); wait_until(lambda: role() is None, "role deleted")
        record("Agents_role_library_creates_edits_and_requires_explicit_confirmation_to_delete")
        send(b"\x1b"); send(b"\x1b")
        acp = rpc.call("pane.spawn", {"name": "acp-draft", "backend": "acp", "cwd": str(root),
            "command": [sys.executable, str(Path(__file__).parent / "fixtures/acp_agent.py"), "--root", str(root)]})
        prefix("f"); wait_until(lambda: shown("选择 pane"), "ACP picker")
        send(b"\x1b[F\r")
        wait_until(lambda: shown("ACP 输入") and requests and set(requests[-1].get("ids", [])) == {acp["id"]}, "ACP is sole visible pane")
        prefix("a"); panel_ready("Agents")
        send(b"\x1b[Fp"); send(b"\x1b"); visible("")
        marker = "VIEW_HIDDEN_ACP_DRAFT_MUST_STAY_EMPTY"
        send("\x1b[200~" + marker + "\x1b[201~")
        prefix("a"); panel_ready("Agents")
        send(b"\x1b[F\r"); wait_until(lambda: shown("对 acp-draft 做什么"), "ACP pane action list")
        send(b"\r"); wait_until(lambda: shown("ACP 输入"), "ACP reopened")
        assert marker not in screen(), "paste into an empty view changed a hidden ACP draft"
        task_count = len(rpc.call("task.list"))
        send(b"\r")
        assert len(rpc.call("task.list")) == task_count, "reopened ACP draft must remain empty"
        screenshot("empty-acp-draft")
        record("parking_only_visible_ACP_ignores_empty_view_paste_and_reopens_with_empty_draft")
        save("requests.json", json.dumps(requests, ensure_ascii=False, indent=2) + "\n")
        save("final-state.json", json.dumps(state(), ensure_ascii=False, indent=2) + "\n")
        prefix("d"); assert tui.wait(timeout=5) == 0
        report["status"] = "passed"
    except Exception as exc:
        report["status"] = "failed"; report["error"] = repr(exc)
        try:
            if master is not None:
                save("failure-screen.txt", screen())
            save("requests.json", json.dumps(requests, ensure_ascii=False, indent=2) + "\n")
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
        if listener:
            listener.close()
        for connection in connections:
            connection.close()
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
    parser.add_argument("--output", default=".dev/test-results/view.json")
    run(parser.parse_args())
