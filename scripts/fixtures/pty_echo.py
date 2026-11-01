#!/usr/bin/env python3
"""Deterministic PTY agent fixture for local development; no provider calls.

--mode echo mirrors input and appends every line to <root>/<pane>.input.
--mode auto also acknowledges injected tasks and submits a short result, so the
task/report UI can be exercised without a real model. A brief is printed but
deliberately not acknowledged: it is standing instructions, not work.
"""

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import tty


TASK = re.compile(r"^\[WTABLE TASK (t-[^\]]+)\]$")
TASK_END = re.compile(r"^\[WTABLE END (t-[^\]]+)\]$")
BRIEF = re.compile(r"^\[WTABLE BRIEF (t-[^\]]+)\]$")
BRIEF_END = re.compile(r"^\[WTABLE BRIEF END (t-[^\]]+)\]$")
STEER = re.compile(r"^\[WTABLE STEER (t-[^\]]+) vs")


def emit(text):
    print(text, flush=True)


def first_line(text):
    for line in text.splitlines():
        if line.strip():
            return line.strip()[:160]
    return "(empty)"


def run_result(task_id, prompt):
    cli = os.environ.get("WTABLE_CLI")
    if not cli:
        emit("[dev-fixture] WTABLE_CLI is missing; cannot ack/result")
        return
    emit("[dev-fixture] ack " + task_id)
    subprocess.run([cli, "ack", task_id], capture_output=True, check=False)
    time.sleep(0.3)
    summary = "[dev-fixture] handled: " + first_line(prompt)
    emit("[dev-fixture] result " + task_id)
    subprocess.run([cli, "result", task_id, summary], capture_output=True, check=False)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--mode", choices=["echo", "auto"], default="echo")
    args = parser.parse_args()
    root = args.root.resolve()
    root.mkdir(parents=True, exist_ok=True)
    pane = os.environ.get("WTABLE_PANE_ID", "unknown")
    log_path = root / (pane + ".input")
    with (root / "fixture-pids.jsonl").open("a") as log:
        log.write(json.dumps(os.getpid()) + "\n")
    if sys.stdin.isatty():
        # Long brief envelopes exceed macOS MAX_CANON in canonical mode.
        # cbreak keeps CR-to-LF translation, so Enter still ends each submission.
        tty.setcbreak(sys.stdin.fileno())
    emit("[dev-fixture] ready pane=%s mode=%s" % (pane, args.mode))

    current = None  # (kind, task_id, [prompt lines])
    for raw in sys.stdin.buffer:
        line = raw.decode("utf-8", "replace").rstrip("\r\n")
        with log_path.open("a") as log:
            log.write(line + "\n")
        if not line:
            continue

        if args.mode == "auto":
            if current is not None:
                kind, task_id, prompt = current
                end = TASK_END if kind == "task" else BRIEF_END
                match = end.match(line)
                if match and match.group(1) == task_id:
                    current = None
                    if kind == "task":
                        run_result(task_id, "\n".join(prompt))
                    continue
                prompt.append(line)
                continue
            match = TASK.match(line)
            if match:
                current = ("task", match.group(1), [])
                emit("[dev-fixture] task " + match.group(1))
                continue
            match = BRIEF.match(line)
            if match:
                current = ("brief", match.group(1), [])
                emit("[dev-fixture] brief " + match.group(1) + " (no ack)")
                continue
            match = STEER.match(line)
            if match:
                emit("[dev-fixture] steer " + match.group(1) + " (ignored)")
                continue
            if line.startswith("{"):
                try:
                    payload = json.loads(line)
                except json.JSONDecodeError:
                    payload = None
                if isinstance(payload, dict) and payload.get("task_id"):
                    task_id = str(payload["task_id"])
                    prompt = str(payload.get("prompt", ""))
                    if "not a delegated task" in str(payload.get("instructions", "")):
                        emit("[dev-fixture] brief " + task_id + " (no ack)")
                    else:
                        emit("[dev-fixture] task " + task_id)
                        run_result(task_id, prompt)
                    continue
            emit("> " + line)
        else:
            emit("> " + line)


if __name__ == "__main__":
    main()
