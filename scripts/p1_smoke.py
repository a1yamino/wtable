#!/usr/bin/env python3
"""P1 panel/onboarding acceptance with a real daemon, TUI and PTYs; no models.

Run from the repository root after building: python3 scripts/p1_smoke.py --binary bin/wtable
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
from smoke import RPC, screen_text, wait_until

FRAME_HELPER = r'''package main
import("fmt";"io";"os";"github.com/charmbracelet/x/vt")
func main(){v:=vt.NewEmulator(120,40);go io.Copy(io.Discard,v);b,_:=io.ReadAll(os.Stdin);v.Write(b);for y:=0;y<40;y++{for x:=0;x<120;x++{c:=v.CellAt(x,y);if c==nil{fmt.Print(" ")}else if c.Width>0{fmt.Print(c.Content);x+=c.Width-1}};fmt.Println()}}
'''


def run(args):
    binary = str(Path(args.binary).resolve())
    script_dir = Path(__file__).resolve().parent
    root = Path(tempfile.mkdtemp(prefix="wt-p1-", dir="/tmp"))
    home, repo = root / "state", root / "repo"
    home.mkdir(); repo.mkdir()
    report = {"status": "running", "provider_calls": 0, "checks": []}
    output = Path(args.output).resolve(); output.parent.mkdir(parents=True, exist_ok=True)
    transcript = bytearray(); rpc = None; tui = None; daemon = None; master = None
    log = (root / "daemon.log").open("wb")
    account = [{"name": "fixture-account", "env": {"P1_ACCOUNT_MARKER": "not-for-brief"}}]
    (home / "accounts.json").write_text(json.dumps(account))
    # Read without the OS canonical line limit: brief envelopes exceed macOS MAX_CANON.
    # cbreak retains CR-to-LF translation, so actual Enter still completes each line.
    (home / "presets.json").write_text(json.dumps([
        {"id": "acp-fixture", "name": "ACP fixture", "backend": "acp", "command": [sys.executable, str(script_dir / "fixtures/acp_agent.py"), "--root", str(root)]},
        {"id": "echo-fixture", "name": "Echo fixture", "backend": "pty", "command": [sys.executable, "-u", "-c", "import os,sys,tty\nfrom pathlib import Path\ntty.setcbreak(0)\np=Path(" + repr(str(root)) + ")/(os.environ['WTABLE_PANE_ID']+'.input')\nfor line in sys.stdin:\n with p.open('a') as f: f.write(line)"]},
    ]))
    env = dict(os.environ, WTABLE_HOME=str(home), TERM="xterm-256color")

    def record(name):
        report["checks"].append({"name": name, "passed": True}); print("PASS", name, flush=True)

    try:
        frame_src = root / "frame.go"; frame_src.write_text(FRAME_HELPER)
        frame_bin = root / "frame"
        subprocess.run(["go", "build", "-o", str(frame_bin), str(frame_src)], check=True, capture_output=True)
        def git(*cmd):
            return subprocess.check_output(["git", "-C", str(repo), *cmd], stderr=subprocess.STDOUT)
        git("-c", "init.templateDir=", "init", "-b", "main")
        git("config", "core.hooksPath", "/dev/null")
        (repo / "seed.txt").write_text("seed\n"); git("add", "seed.txt")
        git("-c", "user.name=P1 Fixture", "-c", "user.email=p1@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "test: seed isolated repository")
        daemon = subprocess.Popen([binary, "daemon"], env=env, stdout=log, stderr=log)
        def connect():
            try:
                return RPC(home, (home / "controller.token").read_text().strip())
            except (FileNotFoundError, ConnectionRefusedError):
                return None
        rpc = wait_until(connect, "daemon")
        seed = rpc.call("pane.spawn", {"name": "seed", "backend": "pty", "command": ["/bin/cat"], "cwd": str(repo)})
        master, slave = pty.openpty(); fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))
        def controlling_tty():
            os.setsid(); fcntl.ioctl(0, termios.TIOCSCTTY, 0)
        tui = subprocess.Popen([binary], cwd=repo, env=env, stdin=slave, stdout=slave, stderr=slave, preexec_fn=controlling_tty)
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
            os.write(master, data.encode() if isinstance(data, str) else data)
        def key(k):
            send(b"\x01" + k.encode()); time.sleep(.15)
        def esc():
            send(b"\x1b"); time.sleep(.15)
        def tab(n=1):
            send(b"\t" * n)
        def state():
            return rpc.call("state.get", {"frames": False})
        def pane(name):
            return next((p for p in state()["panes"] if p["name"] == name), None)
        def task(id):
            return rpc.call("task.get", {"id": id})
        def ready_panel(title="Agents"):
            t = title.strip("[]")
            return wait_until(lambda: shown("侧栏/内容") and (not t or shown(t + " ")) and not shown("处理中"), "panel ready " + t)

        def ensure_panel():
            if not shown("侧栏/内容"):
                key("a")
                wait_until(lambda: shown("侧栏/内容"), "panel open")
        def finish_brief(name):
            p = wait_until(lambda: (p if (p := pane(name)) and p.get("brief_task") else None), "brief created")
            wait_until(lambda: task(p["brief_task"])["state"] == "delivered", "brief injected")
            input_path = root / (p["id"] + ".input")
            wait_until(lambda: input_path.exists() and p["brief_task"] in input_path.read_text(), "brief read by " + name)
            rpc.call("task.done", {"id": p["brief_task"], "result": "fixture read brief"})
            return p

        wait_until(lambda: shown("seed"), "initial TUI")
        key("a"); ready_panel()
        presets = rpc.call("preset.list")["presets"]
        echo_index = next(i for i, p in enumerate(presets) if p["id"] == "echo-fixture")
        for name in ["alpha", "beta", "gamma"]:
            ensure_panel()
            send("a"); wait_until(lambda: shown("添加 Agent"), "preset picker")
            send("e"); wait_until(lambda: shown("名字"), "agent form")
            send(name)
            tab(); send(b"\x1b[H" + b"\x1b[C" * (echo_index + 1) + b"\x1b[D")  # preset → echo fixture, fires the brief default
            tab(5); send("coder role")                        # → role
            tab(); send(b"\x1b[C")                           # → account: fixture-account
            tab(3); send(b"\r")                              # → brief(auto on), connect, role_template, submit
            wait_until(lambda: pane(name) and pane(name)["state"] == "running", "created " + name)
            print("DIAG", name, {k: pane(name).get(k) for k in ("preset", "role", "account", "inject_brief", "brief_task")}, flush=True)
            if name != "gamma":
                finish_brief(name)
        alpha, beta, gamma = pane("alpha"), pane("beta"), pane("gamma")
        assert alpha["account"] == "fixture-account" and alpha["role"] == "coder role"
        assert all(p["preset"] == "echo-fixture" and p["inject_brief"] for p in [alpha, beta, gamma])
        for p in [alpha, beta, gamma]:
            brief = Path(p["brief_path"])
            assert len(brief.read_bytes()) <= 2048 and brief.stat().st_mode & 0o777 == 0o600
            assert "not-for-brief" not in brief.read_text()
        record("panel_creates_three_named_agents_with_brief_and_account_metadata")

        tab(); ready_panel("[Edges]"); send("a")
        send(b"\x1b[H\x1b[C"); tab(); send(b"\x1b[H\x1b[C\x1b[C \x1b[C \r")
        wait_until(lambda: len(state()["edges"]) == 2, "two outgoing edges")
        send("a"); send(b"\x1b[H\x1b[C\x1b[C"); tab(); send(b"\x1b[F \r")
        wait_until(lambda: len(state()["edges"]) == 3, "third edge")
        assert {(e["from"], e["to"]) for e in state()["edges"]} == {(alpha["id"], beta["id"]), (alpha["id"], gamma["id"]), (beta["id"], gamma["id"])}
        record("panel_builds_three_directed_edges_without_typing_ids")
        send("x"); wait_until(lambda: shown("y 确认"), "edge confirmation"); send(b"\r"); time.sleep(.1)
        assert len(state()["edges"]) == 3
        send("y"); wait_until(lambda: len(state()["edges"]) == 2, "edge deletion")
        record("edge_delete_requires_explicit_confirmation")

        tab(); ready_panel("[Tasks]"); send("s")
        send(b"\x1b[C "); tab(); send("multi-pane work")
        tab(3); send(b"\x1b[C"); tab(); send(b"\x1b[F\r")
        def sent_tasks():
            return [t for t in rpc.call("task.list") if t["prompt"] == "multi-pane work"]
        wait_until(lambda: len(sent_tasks()) >= 1, "send created tasks")
        children = sent_tasks()
        assert len(children) == 2, children
        assert all(t["on_complete"] == "notify" and t["report_to"] == gamma["id"] for t in children), children
        finish_brief("gamma")  # A busy target stays queued and drains on its own.
        for t in children:
            wait_until(lambda t=t: task(t["id"])["state"] == "delivered", "queued target delivery")
            rpc.call("task.done", {"id": t["id"], "result": "REPORT_ONLY_MARKER"})
        assert len(rpc.call("report.list")) == 2
        assert "REPORT_ONLY_MARKER" not in screen_text(rpc.call("pane.screen", {"id": gamma["id"]}))
        record("send_queues_busy_targets_and_notify_does_not_execute")
        send("b"); wait_until(lambda: shown("报告收件箱"), "report inbox"); send(b"\r")
        wait_until(lambda: shown("REPORT_ONLY_MARKER"), "report detail"); esc(); esc()

        tab(); ready_panel("[Layers]"); send("n"); send("feature"); tab(2); send(b"\r")
        wait_until(lambda: len(state()["layers"]) == 1, "create layer"); ready_panel("[Layers]")
        layer = state()["layers"][0]
        send(b"\x1b[B\r"); tab(); ready_panel()
        send("a"); wait_until(lambda: shown("添加 Agent"), "preset picker")
        send("e"); wait_until(lambda: shown("名字"), "agent form")
        send("delta"); send(b"\r" * 11)
        wait_until(lambda: pane("delta") and pane("delta")["state"] == "running", "layer agent")
        ready_panel(); delta = pane("delta")
        assert delta["cwd"] == layer["path"] and delta["layer"] == layer["path"]
        record("layer_creation_and_selection_drive_new_agent_directory")

        send(b"\x1b[H\x1b[B\x1b[B\r")  # Focus beta, then remove an earlier pane.
        send("FOCUS_BEFORE\r")
        def received(p, marker):
            path = root / (p["id"] + ".input")
            return path.exists() and marker in path.read_text()
        wait_until(lambda: received(beta, "FOCUS_BEFORE"), "focus beta input")
        key("a"); ready_panel(); send("d"); wait_until(lambda: shown("y 确认"), "remove confirmation")
        send(b"\r"); time.sleep(.1); assert pane("seed") is not None
        send("y"); wait_until(lambda: pane("seed") is None, "seed removal"); ready_panel(); esc()
        send("FOCUS_AFTER\r"); wait_until(lambda: received(beta, "FOCUS_AFTER"), "focus preserved input")
        assert not received(gamma, "FOCUS_AFTER")
        record("confirmed_removal_preserves_focus_by_pane_identity")

        (home / "accounts.json").write_text("[]")
        key("a"); ready_panel(); send("e")
        wait_until(lambda: shown("原账号档案已缺失"), "missing account stays selected")
        esc(); esc(); assert pane("alpha")["account"] == "fixture-account"
        (home / "accounts.json").write_text(json.dumps(account))
        record("missing_account_is_not_silently_replaced")

        key("a"); ready_panel(); send("a"); wait_until(lambda: shown("添加 Agent"), "preset picker")
        send("e"); wait_until(lambda: shown("名字"), "agent form")
        send("chat")
        catalog = rpc.call("preset.list")
        acp_index = next(i for i, p in enumerate(catalog["presets"]) if p["id"] == "acp-fixture")
        tab(); send(b"\x1b[H" + b"\x1b[C" * acp_index)
        tab(9); send(b"\r")
        wait_until(lambda: pane("chat") and pane("chat")["state"] == "running", "ACP create"); ready_panel()
        chat = pane("chat"); wait_until(lambda: task(chat["brief_task"])["state"] == "completed", "ACP brief completion")
        send(b"\r"); wait_until(lambda: shown("ACP 输入"), "persistent ACP composer")
        send("direct ACP prompt\r")
        direct = wait_until(lambda: next((t for t in rpc.call("task.list") if t["prompt"] == "direct ACP prompt"), None), "ACP typed task")
        wait_until(lambda: task(direct["id"])["state"] == "completed", "ACP direct result")
        assert pane("chat")["session_id"] == chat["session_id"]
        record("ACP_persistent_input_reuses_existing_session_and_task_queue")
        target = root / "permission-effect.txt"
        approval_task = rpc.call("task.send", {"to": chat["id"], "prompt": "permission:" + str(target)})
        wait_until(lambda: state()["permissions"], "permission request"); wait_until(lambda: shown("权限请求"), "permission visible")
        send(b"\r"); time.sleep(.1); assert state()["permissions"] and not target.exists()
        send("1"); wait_until(lambda: task(approval_task["id"])["state"] == "completed", "permission rejected")
        assert not target.exists(); record("ACP_permission_priority_prevents_Enter_approval")
        peer_result = root / "peer-permissions.json"
        peer_code = "\n".join([
            "import os,sys,json", "from pathlib import Path",
            "sys.path.insert(0," + repr(str(script_dir)) + ")", "from smoke import RPC,RPCError",
            "rpc=RPC(os.environ['WTABLE_HOME'],os.environ['WTABLE_PEER_TOKEN'])",
            "out={'diag':rpc.call('diag.info'),'denied':[]}",
            "for method in ['preset.list','pane.spawn','pane.update','pane.remove','pane.brief','pane.brief.send','edge.add']:",
            " try: rpc.call(method,{'id':" + repr(alpha["id"]) + "}); raise AssertionError('unexpected permission: '+method)",
            " except RPCError as exc:",
            "  assert '人类控制端权限' in str(exc)", "  out['denied'].append(method)",
            "Path(" + repr(str(peer_result)) + ").write_text(json.dumps(out))", "rpc.close()",
        ])
        rpc.call("pane.spawn", {"name": "permission-probe", "backend": "pty", "cwd": str(repo), "command": [sys.executable, "-c", peer_code]})
        wait_until(peer_result.exists, "live peer controller restriction")
        peer_data = json.loads(peer_result.read_text())
        assert set(peer_data["diag"]) == {"version"} and len(peer_data["denied"]) == 7
        record("live_peer_cannot_recruit_connect_edit_remove_or_access_brief_controls")
        key("d"); assert tui.wait(timeout=5) == 0
        report["status"] = "passed"
    except Exception as exc:
        report["status"] = "failed"; report["error"] = repr(exc)
        if master is not None:
            try:
                (output.parent / "p1-screen.txt").write_text(screen())
                (output.parent / "p1-state.json").write_text(json.dumps(state(), ensure_ascii=False, indent=2))
                (output.parent / "p1-inputs.json").write_text(json.dumps({p.name:p.read_text() for p in root.glob("*.input")}, ensure_ascii=False, indent=2))
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
    parser.add_argument("--output", default=".dev/test-results/p1.json")
    run(parser.parse_args())
