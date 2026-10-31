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

The first launch starts a local daemon and opens the PTY foreground picker; one `Enter` creates the agent you talk to. Press `Ctrl-a`, then `q`, to leave the interface while keeping panes running. Run `./bin/wtable` again to reattach and restore the last focused pane.

State is stored in `~/Library/Application Support/wtable` by default. Set `WTABLE_HOME` to use an independent state directory:

```sh
export WTABLE_HOME="$PWD/.dev/local"
./bin/wtable
```

Use a short absolute path for `WTABLE_HOME` to stay within the macOS Unix socket path limit. Each state directory supports one daemon and one writable TUI client.

## Terminals and agents

Create panes from another terminal. New sessions run in the background and appear in the rail; press `Ctrl-a a` and choose one to open it:

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

## Control panel and agent onboarding

The left rail is always visible. `Ctrl-a a` focuses it; `Enter` opens the selected agent or inbox item, `Space` opens its action list, `n` creates, `p` moves a pane into the utility slot, `x` removes, and `/` filters. For the full Agents / Edges / Tasks / Layers administration panel, press `Ctrl-a Space` and choose 管理面板. Tab / Shift-Tab switches pages, arrow keys or the wheel select rows, and Escape goes back. Enter or Space opens the selected row's action list, so page-specific letters do not have to be memorised.

| Page | Actions |
|---|---|
| Agents | `a` add, `e` edit name/role/account, `d` remove, Enter action list (first entry opens), `p` put in / take out of the utility slot, `v` peek, `B` preview brief, `b` resend brief, `O` roles, `l` diagnostics, `R` restart |
| Edges | `a` choose a source and multiple targets, `x` delete, `t` filter by the focused pane |
| Tasks | `s` send to multiple targets, Enter action list (details/retry/cancel), `r` retry, `x` cancel, `b` reports |
| Layers | `n` create a worktree, Enter use its directory for new panes |

Creation includes preset, PTY/ACP backend, directory, layer, role, account profile, brief injection, and outgoing peer links. Custom commands support quoting and escaping to group arguments but do not expand variables or execute shell syntax. Specify `/bin/zsh -lc 'command'` explicitly when a shell is needed.

Built-in presets are Shell, Codex PTY, Codex ACP, Claude PTY, and Agy PTY. Discovery checks local executables and runs bounded `--version` probes only for known programs without changing login state. A missing `codex-acp` adapter is shown as unavailable and never downloaded automatically. Finding a binary does not establish authentication or protocol compatibility.

```sh
./bin/wtable presets
./bin/wtable recruit coder --preset codex-pty --role 'Implement and validate' --brief
./bin/wtable recruit reviewer --preset codex-acp --role 'Read-only review' --brief --connect coder
./bin/wtable brief coder
./bin/wtable brief send coder
./bin/wtable pane edit --role 'Own retry logic' coder
```

The panel enables briefs by default for non-Shell presets; the CLI requires `--brief`. Briefs are at most 2048 UTF-8 bytes, stored in `<WTABLE_HOME>/briefs/<pane>.md` with mode `0600`. Enabling one exposes `WTABLE_BRIEF` and queues it through the P0 delivery gate. Content includes identity, directory, role, authorized outgoing peers, and reporting instructions without credentials. PTY instructions require ack/result; ACP uses automatic protocol completion. Restart does not automatically replay a previous brief; resend explicitly when needed.

ACP panes have a persistent composer at the bottom. Enter does exactly what the line under it says: it sends when the pane is idle and queues (showing the position) when a turn is running. `Ctrl-J` inserts a newline, `Esc` clears the draft, and `Ctrl-a Enter` is the two-step "interrupt the current turn, then send". Queued chat turns are marked `chat` and stay out of the delegation task list. Permission requests never take the keyboard: the card appears above the composer, typing continues into the pane, `Ctrl-a p` focuses the card, numbers choose an option, `r` rejects, `d` postpones, and `Esc` only collapses the card (the request stays pending). Enter never approves.

Running panes can change name and role. Account changes require an exited, failed, or interrupted pane; command, backend, and directory changes require recreation. Removal requires explicit `y` confirmation, stops the process, cancels related queued work, and removes incident edges while preserving task/report/brief history. Nonzero exit shows failed state, exit code, a redacted tail summary, and log path; exit code 0 remains exited.

### Local presets and account profiles

Additional presets can be configured in `<WTABLE_HOME>/presets.json` with IDs distinct from built-ins. Custom executables are checked for availability but are not launched merely to probe their versions:

```json
[{"id":"custom-agent","name":"Custom agent","backend":"pty","command":["/absolute/path/to/agent","--flag"]}]
```

Account profiles are read from `<WTABLE_HOME>/accounts.json`, without account scanning or login. The UI, snapshots, and briefs contain profile names rather than their environment values; `WTABLE_*` overrides are rejected:

```json
[{"name":"work","env":{"CODEX_HOME":"/absolute/path/to/codex-profile"}}]
```

Editing configuration does not change a running process's environment. Default layer selection lasts only for the current TUI session.

## Rail, canvas, utility slot, and inbox

The workbench has one main canvas, one optional utility slot, and a permanent left rail. The rail has three stable groups: 需要你 (approvals, exits, blocked delivery, unread replies, finished human tasks), 前台 (at most one PTY foreground agent), and Workers. Selecting any agent replaces the main canvas; it never splits the layout. `Ctrl-a s` opens one utility slot on the right (default: a shell whose cwd follows the main pane) and closes it again; closing only removes it from the canvas, so the process keeps running in the rail and the next `Ctrl-a s` reuses it. There are no tabs, park, zoom, layout trees, or split panes.

A foreground agent is the one you talk to and is always a PTY; ACP exists only as a worker backend. The API rejects `foreground + acp` and a second foreground. First launch opens a one-Enter PTY preset picker (ACP is absent); if no PTY preset is installed, the picker offers "recheck" and "temporary shell" instead of a dead end. On reattach the server restores the last focused pane, falling back to a running pane; the utility slot is not restored.

Each rail row shows exactly one primary signal: approval (`⏳`), exit (`✕`), unread (`◉`), queue (`⏸`), then running/idle. The canvas title uses fixed slots and only shows non-zero counts (`▌ coder · ◐ running · 12s · ⏸2 ⏳1 ◉3`). ACP replies count as unread only while another pane is focused, and opening a pane is what clears its unread items. Background PTYs retain their last size; only the main canvas and utility request frames and resizes. Lists show recent output, activity, readiness, endpoint type and denied-operation counts. Creation forms warn about shared directories/layers and accounts; `L` creates an isolated layer and returns to the form. Readiness describes the delivery gate, not task acknowledgement.

`state.get` now distinguishes omitted/null `ids` (all frames), `[]` (metadata only), and a nonempty list (selected frames). `frames: false` always suppresses frames. `pane.peek` returns text without constructing a frame.

Agents `O` opens role template CRUD. Templates live at `<WTABLE_HOME>/roles/<name>.md`, with a `---` metadata header containing `name`, optional `preset`, `backend`, and `cwd`, followed by Markdown instructions. Launch snapshots the instructions and their SHA-256 hash; template edits/deletion and pane restarts retain the existing snapshot. Plain renames preserve it. Selecting a different template captures new instructions; explicitly resend the brief to deliver them. Free-text `--role` remains supported and the entire brief is still capped at 2 KiB.

```sh
./bin/wtable role put --preset codex-pty reviewer ./review-instructions.md
./bin/wtable role list
./bin/wtable recruit reviewer --role-template reviewer --brief
./bin/wtable pane edit --role-template reviewer existing-pane
./bin/wtable role delete reviewer
```

Account `env` values accept legacy strings or exactly one of `literal`, `env_ref`, and `keychain_ref`. References resolve at launch and resolved values are never written back. On macOS, keychain references read existing items using `/usr/bin/security`; wtable does not create or modify items.

```json
[{
  "name": "gateway",
  "endpoint": "compatible",
  "env": {
    "ANTHROPIC_BASE_URL": {"literal": "https://gateway.example"},
    "ANTHROPIC_AUTH_TOKEN": {"env_ref": "WORK_GATEWAY_TOKEN"},
    "OTHER_API_KEY": {"keychain_ref": {"service": "work-agent", "account": "me"}}
  }
}]
```

Endpoint labels are `official` (default), `compatible`, or `custom`. Nonofficial labels display a compatibility notice, not proof of compatibility. Missing references block launch with errors that do not expose values. Account environment changes do not affect running processes.

`wtable audit` returns the latest 256 denied operations: caller, target, method, and time, without request bodies or credentials. Pane metadata retains cumulative counts. Direct grants already prevent agents from assigning to themselves. Content provenance and `--no-relay` are not implemented.

## Collaboration Graph

Press `Ctrl-a G` to open the live graph. Arrows point from sender to recipient. Nodes in a cycle share a level; names determine order within a level. Dashed `you → pane` edges represent the human entry point and cannot be selected or deleted.

Use `j/k` to select nodes and Enter to open the node's action list; its first entry, 打开, focuses the pane in focus mode. Press `e` to switch between nodes and real grant edges; `x` asks to delete the selected grant. Only `y` confirms deletion, cancelling queued tasks in that direction; `n` or Escape cancels, and Enter does not confirm. Arrow keys pan the graph. Related tasks and reports appear below with ID, direction, state, and unread status; reports carry a `report` label. Page Up/Down scrolls the list. Unread counts and their `bell` marker belong only to the recipient. Node boxes show names, and selected-edge details show the `active` count. Escape returns to the pane view.

The controller can request `state.get` with `graph: true` for `graph.nodes` and `graph.edges`; add `frames: false` to skip terminal frames. This projection does not change authorization.

## Keyboard controls

The only leader key is `Ctrl-a` (no Alt combinations). Press it twice to send a literal `Ctrl-a` to the terminal.

| Key | Action |
|---|---|
| `Ctrl-a j` / `Ctrl-a k`, or `Ctrl-a ↓/↑` | next / previous agent (rail order) |
| `Ctrl-a 1..9` | jump to the N-th rail agent |
| `Ctrl-a h` / `Ctrl-a l`, or `Ctrl-a ←/→` | focus the main canvas / utility slot |
| `Ctrl-a a` | focus the rail (`Esc` returns; expands the drawer on narrow terminals) |
| `Ctrl-a p` | focus the next pending approval |
| `Ctrl-a s` | open / close the utility slot |
| `Ctrl-a t` | task composer: queue, steer (only when declared and busy), or interrupt-then-send |
| `Ctrl-a Space` | command palette |
| `Ctrl-a ?` | context help (three lines, with the glyph legend) |
| `Ctrl-a q` (or `d`) | detach; background sessions keep running |
| `Ctrl-a Ctrl-a` | send a literal `Ctrl-a` |

In the rail: `↑↓` select, `Enter` open, `Space` actions, `n` create, `e` edit, `p` utility slot, `x` remove (type `y`), `/` filter, `Esc` back. On an approval card: `1..9` choose, `r` reject, `d` postpone, `[` / `]` switch request, `Enter` details, `Esc` collapse. `R` rejects all requests in the pane after a `y` confirmation; `A` allows all only when every request shares the same tool and `allow_always` option (also two-step).

The composer uses `Enter` to send/queue, `Ctrl-J` for a newline, `Esc` to clear, and `Ctrl-a Enter` to interrupt-then-send after a second confirmation. The command palette (`Ctrl-a Space`) carries the low-frequency actions: graph, task list, queue, management panel, utility direction, restart/stop, and detach. `Ctrl-a G` (graph) and `Ctrl-a b` (tasks) remain as compatibility aliases.

Click a rail row to open that agent; click a pane, approval card, or composer to focus it. Drag the single divider between the main canvas and the utility slot to change the ratio. Mouse focus and resizing are disabled while a dialog is open.

Drag within pane content to select text; releasing the left button copies it to the macOS clipboard. Selection supports multiple lines, reverse dragging, CJK text, and emoji. The selected view stays frozen until Escape or another click restores live output. Selection is limited to the current pane's visible content, without cross-pane selection or automatic scrolling.

The wheel scrolls history in the pane under the pointer and returns to live output at the bottom. When the inner application enables mouse reporting, wheel events are forwarded to it; hold Shift to force workbench scrollback. Clicks and text-selection drags remain local to the workbench.

Input dialogs support Left/Right, Home/End (or Ctrl-A/E), Delete/Backspace, Ctrl-U to delete to the start of the line, and Ctrl-W to delete the previous word. Paste inserts at the cursor. Up/Down browse history for the current input type, stored only for this TUI session. Task input supports Alt-Enter for newlines. Failed submissions keep the draft for editing.

`Ctrl-a t` opens a one-line task composer for the current pane; `Tab` cycles the available modes (queue, steer when the target declares `PushSteer` and is busy, interrupt-then-send), and the mode bar spells out what Enter will do. The multi-target form remains under the management panel's Tasks page. `Ctrl-a b` opens Tasks; Enter opens the action list, whose first entry shows the full prompt/result. Details support scrolling and these actions:

| Detail key | Action |
|---|---|
| `r` / `a` | Retry / reassign, creating a new task with `RetryOf` |
| `v` / `i` | Inspect a one-shot screen snapshot / send raw input |
| `m` | Manually confirm delivery; still await ack/result |
| `d` / `f` | Enter an actual result to mark done / failed |
| `c` / `x` | Request cancellation |
| `C` | Confirm injected work has stopped; requires entering `y` |

Press `b` on the Tasks page to open the report inbox, which shows unread and undeliverable reports. Enter opens the summary, then Enter opens the parent task subject to its existing access rules. Raw input supports `<Enter>`, `<Esc>`, `<Ctrl-C>`, and arrow-key markers without appending Return automatically.

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

Delivery defaults follow the target's capabilities: ACP targets use `prompt`, PTY targets use `push`, and the sender no longer changes that choice. `--delivery pull` (legacy `claim`) remains available only for panes that declare pull; `inject` is accepted as an alias for `push`. Queued tasks without a delivery field are treated as `push`.

```sh
./bin/wtable send --delivery push coder 'Review these changes'
./bin/wtable send --delivery pull coder 'Work to claim explicitly'
./bin/wtable send --steer coder 'Also check the tests'
./bin/wtable send --queue coder 'Deliver once the gate allows it'
```

Steering is accepted only when the target is currently executing a turn and declares `push_steer`. A pane waiting for permission is not considered free for steering. Sending to an idle pane is rejected rather than queued; if the turn ends before delivery, a queued steer is downgraded to ordinary `push` so it cannot be injected into a later, unrelated turn.

Injection checks process liveness, blocked text, output settling, and previous unfinished injection. Defaults are 400ms settling, 3s verification, and 90s ACK timeout. Direct `codex`, `claude`, and `agy` commands use built-in blocked-text patterns. `gemini`, `opencode`, `grok`, and other programs or wrappers require explicit configuration; they are not validated built-in adapters:

```sh
./bin/wtable recruit coder --blocked-regex '(?i)(Log in|permission required)' \
  --tail-lines 12 --settle 400ms --verify 3s --ack-timeout 90s -- codex
```

Rejection returns `blocked:<line>`, `busy:<task>`, or `not-ready` without writing input. A linked agent sender sees only the `blocked` category; the matched terminal line (redacted and clipped) is reserved for the controller. Explicit `--queue` retains work until the gate permits delivery. Delivered, accepted, and cancelling injections all hold the busy gate. ACK does not release it, and ACK timeout does not end accepted work. Bracketed-paste applications receive a complete envelope in one submission; other applications receive a single-line JSON envelope with the task ID, prompt, and acknowledgement instructions. Terminal control characters in task text (including ESC and CR) are rendered as Unicode control pictures, so a prompt cannot terminate bracketed paste early or submit extra lines. The target terminal may limit long input lines; the complete prompt remains available through `task TASK_ID`.

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

The original sender's grant to the report target is checked at creation and completion. A revoked grant produces a readable `undeliverable` report. Completion and the single report for each parent are persisted together. Unknown or cancelled tasks update the same report with a status notification and never trigger handoff. Late final results update that report and restore its unread flag; only the first final result can trigger handoff. The report `task_state` field identifies the parent task state. Summaries are capped at 4KiB without a `ReportMode: full` option; full parent results remain accessible only to the original participants and controller.

### Manual recovery

```sh
./bin/wtable task confirm TASK_ID
./bin/wtable task done TASK_ID 'Actual outcome confirmed by a person'
./bin/wtable task fail TASK_ID 'Failure reason'
./bin/wtable task retry --to reviewer TASK_ID
./bin/wtable cancel TASK_ID
# Run only after confirming the PTY work has stopped. Also releases a running
# pull claim so the pane queue can continue.
./bin/wtable cancel --confirmed TASK_ID
```

Retry accepts failed, unknown, cancelled, or write-unconfirmed tasks, preserves the original record, and links a new task through `RetryOf`. Queued or executing tasks cannot be retried. Retrying write-unconfirmed work marks the old record unknown and still accepts its late result; explicit retry can duplicate work.

Queued tasks cancel immediately; ACP cancellation awaits protocol acknowledgement. Injected PTY work first receives an interrupt and enters `cancelling`, then requires human stop confirmation to become `cancelled`. Terminal history and previous file effects are not erased. A running `pull` claim cannot be stopped through the workbench; after confirming the work has stopped, `cancel --confirmed` releases the pane queue by marking it `cancelled`.

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

The current version is `0.4.0`. The CLI checks the daemon version rather than silently sending new delivery options to an old daemon. Use a separate `WTABLE_HOME` to keep existing sessions running. To reuse a state directory, finish its old sessions, run `shutdown`, then start the new version. Reattaching does not upgrade the daemon; shutdown stops every pane in that state directory.

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
python3 scripts/p1_smoke.py --binary bin/wtable --output .dev/test-results/p1.json
python3 scripts/p2_smoke.py --binary bin/wtable --output .dev/test-results/p2.json
python3 scripts/scheduler_smoke.py --binary bin/wtable --output .dev/test-results/scheduler.json
python3 scripts/view_smoke.py --binary bin/wtable --output .dev/test-results/view.json
```

The smoke script uses a real daemon, PTYs, SQLite, and temporary Git repositories. It asserts authorization behavior, task deduplication, approval effects on files, worktree isolation, and crash recovery. ACP checks use a deterministic local fixture and make no external model calls. The script cleans up its own processes and temporary directories and saves a JSON validation report.

External provider authentication, adapter compatibility, and session recovery must be validated against the actual installed versions. Passing fixture tests does not establish that compatibility.

## Current limitations

- Validated on macOS only. Remote connections and additional observer clients are not supported.
- Scrollback retains up to 10,000 physical lines. Resizing does not reflow historical output. Full VT conformance has not been established.
- The TUI polls full cell snapshots; cell deltas and event replay are not implemented. The original performance targets have not been validated.
- Mouse selection and copying visible content are supported, alongside keyboard scrollback and copying the visible screen. Wheel forwarding to mouse-aware applications is supported; click/drag forwarding and the OSC 52 clipboard protocol are not supported.
- The main canvas selection is persisted as `last_focus` and restored on attach; the utility slot and its ratio are not persisted. The rail order is stable creation order.
- Up to 32 panes and 2,000 task records are retained, with a 64 MiB aggregate prompt/result budget. Panes can be removed from Agents; task history has no cleanup command. The structured chat view is bounded, while complete task results are stored separately.
- Notes, an embedded editor, account pools, scheduled tasks, images, and browser portals are planned for later versions.

P0 delivery/reporting, P1 control-panel onboarding, and P2 Graph v1 are implemented. `docs/`, `.dev/`, and `bin/` remain outside the Git allowlist.
