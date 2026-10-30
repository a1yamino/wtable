// Package terminal owns a pane's PTY process and virtual terminal state.
package terminal

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"wtable/internal/model"
)

const historyLimit = 10000

type Config struct {
	Command  []string
	Cwd      string
	Env      []string
	Cols     int
	Rows     int
	OnExit   func(code int, err error)
	OnChange func()
}

// Terminal serializes VT mutations and snapshots. PTY input and emulator
// responses are written outside that lock: a device query can otherwise
// deadlock the output parser while it is waiting for its reply to be consumed.
type Terminal struct {
	mu       sync.Mutex
	vt       *vt.Emulator
	modes    map[ansi.Mode]bool
	revision uint64
	pty      *os.File
	cmd      *exec.Cmd
	cfg      Config
	inputMu  sync.Mutex
	stopOnce sync.Once
	stopped  chan struct{}
	finished chan struct{}
	readDone chan struct{}
	replies  chan []byte
	errMu    sync.Mutex
	failure  error
}

func Start(ctx context.Context, cfg Config) (*Terminal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(cfg.Command) == 0 || cfg.Command[0] == "" {
		return nil, errors.New("terminal command is empty")
	}
	if cfg.Cols <= 0 {
		cfg.Cols = 80
	}
	if cfg.Rows <= 0 {
		cfg.Rows = 24
	}
	if err := validSize(cfg.Cols, cfg.Rows); err != nil {
		return nil, err
	}
	cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...)
	cmd.Dir = cfg.Cwd
	cmd.Env = append([]string(nil), cfg.Env...)
	if cfg.Env == nil {
		cmd.Env = os.Environ()
	}
	// os/exec retains the last value for duplicate environment keys.
	cmd.Env = append(cmd.Env, "TERM=xterm-256color", "COLORTERM=truecolor")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cfg.Cols), Rows: uint16(cfg.Rows)})
	if err != nil {
		return nil, fmt.Errorf("start PTY: %w", err)
	}
	// creack/pty returns a blocking descriptor. Re-open a duplicate in
	// nonblocking mode so os.File registers it with the Go poller and Close
	// reliably wakes a blocked Read or Write on macOS.
	pollFile, err := pollablePTY(f)
	if err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = f.Close()
		_ = cmd.Wait()
		return nil, fmt.Errorf("configure PTY I/O: %w", err)
	}
	f = pollFile
	t := &Terminal{
		vt: vt.NewEmulator(cfg.Cols, cfg.Rows), modes: make(map[ansi.Mode]bool),
		pty: f, cmd: cmd, cfg: cfg, stopped: make(chan struct{}),
		finished: make(chan struct{}), readDone: make(chan struct{}),
		replies: make(chan []byte, 128),
	}
	t.vt.SetScrollbackSize(historyLimit)
	// Mode callbacks run synchronously within Write, under t.mu.
	t.vt.SetCallbacks(vt.Callbacks{
		EnableMode:  func(m ansi.Mode) { t.modes[m] = true },
		DisableMode: func(m ansi.Mode) { t.modes[m] = false },
	})
	go t.readReplies()
	go t.writeReplies()
	go t.readOutput()
	go t.wait()
	go func() {
		select {
		case <-ctx.Done():
			_ = t.Close()
		case <-t.finished:
		}
	}()
	return t, nil
}

func pollablePTY(f *os.File) (*os.File, error) {
	fd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	p := os.NewFile(uintptr(fd), f.Name())
	_ = f.Close()
	return p, nil
}

func validSize(cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > 1000 || rows > 1000 {
		return fmt.Errorf("invalid terminal size %dx%d (allowed 1..1000)", cols, rows)
	}
	return nil
}

func (t *Terminal) Input(data []byte) error {
	t.inputMu.Lock()
	defer t.inputMu.Unlock()
	select {
	case <-t.stopped:
		return os.ErrClosed
	default:
	}
	if err := t.pty.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return fmt.Errorf("set PTY write deadline: %w", err)
	}
	defer func() { _ = t.pty.SetWriteDeadline(time.Time{}) }()
	for len(data) > 0 {
		n, err := t.pty.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func (t *Terminal) Resize(cols, rows int) error {
	if err := validSize(cols, rows); err != nil {
		return err
	}
	t.mu.Lock()
	select {
	case <-t.stopped:
		t.mu.Unlock()
		return os.ErrClosed
	default:
	}
	if cols == t.vt.Width() && rows == t.vt.Height() {
		t.mu.Unlock()
		return nil
	}
	if err := pty.Setsize(t.pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		t.mu.Unlock()
		return err
	}
	// History is retained as physical rows. The MVP does not reflow old lines.
	t.vt.Resize(cols, rows)
	t.revision++
	t.mu.Unlock()
	t.changed()
	return nil
}

// Snapshot returns a detached copy. Offset counts physical rows above the live
// viewport; alternate-screen programs always show their current screen.
func (t *Terminal) Snapshot(offset int) model.Frame {
	t.mu.Lock()
	defer t.mu.Unlock()
	cols, rows := t.vt.Width(), t.vt.Height()
	alt := t.vt.IsAltScreen()
	history := 0
	if !alt {
		history = t.vt.ScrollbackLen()
	}
	offset = max(0, min(offset, history))
	pos := t.vt.CursorPosition()
	f := model.Frame{
		Cols: cols, Rows: rows, Cells: make([][]model.Cell, rows),
		CursorX: pos.X, CursorY: pos.Y,
		CursorVisible: offset == 0 && !t.vt.CursorHidden(),
		Alternate:     alt, HistoryLines: history, Revision: t.revision,
		BracketedPaste: t.modes[ansi.ModeBracketedPaste], AppCursor: t.modes[ansi.ModeCursorKeys],
		MouseSGR: t.modes[ansi.ModeMouseExtSgr],
	}
	switch {
	case t.modes[ansi.ModeMouseAnyEvent]:
		f.MouseMode = 1003
	case t.modes[ansi.ModeMouseButtonEvent]:
		f.MouseMode = 1002
	case t.modes[ansi.ModeMouseNormal]:
		f.MouseMode = 1000
	}
	for y := 0; y < rows; y++ {
		f.Cells[y] = make([]model.Cell, cols)
		sourceY := history - offset + y
		continuation := false
		for x := 0; x < cols; x++ {
			var c *uv.Cell
			if sourceY < history {
				c = t.vt.ScrollbackCellAt(x, sourceY)
			} else {
				c = t.vt.CellAt(x, sourceY-history)
			}
			out := model.Cell{Text: " ", FG: -1, BG: -1, Width: 1}
			if c != nil {
				out.FG, out.BG = t.color(c.Style.Fg), t.color(c.Style.Bg)
				out.Attr = attributes(c.Style)
				if continuation {
					out.Text, out.Width = "", 0
				} else if c.Content != "" && c.Width > 0 {
					out.Text, out.Width = c.Content, c.Width
					// A clipped wide character has no complete glyph to draw.
					if x+out.Width > cols {
						out.Text, out.Width = " ", 1
					}
				}
			}
			f.Cells[y][x] = out
			continuation = out.Width > 1
		}
	}
	return f
}

func (t *Terminal) color(c color.Color) int32 {
	if c == nil {
		return -1
	}
	switch v := c.(type) {
	case ansi.BasicColor:
		c = t.vt.IndexedColor(int(v))
	case ansi.IndexedColor:
		c = t.vt.IndexedColor(int(v))
	}
	r, g, b, _ := c.RGBA()
	return int32((r>>8)<<16 | (g>>8)<<8 | b>>8)
}

func attributes(s uv.Style) uint32 {
	var a uint32
	for _, pair := range []struct {
		uv    uint8
		model uint32
	}{
		{uv.AttrBold, model.Bold}, {uv.AttrFaint, model.Dim},
		{uv.AttrItalic, model.Italic}, {uv.AttrReverse, model.Reverse},
		{uv.AttrStrikethrough, model.Strike},
	} {
		if s.Attrs&pair.uv != 0 {
			a |= pair.model
		}
	}
	if s.Underline != uv.UnderlineNone {
		a |= model.Underline
	}
	return a
}

func (t *Terminal) readOutput() {
	defer close(t.readDone)
	buf := make([]byte, 32*1024)
	for {
		n, err := t.pty.Read(buf)
		if n > 0 {
			t.mu.Lock()
			_, vtErr := t.vt.Write(buf[:n])
			t.revision++
			t.mu.Unlock()
			t.changed()
			if vtErr != nil {
				t.fail(fmt.Errorf("parse PTY output: %w", vtErr))
				return
			}
		}
		if err != nil {
			// Unix PTYs may return EIO rather than EOF after the slave closes.
			if !errors.Is(err, io.EOF) && !errors.Is(err, syscall.EIO) && !errors.Is(err, os.ErrClosed) {
				t.fail(fmt.Errorf("read PTY output: %w", err))
			}
			return
		}
	}
}

func (t *Terminal) readReplies() {
	defer close(t.replies)
	buf := make([]byte, 4096)
	for {
		n, err := t.vt.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			select {
			case t.replies <- data:
			case <-t.stopped:
				return
			default:
				// A child that requests replies without reading input must not
				// block the parser or grow an unbounded response buffer.
				t.fail(errors.New("terminal device reply queue is full"))
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (t *Terminal) writeReplies() {
	for data := range t.replies {
		if err := t.Input(data); err != nil {
			select {
			case <-t.stopped:
			default:
				t.fail(fmt.Errorf("write terminal reply: %w", err))
			}
			return
		}
	}
}

func (t *Terminal) wait() {
	err := t.cmd.Wait()
	// The root process has exited. Hang up remaining members of our own
	// session's process group while allowing already-buffered output to drain.
	_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGHUP)
	select {
	case <-t.readDone:
	case <-time.After(500 * time.Millisecond):
	}
	_ = t.Close()
	<-t.readDone
	t.errMu.Lock()
	if t.failure != nil {
		err = t.failure
	}
	t.errMu.Unlock()
	close(t.finished)
	if t.cfg.OnExit != nil {
		t.cfg.OnExit(t.cmd.ProcessState.ExitCode(), err)
	}
}

func (t *Terminal) fail(err error) {
	t.errMu.Lock()
	if t.failure == nil {
		t.failure = err
	}
	t.errMu.Unlock()
	_ = t.Close()
}

// Close terminates the process group created by pty.StartWithSize. It is
// idempotent and does not wait for callbacks, which may themselves close a pane.
func (t *Terminal) Close() error {
	var err error
	t.stopOnce.Do(func() {
		close(t.stopped)
		// Interactive shells give foreground jobs their own process group.
		// That group belongs to this PTY too and must be stopped before the
		// shell, so a running editor or command cannot survive pane closure.
		if pgid, e := unix.IoctlGetInt(int(t.pty.Fd()), unix.TIOCGPGRP); e == nil && pgid > 0 && pgid != t.cmd.Process.Pid {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
		killErr := syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
		if killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
			err = killErr
		}
		if closeErr := t.pty.Close(); err == nil {
			err = closeErr
		}
		// Close the pipe, rather than Emulator.Close's unsynchronized closed
		// flag: Read must remain independent of the VT state mutex.
		_ = t.vt.InputPipe().(io.Closer).Close()
	})
	return err
}

func (t *Terminal) changed() {
	if t.cfg.OnChange != nil {
		t.cfg.OnChange()
	}
}
