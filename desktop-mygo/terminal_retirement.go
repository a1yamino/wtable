package main

import (
	"fmt"
	"os"
	"sync"

	ghostty "github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// Close finishes on a worker, while view-owned native input resources must be
// released by the UI thread. The queue owns each closed terminal until then.
type terminalRetirements struct {
	mu    sync.Mutex
	items []*ghostty.Terminal
}

func (q *terminalRetirements) add(term *ghostty.Terminal) {
	q.mu.Lock()
	q.items = append(q.items, term)
	q.mu.Unlock()
}

func (q *terminalRetirements) take() []*ghostty.Terminal {
	q.mu.Lock()
	defer q.mu.Unlock()
	items := q.items
	q.items = nil
	return items
}

func (w *Workbench) closeTerminal(term *NativeTerminal) {
	if err := term.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "wtable terminal close:", err)
	}
	// Publish only after Close returns: MyGo sets its closed flag before it
	// finishes freeing the emulator, so cancellation alone is not sufficient.
	w.retiredTerminals.add(term.Terminal)
	if w.win != nil {
		w.win.Invalidate()
	}
}

func (w *Workbench) releaseRetiredTerminals(c *ui.Context) {
	closed := w.retiredTerminals.take()
	// MyGo v0.7.4 frees its view's native encoders during the next build of a
	// closed terminal. Hidden still builds, but takes no layout, input or focus.
	// A stable container and object keys keep this pass out of live controls'
	// identities, including a replacement terminal for the same pane.
	ui.Column(c.Key("terminal-retirement")).Hidden(true).Children(func() {
		for _, term := range closed {
			ghostty.View(c.Key(term), term).Hidden(true)
		}
	})
}
