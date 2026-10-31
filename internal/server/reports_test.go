package server

import (
	"strings"
	"testing"
	"time"

	"wtable/internal/model"
)

func TestTemporaryReportLateResult(t *testing.T) {
	for _, temporary := range []string{"unknown", "cancelled"} {
		for _, failed := range []bool{false, true} {
			name := temporary + "/completed"
			if failed {
				name = temporary + "/failed"
			}
			t.Run(name, func(t *testing.T) {
				s := taskTestServer(t)
				s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running", DeliveryConfig: model.DeliveryConfig{AckTimeoutMS: 1}}, {ID: "inbox", Backend: "pty", State: "running"}}
				s.state.Edges = []model.Edge{{From: "sender", To: "worker"}, {From: "sender", To: "inbox"}}
				past := time.Now().Add(-time.Second)
				s.state.Tasks = []model.Task{{ID: "parent", From: "sender", To: "worker", Prompt: "work", State: "queued", DeliveryMode: "inject", DeliveredAt: &past, OnComplete: "handoff", ReportTo: "inbox"}}
				if temporary == "cancelled" {
					if _, err := s.cancelTask(rawTask(map[string]string{"id": "parent"})); err != nil {
						t.Fatal(err)
					}
				} else {
					s.state.Tasks[0].State = "delivered"
					done := make(chan struct{})
					go func() { s.deliveryLoop(); close(done) }()
					deadline := time.Now().Add(2 * time.Second)
					for time.Now().Before(deadline) {
						s.mu.Lock()
						ready := s.state.Tasks[0].State == "unknown"
						s.mu.Unlock()
						if ready {
							break
						}
						time.Sleep(5 * time.Millisecond)
					}
					s.cancel()
					<-done
				}
				if len(s.state.Reports) != 1 || len(s.state.Tasks) != 1 {
					t.Fatalf("temporary state generated execution or no report: %+v", s.state)
				}
				report := s.state.Reports[0]
				if report.ID != "report:parent:1" || report.TaskState != temporary || report.HandoffTask != "" || !strings.Contains(report.Summary, temporary) {
					t.Fatalf("invalid status report: %+v", report)
				}
				if _, err := s.reports("inbox", "report.seen", rawTask(map[string]string{"id": report.ID})); err != nil {
					t.Fatal(err)
				}
				if _, err := s.taskResult("worker", rawTask(map[string]any{"id": "parent", "result": "actual result", "failed": failed})); err != nil {
					t.Fatal(err)
				}
				updated := s.state.Reports[0]
				finalState := "completed"
				if failed {
					finalState = "failed"
				}
				if len(s.state.Reports) != 1 || len(s.state.Tasks) != 2 || updated.ID != report.ID || updated.TaskState != finalState || updated.Seen || updated.Summary != "actual result" || updated.HandoffTask == "" || !updated.CreatedAt.Equal(report.CreatedAt) {
					t.Fatalf("late result failed to upgrade once: %+v", s.state)
				}
				persisted, err := s.db.Load()
				if err != nil || persisted.Tasks[0].ReportID != report.ID || persisted.Reports[0].HandoffTask != persisted.Tasks[1].ID {
					t.Fatalf("task/report/handoff not saved together: %+v, %v", persisted, err)
				}
				if failed {
					if _, err := s.taskResult("worker", rawTask(map[string]string{"id": "parent", "result": "late success"})); err != nil {
						t.Fatal(err)
					}
					if len(s.state.Tasks) != 2 || len(s.state.Reports) != 1 || s.state.Reports[0].HandoffTask != updated.HandoffTask || s.state.Reports[0].TaskState != "completed" {
						t.Fatal("late success duplicated final handoff")
					}
				}
			})
		}
	}
}

func TestRevokedQueuedTaskReportsCancellation(t *testing.T) {
	s := taskTestServer(t)
	s.state.Panes = []model.Pane{{ID: "sender", State: "running"}, {ID: "worker", Backend: "pty", State: "running"}, {ID: "inbox", State: "running"}}
	s.state.Edges = []model.Edge{{From: "sender", To: "worker"}, {From: "sender", To: "inbox"}}
	if _, err := s.sendTask("sender", rawTask(model.SendParams{To: "worker", Prompt: "work", OnComplete: "handoff", ReportTo: "inbox"})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.edge("edge.remove", rawTask(model.Edge{From: "sender", To: "worker"})); err != nil {
		t.Fatal(err)
	}
	if len(s.state.Tasks) != 1 || len(s.state.Reports) != 1 || s.state.Reports[0].TaskState != "cancelled" || s.state.Reports[0].HandoffTask != "" {
		t.Fatalf("revocation did not report cancellation without execution: %+v", s.state)
	}
}

func TestRetryRejectedRestoresStatusReport(t *testing.T) {
	s := taskTestServer(t)
	s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running"}, {ID: "inbox", State: "running"}}
	s.state.Tasks = []model.Task{{ID: "parent", From: "controller", To: "worker", Prompt: "work", State: "delivered", DeliveryMode: "inject", DeliveryStatus: "write-unconfirmed", OnComplete: "handoff", ReportTo: "inbox"}}
	if _, err := s.retryTask(rawTask(map[string]any{"id": "parent", "to": "missing", "queue": true})); err == nil {
		t.Fatal("retry to missing pane succeeded")
	}
	if s.state.Tasks[0].State != "delivered" || s.state.Tasks[0].ReportID != "" || len(s.state.Reports) != 0 {
		t.Fatalf("rejected retry left status report behind: %+v", s.state)
	}
}

func TestLegacyFinalReportDoesNotRepeatHandoff(t *testing.T) {
	s := taskTestServer(t)
	s.state.Panes = []model.Pane{{ID: "inbox", Backend: "pty", State: "running"}}
	s.state.Edges = []model.Edge{{From: "sender", To: "inbox"}}
	s.state.Tasks = []model.Task{{ID: "parent", From: "sender", To: "worker", State: "failed", OnComplete: "handoff", ReportTo: "inbox", ReportID: "report:parent:1"}}
	s.state.Reports = []model.Report{{ID: "report:parent:1", ParentTask: "parent", From: "worker", To: "inbox", Mode: "handoff", State: "undeliverable", Summary: "old failure"}}
	if _, err := s.taskResult("worker", rawTask(map[string]string{"id": "parent", "result": "late success"})); err != nil {
		t.Fatal(err)
	}
	if len(s.state.Tasks) != 1 || len(s.state.Reports) != 1 || s.state.Reports[0].TaskState != "completed" || s.state.Reports[0].Summary != "late success" {
		t.Fatalf("legacy report was duplicated or not updated: %+v", s.state)
	}
}

// A handoff chooses its channel from the target's capabilities, exactly like
// an ordinary send. Agent-origin handoffs used to fall back to pull, requiring
// the recipient to poll, and disabled targets still received a task.
func TestHandoffUsesTargetCapabilities(t *testing.T) {
	s := taskTestServer(t)
	closed := model.PaneCaps{ReceiveTasks: false}
	s.state.Panes = []model.Pane{
		{ID: "worker", Backend: "pty", State: "running"},
		{ID: "inbox", Backend: "pty", State: "running"},
		{ID: "closed", Backend: "pty", State: "running", Caps: &closed},
	}
	s.state.Edges = []model.Edge{{From: "sender", To: "inbox"}, {From: "sender", To: "closed"}}
	s.state.Tasks = []model.Task{
		{ID: "open", From: "sender", Origin: "sender", To: "worker", Prompt: "work", State: "delivered", DeliveryMode: "inject", OnComplete: "handoff", ReportTo: "inbox"},
		{ID: "shut", From: "sender", Origin: "sender", To: "worker", Prompt: "work", State: "delivered", DeliveryMode: "inject", OnComplete: "handoff", ReportTo: "closed"},
	}
	if _, err := s.taskResult("worker", rawTask(map[string]string{"id": "open", "result": "done"})); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	children := []model.Task{}
	for _, task := range s.state.Tasks {
		if task.ParentTask == "open" {
			children = append(children, task)
		}
	}
	openReports := append([]model.Report(nil), s.state.Reports...)
	s.mu.Unlock()
	if len(children) != 1 || children[0].DeliveryMode != "push" || children[0].To != "inbox" || children[0].From != "sender" {
		t.Fatalf("agent handoff did not use push for a default PTY target: %+v", children)
	}
	for _, report := range openReports {
		if report.ParentTask == "open" && (report.State != "delivered" || report.HandoffTask != children[0].ID) {
			t.Fatalf("deliverable handoff report is wrong: %+v", report)
		}
	}

	if _, err := s.taskResult("worker", rawTask(map[string]string{"id": "shut", "result": "done"})); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	tasks := append([]model.Task(nil), s.state.Tasks...)
	reports := append([]model.Report(nil), s.state.Reports...)
	s.mu.Unlock()
	for _, task := range tasks {
		if task.ParentTask == "shut" {
			t.Fatalf("disabled target received a handoff task: %+v", task)
		}
	}
	found := false
	for _, report := range reports {
		if report.ParentTask == "shut" {
			found = true
			if report.State != "undeliverable" || report.HandoffTask != "" || !strings.Contains(report.Error, "handoff") {
				t.Fatalf("disabled handoff must be undeliverable: %+v", report)
			}
		}
	}
	if !found {
		t.Fatal("missing report for the disabled handoff")
	}
}

// A handoff whose report target is an ACP pane wakes that pane's pump while
// the state lock is still held by task completion. This used to deadlock the
// whole daemon, before the pump got its own lock.
func TestHandoffToACPTargetWakesWithoutDeadlock(t *testing.T) {
	s := taskTestServer(t)
	s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running"}, {ID: "inbox", Backend: "acp", State: "running"}}
	s.state.Edges = []model.Edge{{From: "sender", To: "inbox"}}
	s.state.Tasks = []model.Task{{ID: "parent", From: "sender", Origin: "sender", To: "worker", Prompt: "work", State: "delivered", DeliveryMode: "inject", OnComplete: "handoff", ReportTo: "inbox"}}
	done := make(chan error, 1)
	go func() {
		_, err := s.taskResult("worker", rawTask(map[string]string{"id": "parent", "result": "done"}))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handoff to an ACP report target deadlocked the daemon")
	}
	if len(s.state.Tasks) != 2 || s.state.Tasks[1].To != "inbox" || s.state.Tasks[1].DeliveryMode != "prompt" {
		t.Fatalf("handoff child not queued for prompt delivery: %+v", s.state.Tasks)
	}
}

func TestReportVisibilityAfterGrantRevocation(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		name := "delivered"
		if revoke {
			name = "undeliverable"
		}
		t.Run(name, func(t *testing.T) {
			s := taskTestServer(t)
			s.state.Panes = []model.Pane{{ID: "sender", Backend: "pty", State: "running"}, {ID: "worker", Backend: "pty", State: "running"}, {ID: "inbox", Backend: "pty", State: "running"}}
			s.state.Edges = []model.Edge{{From: "sender", To: "worker"}, {From: "sender", To: "inbox"}}
			task, err := s.sendTask("sender", rawTask(model.SendParams{To: "worker", Prompt: "work", DeliveryMode: "pull", ReportTo: "inbox"}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.nextTask("worker", nil); err != nil {
				t.Fatal(err)
			}
			if revoke {
				if _, err := s.edge("edge.remove", rawTask(model.Edge{From: "sender", To: "inbox"})); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.taskResult("worker", rawTask(map[string]string{"id": task.ID, "result": "private result"})); err != nil {
				t.Fatal(err)
			}
			if !revoke {
				if _, err := s.edge("edge.remove", rawTask(model.Edge{From: "sender", To: "inbox"})); err != nil {
					t.Fatal(err)
				}
			}
			report := s.state.Reports[0]
			if report.State != name {
				t.Fatalf("report state = %q, want %q", report.State, name)
			}
			args := rawTask(map[string]string{"id": report.ID})
			for _, principal := range []string{"controller", "sender", "worker", "inbox", "outsider"} {
				allowed := principal != "outsider" && !(principal == "inbox" && revoke)
				value, err := s.reports(principal, "report.list", nil)
				if err != nil {
					t.Fatal(err)
				}
				listed := value.([]model.Report)
				if (len(listed) == 1) != allowed {
					t.Errorf("%s report.list returned %d reports; allowed=%v", principal, len(listed), allowed)
				}
				value, err = s.reports(principal, "report.get", args)
				if (err == nil) != allowed {
					t.Errorf("%s report.get error=%v; allowed=%v", principal, err, allowed)
				}
				if allowed && err == nil && value.(model.Report).Summary != "private result" {
					t.Errorf("%s lost retained summary", principal)
				}
			}
			_, err = s.reports("inbox", "report.seen", args)
			if (err != nil) != revoke || s.state.Reports[0].Seen == revoke {
				t.Fatalf("recipient report.seen error=%v seen=%v; revoked=%v", err, s.state.Reports[0].Seen, revoke)
			}
			if _, err := s.reports("controller", "report.seen", args); err != nil {
				t.Fatalf("controller cannot handle retained report: %v", err)
			}
		})
	}
}
