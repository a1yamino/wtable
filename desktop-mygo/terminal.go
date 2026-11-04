package main

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	mygoterminal "github.com/egoist/mygo/plugins/terminal"

	"wtable/internal/model"
	"wtable/internal/rpc"
)

// NativeTerminal renders a daemon-owned PTY. Closing it only detaches this view.
type NativeTerminal struct {
	Terminal *mygoterminal.Terminal
	conn     *terminalConn
	app      *App
	once     sync.Once
}

func (a *App) OpenTerminal(ctx context.Context, pane model.Pane) (*NativeTerminal, error) {
	if a.startupErr != nil {
		return nil, a.startupErr
	}
	if pane.ID == "" || pane.Workspace == "" || pane.Backend != "pty" {
		return nil, errors.New("请选择有效的 PTY pane")
	}
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return nil, errors.New("工作台正在退出")
	}
	streamCtx, cancel := context.WithCancel(a.ctx)
	c := &terminalConn{app: a, id: pane.ID, workspace: pane.Workspace, ctx: streamCtx, cancel: cancel, ready: make(chan struct{}), status: "retry", attach: true}
	t := &NativeTerminal{conn: c, app: a}
	a.mu.Unlock()
	c.stopCaller = context.AfterFunc(ctx, cancel)
	term, err := mygoterminal.New(mygoterminal.Options{
		Conn: c, ResizeByConn: true,
		Theme: desktopTerminalTheme(), Cursor: mygoterminal.CursorBar,
		Font:    mygoterminal.Font{Family: "SF Mono, Menlo, monospace", Size: 13, LineHeight: 1.22},
		Padding: mygoterminal.Padding{X: 12, Y: 9},
	})
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	t.Terminal = term
	c.term = term
	close(c.ready)
	a.mu.Lock()
	if a.closing || streamCtx.Err() != nil {
		a.mu.Unlock()
		_ = t.Close()
		return nil, errors.New("终端连接已取消")
	}
	if a.terminals == nil {
		a.terminals = make(map[*NativeTerminal]struct{})
	}
	a.terminals[t] = struct{}{}
	a.mu.Unlock()
	context.AfterFunc(streamCtx, func() { _ = c.Close() })
	return t, nil
}

func (t *NativeTerminal) Close() error {
	var err error
	t.once.Do(func() {
		err = t.conn.Close()
		if e := t.Terminal.Close(); err == nil {
			err = e
		}
		t.app.mu.Lock()
		delete(t.app.terminals, t)
		t.app.mu.Unlock()
	})
	return err
}

func (t *NativeTerminal) Status() string {
	t.conn.mu.Lock()
	defer t.conn.mu.Unlock()
	return t.conn.status
}

func (t *NativeTerminal) Err() error {
	t.conn.mu.Lock()
	defer t.conn.mu.Unlock()
	return t.conn.err
}

type terminalSize struct{ cols, rows int }

// Read is consumed by MyGo's reader goroutine, and Write by its input worker.
// Resize only records a request: the reader applies it between consumed chunks,
// then reattaches at the new size so Ghostty agrees with the daemon's VT.
type terminalConn struct {
	app           *App
	id, workspace string
	ctx           context.Context
	cancel        context.CancelFunc
	stopCaller    func() bool
	ready         chan struct{}
	mu            sync.Mutex
	stream        *rpc.Client
	status        string
	err           error
	writeErr      error
	pending       terminalSize
	attach        bool
	cursor        uint64
	buffer        []byte
	term          *mygoterminal.Terminal
}

func (c *terminalConn) state(status string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status != "closed" {
		if status == "live" && c.writeErr != nil {
			status, err = "retry", c.writeErr
		}
		c.status, c.err = status, err
	}
}

func (c *terminalConn) Close() error {
	c.cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopCaller != nil {
		c.stopCaller()
	}
	c.status = "closed"
	if c.stream != nil {
		err := c.stream.Close()
		c.stream = nil
		return err
	}
	return nil
}

func (c *terminalConn) Read(p []byte) (int, error) {
	select {
	case <-c.ready:
	case <-c.ctx.Done():
		return 0, io.EOF
	}
	for {
		if c.ctx.Err() != nil {
			return 0, io.EOF
		}
		if len(c.buffer) > 0 {
			n := copy(p, c.buffer)
			c.buffer = c.buffer[n:]
			return n, nil
		}
		if err := c.readNext(); err != nil {
			if c.ctx.Err() != nil {
				return 0, io.EOF
			}
			c.state("retry", err)
			c.mu.Lock()
			if c.stream != nil {
				_ = c.stream.Close()
				c.stream = nil
			}
			c.mu.Unlock()
			c.attach = true
			timer := time.NewTimer(650 * time.Millisecond)
			select {
			case <-c.ctx.Done():
				timer.Stop()
				return 0, io.EOF
			case <-timer.C:
			}
		}
	}
}

func (c *terminalConn) readNext() error {
	c.mu.Lock()
	stream := c.stream
	c.mu.Unlock()
	if stream == nil {
		token, err := c.app.paths.ClientToken()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
		client, err := rpc.Dial(ctx, c.app.paths.Socket, token)
		cancel()
		if err != nil {
			return err
		}
		c.mu.Lock()
		if c.ctx.Err() != nil {
			c.mu.Unlock()
			_ = client.Close()
			return c.ctx.Err()
		}
		c.stream = client
		stream = client
		c.mu.Unlock()
	}
	c.mu.Lock()
	size := c.pending
	c.pending = terminalSize{}
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()
	if size.cols > 0 {
		if err := c.app.control.Call(ctx, "pane.resize", map[string]any{"id": c.id, "workspace": c.workspace, "cols": size.cols, "rows": size.rows}, nil); err != nil {
			c.mu.Lock()
			if c.pending.cols == 0 {
				c.pending = size
			}
			c.mu.Unlock()
			return err
		}
		c.attach = true
	}
	if c.attach {
		var frame model.Frame
		if err := stream.Call(ctx, "pane.screen", c.params(), &frame); err != nil {
			return err
		}
		var attachment struct {
			History, Snapshot []byte
			Cursor            uint64
		}
		if err := stream.Call(ctx, "pane.attach", c.params(), &attachment); err != nil {
			return err
		}
		c.term.Resize(frame.Cols, frame.Rows)
		// Reset and clear old scrollback before replacing the retained history.
		c.buffer = append([]byte("\x1bc\x1b[3J"), attachment.History...)
		c.buffer = append(c.buffer, attachment.Snapshot...)
		c.cursor, c.attach = attachment.Cursor, false
		c.state("live", nil)
		return nil
	}
	var chunk struct {
		Data   []byte
		Cursor uint64
	}
	if err := stream.Call(ctx, "pane.output", map[string]any{"id": c.id, "workspace": c.workspace, "cursor": c.cursor, "max_bytes": 64 << 10, "wait_ms": 650}, &chunk); err != nil {
		return err
	}
	if chunk.Cursor != c.cursor+uint64(len(chunk.Data)) {
		c.attach = true
		return nil
	}
	c.cursor, c.buffer = chunk.Cursor, chunk.Data
	c.state("live", nil)
	return nil
}

func (c *terminalConn) params() map[string]string {
	return map[string]string{"id": c.id, "workspace": c.workspace}
}

func (c *terminalConn) Write(p []byte) (int, error) {
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	if err := c.app.control.Call(ctx, "pane.input", map[string]string{"id": c.id, "workspace": c.workspace, "data": string(p)}, nil); err != nil {
		c.mu.Lock()
		c.writeErr = err
		c.mu.Unlock()
		c.state("retry", err)
		return 0, err
	}
	return len(p), nil
}

func (c *terminalConn) Resize(cols, rows int) error {
	cols, rows = max(2, min(240, cols)), max(1, min(100, rows))
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() != nil {
		return io.ErrClosedPipe
	}
	c.pending = terminalSize{cols, rows}
	return nil
}
