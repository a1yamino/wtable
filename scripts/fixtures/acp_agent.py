#!/usr/bin/env python3
"""Deterministic ACP v1 stdio agent used by scripts/smoke.py; no provider calls."""

import argparse
import json
import os
from pathlib import Path
import sys
import time


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--slow-init", action="store_true")
    args = parser.parse_args()
    root = args.root.resolve()
    session = "smoke-fixture-session"
    pending = {}
    next_permission = 0
    with (root / "fixture-pids.jsonl").open("a") as log:
        log.write(json.dumps(os.getpid()) + "\n")

    def send(value):
        sys.stdout.write(json.dumps(value, ensure_ascii=False) + "\n")
        sys.stdout.flush()

    def reply(request_id, result):
        send({"jsonrpc": "2.0", "id": request_id, "result": result})

    def update(text):
        send({"jsonrpc": "2.0", "method": "session/update", "params": {
            "sessionId": session, "update": {"sessionUpdate": "agent_message_chunk",
                                              "content": {"type": "text", "text": text}}}})

    def safe_path(value):
        target = Path(value).resolve()
        if root not in target.parents:
            raise ValueError("fixture target must be inside its temporary root")
        return target

    for line in sys.stdin:
        request = json.loads(line)
        method, params = request.get("method"), request.get("params", {})
        request_id = request.get("id")
        if method == "initialize":
            if args.slow_init:
                time.sleep(30)
            assert params["protocolVersion"] == 1
            caps = params.get("clientCapabilities", {})
            assert not caps.get("terminal", False)
            assert not any(caps.get("fs", {}).values())
            reply(request_id, {"protocolVersion": 1, "agentCapabilities": {"loadSession": True},
                               "agentInfo": {"name": "wtable-smoke-fixture", "version": "1"},
                               "authMethods": []})
        elif method == "session/new":
            assert Path(params["cwd"]).is_absolute() and params["mcpServers"] == []
            reply(request_id, {"sessionId": session})
        elif method == "session/load":
            assert params["sessionId"] == session
            reply(request_id, {})
        elif method == "session/prompt":
            text = "".join(part.get("text", "") for part in params["prompt"])
            if text.startswith("disconnect:"):
                safe_path(text.split(":", 1)[1]).write_text("effect before disconnect\n")
                os._exit(7)
            if text.startswith("permission:"):
                target = safe_path(text.split(":", 1)[1])
                next_permission += 1
                permission_id = "write-" + str(next_permission)
                pending[permission_id] = (request_id, target)
                send({"jsonrpc": "2.0", "id": permission_id, "method": "session/request_permission",
                      "params": {"sessionId": session,
                                 "toolCall": {"toolCallId": permission_id, "title": "Write fixture file",
                                              "kind": "edit", "locations": [{"path": str(target)}]},
                                 "options": [{"optionId": "reject", "name": "Reject once", "kind": "reject_once"},
                                             {"optionId": "allow", "name": "Allow once", "kind": "allow_once"}]}})
            elif text.startswith("hold:"):
                marker = safe_path(text.split(":", 1)[1])
                with marker.open("a") as stream:
                    stream.write("prompt-started\n")
                pending["hold"] = (request_id, None)
                update("holding")
            else:
                update("fixture ")
                update("stream result")
                reply(request_id, {"stopReason": "end_turn"})
        elif method == "session/cancel":
            for key, (prompt_id, _) in list(pending.items()):
                reply(prompt_id, {"stopReason": "cancelled"})
                del pending[key]
        elif method is None and request_id in pending:
            prompt_id, target = pending.pop(request_id)
            outcome = request.get("result", {}).get("outcome", {})
            if outcome.get("outcome") == "selected" and outcome.get("optionId") == "allow":
                target.write_text("explicitly allowed\n")
                update("permission allowed")
            elif outcome.get("outcome") == "selected" and outcome.get("optionId") == "reject":
                update("permission rejected")
            else:
                reply(prompt_id, {"stopReason": "cancelled"})
                continue
            reply(prompt_id, {"stopReason": "end_turn"})
        elif request_id is not None:
            send({"jsonrpc": "2.0", "id": request_id,
                  "error": {"code": -32601, "message": "unsupported fixture method"}})


if __name__ == "__main__":
    main()
