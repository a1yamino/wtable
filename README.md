# wtable

English | [简体中文](README.zh-CN.md)

A terminal workbench for running multiple coding agents on macOS. Built in Go with its own panes and real PTYs, without tmux or zellij.

This MVP runs terminal CLIs side by side, connects to structured agent sessions through ACP, routes tasks through explicit authorization links, and isolates working directories with Git worktrees.

## Getting started

Requires Go 1.27 or later. Git features require a local Git installation. Agents use their own installed executables and login configurations.

```sh
go build -o bin/wtable ./cmd/wtable
./bin/wtable
```

The first launch starts a local daemon and creates a shell pane. Press `Ctrl-a`, then `d`, to leave the interface while keeping the pane running. Run `./bin/wtable` again to reattach.

State is stored in `~/Library/Application Support/wtable` by default. Set `WTABLE_HOME` to use an independent state directory:

```sh
export WTABLE_HOME="$PWD/.dev/local"
./bin/wtable
```

Use a short absolute path for `WTABLE_HOME` to stay within the macOS Unix socket path limit. Each state directory supports one daemon and one writable TUI client.

## Terminals and agents

Create panes from another terminal. They appear automatically in the running TUI:

```sh
./bin/wtable new shell --dir "$PWD"
./bin/wtable recruit coder -- codex
./bin/wtable recruit reviewer -- claude
./bin/wtable list
```

The command after `--` is executed directly, without shell string parsing by the workbench. Ordinary CLIs use the PTY backend and display their actual terminal interface. Install and authenticate these programs separately.

For an installed ACP v1 adapter, use:

```sh
./bin/wtable recruit assistant --backend acp -- /absolute/path/to/acp-agent
./bin/wtable send assistant 'Analyze this project and suggest an implementation approach'
./bin/wtable tasks
```

ACP tasks are dispatched automatically. Output, tool activity, and permission requests appear in the TUI. Permission requests require an explicit option selection; pressing Enter does not approve them. Without an open TUI, run `permissions` to inspect requests, then use `approve REQUEST OPTION` with one of the returned option IDs.

PTY and ACP are separate backends. Switching the same provider session between them is not supported. The workbench does not infer task completion from terminal silence or output formatting.

## Keyboard controls

Press `Ctrl-a` first, then the indicated key:

| Key | Action |
|---|---|
| `?` | Show help |
| `n` | Create a shell pane |
| `c` | Enter a shell command and create a pane |
| `o` | Focus the next pane |
| `s` | Cycle through grid, side-by-side, and stacked layouts |
| `z` | Zoom the current pane or restore the layout |
| `b` | Show or hide the task bar |
| `[` | Enter scrollback mode; use arrow keys or `j/k` to scroll, `y` to copy visible text, and `q` to leave |
| `t` | Submit a task; Tab switches PTY inject/claim, Ctrl-Q toggles explicit queuing |
| `g` / `r` | Add or remove a directed authorization link by entering the source and destination panes |
| `k` | Open tasks: ↑↓ select, Enter view details, `c` request cancellation, Esc return |
| `x` | Stop the current pane; enter `y` to confirm |
| `R` | Restart the current exited, failed, or interrupted pane in place |
| `d` / `q` | Detach, keeping background sessions running |
| `a` | Send a literal `Ctrl-a` to the current terminal |

Left-click a pane title or its content to focus it. Drag a divider to resize; grid dividers resize whole rows or columns. Proportions last for the current TUI session and reset when changing layouts or adding/removing panes; zooming and restoring preserves them. Mouse focus and resizing are disabled while a dialog is open.

Drag within pane content to select text; releasing the left button copies it to the macOS clipboard. Selection supports multiple lines, reverse dragging, CJK text, and emoji. The selected view stays frozen until Escape or another click restores live output. Selection is limited to the current pane's visible content, without cross-pane selection or automatic scrolling.

The wheel scrolls history in the pane under the pointer and returns to live output at the bottom. When the inner application enables mouse reporting, wheel events are forwarded to it; hold Shift to force workbench scrollback. Clicks and text-selection drags remain local to the workbench.

Input dialogs support Left/Right, Home/End (or Ctrl-A/E), Delete/Backspace, Ctrl-U to delete to the start of the line, and Ctrl-W to delete the previous word. Paste inserts at the cursor. Up/Down browse history for the current input type, stored only for this TUI session. Task input supports Alt-Enter for newlines. Failed submissions keep the draft for editing.

`Ctrl-a t` sends a task and shows its target and actual state. `Ctrl-a k` opens tasks; Enter or a click opens the full prompt/result. Details support scrolling and these actions:

| Detail key | Action |
|---|---|
| `r` / `a` | Retry / reassign, creating a new task with `RetryOf` |
| `v` / `i` | Inspect a one-shot screen snapshot / send raw input |
| `m` | Manually confirm delivery; still await ack/result |
| `d` / `f` | Enter an actual result to mark done / failed |
| `c` / `x` | Request cancellation |
| `C` | Confirm injected work has stopped; requires entering `y` |

Tab switches the task list to the report inbox, which shows unread and undeliverable reports. Enter opens the summary, then Enter opens the parent task subject to its existing access rules. Raw input supports `<Enter>`, `<Esc>`, `<Ctrl-C>`, and arrow-key markers without appending Return automatically.

Exited panes show a restart control: focus one and press Enter, click the control, or use `Ctrl-a R`. Restart retains the pane ID, name, and authorization edges and reruns its original command. Restoring PTY chat history depends on whether that command includes the provider's resume arguments.

Ordinary keystrokes go directly to the PTY. Multiline paste requires confirmation when the application has not enabled bracketed paste. Copying uses macOS `pbcopy`.

## Task collaboration

The human controller can send tasks to any running pane. An agent inside a pane can send tasks only to explicitly linked destinations:

```sh
./bin/wtable connect coder reviewer
./bin/wtable send --key review-1 reviewer 'Review the current changes'
./bin/wtable tasks
./bin/wtable task TASK_ID
./bin/wtable wait --timeout 5m TASK_ID
```

Links are directional. Authorize each direction separately for two-way collaboration. `disconnect` cancels tasks on that link that have not yet been dispatched; it does not undo actions already taken.

### Delivery and acknowledgement

Human-to-PTY tasks default to `inject`; ACP targets use `prompt`. Agent-to-PTY tasks retain `claim` by default and can explicitly request `--delivery inject`. Existing PTY tasks without a delivery field retain claim semantics.

```sh
./bin/wtable send --delivery inject coder 'Review these changes'
./bin/wtable send --delivery claim coder 'Work to claim explicitly'
./bin/wtable send --queue coder 'Deliver once the gate allows it'
```

Injection checks process liveness, blocked text, output settling, and previous unfinished injection. Defaults are 400ms settling, 3s verification, and 90s ACK timeout. Direct `codex`, `claude`, and `agy` commands use built-in blocked-text patterns. `gemini`, `opencode`, `grok`, and other programs or wrappers require explicit configuration; they are not validated built-in adapters:

```sh
./bin/wtable recruit coder --blocked-regex '(?i)(Log in|permission required)' \
  --tail-lines 12 --settle 400ms --verify 3s --ack-timeout 90s -- codex
```

Rejection returns `blocked:<line>`, `busy:<task>`, or `not-ready` without writing input. Explicit `--queue` retains work until the gate permits delivery. Delivered, accepted, and cancelling injections all hold the busy gate. ACK does not release it, and ACK timeout does not end accepted work. Bracketed-paste applications receive a complete envelope in one submission; other applications receive a single-line JSON envelope with the task ID, prompt, and acknowledgement instructions. The target terminal may limit long input lines; the complete prompt remains available through `task TASK_ID`.

`delivered` proves a write or protocol dispatch, not that the agent has accepted work. A PTY receiver calls `ack` to become `accepted`, or submits a result directly as evidence. Echo-only verification records `write-unconfirmed`. ACK timeout becomes `unknown`, never an automatic retry. ACP becomes accepted on the first update for the active turn and completes on the prompt response; CLI `result` is unnecessary.

Each pane receives `WTABLE_CLI`, `WTABLE_PANE_ID`, `WTABLE_PEER_TOKEN`, and `WTABLE_HOME`. Run these commands inside the agent pane without copying or printing credentials:

```sh
"$WTABLE_CLI" ack TASK_ID
"$WTABLE_CLI" result TASK_ID 'Actual result'
# Claim mode takes one queued task at a time and retains the running state value.
"$WTABLE_CLI" next --timeout 30s
"$WTABLE_CLI" send --delivery inject reviewer 'Review these changes'
```

Results can also be submitted through stdin. Delivered, accepted, unknown, cancelled, failed, and legacy running tasks accept late results; completed tasks reject overwrites. Reusing a sender's deduplication key with identical content returns the original task; conflicting content is rejected.

### Reports and follow-up work

```sh
./bin/wtable send --on-complete notify --report-to reviewer coder 'Analyze and notify reviewer'
./bin/wtable send --on-complete handoff --report-to reviewer coder 'Prepare work for reviewer'
./bin/wtable reports
./bin/wtable report REPORT_ID
./bin/wtable report seen REPORT_ID
```

`notify` writes only an inbox report: no terminal input and no new task. `handoff` creates a task with `ParentTask`, inheriting the parent's `From` and current `Origin` and applying normal delivery checks. Authorization always uses the original `From`; the parent's `To` and the report's `From` identify the executor. No report target defaults to `none`; specifying a target without a mode defaults to `notify`.

The original sender's grant to the report target is checked at creation and completion. A revoked grant produces a readable `undeliverable` report. Completion and the single report for each parent are persisted together. Summaries are capped at 4KiB without a `ReportMode: full` option; full parent results remain accessible only to the original participants and controller.

### Manual recovery

```sh
./bin/wtable task confirm TASK_ID
./bin/wtable task done TASK_ID 'Actual outcome confirmed by a person'
./bin/wtable task fail TASK_ID 'Failure reason'
./bin/wtable task retry --to reviewer TASK_ID
./bin/wtable cancel TASK_ID
# Run only after confirming the PTY work has stopped.
./bin/wtable cancel --confirmed TASK_ID
```

Retry accepts failed, unknown, cancelled, or write-unconfirmed tasks, preserves the original record, and links a new task through `RetryOf`. Queued or executing tasks cannot be retried. Retrying write-unconfirmed work marks the old record unknown and still accepts its late result; explicit retry can duplicate work.

Queued tasks cancel immediately; ACP cancellation awaits protocol acknowledgement. Injected PTY work first receives an interrupt and enters `cancelling`, then requires human stop confirmation to become `cancelled`. Terminal history and previous file effects are not erased. Legacy running claim tasks cannot be cancelled reliably; inspect or stop the pane instead.

Links restrict workbench API access, not direct file access by the same operating system user. Worktrees isolate working directories; they are not an operating system sandbox.

## Git worktrees

```sh
./bin/wtable layer create feature-a --dir /path/to/repo
./bin/wtable layer list
./bin/wtable recruit coder-a --layer feature-a -- codex
```

The default base is `origin/main` when it exists, otherwise `HEAD`. Use `--base REF` to choose another reference. The new branch is named `wtable/layer/NAME`, and its working directory is stored beneath the workbench state directory.

Run `wtable changes` or `wtable diff [PATH]` from the target repository to inspect changes. The MVP does not automatically commit, merge, delete, or archive worktrees.

## Upgrading, recovery, and diagnostics

P0 is version `0.2.0`. The CLI checks the daemon version rather than silently sending new delivery options to an old daemon. Use a separate `WTABLE_HOME` to keep existing sessions running. To reuse a state directory, finish its old sessions, run `shutdown`, then start the new version. Reattaching does not upgrade the daemon; shutdown stops every pane in that state directory.

```sh
./bin/wtable diag
./bin/wtable stop coder
./bin/wtable restart coder
./bin/wtable shutdown
```

- Detaching the TUI leaves the daemon, PTYs, and agents running.
- Restarting the daemon restores pane configuration, tasks, results, and worktree records. It does not resurrect the original PTY processes or automatically replay tasks with unknown outcomes.
- `restart` is explicit. When an ACP session ID has been saved, recovery is attempted only if the adapter supports `loadSession`. Otherwise, the command reports an error; create a pane with a new name to start a fresh session.
- `shutdown` stops the daemon and all panes for that state directory. Logs are written to `daemon.log` in the state directory.

## Validation

```sh
go test -race ./internal/terminal ./internal/agent ./internal/server
go vet ./...
go build -o bin/wtable ./cmd/wtable
python3 scripts/smoke.py --binary bin/wtable --output .dev/test-results/smoke.json
python3 scripts/p0_smoke.py --binary bin/wtable --output .dev/test-results/p0.json
```

The smoke script uses a real daemon, PTYs, SQLite, and temporary Git repositories. It asserts authorization behavior, task deduplication, approval effects on files, worktree isolation, and crash recovery. ACP checks use a deterministic local fixture and make no external model calls. The script cleans up its own processes and temporary directories and saves a JSON validation report.

External provider authentication, adapter compatibility, and session recovery must be validated against the actual installed versions. Passing fixture tests does not establish that compatibility.

## Current limitations

- Validated on macOS only. Remote connections and additional observer clients are not supported.
- Scrollback retains up to 10,000 physical lines. Resizing does not reflow historical output. Full VT conformance has not been established.
- The TUI polls full cell snapshots; cell deltas and event replay are not implemented. The original performance targets have not been validated.
- Mouse selection and copying visible content are supported, alongside keyboard scrollback and copying the visible screen. Wheel forwarding to mouse-aware applications is supported; click/drag forwarding and the OSC 52 clipboard protocol are not supported.
- Pane arrangement, size proportions, focus, and zoom belong to the current TUI session. Reattaching lays out the existing panes again.
- Up to 32 panes and 2,000 task records are retained. The structured chat view is bounded; complete task results are stored separately. There is no built-in cleanup command.
- Notes, role management, an embedded editor, account pools, scheduled tasks, images, and browser portals are planned for later versions.

P0 delivery and reporting are implemented. The P1 creation panel and brief onboarding, and P2 Graph, are not yet implemented. `docs/`, `.dev/`, and `bin/` remain outside the Git allowlist.
