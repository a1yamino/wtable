# wtable native desktop

A Go-native MyGo application for the existing wtable daemon. MyGo draws the
workspace rail, Focus view, forms, and spatial Board; its Ghostty terminal
plugin renders the daemon's PTYs. No WebView, JavaScript, React, or frontend
build is used. The Wails v3 implementation is retained separately on branch
`pluto/feat/wails3` for comparison.

```sh
# From the repository root.
make desktop-mygo-dev

# Build desktop-mygo/build/darwin-arm64/wtable.app on Apple Silicon.
make desktop-mygo
```

The desktop binary reuses `WTABLE_HOME`. If no daemon is running, it starts the
same binary in `daemon` mode and keeps panes alive after the window closes.
All other command arguments use the same CLI implementation as `cmd/wtable`,
including the `WTABLE_CLI` commands agents invoke. Only one desktop or TUI may
hold the daemon's writable UI lease at a time.

## Native build requirements

Use Go 1.27.3 or later and macOS 13 or later. MyGo v0.7.4 and its CLI are pinned
in the root `go.mod`; `go tool mygo` runs that version. Both desktop Make targets
set `CGO_ENABLED=0`. The package command includes and signs libghostty-vt with
the application, so the packaged app does not download its terminal library.
For `go run` and tests, the plugin downloads and verifies the library in the
user's MyGo cache on first use.

`make desktop-mygo` produces an ad-hoc signed local app without a disk image.
The output directory is `desktop-mygo/build/darwin-<arch>/`. To create a DMG, run
`cd desktop-mygo && go tool mygo build`. Distribution signing and notarization are
not configured. The daemon and CLI retain their existing Unix requirements;
the framework's cross-platform support does not make this application a
Windows application.

The desktop UI currently supports:

- collapsible workspace groups with per-agent backend and state labels
- keyboard-focused search across Agent names, backends, and roles
- a focused main-agent view with one resizable auxiliary terminal
- a spatial Board where agents can be moved without overlapping
- multiple terminals expanded and resized inside Board nodes
- creating and deleting shell or installed-agent panes
- editing pane names, roles, accounts, and the main-agent designation
- bidirectional authorization links with per-direction control
- locally saved global topology templates with Board preview and overrides
- live PTY output, input, resize, and automatic workspace refresh

Each open terminal has an independent output connection and a fixed pane/workspace
identity. Focus keeps its main and auxiliary terminals; Board keeps its expanded
nodes. Closing an auxiliary view, collapsing a node, switching views, or opening
a template preview releases terminal instances that are no longer displayed.
The daemon's processes keep running, and reopening a terminal restores its
history and screen from the daemon.

Input and resize share the connection holding `ui.attach`. A resize
is followed by an atomic terminal attachment at the new grid size; the view
replays history and the authoritative screen before resuming its byte cursor.
Closing a terminal connection ends its subscription without stopping the pane.

MyGo v0.7.4 releases view-owned native input helpers when a closed terminal's
view builds again. The client therefore queues terminals only after `Close`
finishes and consumes them in a hidden UI cleanup pass. This pass takes no
layout space or input, and does not keep focus handles for discarded terminals.
If the window is occluded, cleanup waits for its next build; active connections
are already closed. Closing the application's only window ends the process,
which also releases any final native resources awaiting a cleanup frame.

Desktop preferences and global templates are saved atomically in
`$WTABLE_HOME/desktop.json` (or the default state directory). This is a new
native format; browser localStorage is not imported. Templates retain each
authorization direction separately. Applying a template asks before stopping
and replacing the current workspace's agents; it does not restore those agents
if creation fails.

Use Cmd/Ctrl+1 for Focus, Cmd/Ctrl+2 for Board, Cmd/Ctrl+T for templates, and
Cmd/Ctrl+K to search Agents. Escape closes the current configuration panel and
returns focus to the workbench. Agent forms keep account and directory options
under Advanced settings; editing an Agent expands those options.

Configuration panels overlay the terminal without resizing its grid. The
template inspector keeps the Board interactive, and its camera controls frame
nodes in the space beside the inspector. Success messages disappear after a
few seconds; errors remain until dismissed. Neither changes the terminal layout.

Board toolbar controls and gestures on blank canvas change the camera without
resizing its terminal grid. Inside a terminal, Ghostty's native shortcuts apply,
including Cmd/Ctrl plus mouse wheel to change the terminal font size.

## Validation

```sh
go vet ./desktop-mygo ./cmd/wtable ./internal/cli
go tool mygo vet ./desktop-mygo
go test -race ./desktop-mygo
make desktop-mygo
```

The terminal integration test runs a real daemon and Ghostty in an isolated
state directory. It checks input, independent pane streams, workspace identity,
resize recovery, and detaching/reopening without killing the pane.

To drive a real native window, build the opt-in automation target and run the
smoke script from the repository root:

```sh
cd desktop-mygo
GOFLAGS='-tags=e2e' CGO_ENABLED=0 go tool mygo build -debug -skip-dmg -o ../.dev/native-e2e
cd ..
python3 scripts/native_smoke.py \
  --binary .dev/native-e2e/darwin-arm64/wtable.app/Contents/MacOS/wtable \
  --output .dev/test-results/native.json
```

The script requires a macOS desktop session. It creates an isolated state
directory and records assertions and screenshots. Only builds with the `e2e`
tag contain the automation plugin; production builds expose no automation
socket.

Structured task dispatch, Markdown session rendering, logs, reusable execution
order, and freely arranged multi-terminal Focus layouts are intentionally
deferred.
