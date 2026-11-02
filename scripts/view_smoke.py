#!/usr/bin/env python3
"""Real-daemon/TTY acceptance for the rail + single-canvas interaction model.

Run after building: python3 scripts/view_smoke.py --binary bin/wtable
No model calls; peers are local Python fixtures.
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
    script_dir = Path(__file__).resolve().parent
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
    (home / "presets.json").write_text(json.dumps([
        {"id": "acp-fixture", "name": "ACP fixture", "backend": "acp",
         "command": [sys.executable, str(script_dir / "fixtures/acp_agent.py"), "--root", str(root)]},
    ]))

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
                "command": [sys.executable, str(script_dir / "smoke.py"), "--peer", "--inbox", str(inbox), "--outbox", str(outbox)]})
            wait_until(lambda: "PEER_READY" in screen_text(rpc.call("pane.screen", {"id": pane["id"]})), name + " ready")
            peers[name] = pane
        ids = {name: pane["id"] for name, pane in peers.items()}

        def state(**params):
            return rpc.call("state.get", {"frames": False, "graph": True, **params})

        def pane_by_id(pid):
            return next(p for p in state()["panes"] if p["id"] == pid)

        def size(pid):
            frame = rpc.call("pane.screen", {"id": pid})
            return frame["cols"], frame["rows"]

        # Observe the TUI's actual requests without logging auth or pane payloads.
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

        def send(data):
            os.write(master, data.encode() if isinstance(data, str) else data); time.sleep(.15)

        def prefix(key):
            send(b"\x01" + key.encode())

        def screenshot(name):
            save(name + "-screen.txt", screen())

        def visible(*pane_ids):
            expected = set(pane_ids)
            wait_until(lambda: requests and requests[-1].get("frames") is True and set(requests[-1].get("ids", [])) == expected, "visible " + str(pane_ids))

        def resize(cols, rows):
            fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0)); time.sleep(.4)

        def pane_text(pid):
            return screen_text(rpc.call("pane.screen", {"id": pid}))

        wait_until(lambda: shown("选择工作区"), "workspace picker")
        send(b"\r")
        wait_until(lambda: shown("PEER_READY"), "initial TUI")
        visible(ids["A"])
        assert size(ids["B"]) == size(ids["C"]) == (80, 24)
        assert screen().count("PEER_READY") == 1
        screenshot("single")
        record("initial_canvas_requests_only_the_main_pane_and_background_keeps_nominal_size")

        # Switching agents replaces the canvas; it never creates a split.
        prefix("j"); visible(ids["B"])
        wait_until(lambda: state()["last_focus"] == ids["B"], "last focus updated")
        assert screen().count("PEER_READY") == 1
        assert size(ids["C"]) == (80, 24)
        prefix("k"); visible(ids["A"])
        screenshot("switched")
        record("rail_navigation_replaces_main_canvas_without_splitting_and_persists_last_focus")

        # Utility slot: default shell follows the main pane directory, closing
        # keeps the process alive in the rail, reopening reuses it.
        before = {p["id"] for p in state()["panes"]}
        prefix("s")
        wait_until(lambda: len(state()["panes"]) == len(before) + 1, "utility shell spawned")
        shell_id = next(p["id"] for p in state()["panes"] if p["id"] not in before)
        wait_until(lambda: pane_by_id(shell_id)["state"] == "running", "utility shell running")
        shell = pane_by_id(shell_id)
        assert shell["cwd"] == peers["A"]["cwd"]
        visible(ids["A"], shell["id"])
        shell_size = wait_until(lambda: (size(shell["id"]) if size(shell["id"])[0] > 2 else None), "utility sized")
        prefix("s"); visible(ids["A"])
        resize(130, 44)
        assert size(shell["id"]) == shell_size, "closed utility must keep its size"
        count = len(state()["panes"])
        prefix("s")
        visible(ids["A"], shell["id"])
        assert len(state()["panes"]) == count, "reopened utility must reuse the existing shell"
        screenshot("utility")
        prefix("s"); visible(ids["A"])
        resize(120, 40)
        record("utility_slot_spawns_shell_in_main_cwd_keeps_it_running_and_reuses_it")

        # Rail keyboard focus: End selects the last agent, Enter opens it.
        prefix("a")
        wait_until(lambda: shown("需要你") or shown("Workers"), "rail focused")
        send(b"\x1b[F")
        send(b"\r")
        wait_until(lambda: state()["last_focus"] == shell["id"], "rail opened shell")
        visible(shell["id"])
        screenshot("rail")
        record("rail_enter_opens_the_selected_agent_and_clears_keyboard_focus_back_to_canvas")

        # RPC for the remaining checks; keep the rail order stable by removing
        # the temporary shell first.
        rpc.call("pane.remove", {"id": shell["id"]})
        wait_until(lambda: all(p["id"] != shell["id"] for p in state()["panes"]), "utility shell removed")

        # API validation: ACP is hidden and rejected for every pane kind.
        for kind in ("foreground", "worker"):
            catalog = rpc.call("preset.list", {"kind": kind})["presets"]
            assert all(p["backend"] == "pty" for p in catalog)
            assert not any(p["id"] == "acp-fixture" for p in catalog)
        for params in [
            {"name": "bad-acp", "kind": "worker", "backend": "acp", "cwd": str(root),
             "command": [sys.executable, str(script_dir / "fixtures/acp_agent.py"), "--root", str(root)]},
            {"name": "bad-acp-preset", "kind": "worker", "preset": "acp-fixture", "cwd": str(root)},
        ]:
            try:
                rpc.call("pane.spawn", params)
                raise AssertionError("ACP spawn was accepted")
            except Exception as exc:
                assert "ACP 已停用" in str(exc), str(exc)
        try:
            rpc.call("pane.spawn", {"name": "second-foreground", "kind": "foreground", "backend": "pty", "cwd": str(root), "command": ["/bin/cat"]})
            raise AssertionError("second foreground was accepted")
        except Exception as exc:
            assert "前台" in str(exc), str(exc)
        record("api_rejects_direct_and_preset_acp_and_a_second_foreground_and_filters_presets")

        # Inbox: a completed task from the human appears once and disappears
        # when the target pane is opened.
        done = rpc.call("task.send", {"to": ids["A"], "prompt": "VIEW_INBOX"})
        wait_until(lambda: rpc.call("task.get", {"id": done["id"]})["state"] in ("queued", "delivered", "accepted"), "task queued")
        rpc.call("task.done", {"id": done["id"], "result": "INBOX_RESULT"})
        wait_until(lambda: any(i.get("task_id") == done["id"] for i in state().get("inbox", [])), "task inbox item")
        assert not rpc.call("task.get", {"id": done["id"]})["seen"]
        prefix("1"); visible(ids["A"])
        wait_until(lambda: rpc.call("task.get", {"id": done["id"]})["seen"], "task marked seen on open")
        wait_until(lambda: not any(i.get("task_id") == done["id"] for i in state().get("inbox", [])), "inbox item cleared on open")
        record("inbox_lists_completed_human_work_and_clears_only_when_the_pane_is_opened")

        # Narrow terminals compress the rail to a glyph strip; Ctrl-a a expands
        # it into a drawer, and Esc puts it back.
        resize(84, 40)
        wait_until(lambda: not shown("Workers") and not shown("需要你"), "narrow rail strip")
        screenshot("narrow")
        prefix("a")
        wait_until(lambda: shown("需要你") or shown("Workers") or shown("前台"), "rail drawer expanded")
        send(b"\x1b")
        wait_until(lambda: not shown("Workers") and not shown("需要你"), "rail drawer collapsed")
        resize(120, 40)
        screenshot("wide")
        record("narrow_terminal_compresses_the_rail_to_a_strip_and_ctrl_a_a_expands_a_drawer")

        # state.get id semantics stay intact.
        blank = state(frames=True, ids=[])
        assert all("frame" not in pane for pane in blank["panes"]), "ids [] must mean metadata only"
        all_frames = state(frames=True)
        assert all("frame" in pane for pane in all_frames["panes"] if pane["backend"] == "pty"), "omitted ids must retain all PTY frames"
        record("state_get_ids_contract_is_unchanged")

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
