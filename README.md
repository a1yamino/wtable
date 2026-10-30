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
| `t` | Submit a task to the current pane; ACP dispatches automatically, while PTY agents must claim it |
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

`Ctrl-a t` sends a task to the focused pane, displays queue feedback with the target name, and opens the task bar. PTY agents still need to claim tasks explicitly. `Ctrl-a k` opens the task list; click a row or press Enter to see the complete prompt and result. Arrow keys, page keys, and the wheel scroll details. Press `c` to cancel queued tasks or request cancellation of running ACP tasks. Running PTY tasks cannot be cancelled reliably; the UI reports this restriction.

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

Agents running in PTYs must explicitly use workbench commands to receive tasks and report results. Each pane receives `WTABLE_CLI`, `WTABLE_PANE_ID`, `WTABLE_PEER_TOKEN`, and `WTABLE_HOME` in its environment. Instructions for a CLI agent can include:

```sh
# Run inside a pane created by wtable. Do not copy or print credentials.
"$WTABLE_CLI" peers
"$WTABLE_CLI" next --timeout 30s
"$WTABLE_CLI" result TASK_ID 'Review complete. Findings follow...'
"$WTABLE_CLI" send reviewer 'Please review these changes'
```

`next` returns task JSON, or `null` when no task is available. A pane can claim one task at a time. To submit a result through standard input, use `printf '%s' "$result" | "$WTABLE_CLI" result TASK_ID -`.

The same sender, `--key`, and task content return the original task. Reusing a key with different content is rejected. A dispatched task whose outcome cannot be confirmed is marked `unknown` and is not automatically redelivered. `cancelling` means cancellation has been requested but the agent has not yet confirmed it.

Links restrict workbench API access, not direct file access by the same operating system user. Worktrees isolate working directories; they are not an operating system sandbox.

## Git worktrees

```sh
./bin/wtable layer create feature-a --dir /path/to/repo
./bin/wtable layer list
./bin/wtable recruit coder-a --layer feature-a -- codex
```

The default base is `origin/main` when it exists, otherwise `HEAD`. Use `--base REF` to choose another reference. The new branch is named `wtable/layer/NAME`, and its working directory is stored beneath the workbench state directory.

Run `wtable changes` or `wtable diff [PATH]` from the target repository to inspect changes. The MVP does not automatically commit, merge, delete, or archive worktrees.

## Recovery and diagnostics

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
go test -race ./internal/terminal ./internal/agent
go vet ./...
go build -o bin/wtable ./cmd/wtable
python3 scripts/smoke.py --binary bin/wtable --output .dev/test-results/smoke.json
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

The original plan and current scope notes are kept locally in `.dev/plan.md`. Both `.dev/` and `bin/` are ignored by Git.
