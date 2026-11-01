package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"wtable/internal/agent"
	"wtable/internal/model"
)

// maxTaskContentBytes bounds the aggregate prompt/result text a long-lived
// state directory can accumulate. The per-task limits alone still allow
// 2,000 * 1 MiB of retained content, and every mutation marshals the whole
// state; this cap keeps a single agent from making the daemon unresponsive.
var maxTaskContentBytes = 64 << 20

func (s *Server) edge(method string, raw json.RawMessage) (any, error) {
	var a struct {
		From      string `json:"from"`
		To        string `json:"to"`
		Workspace string `json:"workspace,omitempty"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	scope := ""
	if a.Workspace != "" {
		clean, err := cleanWorkspacePath(a.Workspace)
		if err != nil {
			return nil, err
		}
		scope = clean
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	from, err := s.resolvePaneLocked(scope, a.From)
	if err != nil {
		return nil, err
	}
	if scope == "" {
		scope = from.Workspace
	}
	to, err := s.resolvePaneLocked(scope, a.To)
	if err != nil {
		return nil, err
	}
	if from.ID == to.ID {
		return nil, errors.New("不能连接到自身")
	}
	if from.Workspace != to.Workspace {
		return nil, errors.New("不能跨工作区连线")
	}
	edge := model.Edge{From: from.ID, To: to.ID}
	if method == "edge.add" {
		if !s.hasEdgeLocked(edge.From, edge.To) {
			s.state.Edges = append(s.state.Edges, edge)
		}
	} else {
		for i, e := range s.state.Edges {
			if e == edge {
				s.state.Edges = append(s.state.Edges[:i], s.state.Edges[i+1:]...)
				break
			}
		}
		for i := range s.state.Tasks {
			t := &s.state.Tasks[i]
			if t.From == edge.From && t.To == edge.To && t.State == "queued" {
				t.State = "cancelled"
				t.Result = "授权连线已移除"
				t.UpdatedAt = time.Now()
				t.Seen = false
				s.completeReportLocked(t)
			}
		}
	}
	return edge, s.saveLocked()
}

func (s *Server) peers(principal string, raw json.RawMessage) []model.Pane {
	var a struct {
		Workspace string `json:"workspace,omitempty"`
	}
	if len(raw) > 0 {
		_ = decode(raw, &a)
	}
	workspace := ""
	if a.Workspace != "" {
		if clean, err := cleanWorkspacePath(a.Workspace); err == nil {
			workspace = clean
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if principal != "controller" {
		if p := s.paneByIDLocked(principal); p != nil {
			workspace = p.Workspace
		}
	}
	panes := []model.Pane{}
	for _, p := range s.state.Panes {
		if workspace != "" && p.Workspace != workspace {
			continue
		}
		if principal == "controller" || s.hasEdgeLocked(principal, p.ID) {
			p.Messages = nil
			p.Command = nil
			if principal != "controller" {
				// A peer needs addressing metadata, not another pane's terminal
				// diagnostics, role text or brief paths. Command/Messages were
				// already stripped; clear the remaining screen-bearing fields.
				p.Error = ""
				p.ExitSummary = ""
				p.LogPath = ""
				p.BriefPath = ""
				p.BriefTask = ""
				p.RoleInstructions = ""
				p.RoleHash = ""
				p.SessionID = ""
				p.LastOutput = ""
				p.Readiness = ""
				p.ReadinessReason = ""
				p.Frame = nil
				p.DeliveryConfig = model.DeliveryConfig{}
			}
			panes = append(panes, p)
		}
	}
	return panes
}

func (s *Server) sendTask(principal string, raw json.RawMessage) (model.Task, error) {
	var a model.SendParams
	if err := decode(raw, &a); err != nil {
		return model.Task{}, err
	}
	s.mu.Lock()
	existing := false
	if a.DedupeKey != "" {
		for _, prior := range s.state.Tasks {
			if prior.From == principal && prior.DedupeKey == a.DedupeKey {
				existing = true
				break
			}
		}
	}
	t, err := s.createTaskLocked(principal, a, "")
	s.mu.Unlock()
	if err != nil {
		return model.Task{}, err
	}
	if existing {
		return t, nil
	}
	s.notifyNextWaiter(t.To)
	s.wake(t.To)
	// Give an idle pane a short window to take the task, so callers see
	// delivered when it was immediate and queued(reason) when it was not.
	t = s.awaitFirstHop(t.ID, 1200*time.Millisecond)
	return t, nil
}

func (s *Server) createTaskLocked(principal string, a model.SendParams, retryOf string) (model.Task, error) {
	if strings.TrimSpace(a.Prompt) == "" || len(a.Prompt) > 1<<20 {
		return model.Task{}, errors.New("任务内容不能为空且不得超过 1 MiB")
	}
	if len(a.DedupeKey) > 256 {
		return model.Task{}, errors.New("dedupe_key 最长 256 字节")
	}
	scope := ""
	if principal != "controller" {
		if pp := s.paneByIDLocked(principal); pp != nil {
			scope = pp.Workspace
		}
	} else if a.Workspace != "" {
		clean, err := cleanWorkspacePath(a.Workspace)
		if err != nil {
			return model.Task{}, err
		}
		scope = clean
	}
	p, err := s.resolvePaneLocked(scope, a.To)
	if err != nil {
		return model.Task{}, errors.New("目标 pane 不存在或不属于该工作区")
	}
	if scope == "" {
		scope = p.Workspace
	}
	if p.Workspace != scope {
		return model.Task{}, errors.New("目标 pane 不属于该工作区")
	}
	if principal != "controller" && !s.hasEdgeLocked(principal, p.ID) {
		return model.Task{}, deniedError("没有发送到此 pane 的授权连线", p.ID)
	}
	if a.Kind == "" {
		a.Kind = model.TaskKindDelegation
	}
	if !contains([]string{model.TaskKindChat, model.TaskKindDelegation, model.TaskKindHandoff, model.TaskKindBrief}, a.Kind) {
		return model.Task{}, errors.New("kind 必须为 chat、delegation、handoff 或 brief")
	}
	if a.Kind == model.TaskKindBrief {
		// Only the workbench itself emits briefs. A peer must not be able to
		// relabel work as instructions to bypass the busy gate.
		if principal != "controller" {
			return model.Task{}, deniedError("brief 只能由工作台发送", p.ID)
		}
		a.OnComplete = "none"
		a.ReportTo = ""
	}
	if a.Kind == model.TaskKindChat {
		// Chat turns are conversation, not delegation: they never schedule a
		// report or a handoff.
		a.OnComplete = "none"
		a.ReportTo = ""
	}
	a.To = p.ID
	mode, err := resolveDelivery(p, a.DeliveryMode)
	if err != nil {
		return model.Task{}, err
	}
	// Steering only makes sense while a turn is in progress. Accepting it for
	// an idle pane would strand it in the queue and later inject it into an
	// unrelated turn, so reject it at the sender instead.
	if mode == "steer" && !s.paneBusyLocked(p.ID) {
		return model.Task{}, errors.New("steer 需要目标正在执行 turn；目标当前空闲，请改用普通 send 或稍后重试")
	}
	a.DeliveryMode = mode
	if a.OnComplete == "" {
		a.OnComplete = "none"
		if a.ReportTo != "" {
			a.OnComplete = "notify"
		}
	}
	if !contains([]string{"none", "notify", "handoff"}, a.OnComplete) {
		return model.Task{}, errors.New("on_complete 必须为 none、notify 或 handoff")
	}
	if a.OnComplete == "none" && a.ReportTo != "" {
		return model.Task{}, errors.New("on_complete=none 不能指定 report_to")
	}
	if a.OnComplete != "none" {
		recipient, err := s.resolvePaneLocked(scope, a.ReportTo)
		if err != nil {
			return model.Task{}, errors.New("report_to pane 不存在或不属于该工作区")
		}
		if recipient.Workspace != p.Workspace {
			return model.Task{}, errors.New("report_to 不能跨工作区")
		}
		a.ReportTo = recipient.ID
		if principal != "controller" && !s.hasEdgeLocked(principal, recipient.ID) {
			return model.Task{}, deniedError("没有发送到 report_to 的授权连线", recipient.ID)
		}
	}
	if a.ParentTask != "" {
		parent := s.taskLocked(a.ParentTask)
		if parent == nil || (principal != "controller" && parent.From != principal && parent.To != principal) {
			return model.Task{}, deniedError("无权引用 parent_task")
		}
		if parent.Workspace != "" && parent.Workspace != p.Workspace {
			return model.Task{}, errors.New("parent_task 不能跨工作区")
		}
	}
	if a.IdempotencyKey == "" {
		a.IdempotencyKey = a.DedupeKey
	}
	if a.IdempotencyKey != "" {
		if len(a.IdempotencyKey) > 256 {
			return model.Task{}, errors.New("idempotency_key 最长 256 字节")
		}
		for _, t := range s.state.Tasks {
			key := t.IdempotencyKey
			if key == "" {
				key = t.DedupeKey
			}
			if t.From == principal && t.Workspace == p.Workspace && key == a.IdempotencyKey {
				if t.To != a.To || t.Prompt != a.Prompt || t.OnComplete != a.OnComplete || t.ReportTo != a.ReportTo || t.ParentTask != a.ParentTask {
					return model.Task{}, errors.New("task idempotency key was reused with a different prompt")
				}
				return t, nil
			}
		}
	}
	if p.State != "running" {
		return model.Task{}, errors.New("not-ready: 目标 pane 没有运行，请先 restart")
	}
	if len(s.state.Tasks) >= 2000 {
		return model.Task{}, errors.New("MVP 任务记录上限为 2,000 条")
	}
	if s.taskContentBytesLocked()+len(a.Prompt) > maxTaskContentBytes {
		return model.Task{}, errors.New("任务内容总量已达到 64 MiB 上限；请缩短内容或使用新的 WTABLE_HOME")
	}
	now := time.Now()
	origin := principal
	if origin == "controller" {
		origin = "human"
	}
	t := model.Task{ID: newID("t-"), From: principal, Origin: origin, To: a.To, Prompt: a.Prompt, Kind: a.Kind, State: "queued", Workspace: p.Workspace, DeliveryMode: a.DeliveryMode, OnComplete: a.OnComplete, ReportTo: a.ReportTo, ParentTask: a.ParentTask, RetryOf: retryOf, DedupeKey: a.DedupeKey, IdempotencyKey: a.IdempotencyKey, CreatedAt: now, UpdatedAt: now}
	s.state.Tasks = append(s.state.Tasks, t)
	return t, s.saveLocked()
}

func (s *Server) nextTask(principal string, raw json.RawMessage) (any, error) {
	// A pointer distinguishes an omitted field (default 30s) from an explicit
	// timeout_ms=0, which the CLI documents as "return immediately".
	var a struct {
		TimeoutMS *int `json:"timeout_ms"`
	}
	if len(raw) > 0 {
		_ = decode(raw, &a)
	}
	if principal == "controller" {
		return nil, deniedError("next 必须在接收任务的 pane 内运行")
	}
	s.mu.Lock()
	if p := s.paneLocked(principal); p != nil && p.Backend == "acp" {
		s.mu.Unlock()
		return nil, errors.New("ACP 任务由 prompt 通道自动接收，不需要 next")
	}
	s.mu.Unlock()

	timeout := 30 * time.Second
	if a.TimeoutMS != nil {
		if *a.TimeoutMS <= 0 {
			timeout = 0
		} else {
			timeout = time.Duration(*a.TimeoutMS) * time.Millisecond
		}
	}
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		if s.closing || s.ctx.Err() != nil {
			s.mu.Unlock()
			return nil, errors.New("daemon 正在关闭")
		}
		for _, t := range s.state.Tasks {
			if t.To == principal && (t.State == "running" || (t.State == "accepted" && taskMode(t) == "pull")) {
				s.mu.Unlock()
				return nil, errors.New("已有领取中的任务，请先提交 result")
			}
		}
		for i := range s.state.Tasks {
			t := &s.state.Tasks[i]
			if t.To != principal || t.State != "queued" || taskMode(*t) != "pull" {
				continue
			}
			if t.From != "controller" && !s.hasEdgeLocked(t.From, t.To) {
				continue
			}
			now := time.Now()
			t.State = "running"
			t.AcceptedAt = &now
			t.DeliveredVia = "pull"
			t.QueueReason = ""
			t.UpdatedAt = now
			copy := *t
			err := s.saveLocked()
			s.mu.Unlock()
			return copy, err
		}
		s.mu.Unlock()

		if timeout == 0 || time.Now().After(deadline) {
			return nil, nil
		}
		ch, ok := s.registerNextWaiter(principal)
		if !ok {
			return nil, errors.New("已有等待中的 next；同一个 pane 不要并发领取")
		}
		wait := time.Until(deadline)
		if wait > time.Second {
			wait = time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			s.clearNextWaiter(principal, ch)
			return nil, errors.New("daemon 正在关闭")
		case <-ch:
			timer.Stop()
		case <-timer.C:
		}
		s.clearNextWaiter(principal, ch)
	}
}

// waitTask blocks until the task has a result (or the wait times out). Only the
// sender may wait; a timeout reports unknown state instead of an error.
func (s *Server) waitTask(principal string, raw json.RawMessage) (any, error) {
	var a struct {
		ID        string `json:"id"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	s.mu.Lock()
	t := s.taskLocked(a.ID)
	if t == nil {
		s.mu.Unlock()
		return nil, errors.New("任务不存在")
	}
	if principal != "controller" && t.From != principal {
		s.mu.Unlock()
		return nil, deniedError("only the task sender can wait for its result")
	}
	if finished(t.State) {
		copy := *t
		s.mu.Unlock()
		return copy, nil
	}
	if s.taskWaiters == nil {
		s.taskWaiters = map[string]chan struct{}{}
	}
	ch := make(chan struct{}, 1)
	s.taskWaiters[a.ID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.taskWaiters[a.ID] == ch {
			delete(s.taskWaiters, a.ID)
		}
		s.mu.Unlock()
	}()

	timeout := time.Duration(a.TimeoutMS) * time.Millisecond
	if a.TimeoutMS <= 0 {
		timeout = 60 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		t = s.taskLocked(a.ID)
		if t == nil {
			s.mu.Unlock()
			return nil, errors.New("任务不存在")
		}
		if finished(t.State) {
			copy := *t
			s.mu.Unlock()
			return copy, nil
		}
		copy := *t
		s.mu.Unlock()
		select {
		case <-s.ctx.Done():
			return copy, nil
		case <-ch:
		case <-timer.C:
			return copy, nil
		}
	}
}

func finished(state string) bool {
	return state == "completed" || state == "failed" || state == "cancelled"
}

// taskContentBytesLocked reports the retained prompt/result bytes. It is a
// simple scan: callers already hold s.mu and the record cap is 2,000.
func (s *Server) taskContentBytesLocked() int {
	total := 0
	for i := range s.state.Tasks {
		total += len(s.state.Tasks[i].Prompt) + len(s.state.Tasks[i].Result)
	}
	return total
}

// setTaskResultLocked stores a result only when the aggregate budget allows
// it. PTY results arrive through finishTask and ACP results through runQueue;
// sharing this check keeps both paths under the same limit.
func (s *Server) setTaskResultLocked(t *model.Task, result string) error {
	if delta := len(result) - len(t.Result); delta > 0 && s.taskContentBytesLocked()+delta > maxTaskContentBytes {
		return errors.New("任务内容总量已达到 64 MiB 上限；请缩短结果或使用新的 WTABLE_HOME")
	}
	t.Result = result
	return nil
}

func (s *Server) taskResult(principal string, raw json.RawMessage) (any, error) {
	return s.finishTask(principal, raw, false)
}
func (s *Server) finishTask(principal string, raw json.RawMessage, manual bool) (any, error) {
	var a struct {
		ID     string `json:"id"`
		Result string `json:"result"`
		Failed bool   `json:"failed"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if len(a.Result) > 1<<20 {
		return nil, errors.New("结果超过 1 MiB")
	}
	s.mu.Lock()
	t := s.taskLocked(a.ID)
	if t == nil {
		s.mu.Unlock()
		return nil, errors.New("任务不存在")
	}
	if (!manual && t.To != principal) || (manual && principal != "controller") {
		s.mu.Unlock()
		return nil, deniedError("only the task recipient can submit its result")
	}
	// A late result is accepted for delivered/accepted/unknown/cancelled/failed;
	// only a task that already produced a final result is closed for good.
	if t.State == "completed" {
		s.mu.Unlock()
		return nil, errors.New("task already has a result")
	}
	if !contains([]string{"running", "delivered", "accepted", "unknown", "cancelled", "failed", "cancelling"}, t.State) {
		s.mu.Unlock()
		return nil, fmt.Errorf("任务状态为 %s，不能提交结果", t.State)
	}
	if err := s.setTaskResultLocked(t, a.Result); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	t.State = "completed"
	if a.Failed {
		t.State = "failed"
	}
	t.UpdatedAt = time.Now()
	t.Seen = false
	if t.AcceptedAt == nil && !manual {
		now := time.Now()
		t.AcceptedAt = &now
	}
	if manual {
		t.Origin = "human"
	}
	t.ResultPushed = true
	s.completeReportLocked(t)
	copy := *s.taskLocked(a.ID)
	err := s.saveLocked()
	s.mu.Unlock()
	s.notifyTaskWaiter(a.ID)
	s.wake(copy.To)
	return copy, err
}

func (s *Server) getTasks(principal, method string, raw json.RawMessage) (any, error) {
	var a struct {
		ID        string `json:"id"`
		Workspace string `json:"workspace,omitempty"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	scope := ""
	if a.Workspace != "" {
		clean, err := cleanWorkspacePath(a.Workspace)
		if err != nil {
			return nil, err
		}
		scope = clean
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if principal != "controller" {
		if p := s.paneByIDLocked(principal); p != nil {
			scope = p.Workspace
		}
	}
	if method == "task.get" {
		t := s.taskLocked(a.ID)
		if t == nil {
			return nil, errors.New("任务不存在")
		}
		if scope != "" && t.Workspace != "" && t.Workspace != scope {
			return nil, deniedError("任务不属于该工作区")
		}
		if principal != "controller" && t.From != principal && t.To != principal {
			return nil, deniedError("无权读取任务")
		}
		return *t, nil
	}
	tasks := []model.Task{}
	for _, t := range s.state.Tasks {
		// Briefs are standing instructions, not delegated work. task.get still
		// returns one by ID; the default list stays readable.
		if t.Kind == model.TaskKindBrief {
			continue
		}
		if scope != "" && t.Workspace != "" && t.Workspace != scope {
			continue
		}
		if principal == "controller" || t.From == principal || t.To == principal {
			t.Prompt = short(t.Prompt, 300)
			t.Result = short(t.Result, 500)
			tasks = append(tasks, t)
		}
	}
	return tasks, nil
}

func (s *Server) runQueue(paneID string) {
	for {
		s.mu.Lock()
		if s.storeErr != nil || s.ctx.Err() != nil {
			s.mu.Unlock()
			return
		}
		p := s.paneLocked(paneID)
		a := s.agents[paneID]
		if p == nil || p.State != "running" || a == nil || p.TurnState == "running" || p.TurnState == "waiting-permission" {
			s.mu.Unlock()
			return
		}
		var taskID, prompt string
		for i := range s.state.Tasks {
			t := &s.state.Tasks[i]
			if t.To == paneID && t.State == "queued" && (t.DeliveryMode == "prompt" || t.DeliveryMode == "") && (t.From == "controller" || s.hasEdgeLocked(t.From, paneID)) {
				t.State = "delivered"
				t.DeliveryMode = "prompt"
				t.DeliveredVia = "prompt"
				t.DeliveryStatus = "submitted"
				t.DeliveryError = ""
				t.QueueReason = ""
				now := time.Now()
				t.DeliveredAt = &now
				t.UpdatedAt = now
				taskID = t.ID
				prompt = t.Prompt
				break
			}
		}
		if taskID == "" {
			s.mu.Unlock()
			return
		}
		p.TurnState = "running"
		runID := s.runs[paneID]
		turnCtx, turnCancel := context.WithCancel(s.ctx)
		s.taskCancels[taskID] = turnCancel
		if err := s.saveLocked(); err != nil {
			turnCancel()
			delete(s.taskCancels, taskID)
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		s.message(paneID, runID, "user", prompt)
		s.mu.Lock()
		if s.storeErr != nil {
			turnCancel()
		}
		s.mu.Unlock()
		result, err := a.Prompt(turnCtx, prompt)
		turnCancel()
		s.mu.Lock()
		delete(s.taskCancels, taskID)
		if s.closing || s.runs[paneID] != runID {
			s.mu.Unlock()
			return
		}
		t := s.taskLocked(taskID)
		p = s.paneLocked(paneID)
		if t != nil && (activeState(t.State) || t.State == "unknown") {
			t.State = "completed"
			stored := result
			if err != nil {
				t.State = "failed"
				stored = err.Error()
				if agent.IsUncertain(err) {
					t.State = "unknown"
				} else if errors.Is(err, context.Canceled) {
					t.State = "cancelled"
				}
			}
			if len(stored) > 1<<20 {
				stored = short(stored, 250000)
				t.State = "failed"
				stored = "结果超过 1 MiB，已截断：\n" + stored
			}
			if capErr := s.setTaskResultLocked(t, stored); capErr != nil {
				t.State = "failed"
				t.Result = capErr.Error()
			}
			t.UpdatedAt = time.Now()
			t.Seen = false
			s.completeReportLocked(t)
		}
		p = s.paneLocked(paneID)
		if p != nil && s.runs[paneID] == runID && p.State == "running" {
			p.TurnState = "idle"
		}
		s.saveLocked()
		s.mu.Unlock()
	}
}

func (s *Server) cancelTask(raw json.RawMessage) (any, error) {
	var args struct {
		ID        string `json:"id"`
		Confirmed bool   `json:"confirmed"`
	}
	if err := decode(raw, &args); err != nil {
		return nil, err
	}
	s.mu.Lock()
	t := s.taskLocked(args.ID)
	if t == nil {
		s.mu.Unlock()
		return nil, errors.New("任务不存在")
	}
	paneID := t.To
	s.mu.Unlock()
	lane := s.inputLane(paneID)
	lane.Lock()
	defer lane.Unlock()
	s.mu.Lock()
	t = s.taskLocked(args.ID)
	if contains([]string{"completed", "failed", "cancelled"}, t.State) {
		copy := *t
		s.mu.Unlock()
		return copy, nil
	}
	a := s.agents[t.To]
	term := s.terminals[t.To]
	if t.State != "queued" && taskMode(*t) == "pull" && !args.Confirmed {
		s.mu.Unlock()
		return nil, errors.New("PTY 领取任务不能可靠取消；确认工作已停止后使用 --confirmed，或停止 pane")
	}
	running := t.State != "queued"
	if running && a == nil && args.Confirmed && t.State != "cancelling" && taskMode(*t) != "pull" {
		s.mu.Unlock()
		return nil, errors.New("请先请求取消，再确认任务已经停止")
	}
	t.State = "cancelled"
	if running && (a != nil || !args.Confirmed) {
		t.State = "cancelling"
	}
	t.Result = "用户请求取消；已发生的操作不会撤销"
	t.UpdatedAt = time.Now()
	t.Seen = false
	cancel := s.taskCancels[t.ID]
	s.completeReportLocked(t)
	copy := *t
	err := s.saveLocked()
	s.mu.Unlock()
	if err != nil {
		return copy, err
	}
	if running && a != nil && cancel != nil {
		cancel()
	}
	if running && a == nil && !args.Confirmed {
		if term == nil {
			return copy, errors.New("没有运行中的 PTY；确认停止后使用 confirmed=true")
		}
		if err := term.Input([]byte{3}); err != nil {
			return copy, err
		}
	}
	return copy, nil
}

// promotedAt moves a queued task ahead of every other queued task for its pane.
func (s *Server) promoteTask(raw json.RawMessage) (model.Task, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := decode(raw, &a); err != nil {
		return model.Task{}, err
	}
	s.mu.Lock()
	t := s.taskLocked(a.ID)
	if t == nil {
		s.mu.Unlock()
		return model.Task{}, errors.New("任务不存在")
	}
	if t.State != "queued" {
		s.mu.Unlock()
		return model.Task{}, errors.New("只有排队中的任务可以提前")
	}
	var first *model.Task
	for i := range s.state.Tasks {
		other := &s.state.Tasks[i]
		// Keep a queued brief ahead of delegated work; promoting work must not
		// make the agent start without its instructions.
		if other.Kind == model.TaskKindBrief {
			continue
		}
		if other.To == t.To && other.State == "queued" && other.ID != t.ID {
			if first == nil || other.CreatedAt.Before(first.CreatedAt) {
				first = other
			}
		}
	}
	if first == nil {
		copy := *t
		s.mu.Unlock()
		return copy, nil
	}
	t.CreatedAt = first.CreatedAt.Add(-time.Nanosecond)
	t.UpdatedAt = time.Now()
	copy := *t
	err := s.saveLocked()
	s.mu.Unlock()
	if err == nil {
		s.wake(copy.To)
	}
	return copy, err
}

// editTask changes the prompt of a task that has not been delivered yet.
func (s *Server) editTask(raw json.RawMessage) (model.Task, error) {
	var a struct {
		ID     string `json:"id"`
		Prompt string `json:"prompt"`
	}
	if err := decode(raw, &a); err != nil {
		return model.Task{}, err
	}
	if strings.TrimSpace(a.Prompt) == "" || len(a.Prompt) > 1<<20 {
		return model.Task{}, errors.New("任务内容不能为空且不得超过 1 MiB")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.taskLocked(a.ID)
	if t == nil {
		return model.Task{}, errors.New("任务不存在")
	}
	if t.State != "queued" {
		return model.Task{}, errors.New("只有排队中的任务可以修改")
	}
	if delta := len(a.Prompt) - len(t.Prompt); delta > 0 && s.taskContentBytesLocked()+delta > maxTaskContentBytes {
		return model.Task{}, errors.New("任务内容总量已达到 64 MiB 上限")
	}
	t.Prompt = a.Prompt
	t.UpdatedAt = time.Now()
	return *t, s.saveLocked()
}

// interruptSend is the atomic "stop the current turn, then queue this message"
// path used by the composer. It validates everything that could fail before it
// cancels anything, so a rejected message never destroys in-flight work.
func (s *Server) interruptSend(principal string, raw json.RawMessage) (model.Task, error) {
	var a struct {
		To     string `json:"to"`
		Prompt string `json:"prompt"`
		Kind   string `json:"kind"`
	}
	if err := decode(raw, &a); err != nil {
		return model.Task{}, err
	}
	if len(a.Prompt) > 1<<20 {
		return model.Task{}, errors.New("任务内容不得超过 1 MiB")
	}
	if strings.TrimSpace(a.Prompt) == "" {
		a.Prompt = ""
	}
	s.mu.Lock()
	p := s.paneLocked(a.To)
	if p == nil {
		s.mu.Unlock()
		return model.Task{}, errors.New("目标 pane 不存在")
	}
	if principal != "controller" && !s.hasEdgeLocked(principal, p.ID) {
		s.mu.Unlock()
		return model.Task{}, deniedError("没有发送到此 pane 的授权连线", p.ID)
	}
	if p.State != "running" {
		s.mu.Unlock()
		return model.Task{}, errors.New("not-ready: 目标 pane 没有运行，请先 restart")
	}
	var active []*model.Task
	for i := range s.state.Tasks {
		t := &s.state.Tasks[i]
		// Interrupting a turn must not cancel the standing instructions.
		if t.Kind == model.TaskKindBrief {
			continue
		}
		if t.To == p.ID && activeState(t.State) {
			active = append(active, t)
		}
	}
	if a.Prompt == "" && len(active) == 0 {
		s.mu.Unlock()
		return model.Task{}, errors.New("当前没有正在执行的 turn 可以中断")
	}
	if a.Prompt != "" {
		if _, err := resolveDelivery(p, "prompt"); err != nil {
			// PTY panes do not accept the prompt path used by the composer.
			if _, err = resolveDelivery(p, ""); err != nil {
				s.mu.Unlock()
				return model.Task{}, err
			}
		}
		if len(s.state.Tasks) >= 2000 {
			s.mu.Unlock()
			return model.Task{}, errors.New("MVP 任务记录上限为 2,000 条")
		}
		if s.taskContentBytesLocked()+len(a.Prompt) > maxTaskContentBytes {
			s.mu.Unlock()
			return model.Task{}, errors.New("任务内容总量已达到 64 MiB 上限")
		}
	}
	var cancels []context.CancelFunc
	for _, t := range active {
		if c := s.taskCancels[t.ID]; c != nil {
			cancels = append(cancels, c)
		}
		t.State = "cancelled"
		t.Result = "被新消息中断"
		t.UpdatedAt = time.Now()
		t.Seen = false
		s.completeReportLocked(t)
	}
	var created model.Task
	var err error
	if a.Prompt != "" {
		if a.Kind == "" {
			a.Kind = "chat"
		}
		created, err = s.createTaskLocked(principal, model.SendParams{To: p.ID, Prompt: a.Prompt, DeliveryMode: "", Kind: a.Kind, Queue: true}, "")
	} else {
		err = s.saveLocked()
	}
	paneID := p.ID
	s.mu.Unlock()
	if err != nil {
		return model.Task{}, err
	}
	for _, cancel := range cancels {
		cancel()
	}
	if len(active) > 0 && p.Backend == "pty" {
		lane := s.inputLane(paneID)
		lane.Lock()
		if term := s.terminals[paneID]; term != nil {
			_ = term.Input([]byte{3})
		}
		lane.Unlock()
	}
	if created.ID != "" {
		s.notifyNextWaiter(created.To)
		s.wake(created.To)
	}
	return created, nil
}
