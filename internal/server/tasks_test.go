package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wtable/internal/agent"
	"wtable/internal/model"
	"wtable/internal/store"
	"wtable/internal/terminal"
)

func taskTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{db: db, ctx: ctx, cancel: cancel, state: store.State{Version: 1}, terminals: map[string]*terminal.Terminal{}, agents: map[string]*agent.Client{}}
	t.Cleanup(func() {
		cancel()
		for _, term := range s.terminals {
			term.Close()
		}
		db.Close()
	})
	return s
}
func rawTask(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func TestCompletionReportPreservesSenderGrant(t *testing.T) {
	s := taskTestServer(t)
	s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running"}, {ID: "inbox", Backend: "pty", State: "running"}}
	s.state.Edges = []model.Edge{{From: "sender", To: "worker"}, {From: "sender", To: "inbox"}}
	task, err := s.sendTask("sender", rawTask(model.SendParams{To: "worker", Prompt: "work", ReportTo: "inbox"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.nextTask("worker", nil); err != nil {
		t.Fatal(err)
	}
	s.state.Edges = s.state.Edges[:1]
	result, err := s.finishTask("controller", rawTask(map[string]any{"id": task.ID, "result": "done"}), true)
	if err != nil {
		t.Fatal(err)
	}
	done := result.(model.Task)
	if done.From != "sender" || done.Origin != "human" {
		t.Fatalf("manual completion changed sender: %+v", done)
	}
	if len(s.state.Reports) != 1 || s.state.Reports[0].State != "undeliverable" {
		t.Fatalf("revoked grant bypassed: %+v", s.state.Reports)
	}
	if len(s.state.Tasks) != 1 {
		t.Fatal("notify generated execution")
	}
	if _, err := s.taskResult("worker", rawTask(map[string]any{"id": task.ID, "result": "done"})); err == nil {
		t.Fatal("completed duplicate must reject")
	}
	persisted, err := s.db.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Reports) != 1 || persisted.Tasks[0].ReportID != persisted.Reports[0].ID || persisted.Tasks[0].State != "completed" {
		t.Fatalf("completion/report not persisted together: %+v", persisted)
	}
	if _, err := s.getTasks("inbox", "task.get", rawTask(map[string]string{"id": task.ID})); err == nil {
		t.Fatal("report recipient gained full task access")
	}
}
func TestHandoffAndLateResults(t *testing.T) {
	for _, state := range []string{"delivered", "accepted", "unknown", "cancelled", "failed", "running"} {
		t.Run(state, func(t *testing.T) {
			s := taskTestServer(t)
			s.state.Panes = []model.Pane{{ID: "inbox", Backend: "pty", State: "running"}}
			s.state.Edges = []model.Edge{{From: "sender", To: "inbox"}}
			s.state.Tasks = []model.Task{{ID: "parent", From: "sender", Origin: "sender", To: "worker", State: state, OnComplete: "handoff", ReportTo: "inbox"}}
			if _, err := s.taskResult("worker", rawTask(map[string]string{"id": "parent", "result": "finished"})); err != nil {
				t.Fatal(err)
			}
			if len(s.state.Tasks) != 2 {
				t.Fatalf("missing handoff: %+v", s.state.Tasks)
			}
			next := s.state.Tasks[1]
			if next.ParentTask != "parent" || next.From != "sender" || next.State != "queued" || next.OnComplete != "none" {
				t.Fatalf("invalid handoff: %+v", next)
			}
		})
	}
}
func TestPTYGateVerificationAndCancellation(t *testing.T) {
	s := taskTestServer(t)
	term, err := terminal.Start(s.ctx, terminal.Config{Command: []string{"/bin/sh", "-c", "printf 'Log in\\n'; sleep 30"}, Cols: 160, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	s.terminals["pty"] = term
	s.state.Panes = []model.Pane{{ID: "pty", Backend: "pty", State: "running", DeliveryConfig: model.DeliveryConfig{BlockedRegex: "Log in", SettleMS: 10, VerifyMS: 30, AckTimeoutMS: 100}}}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(strings.Join(frameLines(term.Snapshot(0)), "\n"), "Log in") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := s.sendTask("controller", rawTask(model.SendParams{To: "pty", Prompt: "must not arrive"})); err == nil || !strings.HasPrefix(err.Error(), "blocked:") {
		t.Fatalf("gate did not block: %v", err)
	}
	if len(s.state.Tasks) != 0 {
		t.Fatal("rejected delivery created task")
	}
	s.state.Panes[0].DeliveryConfig.BlockedRegex = ""
	task, err := s.sendTask("controller", rawTask(model.SendParams{To: "pty", Prompt: "echo only"}))
	if err != nil {
		t.Fatal(err)
	}
	if task.State != "delivered" {
		t.Fatalf("not delivered: %+v", task)
	}
	time.Sleep(80 * time.Millisecond)
	s.mu.Lock()
	status := s.taskLocked(task.ID).DeliveryStatus
	s.mu.Unlock()
	if status != "write-unconfirmed" {
		t.Fatalf("echo counted as acceptance: %s", status)
	}
	if _, err := s.cancelTask(rawTask(map[string]any{"id": task.ID})); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	state := s.taskLocked(task.ID).State
	s.mu.Unlock()
	if state != "cancelling" {
		t.Fatalf("Ctrl-C falsely confirms stop: %s", state)
	}
	if _, err := s.cancelTask(rawTask(map[string]any{"id": task.ID, "confirmed": true})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.taskResult("pty", rawTask(map[string]string{"id": task.ID, "result": "late result"})); err != nil {
		t.Fatal(err)
	}
}

func TestRetryUnconfirmedCreatesNewAttempt(t *testing.T) {
	s := taskTestServer(t)
	s.state.Panes = []model.Pane{{ID: "pty", Backend: "pty", State: "running"}, {ID: "inbox", State: "running"}}
	s.state.Tasks = []model.Task{{ID: "old", From: "controller", To: "pty", Prompt: "work", State: "delivered", DeliveryMode: "inject", DeliveryStatus: "write-unconfirmed", OnComplete: "handoff", ReportTo: "inbox"}}
	value, err := s.retryTask(rawTask(map[string]any{"id": "old", "queue": true}))
	if err != nil {
		t.Fatal(err)
	}
	next := value.(model.Task)
	if next.ID == "old" || next.RetryOf != "old" || next.State != "queued" || s.state.Tasks[0].State != "unknown" {
		t.Fatalf("invalid retry: %+v", s.state.Tasks)
	}
	if len(s.state.Reports) != 1 || s.state.Reports[0].TaskState != "unknown" || s.state.Reports[0].HandoffTask != "" || len(s.state.Tasks) != 2 {
		t.Fatalf("retry did not report unknown without handoff: %+v", s.state)
	}
	for _, state := range []string{"queued", "running", "accepted", "cancelling"} {
		s.state.Tasks[0].State = state
		s.state.Tasks[0].DeliveryStatus = "write-unconfirmed"
		if _, err := s.retryTask(rawTask(map[string]any{"id": "old", "queue": true})); err == nil {
			t.Fatalf("retried active %s task", state)
		}
	}
}

func TestInjectedCancellationSurvivesProcessExit(t *testing.T) {
	s := taskTestServer(t)
	s.runs = map[string]string{"pty": "run"}
	s.state.Panes = []model.Pane{{ID: "pty", Backend: "pty", State: "running", DeliveryConfig: model.DeliveryConfig{SettleMS: 1, VerifyMS: 10, AckTimeoutMS: 100}}, {ID: "inbox", State: "running"}}
	exited := make(chan struct{})
	term, err := terminal.Start(s.ctx, terminal.Config{
		Command: []string{"/bin/cat"}, Cols: 160, Rows: 24,
		OnExit: func(code int, err error) {
			s.paneExited("pty", "run", code, err)
			close(exited)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.terminals["pty"] = term
	time.Sleep(10 * time.Millisecond)
	task, err := s.sendTask("controller", rawTask(model.SendParams{To: "pty", Prompt: "cancel after injection", OnComplete: "notify", ReportTo: "inbox"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.cancelTask(rawTask(map[string]any{"id": task.ID, "confirmed": true})); err == nil {
		t.Fatal("confirmation must require a prior cancellation request")
	}
	if _, err := s.cancelTask(rawTask(map[string]any{"id": task.ID})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("cat did not exit after cancellation interrupt")
	}
	persisted, err := s.db.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Panes[0].ExitCode == nil || persisted.Tasks[0].State != "cancelling" {
		t.Fatalf("exit discarded pending confirmation: %+v", persisted)
	}
	confirmed, err := s.cancelTask(rawTask(map[string]any{"id": task.ID, "confirmed": true}))
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.(model.Task).State != "cancelled" {
		t.Fatalf("explicit stop confirmation failed: %+v", confirmed)
	}
	if len(s.state.Reports) != 1 || s.state.Reports[0].TaskState != "cancelled" {
		t.Fatalf("confirmed PTY cancellation did not report: %+v", s.state.Reports)
	}
	late, err := s.taskResult("pty", rawTask(map[string]string{"id": task.ID, "result": "late result"}))
	if err != nil {
		t.Fatal(err)
	}
	if late.(model.Task).State != "completed" || late.(model.Task).Result != "late result" {
		t.Fatalf("late result lost after cancellation: %+v", late)
	}
	if _, err := s.taskResult("pty", rawTask(map[string]string{"id": task.ID, "result": "duplicate"})); err == nil {
		t.Fatal("completed result was overwritten")
	}
}
