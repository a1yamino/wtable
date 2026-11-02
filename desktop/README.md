# wtable desktop prototype

Wails desktop shell for the existing wtable daemon. The window combines an
Agent rail, an `xterm.js` terminal backed by the real PTY, and a zoomable
collaboration canvas.

```sh
# From the repository root.
make desktop-dev

# Build desktop/build/bin/wtable.app.
make desktop
```

The desktop binary reuses `WTABLE_HOME`. If no daemon is running, it starts the
same binary in `daemon` mode and keeps panes alive after the window closes.

## Native build requirements

Wails uses cgo on macOS to bridge Go with AppKit and WebKit. The desktop binary
links Cocoa, WebKit, Foundation, UserNotifications, Security and related system
frameworks at runtime, so it must be built on macOS with the Apple SDK. A plain
`CGO_ENABLED=0 go build ./desktop` produces a binary without the desktop
frontend; adding Wails' `desktop,production` tags fails without cgo.

The supported deployment target is macOS 13 or newer. `make desktop` pins the
cgo compiler and linker deployment target to 13.0 to match the Go toolchain and
the application plist.

The prototype currently supports:

- workspace selection
- live PTY output through the raw terminal stream
- terminal input and resize
- creating a foreground shell
- focusing panes from the rail or collaboration canvas
