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
