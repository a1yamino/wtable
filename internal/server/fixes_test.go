package server

import (
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"wtable/internal/agent"
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

func TestTaskContentCapAcrossPaths(t *testing.T) {
	oldLimit := maxTaskContentBytes
	maxTaskContentBytes = 32
	t.Cleanup(func() { maxTaskContentBytes = oldLimit })

	s := taskTestServer(t)
	caps := model.PaneCaps{ReceiveTasks: true, ReturnResults: true, PushIdle: true, Pull: true}
	s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running", Caps: &caps}}
	s.state.Tasks = []model.Task{{ID: "old", From: "controller", To: "worker", Prompt: strings.Repeat("a", 24), State: "delivered"}}

	if _, err := s.sendTask("controller", rawTask(model.SendParams{To: "worker", Prompt: "123456789"})); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("create bypassed the aggregate cap: %v", err)
	}
	if _, err := s.finishTask("controller", rawTask(map[string]any{"id": "old", "result": "123456789"}), true); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("result submit bypassed the aggregate cap: %v", err)
	}
	if s.state.Tasks[0].Result != "" {
		t.Fatalf("rejected result was stored: %q", s.state.Tasks[0].Result)
	}
}

func TestPeerListHidesPaneDiagnostics(t *testing.T) {
	s := taskTestServer(t)
	s.state.Panes = []model.Pane{
		{ID: "peer", Name: "Peer"},
		{ID: "target", Name: "Target", Error: "secret error", ExitSummary: "secret screen", LogPath: "/tmp/log",
			BriefPath: "/tmp/brief", BriefTask: "brief", RoleInstructions: "secret role", RoleHash: "hash",
			SessionID: "session", LastOutput: "secret output", Readiness: "blocked", ReadinessReason: "secret",
			DeliveryConfig: model.DeliveryConfig{BlockedRegex: "secret"}},
	}
	s.state.Edges = []model.Edge{{From: "peer", To: "target"}}

	got := s.peers("peer", nil)
	if len(got) != 1 || got[0].ID != "target" || got[0].Name != "Target" {
		t.Fatalf("peer lost the target identity: %+v", got)
	}
	target := got[0]
	if target.Error != "" || target.ExitSummary != "" || target.LogPath != "" || target.BriefPath != "" ||
		target.BriefTask != "" || target.RoleInstructions != "" || target.RoleHash != "" || target.SessionID != "" ||
		target.LastOutput != "" || target.Readiness != "" || target.ReadinessReason != "" || target.DeliveryConfig.BlockedRegex != "" {
		t.Fatalf("peer received target diagnostics: %+v", target)
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

type taskWaitOutcome struct {
	task model.Task
	err  error
}

func startTaskWait(s *Server, id string, timeoutMS int) chan taskWaitOutcome {
	result := make(chan taskWaitOutcome, 1)
	go func() {
		value, err := s.waitTask("controller", rawTask(map[string]any{"id": id, "timeout_ms": timeoutMS}))
		outcome := taskWaitOutcome{err: err}
		if value != nil {
			outcome.task = value.(model.Task)
		}
		result <- outcome
	}()
	return result
}

func awaitTaskWaiterCount(t *testing.T, s *Server, id string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		count := 0
		for key := range s.taskWaiters {
			if key == id || strings.HasPrefix(key, id+"\x00") {
				count++
			}
		}
		s.mu.Unlock()
		if count == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("task %s did not register %d waiters", id, want)
}

func receiveTaskWait(t *testing.T, result chan taskWaitOutcome) model.Task {
	t.Helper()
	select {
	case outcome := <-result:
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		return outcome.task
	case <-time.After(2 * time.Second):
		t.Fatal("task.wait was not notified")
		return model.Task{}
	}
}

func TestTaskWaitManualResultWakesAllWaiters(t *testing.T) {
	s := taskTestServer(t)
	s.state.Tasks = []model.Task{{ID: "task", From: "controller", To: "worker", State: "running"}}
	first := startTaskWait(s, "task", 2000)
	second := startTaskWait(s, "task", 2000)
	awaitTaskWaiterCount(t, s, "task", 2)

	if _, err := s.finishTask("controller", rawTask(map[string]string{"id": "task", "result": "done"}), true); err != nil {
		t.Fatal(err)
	}
	for _, result := range []chan taskWaitOutcome{first, second} {
		if task := receiveTaskWait(t, result); task.State != "completed" || task.Result != "done" {
			t.Fatalf("waiter received stale task: %+v", task)
		}
	}
}

func TestTaskWaitACPCompletionAndCancelWake(t *testing.T) {
	t.Run("acp completion", func(t *testing.T) {
		s := taskTestServer(t)
		s.runs = map[string]string{"worker": "run"}
		s.state.Panes = []model.Pane{{ID: "worker", Backend: "acp", State: "running", TurnState: "running"}}
		s.state.Tasks = []model.Task{{ID: "task", From: "controller", To: "worker", State: "accepted", DeliveryMode: "prompt"}}
		result := startTaskWait(s, "task", 2000)
		awaitTaskWaiterCount(t, s, "task", 1)
		if !s.finishPrompt("worker", "run", "task", "acp done", nil) {
			t.Fatal("ACP completion was rejected")
		}
		if task := receiveTaskWait(t, result); task.State != "completed" || task.Result != "acp done" {
			t.Fatalf("ACP waiter received %+v", task)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		s := taskTestServer(t)
		s.state.Tasks = []model.Task{{ID: "task", From: "controller", To: "worker", State: "queued", DeliveryMode: "pull"}}
		result := startTaskWait(s, "task", 2000)
		awaitTaskWaiterCount(t, s, "task", 1)
		if _, err := s.cancelTask(rawTask(map[string]string{"id": "task"})); err != nil {
			t.Fatal(err)
		}
		if task := receiveTaskWait(t, result); task.State != "cancelled" {
			t.Fatalf("cancel waiter received %+v", task)
		}
	})
}

func TestTaskWaitTimeoutReturnsCurrentState(t *testing.T) {
	s := taskTestServer(t)
	s.state.Tasks = []model.Task{{ID: "task", From: "controller", To: "worker", State: "running", Result: "old"}}
	result := startTaskWait(s, "task", 30)
	awaitTaskWaiterCount(t, s, "task", 1)
	s.mu.Lock()
	s.taskLocked("task").Result = "current"
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	if task := receiveTaskWait(t, result); task.State != "running" || task.Result != "current" {
		t.Fatalf("timeout returned a stale snapshot: %+v", task)
	}
}

func TestTaskResultSaveFailureDoesNotNotifyWaiter(t *testing.T) {
	s := taskTestServer(t)
	s.state.Tasks = []model.Task{{ID: "task", From: "controller", To: "worker", State: "running"}}
	ch := make(chan struct{}, 1)
	s.taskWaiters = map[string]chan struct{}{"task\x00waiter": ch}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.finishTask("controller", rawTask(map[string]string{"id": "task", "result": "done"}), true); err == nil {
		t.Fatal("result unexpectedly saved")
	}
	select {
	case <-ch:
		t.Fatal("failed persistence notified a waiter")
	default:
	}
}

func TestInterruptSendInvalidKindHasNoSideEffects(t *testing.T) {
	s := onboardingServer(t)
	s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running"}, {ID: "inbox", State: "running"}}
	s.state.Tasks = []model.Task{{ID: "old", From: "controller", To: "worker", State: "delivered", DeliveryMode: "push", OnComplete: "notify", ReportTo: "inbox"}}
	cancelled := false
	s.taskCancels["old"] = func() { cancelled = true }

	if _, err := s.interruptSend("controller", rawTask(map[string]any{"to": "worker", "prompt": "new", "kind": "invalid"})); err == nil {
		t.Fatal("invalid kind was accepted")
	}
	s.mu.Lock()
	old := *s.taskLocked("old")
	tasks := len(s.state.Tasks)
	reports := len(s.state.Reports)
	s.mu.Unlock()
	if old.State != "delivered" || old.Result != "" || tasks != 1 || reports != 0 || cancelled {
		t.Fatalf("rejected interrupt had side effects: task=%+v tasks=%d reports=%d cancelled=%t", old, tasks, reports, cancelled)
	}
}

func TestInterruptSendSaveFailureDoesNotCancel(t *testing.T) {
	s := onboardingServer(t)
	s.state.Panes = []model.Pane{{ID: "worker", Backend: "acp", State: "running"}, {ID: "inbox", State: "running"}}
	s.state.Tasks = []model.Task{{ID: "old", From: "controller", To: "worker", State: "accepted", DeliveryMode: "prompt", OnComplete: "notify", ReportTo: "inbox"}}
	s.state.Reports = []model.Report{{ID: "existing", ParentTask: "old", TaskState: "accepted", Summary: "unchanged"}}
	cancelled := false
	s.taskCancels["old"] = func() { cancelled = true }
	priorTasks := append([]model.Task(nil), s.state.Tasks...)
	priorReports := append([]model.Report(nil), s.state.Reports...)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.interruptSend("controller", rawTask(map[string]any{"to": "worker", "prompt": "new"})); err == nil {
		t.Fatal("interrupt unexpectedly saved")
	}
	if cancelled {
		t.Fatal("external cancellation ran after a failed commit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !reflect.DeepEqual(s.state.Tasks, priorTasks) {
		t.Fatalf("failed commit changed old/new tasks:\nbefore=%+v\nafter=%+v", priorTasks, s.state.Tasks)
	}
	if !reflect.DeepEqual(s.state.Reports, priorReports) {
		t.Fatalf("failed commit changed reports:\nbefore=%+v\nafter=%+v", priorReports, s.state.Reports)
	}
}

func TestPromotedOrderSharedByAllDeliveryBackends(t *testing.T) {
	for _, mode := range []string{"push", "pull", "prompt"} {
		t.Run(mode, func(t *testing.T) {
			s := onboardingServer(t)
			backend := "pty"
			if mode == "prompt" {
				backend = "acp"
			}
			s.state.Panes = []model.Pane{{ID: "worker", Backend: backend, State: "exited"}}
			now := time.Now()
			s.state.Tasks = []model.Task{
				{ID: "first", From: "controller", To: "worker", State: "queued", DeliveryMode: mode, CreatedAt: now.Add(-time.Minute)},
				{ID: "second", From: "controller", To: "worker", State: "queued", DeliveryMode: mode, CreatedAt: now},
			}
			if _, err := s.promoteTask(rawTask(map[string]string{"id": "second"})); err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			s.state.Panes[0].State = "running"
			var selected string
			if mode == "push" {
				selected = s.orderedQueuedTasksLocked("worker")[0].ID
			} else {
				selected = s.nextQueuedTaskLocked("worker", mode, "").ID
			}
			s.mu.Unlock()
			if mode == "pull" {
				value, err := s.nextTask("worker", rawTask(map[string]any{"timeout_ms": 0}))
				if err != nil {
					t.Fatal(err)
				}
				selected = value.(model.Task).ID
			}
			if selected != "second" {
				t.Fatalf("%s selected %q after promotion", mode, selected)
			}
		})
	}
}

func TestQueueClaimRechecksGrantCapabilitiesAndBriefPriority(t *testing.T) {
	s := onboardingServer(t)
	caps := model.PaneCaps{ReceiveTasks: true, ReturnResults: true, Prompt: true, Pull: true}
	s.state.Panes = []model.Pane{{ID: "worker", Backend: "acp", State: "running", Caps: &caps}}
	s.state.Tasks = []model.Task{
		{ID: "work", From: "peer", To: "worker", State: "queued", DeliveryMode: "prompt", CreatedAt: time.Now()},
		{ID: "brief", From: "controller", To: "worker", Kind: model.TaskKindBrief, State: "queued", DeliveryMode: "prompt", CreatedAt: time.Now().Add(time.Minute)},
	}
	s.mu.Lock()
	if got := s.nextQueuedTaskLocked("worker", "prompt", "").ID; got != "brief" {
		s.mu.Unlock()
		t.Fatalf("brief lost priority: %s", got)
	}
	s.state.Panes[0].Caps.Prompt = false
	if got := s.nextQueuedTaskLocked("worker", "prompt", ""); got != nil {
		s.mu.Unlock()
		t.Fatalf("prompt survived capability revocation: %+v", got)
	}
	s.state.Panes[0].Caps.Prompt = true
	s.state.Tasks[1].State = "cancelled"
	if got := s.nextQueuedTaskLocked("worker", "prompt", ""); got != nil {
		s.mu.Unlock()
		t.Fatalf("peer task survived grant revocation: %+v", got)
	}
	s.mu.Unlock()
}

func TestNextTaskContextCancelLeavesNoWaiter(t *testing.T) {
	s := onboardingServer(t)
	s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running"}}
	s.runs["worker"] = "run"
	done := make(chan error, 1)
	go func() {
		_, err := s.nextTask("worker", rawTask(map[string]any{"timeout_ms": 2000}))
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		_, registered := s.nextWaiters[nextWaiterKey("worker", "run")]
		s.mu.Unlock()
		if registered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("next did not register")
		}
		time.Sleep(time.Millisecond)
	}
	s.cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("next did not leave after context cancellation")
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		count := len(s.nextWaiters)
		s.mu.Unlock()
		if count == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("next waiter was not cleared after context cancellation")
}

func TestNextWaiterRegistrationDoesNotSpawnLifecycleGoroutines(t *testing.T) {
	s := onboardingServer(t)
	before := runtime.NumGoroutine()
	for i := 0; i < 32; i++ {
		ch, ok := s.registerNextWaiter("worker", "run")
		if !ok {
			t.Fatal("waiter registration unexpectedly busy")
		}
		s.clearNextWaiter("worker", "run", ch)
	}
	// Allow one unrelated runtime goroutine, but reject the old implementation's
	// one permanently blocked s.ctx watcher per registration.
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Fatalf("waiter registrations accumulated goroutines: before=%d after=%d", before, after)
	}
}

func TestNextTaskBoundToRunGeneration(t *testing.T) {
	s := onboardingServer(t)
	s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running"}}
	s.runs["worker"] = "old-run"
	type nextOutcome struct {
		value any
		err   error
	}
	oldResult := make(chan nextOutcome, 1)
	go func() {
		value, err := s.nextTask("worker", rawTask(map[string]any{"timeout_ms": 2000}))
		oldResult <- nextOutcome{value: value, err: err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		_, registered := s.nextWaiters[nextWaiterKey("worker", "old-run")]
		s.mu.Unlock()
		if registered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("old run did not block in next")
		}
		time.Sleep(time.Millisecond)
	}

	s.mu.Lock()
	s.runs["worker"] = "new-run"
	s.state.Tasks = append(s.state.Tasks, model.Task{ID: "new-task", From: "controller", To: "worker", Prompt: "new generation only", State: "queued", DeliveryMode: "pull", CreatedAt: time.Now()})
	s.mu.Unlock()
	value, err := s.nextTask("worker", rawTask(map[string]any{"timeout_ms": 0}))
	if err != nil || value == nil || value.(model.Task).ID != "new-task" {
		t.Fatalf("new run was blocked by old waiter: value=%+v err=%v", value, err)
	}
	s.notifyNextWaiter("worker")
	select {
	case outcome := <-oldResult:
		if outcome.err == nil || outcome.value != nil {
			t.Fatalf("old run claimed or read new work: value=%+v err=%v", outcome.value, outcome.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old run did not leave its long poll")
	}
}

func TestPromotedTaskUsesActualPTYPump(t *testing.T) {
	s := taskTestServer(t)
	term, err := terminal.Start(s.ctx, terminal.Config{Command: []string{"/bin/cat"}, Cols: 160, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	s.terminals["worker"] = term
	s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running", DeliveryConfig: model.DeliveryConfig{SettleMS: 1, VerifyMS: 1000, AckTimeoutMS: 5000}}}
	now := time.Now()
	s.state.Tasks = []model.Task{
		{ID: "first", From: "controller", To: "worker", Prompt: "first", State: "queued", DeliveryMode: "push", CreatedAt: now.Add(-time.Minute)},
		{ID: "second", From: "controller", To: "worker", Prompt: "second", State: "queued", DeliveryMode: "push", CreatedAt: now},
	}
	if _, err := s.promoteTask(rawTask(map[string]string{"id": "second"})); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	s.wake("worker")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		second := *s.taskLocked("second")
		first := *s.taskLocked("first")
		s.mu.Unlock()
		screen := strings.Join(frameLines(term.Snapshot(0)), "\n")
		if second.State == "delivered" && strings.Contains(screen, "second") {
			if first.State != "queued" {
				t.Fatalf("actual pump also delivered unpromoted work: %+v", first)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Fatalf("promoted task did not reach actual PTY: %+v", s.state.Tasks)
}

func TestPTYCancellationInputFailureIsVisible(t *testing.T) {
	for _, operation := range []string{"cancel", "interrupt"} {
		t.Run(operation, func(t *testing.T) {
			s := onboardingServer(t)
			term, err := terminal.Start(s.ctx, terminal.Config{Command: []string{"/bin/cat"}, Cols: 80, Rows: 24})
			if err != nil {
				t.Fatal(err)
			}
			s.terminals["worker"] = term
			s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running"}}
			s.state.Tasks = []model.Task{{ID: "old", From: "controller", To: "worker", State: "delivered", DeliveryMode: "push"}}
			if err := term.Close(); err != nil {
				t.Fatal(err)
			}

			var created model.Task
			if operation == "cancel" {
				_, err = s.cancelTask(rawTask(map[string]string{"id": "old"}))
			} else {
				created, err = s.interruptSend("controller", rawTask(map[string]any{"to": "worker", "prompt": "new"}))
			}
			if err == nil {
				t.Fatal("failed Ctrl-C write was reported as success")
			}
			s.mu.Lock()
			old := *s.taskLocked("old")
			s.mu.Unlock()
			if old.State != "cancelling" || !strings.Contains(old.Result, "写入失败") || !strings.Contains(old.DeliveryError, "cancel_interrupt:") {
				t.Fatalf("failed interrupt lost its explanation: %+v", old)
			}
			if operation == "interrupt" && (created.ID == "" || created.State != "queued") {
				t.Fatalf("committed message was hidden after Ctrl-C failure: %+v", created)
			}
			persisted, loadErr := s.db.Load()
			if loadErr != nil || persisted.Tasks[0].State != "cancelling" || persisted.Tasks[0].DeliveryError != old.DeliveryError {
				t.Fatalf("cancellation failure was not durable: %+v, %v", persisted.Tasks, loadErr)
			}
		})
	}
}

func TestInterruptSendDoesNotConfirmExistingPTYCancellation(t *testing.T) {
	s := onboardingServer(t)
	s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running"}}
	s.state.Tasks = []model.Task{{ID: "old", From: "controller", To: "worker", State: "cancelling", DeliveryMode: "push", Result: "pending"}}
	created, err := s.interruptSend("controller", rawTask(map[string]any{"to": "worker", "prompt": "new"}))
	if err != nil {
		t.Fatal(err)
	}
	if created.State != "queued" || s.state.Tasks[0].State != "cancelling" || s.state.Tasks[0].Result != "pending" {
		t.Fatalf("interrupt confirmed or rewrote pending cancellation: %+v", s.state.Tasks)
	}
}

func TestTaskWaitUnknownCommitPathsWake(t *testing.T) {
	t.Run("ack timeout", func(t *testing.T) {
		s := taskTestServer(t)
		deliveredAt := time.Now().Add(-time.Second)
		s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running", DeliveryConfig: model.DeliveryConfig{AckTimeoutMS: 1}}}
		s.state.Tasks = []model.Task{{ID: "task", From: "controller", To: "worker", State: "delivered", DeliveryMode: "push", DeliveredAt: &deliveredAt}}
		result := startTaskWait(s, "task", 2000)
		awaitTaskWaiterCount(t, s, "task", 1)
		done := make(chan struct{})
		go func() { s.deliveryLoop(); close(done) }()
		if task := receiveTaskWait(t, result); task.State != "unknown" {
			t.Fatalf("ACK timeout waiter received %+v", task)
		}
		s.cancel()
		<-done
	})

	t.Run("pane exit", func(t *testing.T) {
		s := taskTestServer(t)
		s.runs = map[string]string{"worker": "run"}
		s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running"}}
		s.state.Tasks = []model.Task{{ID: "task", From: "controller", To: "worker", State: "accepted", DeliveryMode: "push"}}
		result := startTaskWait(s, "task", 2000)
		awaitTaskWaiterCount(t, s, "task", 1)
		s.paneExited("worker", "run", 0, nil)
		if task := receiveTaskWait(t, result); task.State != "unknown" {
			t.Fatalf("pane exit waiter received %+v", task)
		}
	})

	t.Run("pane remove", func(t *testing.T) {
		s := onboardingServer(t)
		s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running"}}
		s.state.Tasks = []model.Task{{ID: "task", From: "controller", To: "worker", State: "delivered", DeliveryMode: "push"}}
		result := startTaskWait(s, "task", 2000)
		awaitTaskWaiterCount(t, s, "task", 1)
		if err := s.removePane("worker", ""); err != nil {
			t.Fatal(err)
		}
		if task := receiveTaskWait(t, result); task.State != "unknown" {
			t.Fatalf("pane removal waiter received %+v", task)
		}
	})

	t.Run("pty write failure", func(t *testing.T) {
		s := taskTestServer(t)
		term, err := terminal.Start(s.ctx, terminal.Config{Command: []string{"/bin/cat"}, Cols: 80, Rows: 24})
		if err != nil {
			t.Fatal(err)
		}
		s.terminals["worker"] = term
		s.state.Panes = []model.Pane{{ID: "worker", Backend: "pty", State: "running"}}
		s.state.Tasks = []model.Task{{ID: "task", From: "controller", To: "worker", State: "queued", DeliveryMode: "push"}}
		if err := term.Close(); err != nil {
			t.Fatal(err)
		}
		result := startTaskWait(s, "task", 2000)
		awaitTaskWaiterCount(t, s, "task", 1)
		if err := s.deliverInLane("task", true); err == nil {
			t.Fatal("closed PTY write unexpectedly succeeded")
		}
		if task := receiveTaskWait(t, result); task.State != "unknown" {
			t.Fatalf("write failure waiter received %+v", task)
		}
	})

	t.Run("uncertain ACP result", func(t *testing.T) {
		s := taskTestServer(t)
		s.runs = map[string]string{"worker": "run"}
		s.state.Panes = []model.Pane{{ID: "worker", Backend: "acp", State: "running", TurnState: "running"}}
		s.state.Tasks = []model.Task{{ID: "task", From: "controller", To: "worker", State: "delivered", DeliveryMode: "prompt"}}
		result := startTaskWait(s, "task", 2000)
		awaitTaskWaiterCount(t, s, "task", 1)
		if !s.finishPrompt("worker", "run", "task", "", &agent.UncertainError{Err: errors.New("write failed")}) {
			t.Fatal("ACP result was not committed")
		}
		if task := receiveTaskWait(t, result); task.State != "unknown" {
			t.Fatalf("ACP uncertainty waiter received %+v", task)
		}
	})
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
