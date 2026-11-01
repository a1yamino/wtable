#!/usr/bin/env python3
"""Workspace isolation acceptance: two teams, startup picker and Ctrl-a w.

Run from the repository root after building:
python3 scripts/workspace_smoke.py --binary bin/wtable
"""
import argparse
import fcntl
import json
import os
from pathlib import Path
import pty
import struct
import subprocess
import sys
import tempfile
import termios
import threading
import time

from smoke import RPC, wait_until

FRAME_HELPER = r'''package main
import("fmt";"io";"os";"github.com/charmbracelet/x/vt")
func main(){v:=vt.NewEmulator(120,40);go io.Copy(io.Discard,v);b,_:=io.ReadAll(os.Stdin);v.Write(b);for y:=0;y<40;y++{for x:=0;x<120;x++{c:=v.CellAt(x,y);if c==nil{fmt.Print(" ")}else if c.Width>0{fmt.Print(c.Content);x+=c.Width-1}};fmt.Println()}}
'''


def run(args):
    binary = str(Path(args.binary).resolve())
    root = Path(tempfile.mkdtemp(prefix="wt-ws-", dir="/tmp")).resolve()
    home, empty = root / "state", root / "empty-path"
    dirs = {name: root / name for name in ("alpha", "beta", "gamma")}
    home.mkdir()
    empty.mkdir()
    for path in dirs.values():
        path.mkdir()
    (home / "presets.json").write_text("[]")
    env = dict(os.environ, WTABLE_HOME=str(home), TERM="xterm-256color", PATH=str(empty))
    report = {"status": "running", "provider_calls": 0, "checks": []}
    output = Path(args.output).resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    transcript = bytearray()
    daemon = tui = master = None
    log = (root / "daemon.log").open("wb")

    def record(name):
        report["checks"].append({"name": name, "passed": True})
        print("PASS", name, flush=True)

    try:
        frame_src = root / "frame.go"
        frame_src.write_text(FRAME_HELPER)
        frame_bin = root / "frame"
        subprocess.run(["go", "build", "-o", str(frame_bin), str(frame_src)], check=True, capture_output=True)
        daemon = subprocess.Popen([binary, "daemon"], env=env, stdout=log, stderr=log)

        def connect():
            try:
                return RPC(home, (home / "controller.token").read_text().strip())
            except (FileNotFoundError, ConnectionRefusedError):
                return None

        rpc = wait_until(connect, "daemon")

        def spawn(name, path, marker):
            command = ["/bin/sh", "-c", 'printf "%s_READY\\n"; while read line; do echo "%s:$line"; done' % (marker, marker)]
            pane = rpc.call("pane.spawn", {"name": name, "backend": "pty", "kind": "foreground", "cwd": str(path), "command": command})
            wait_until(lambda: next(p for p in rpc.call("state.get", {"frames": False})["panes"] if p["id"] == pane["id"])["state"] == "running", name + " running")
            return pane

        alpha = spawn("alpha", dirs["alpha"], "ALPHA")
        beta = spawn("beta", dirs["beta"], "BETA")

        def launch(cwd):
            nonlocal transcript, master
            transcript = bytearray()
            master, slave = pty.openpty()
            fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))

            def controlling_tty():
                os.setsid()
                fcntl.ioctl(0, termios.TIOCSCTTY, 0)
            proc = subprocess.Popen([binary], cwd=cwd, env=env, stdin=slave, stdout=slave, stderr=slave, preexec_fn=controlling_tty)
            os.close(slave)
            threading.Thread(target=drain, daemon=True).start()
            return proc

        def drain():
            try:
                while True:
                    transcript.extend(os.read(master, 65536))
            except OSError:
                pass

        def screen():
            return subprocess.check_output([str(frame_bin)], input=bytes(transcript)).decode()

        def shown(text):
            return text in screen()

        def send(data):
            os.write(master, data.encode() if isinstance(data, str) else data)
            time.sleep(0.15)

        def scoped(workspace):
            return rpc.call("state.get", {"frames": False, "workspace": str(workspace)})

        tui = launch(dirs["alpha"])
        wait_until(lambda: shown("选择工作区"), "workspace picker")
        assert shown("当前目录") and shown("beta"), screen()
        # The most recently opened workspace (beta) is preselected.
        send(b"\r")
        wait_until(lambda: shown("BETA_READY"), "beta team visible")
        assert not shown("ALPHA_READY"), "alpha leaked into beta's view"
        record("startup_picker_preselects_last_workspace_and_scopes_the_view")

        send(b"\x01w")
        wait_until(lambda: shown("选择工作区"), "switch picker")
        send(b"\x1b[B")
        send(b"\r")
        wait_until(lambda: shown("ALPHA_READY"), "alpha team visible")
        assert not shown("BETA_READY"), "beta leaked into alpha's view"
        record("ctrl_a_w_switches_the_whole_team_without_stopping_the_other")

        panes = rpc.call("state.get", {"frames": False})["panes"]
        assert {p["id"] for p in panes} == {alpha["id"], beta["id"]}
        assert all(p["state"] == "running" for p in panes), panes
        assert [p["id"] for p in scoped(dirs["alpha"])["panes"]] == [alpha["id"]]
        assert [p["id"] for p in scoped(dirs["beta"])["panes"]] == [beta["id"]]
        record("background_team_keeps_running_and_state_get_is_scopeable")

        send(b"\x01d")
        tui.wait(timeout=10)
        tui = None

        # A fresh launch defaults back to the last opened workspace (alpha).
        transcript = bytearray()
        tui = launch(dirs["alpha"])
        wait_until(lambda: shown("选择工作区"), "workspace picker after restart")
        send(b"\r")
        wait_until(lambda: shown("ALPHA_READY"), "restored last workspace")
        record("restart_preselects_the_last_opened_workspace")

        # Path entry, then an empty workspace opens the normal first-run picker.
        send(b"\x01w")
        wait_until(lambda: shown("选择工作区"), "switch picker for path entry")
        send(b"\x1b[B")
        send(b"\x1b[B")
        send(b"\r")
        wait_until(lambda: shown("打开工作区目录"), "workspace path input")
        send(str(dirs["gamma"]))
        send(b"\r")
        wait_until(lambda: shown("新建前台 agent"), "first-run picker in empty workspace")
        send(b"\x1b")
        record("path_entry_opens_an_unregistered_workspace_and_offers_first_run")

        assert any(w["path"] == str(dirs["gamma"]) for w in rpc.call("workspace.list", {})), rpc.call("workspace.list", {})
        send(b"\x01d")
        tui.wait(timeout=10)
        tui = None
        report["status"] = "passed"
    except Exception as exc:
        report["status"] = "failed"
        report["error"] = repr(exc)
        raise
    finally:
        if tui and tui.poll() is None:
            tui.terminate()
            try:
                tui.wait(timeout=5)
            except subprocess.TimeoutExpired:
                tui.kill()
        if daemon and daemon.poll() is None:
            daemon.terminate()
            try:
                daemon.wait(timeout=5)
            except subprocess.TimeoutExpired:
                daemon.kill()
        log.close()
        output.write_text(json.dumps(report, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default="bin/wtable")
    parser.add_argument("--output", default=".dev/test-results/workspace.json")
    run(parser.parse_args())


if __name__ == "__main__":
    main()
