package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"wtable/internal/model"
	"wtable/internal/store"
	"wtable/internal/terminal"
)

func TestMigratePaneKindsRunsOnce(t *testing.T) {
	now := time.Now()
	state := store.State{Panes: []model.Pane{
		{ID: "acp", Backend: "acp", CreatedAt: now},
		{ID: "old", Backend: "pty", CreatedAt: now.Add(-time.Hour)},
		{ID: "new", Backend: "pty", CreatedAt: now.Add(-time.Minute)},
	}}
	migratePaneKinds(&state)
	if state.Panes[0].Kind != model.PaneWorker || state.Panes[1].Kind != model.PaneWorker || state.Panes[2].Kind != model.PaneForeground {
		t.Fatalf("unexpected inferred kinds: %+v", state.Panes)
	}
	if !state.KindMigrated {
		t.Fatal("migration flag not set")
	}
	// A later change must not be re-inferred on every daemon start.
	state.Panes[2].Kind = model.PaneWorker
	migratePaneKinds(&state)
	if state.Panes[2].Kind != model.PaneWorker {
		t.Fatal("migration ran twice")
	}
}

func TestDeriveInboxOrdersBlockingOldestFirstThenNewestInfo(t *testing.T) {
	now := time.Now()
	snapshot := model.Snapshot{
		Panes: []model.Pane{
			{ID: "a", Name: "alpha", State: "running", Readiness: "ready", Unread: 2, LastActivity: now, LastOutput: "reply"},
			{ID: "b", Name: "beta", State: "running", Readiness: "ready"},
		},
		Permissions: []model.Permission{
			{ID: "p2", PaneID: "b", Title: "write", CreatedAt: now.Add(-time.Minute)},
			{ID: "p1", PaneID: "a", Title: "shell", CreatedAt: now.Add(-2 * time.Minute)},
		},
		Tasks: []model.Task{
			{ID: "done", From: "controller", To: "a", Kind: "delegation", State: "completed", UpdatedAt: now.Add(-time.Second)},
			{ID: "chat", From: "controller", To: "a", Kind: "chat", State: "failed", UpdatedAt: now},
		},
	}
	items := deriveInbox(snapshot)
	if len(items) != 4 {
		t.Fatalf("expected 2 blocking + task + unread, got %+v", items)
	}
	if items[0].PermissionID != "p1" || items[1].PermissionID != "p2" {
		t.Fatalf("blocking requests must be oldest first: %+v", items[:2])
	}
	if items[2].Severity != model.InboxInfo || items[3].Severity != model.InboxInfo {
		t.Fatalf("remaining items must be info: %+v", items[2:])
	}
	for _, item := range items {
		if item.TaskID == "chat" {
			t.Fatal("chat turns must not enter the delegation inbox")
		}
	}
}

func TestSpawnValidatesForegroundKinds(t *testing.T) {
	s := onboardingServer(t)
	cwd := s.paths.Home
	if _, err := s.spawn(model.SpawnParams{Name: "fg-acp", Kind: model.PaneForeground, Backend: "acp", Cwd: cwd, Command: []string{"/bin/cat"}}, ""); err == nil || err.Error() != acpDisabledError {
		t.Fatalf("foreground ACP accepted: %v", err)
	}
	first, err := s.spawn(model.SpawnParams{Name: "fg", Kind: model.PaneForeground, Backend: "pty", Cwd: cwd, Command: []string{"/bin/cat"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.stop(first.ID, "")
	if _, err := s.spawn(model.SpawnParams{Name: "fg2", Kind: model.PaneForeground, Backend: "pty", Cwd: cwd, Command: []string{"/bin/cat"}}, ""); err == nil || !strings.Contains(err.Error(), "前台") {
		t.Fatalf("second foreground accepted: %v", err)
	}
	worker, err := s.spawn(model.SpawnParams{Name: "worker", Backend: "pty", Cwd: cwd, Command: []string{"/bin/cat"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.stop(worker.ID, "")
	if worker.Kind != model.PaneWorker {
		t.Fatalf("worker kind = %q", worker.Kind)
	}
}

func TestChatTaskNeverSchedulesReports(t *testing.T) {
	s := onboardingServer(t)
	s.state.Panes = []model.Pane{{ID: "acp", Backend: "acp", State: "running"}}
	s.state.Edges = []model.Edge{{From: "controller", To: "acp"}}
	s.mu.Lock()
	task, err := s.createTaskLocked("controller", model.SendParams{To: "acp", Prompt: "hi", Kind: "chat", OnComplete: "notify", ReportTo: "acp"}, "")
	if err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	task.State = "completed"
	task.Result = "done"
	s.completeReportLocked(&task)
	reports := len(s.state.Reports)
	s.mu.Unlock()
	if task.OnComplete != "none" || task.ReportTo != "" {
		t.Fatalf("chat task kept report intent: %+v", task)
	}
	if reports != 0 {
		t.Fatalf("chat task produced %d reports", reports)
	}
}

func TestPromoteAndEditOnlyTouchQueuedTasks(t *testing.T) {
	s := onboardingServer(t)
	now := time.Now()
	s.state.Panes = []model.Pane{{ID: "p", Backend: "pty", State: "exited"}}
	s.state.Tasks = []model.Task{
		{ID: "t1", To: "p", State: "queued", Prompt: "first", CreatedAt: now.Add(-2 * time.Minute)},
		{ID: "t2", To: "p", State: "queued", Prompt: "second", CreatedAt: now.Add(-time.Minute)},
		{ID: "t3", To: "p", State: "queued", Prompt: "third", CreatedAt: now},
		{ID: "done", To: "p", State: "completed", Prompt: "old", CreatedAt: now.Add(-time.Hour)},
	}
	if _, err := s.promoteTask(rawTask(map[string]string{"id": "t3"})); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	if !s.taskLocked("t3").CreatedAt.Before(s.taskLocked("t1").CreatedAt) {
		s.mu.Unlock()
		t.Fatal("promoted task is not first")
	}
	s.mu.Unlock()
	if _, err := s.editTask(rawTask(map[string]string{"id": "t2", "prompt": "changed"})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.editTask(rawTask(map[string]string{"id": "done", "prompt": "changed"})); err == nil {
		t.Fatal("completed task was edited")
	}
}

func TestInterruptSendValidatesBeforeCancelling(t *testing.T) {
	s := onboardingServer(t)
	s.state.Panes = []model.Pane{{ID: "p", Backend: "pty", State: "running"}}
	cancelled := false
	s.taskCancels["old"] = func() { cancelled = true }
	s.state.Tasks = []model.Task{{ID: "old", From: "controller", To: "p", Prompt: "work", State: "delivered", DeliveryMode: "push", CreatedAt: time.Now()}}
	term, err := terminal.Start(s.ctx, terminal.Config{Command: []string{"/bin/cat"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	s.terminals["p"] = term

	tooBig := strings.Repeat("x", (1<<20)+1)
	if _, err := s.interruptSend("controller", rawTask(map[string]any{"to": "p", "prompt": tooBig})); err == nil {
		t.Fatal("oversized interrupt was accepted")
	}
	s.mu.Lock()
	state := s.taskLocked("old").State
	s.mu.Unlock()
	if state != "delivered" || cancelled {
		t.Fatalf("rejected interrupt cancelled work: state=%s cancelled=%t", state, cancelled)
	}

	task, err := s.interruptSend("controller", rawTask(map[string]any{"to": "p", "prompt": "new"}))
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	old := *s.taskLocked("old")
	s.mu.Unlock()
	if old.State != "cancelling" || !strings.Contains(old.Result, "人工确认") || cancelled {
		t.Fatalf("PTY interrupt bypassed stop confirmation: %+v cancelled=%t", old, cancelled)
	}
	if task.Kind != "chat" || task.State != "queued" {
		t.Fatalf("new message not queued as chat: %+v", task)
	}
}

func TestUIFocusAcknowledgesUnreadForOnePane(t *testing.T) {
	s := onboardingServer(t)
	s.state.Panes = []model.Pane{
		{ID: "a", Backend: "acp", State: "running", Unread: 3},
		{ID: "b", Backend: "acp", State: "running", Unread: 2},
	}
	s.state.Tasks = []model.Task{{ID: "t", To: "a", State: "completed", Seen: false, UpdatedAt: time.Now()}}
	s.state.Reports = []model.Report{{ID: "r", To: "a", Seen: false, State: "delivered"}}

	result, err := s.dispatch(connection{id: 1, principal: "controller"}, "ui.focus", json.RawMessage(`{"id":"a"}`))
	if err != nil || result != true {
		t.Fatalf("ui.focus failed: %v %v", result, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.LastFocus != "a" || s.state.Panes[0].Unread != 0 || s.state.Panes[1].Unread != 2 {
		t.Fatalf("focus cleared the wrong unread: %+v", s.state.Panes)
	}
	if !s.taskLocked("t").Seen || !s.state.Reports[0].Seen {
		t.Fatal("opening the pane did not acknowledge its results")
	}
}

func TestSnapshotSortsPermissionsAndKeepsPreviews(t *testing.T) {
	s := onboardingServer(t)
	now := time.Now()
	s.approvals = map[string]*approval{
		"c": {permission: model.Permission{ID: "c", PaneID: "p", Title: "third", Kind: "shell", Preview: "input: rm -rf build", CreatedAt: now.Add(2 * time.Second)}},
		"a": {permission: model.Permission{ID: "a", PaneID: "p", Title: "first", Kind: "edit", Preview: "input: git push", CreatedAt: now}},
		"b": {permission: model.Permission{ID: "b", PaneID: "p", Title: "second", Kind: "read", Preview: "input: cat secrets", CreatedAt: now.Add(time.Second)}},
	}
	snapshot, err := s.snapshot(json.RawMessage(`{"frames":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Permissions) != 3 {
		t.Fatalf("expected 3 permissions, got %d", len(snapshot.Permissions))
	}
	order := []string{snapshot.Permissions[0].ID, snapshot.Permissions[1].ID, snapshot.Permissions[2].ID}
	if strings.Join(order, "") != "abc" {
		t.Fatalf("permission order is unstable: %v", order)
	}
	if snapshot.Permissions[0].Preview != "input: git push" || snapshot.Permissions[1].Kind != "read" {
		t.Fatalf("preview/kind lost: %+v", snapshot.Permissions)
	}
}

func TestPermissionDecisionsAreAudited(t *testing.T) {
	s := onboardingServer(t)
	perm := model.Permission{ID: "perm", PaneID: "p", Title: "write", Options: []model.PermissionOption{
		{ID: "allow", Name: "Allow", Kind: "allow_once"},
		{ID: "reject", Name: "Reject", Kind: "reject_once"},
	}}
	a := &approval{permission: perm, answer: make(chan string, 1)}
	s.mu.Lock()
	s.approvals[perm.ID] = a
	s.mu.Unlock()
	if err := s.replyPermission(json.RawMessage(`{"id":"perm","option_id":"reject","batch":true}`)); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.state.Decisions) != 1 {
		t.Fatalf("expected one decision, got %+v", s.state.Decisions)
	}
	d := s.state.Decisions[0]
	if d.Decision != "deny" || d.OptionKind != "reject_once" || !d.Batch {
		t.Fatalf("unexpected audit entry: %+v", d)
	}
	if a.permission.ID != "perm" {
		t.Fatal("approval identity changed")
	}
}
