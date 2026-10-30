package server

import (
	"testing"

	"wtable/internal/model"
)

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
			task, err := s.sendTask("sender", rawTask(model.SendParams{To: "worker", Prompt: "work", ReportTo: "inbox"}))
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
