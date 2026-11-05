package main

import (
	"context"
	"net"
	"testing"

	ghostty "github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
	"wtable/internal/model"
)

// Leaving Focus must detach the old main/dock views; collapsing Board and
// entering a template preview must also stop streams that are no longer shown.
func TestWorkbenchReleasesInvisibleTerminals(t *testing.T) {
	w := newWorkbench()
	t.Cleanup(w.cancel)
	w.ready, w.active = true, "/workspace"
	w.data.Panes = []model.Pane{
		{ID: "main", Workspace: w.active, Backend: "pty"},
		{ID: "dock", Workspace: w.active, Backend: "pty"},
		{ID: "other", Workspace: w.active, Backend: "pty"},
		{ID: "structured", Workspace: w.active, Backend: "acp"},
		{ID: "foreign", Workspace: "/elsewhere", Backend: "pty"},
	}
	contexts := map[string]context.Context{}
	add := func(id string) {
		ctx, cancel := context.WithCancel(w.ctx)
		contexts[id] = ctx
		w.terminals[id] = &terminalSlot{cancel: cancel}
	}
	for _, pane := range w.data.Panes {
		add(pane.ID)
	}
	state := w.workspaceUI()
	state.FocusID, state.DockID = "missing", "dock"
	w.reconcileTerminals()
	if len(w.terminals) != 2 || contexts["main"].Err() != nil || contexts["dock"].Err() != nil {
		t.Fatal("Focus must keep its first-pane fallback and dock")
	}
	for _, id := range []string{"other", "structured", "foreign"} {
		if contexts[id].Err() == nil {
			t.Fatalf("invisible terminal %q was not canceled", id)
		}
	}
	state.FocusID = "structured"
	w.reconcileTerminals()
	if len(w.terminals) != 1 || contexts["main"].Err() == nil || contexts["dock"].Err() != nil {
		t.Fatal("a structured main pane must not keep the previous PTY view")
	}
	state.Mode = "board"
	state.Layouts["dock"] = NodeLayout{Expanded: true}
	w.reconcileTerminals()
	if len(w.terminals) != 1 || contexts["main"].Err() == nil || contexts["dock"].Err() != nil {
		t.Fatal("Board must retain only expanded PTY nodes")
	}
	state.Layouts["dock"] = NodeLayout{}
	w.reconcileTerminals()
	if len(w.terminals) != 0 || contexts["dock"].Err() == nil {
		t.Fatal("collapsing a Board node must detach its stream")
	}
	add("dock")
	state.Layouts["dock"] = NodeLayout{Expanded: true}
	w.preview = &TopologyTemplate{}
	w.reconcileTerminals()
	if len(w.terminals) != 0 || contexts["dock"].Err() == nil {
		t.Fatal("template preview must not retain live terminal streams")
	}
}

// A delayed action from a detached view must not cancel its replacement.
func TestTerminalSlotRemovalPreservesReplacement(t *testing.T) {
	w := newWorkbench()
	t.Cleanup(w.cancel)
	oldCtx, oldCancel := context.WithCancel(w.ctx)
	old := &terminalSlot{cancel: oldCancel}
	w.terminals["pane"] = old
	w.removeTerminalSlot("pane", old)
	ctx, cancel := context.WithCancel(w.ctx)
	replacement := &terminalSlot{cancel: cancel}
	w.terminals["pane"] = replacement
	w.removeTerminalSlot("pane", old)
	if oldCtx.Err() == nil || ctx.Err() != nil || w.terminals["pane"] != replacement {
		t.Fatal("stale terminal action affected the replacement slot")
	}
}

// Closing a modal after its opener took focus must restore the terminal's
// keyboard input immediately, without retaining a ui.Handle or polling frames.
func TestTerminalFocusReturnsAfterDrawerDismissal(t *testing.T) {
	w := newWorkbench()
	t.Cleanup(w.cancel)
	w.ready, w.active = true, "/workspace"
	pane := model.Pane{ID: "pane", Name: "Shell", Workspace: w.active, Backend: "pty"}
	w.data.Panes = []model.Pane{pane}
	w.workspaceUI().FocusID = pane.ID
	conn, peer := net.Pipe()
	term, err := ghostty.New(ghostty.Options{Conn: conn})
	if err != nil {
		t.Fatal(err)
	}
	w.terminals[pane.ID] = &terminalSlot{
		terminal: &NativeTerminal{Terminal: term, conn: &terminalConn{}},
		cancel:   func() {},
	}
	tt := ui.NewTester(w.view, 1200, 800)
	t.Cleanup(func() {
		_ = term.Close()
		tt.Frame()
		_ = peer.Close()
	})
	tt.Frame()
	w.lastTerminalID = pane.ID
	w.openWorkspace(false)
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Advance(0)
	if w.forms.Kind != "" || w.pendingTerminalFocus != "" {
		t.Fatal("drawer dismissal did not consume its terminal focus request")
	}
	// Focused() is observed by the real terminal element on the following frame.
	w.lastTerminalID = ""
	tt.Frame()
	if w.lastTerminalID != pane.ID {
		t.Fatal("modal focus restoration overrode the terminal focus request")
	}
	if tt.FramePending() {
		t.Fatal("restoring focus left an unnecessary frame loop")
	}
}
