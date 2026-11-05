package main

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	ghostty "github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

type gatedTerminalConn struct {
	closing chan struct{}
	finish  chan struct{}
	done    chan struct{}
	once    sync.Once
}

func (c *gatedTerminalConn) Read([]byte) (int, error) {
	<-c.done
	return 0, io.EOF
}

func (c *gatedTerminalConn) Write(data []byte) (int, error) { return len(data), nil }

func (c *gatedTerminalConn) Close() error {
	c.once.Do(func() {
		close(c.closing)
		<-c.finish
		close(c.done)
	})
	return nil
}

// MyGo sets its closed flag before Conn.Close and emulator release finish.
// A retired View must never be built until the complete Close has returned.
func TestTerminalRetirementWaitsForClose(t *testing.T) {
	w := newWorkbench()
	defer w.cancel()
	conn := &gatedTerminalConn{closing: make(chan struct{}), finish: make(chan struct{}), done: make(chan struct{})}
	term, err := ghostty.New(ghostty.Options{Conn: conn})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	native := &NativeTerminal{Terminal: term, app: w.service, conn: &terminalConn{ctx: ctx, cancel: cancel}}
	finished := make(chan struct{})
	go func() {
		w.closeTerminal(native)
		close(finished)
	}()
	defer func() {
		select {
		case <-conn.finish:
		default:
			close(conn.finish)
		}
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("terminal close did not finish")
		}
	}()
	select {
	case <-conn.closing:
	case <-time.After(3 * time.Second):
		t.Fatal("terminal never entered Conn.Close")
	}
	if pending := w.retiredTerminals.take(); len(pending) != 0 {
		t.Fatal("terminal was published while Close was still blocked")
	}
	close(conn.finish)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("terminal was not retired after Close completed")
	}
	closed := w.retiredTerminals.take()
	if len(closed) != 1 || closed[0] != term {
		t.Fatal("the completed terminal was not queued exactly once")
	}
	w.retiredTerminals.add(closed[0])
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Text(c, "live content")
		w.releaseRetiredTerminals(c)
	}, 320, 200)
	tt.Frame()
	if len(w.retiredTerminals.take()) != 0 || !tt.HasText("live content") {
		t.Fatal("retirement did not drain without affecting normal content")
	}
}
