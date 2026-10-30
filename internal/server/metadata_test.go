package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"wtable/internal/model"
)

func TestStateFrameSelectionAndPeek(t *testing.T) {
	s := onboardingServer(t)
	pane, err := s.spawn(model.SpawnParams{Name: "fixture", Cwd: s.paths.Home, Command: []string{"/bin/cat"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.stop(pane.ID)
	for _, tc := range []struct {
		raw   string
		frame bool
	}{
		{`{}`, true}, {`{"ids":null}`, true}, {`{"ids":[]}`, false},
		{`{"frames":false}`, false}, {`{"frames":true,"ids":[]}`, false},
		{`{"ids":["missing"]}`, false}, {`{"ids":["` + pane.ID + `"]}`, true},
	} {
		snap, err := s.snapshot(json.RawMessage(tc.raw))
		if err != nil {
			t.Fatal(err)
		}
		if got := snap.Panes[0].Frame != nil; got != tc.frame {
			t.Fatalf("%s: frame=%t", tc.raw, got)
		}
	}
	if _, err := s.peek(rawTask(map[string]any{"id": pane.ID, "lines": 20})); err != nil {
		t.Fatal(err)
	}
}

func TestDeniedAuditBoundedAndPrivate(t *testing.T) {
	s := onboardingServer(t)
	s.state.Panes = []model.Pane{{ID: "sender", Name: "sender"}, {ID: "target", Name: "target", Backend: "pty", State: "running"}}
	for i := 0; i < 260; i++ {
		_, err := s.dispatch(connection{principal: "sender"}, "task.send", rawTask(model.SendParams{To: "target", Prompt: "do-not-record-prompt", DeliveryMode: "claim"}))
		if err == nil {
			t.Fatal("unauthorized send accepted")
		}
	}
	if len(s.state.Denied) != 256 || s.state.Panes[0].DeniedCount != 260 {
		t.Fatalf("audit bounds/count incorrect: %d %d", len(s.state.Denied), s.state.Panes[0].DeniedCount)
	}
	event := s.state.Denied[0]
	if event.From != "sender" || event.To != "target" || event.Method != "task.send" || event.At.IsZero() {
		t.Fatal(event)
	}
	data, _ := json.Marshal(s.state.Denied)
	if strings.Contains(string(data), "do-not-record") {
		t.Fatal("audit recorded payload")
	}
	if _, err := s.dispatch(connection{principal: "sender"}, "audit.list", nil); err == nil {
		t.Fatal("peer accessed controller audit")
	}
	persisted, err := s.db.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Panes[0].DeniedCount != 261 || len(persisted.Denied) != 256 {
		t.Fatal("audit not persisted")
	}
}

func TestDeniedAuditReportTarget(t *testing.T) {
	s := onboardingServer(t)
	s.state.Panes = []model.Pane{{ID: "sender"}, {ID: "worker", Backend: "pty", State: "running"}, {ID: "inbox"}}
	s.state.Edges = []model.Edge{{From: "sender", To: "worker"}}
	_, err := s.dispatch(connection{principal: "sender"}, "task.send", rawTask(model.SendParams{To: "worker", Prompt: "work", DeliveryMode: "claim", OnComplete: "notify", ReportTo: "inbox"}))
	if err == nil || len(s.state.Denied) != 1 || s.state.Denied[0].To != "inbox" {
		t.Fatalf("report denial attributed incorrectly: %v %+v", err, s.state.Denied)
	}
}

func TestPaneMetadataRedactsCredentials(t *testing.T) {
	s := onboardingServer(t)
	pane, err := s.spawn(model.SpawnParams{Name: "fixture", Cwd: s.paths.Home, Command: []string{"/bin/sh", "-c", `printf 'token: %s\n' "$WTABLE_PEER_TOKEN"; exec /bin/cat`}}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.stop(pane.ID)
	deadline := time.Now().Add(2 * time.Second)
	for {
		snap, err := s.snapshot(json.RawMessage(`{"frames":false}`))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(snap.Panes[0].LastOutput, "token:") {
			if !strings.Contains(snap.Panes[0].LastOutput, "[REDACTED]") {
				t.Fatal("credential exposed by pane summary")
			}
			out, err := s.peek(rawTask(map[string]string{"id": pane.ID}))
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(out)
			if !strings.Contains(string(data), "[REDACTED]") {
				t.Fatal("credential exposed by peek")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture output missing")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
