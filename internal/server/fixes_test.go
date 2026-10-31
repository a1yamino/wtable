package server

import (
	"strings"
	"testing"
	"time"

	"wtable/internal/model"
	"wtable/internal/terminal"
)

func TestNextTaskZeroTimeoutIsImmediate(t *testing.T) {
	s := taskTestServer(t)
	s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running"}}

	start := time.Now()
	value, err := s.nextTask("worker", rawTask(map[string]any{"timeout_ms": 0}))
	if err != nil {
		t.Fatal(err)
	}
	if value != nil {
		t.Fatalf("expected no task, got %#v", value)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("timeout_ms=0 waited %v", elapsed)
	}
}

func TestSteerRequiresBusyTarget(t *testing.T) {
	s := taskTestServer(t)
	caps := model.PaneCaps{ReceiveTasks: true, ReturnResults: true, PushIdle: true, PushSteer: true, Pull: true}
	s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running", Caps: &caps}}

	if _, err := s.sendTask("controller", rawTask(model.SendParams{To: "worker", Prompt: "x", DeliveryMode: "steer"})); err == nil || !strings.Contains(err.Error(), "steer") {
		t.Fatalf("idle steer was accepted: %v", err)
	}
	if len(s.state.Tasks) != 0 {
		t.Fatal("rejected steer still created a task")
	}
}

func TestQueuedSteerDowngradesWhenIdle(t *testing.T) {
	s := taskTestServer(t)
	caps := model.PaneCaps{ReceiveTasks: true, ReturnResults: true, PushIdle: true, PushSteer: true, Pull: true}
	s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running", Caps: &caps}}
	s.state.Tasks = []model.Task{{ID: "steer", From: "controller", To: "worker", Prompt: "x", State: "queued", DeliveryMode: "steer", CreatedAt: time.Now()}}

	s.pumpStep("worker")

	s.mu.Lock()
	got := *s.taskLocked("steer")
	s.mu.Unlock()
	if got.DeliveryMode != "push" {
		t.Fatalf("queued steer was not downgraded: %+v", got)
	}
	if got.QueueReason == "" {
		t.Fatal("downgraded task has no explanation")
	}
}

func TestPullClaimCancelRequiresConfirmation(t *testing.T) {
	s := taskTestServer(t)
	s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running"}}
	s.state.Tasks = []model.Task{{ID: "pull", From: "controller", To: "worker", Prompt: "x", State: "running", DeliveryMode: "pull"}}

	if _, err := s.cancelTask(rawTask(map[string]any{"id": "pull"})); err == nil {
		t.Fatal("unconfirmed pull cancel was accepted")
	}
	s.mu.Lock()
	stillRunning := s.taskLocked("pull").State
	s.mu.Unlock()
	if stillRunning != "running" {
		t.Fatalf("rejected cancel changed state to %s", stillRunning)
	}

	value, err := s.cancelTask(rawTask(map[string]any{"id": "pull", "confirmed": true}))
	if err != nil {
		t.Fatal(err)
	}
	if task := value.(model.Task); task.State != "cancelled" {
		t.Fatalf("confirmed pull cancel left state %s", task.State)
	}
}

func TestTaskSeenBelongsToRecipient(t *testing.T) {
	s := taskTestServer(t)
	s.state.Tasks = []model.Task{{ID: "task", From: "sender", To: "recipient", State: "completed"}}

	if _, err := s.taskMark("sender", "task.seen", rawTask(map[string]string{"id": "task"})); err == nil {
		t.Fatal("sender cleared the recipient's unread flag")
	}
	value, err := s.taskMark("recipient", "task.seen", rawTask(map[string]string{"id": "task"}))
	if err != nil {
		t.Fatal(err)
	}
	if !value.(model.Task).Seen {
		t.Fatal("recipient could not mark the task seen")
	}
}

func TestBlockedReasonHidesPeerLine(t *testing.T) {
	raw := "blocked:Log in to the gateway with token=secret"
	peer := sanitizedDeliveryReason(errLike(raw), "peer")
	if strings.Contains(peer, "secret") || strings.Contains(peer, "Log in") {
		t.Fatalf("peer received target screen content: %q", peer)
	}
	if !strings.HasPrefix(peer, "blocked:") {
		t.Fatalf("peer lost the blocked category: %q", peer)
	}
	controller := sanitizedDeliveryReason(errLike(raw), "controller")
	if controller != raw {
		t.Fatalf("controller reason changed unexpectedly: %q", controller)
	}
}

// errLike avoids importing errors just for a string error in this test.
type errLike string

func (e errLike) Error() string { return string(e) }

func TestDeliveryHonorsRevokedCaps(t *testing.T) {
	s := taskTestServer(t)
	caps := model.PaneCaps{ReceiveTasks: true, ReturnResults: true, PushIdle: false, Pull: true}
	s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running", Caps: &caps}}
	s.state.Tasks = []model.Task{{ID: "push", From: "controller", To: "worker", Prompt: "x", State: "queued", DeliveryMode: "push"}}

	if err := s.deliverInLane("push", false); err == nil || !strings.Contains(err.Error(), "caps") {
		t.Fatalf("push delivered after capability revocation: %v", err)
	}
	s.mu.Lock()
	got := *s.taskLocked("push")
	s.mu.Unlock()
	if got.State != "queued" || !strings.Contains(got.QueueReason, "caps") {
		t.Fatalf("revoked task not queued with a reason: %+v", got)
	}
}

func TestPeerBlockedReasonIsSanitizedEndToEnd(t *testing.T) {
	s := taskTestServer(t)
	term, err := terminal.Start(s.ctx, terminal.Config{Command: []string{"/bin/sh", "-c", "printf 'Log in\\n'; sleep 5"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	s.terminals["pty"] = term
	s.state.Panes = []model.Pane{{ID: "pty", Backend: "pty", State: "running", DeliveryConfig: model.DeliveryConfig{BlockedRegex: "Log in", SettleMS: 10, VerifyMS: 30, AckTimeoutMS: 1000}}}
	s.state.Edges = []model.Edge{{From: "peer", To: "pty"}}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(strings.Join(frameLines(term.Snapshot(0)), "\n"), "Log in") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	task, err := s.sendTask("peer", rawTask(model.SendParams{To: "pty", Prompt: "work"}))
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		current := *s.taskLocked(task.ID)
		s.mu.Unlock()
		if current.QueueReason != "" {
			if strings.Contains(current.QueueReason, "Log in") || strings.Contains(current.DeliveryError, "Log in") {
				t.Fatalf("target screen line leaked to peer: %+v", current)
			}
			if !strings.HasPrefix(current.QueueReason, "blocked:") {
				t.Fatalf("missing blocked category: %+v", current)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("blocked reason was not recorded: %+v", current)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReportDoesNotLeakBlockedLine(t *testing.T) {
	s := taskTestServer(t)
	s.state.Panes = []model.Pane{{ID: "target", Backend: "pty", State: "running"}, {ID: "inbox", State: "running"}}
	s.state.Tasks = []model.Task{{ID: "task", From: "controller", To: "target", ReportTo: "inbox", OnComplete: "notify", State: "cancelled", DeliveryError: "blocked:secret target screen"}}
	s.mu.Lock()
	s.completeReportLocked(&s.state.Tasks[0])
	reports := append([]model.Report(nil), s.state.Reports...)
	s.mu.Unlock()
	if len(reports) != 1 {
		t.Fatalf("missing report: %+v", reports)
	}
	if strings.Contains(reports[0].Summary, "secret target screen") {
		t.Fatalf("report leaked the target's blocked line: %q", reports[0].Summary)
	}
	if !strings.Contains(reports[0].Summary, "blocked:") {
		t.Fatalf("report lost the blocked category: %q", reports[0].Summary)
	}
}

func TestSnapshotTruncatesReportSummary(t *testing.T) {
	s := taskTestServer(t)
	long := strings.Repeat("x", 2000)
	s.state.Reports = []model.Report{{ID: "report", ParentTask: "task", Summary: long}}

	snap, err := s.snapshot(rawTask(map[string]bool{"frames": false}))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Reports) != 1 || len(snap.Reports[0].Summary) >= len(long) || !strings.HasSuffix(snap.Reports[0].Summary, "…") {
		t.Fatalf("snapshot report summary not bounded: %d", len(snap.Reports[0].Summary))
	}
	full, err := s.reports("controller", "report.get", rawTask(map[string]string{"id": "report"}))
	if err != nil {
		t.Fatal(err)
	}
	if full.(model.Report).Summary != long {
		t.Fatal("report.get should retain the full summary")
	}
}
