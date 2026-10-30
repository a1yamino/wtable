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

func (s *Server) edge(method string, raw json.RawMessage) (any, error) {
	var a model.Edge
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	from, to := s.paneLocked(a.From), s.paneLocked(a.To)
	if from == nil || to == nil {
		return nil, errors.New("连线的 pane 不存在")
	}
	if from.ID == to.ID {
		return nil, errors.New("不能连接到自身")
	}
	a.From = from.ID
	a.To = to.ID
	if method == "edge.add" {
		if !s.hasEdgeLocked(a.From, a.To) {
			s.state.Edges = append(s.state.Edges, a)
		}
	} else {
		for i, e := range s.state.Edges {
			if e == a {
				s.state.Edges = append(s.state.Edges[:i], s.state.Edges[i+1:]...)
				break
			}
		}
		for i := range s.state.Tasks {
			t := &s.state.Tasks[i]
			if t.From == a.From && t.To == a.To && t.State == "queued" {
				t.State = "cancelled"
				t.Result = "授权连线已移除"
				t.UpdatedAt = time.Now()
			}
		}
	}
	return a, s.saveLocked()
}

func (s *Server) peers(principal string) []model.Pane {
	s.mu.Lock()
	defer s.mu.Unlock()
	panes := []model.Pane{}
	for _, p := range s.state.Panes {
		if principal == "controller" || s.hasEdgeLocked(principal, p.ID) {
			p.Messages = nil
			p.Command = nil
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
	paneID := a.To
	if p := s.paneLocked(a.To); p != nil {
		paneID = p.ID
	}
	s.mu.Unlock()
	lane := s.inputLane(paneID)
	lane.Lock()
	defer lane.Unlock()
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
	if !existing && t.DeliveryMode == "inject" && !a.Queue {
		if err := s.deliverInLane(t.ID, true); err != nil {
			return t, err
		}
		s.mu.Lock()
		t = *s.taskLocked(t.ID)
		s.mu.Unlock()
	} else if !existing && t.DeliveryMode == "prompt" {
		go s.runQueue(t.To)
	}
	return t, nil
}

func (s *Server) createTaskLocked(principal string, a model.SendParams, retryOf string) (model.Task, error) {
	if strings.TrimSpace(a.Prompt) == "" || len(a.Prompt) > 1<<20 {
		return model.Task{}, errors.New("任务内容不能为空且不得超过 1 MiB")
	}
	if len(a.DedupeKey) > 256 {
		return model.Task{}, errors.New("dedupe_key 最长 256 字节")
	}
	p := s.paneLocked(a.To)
	if p == nil {
		return model.Task{}, errors.New("目标 pane 不存在")
	}
	if principal != "controller" && !s.hasEdgeLocked(principal, p.ID) {
		return model.Task{}, errors.New("没有发送到此 pane 的授权连线")
	}
	a.To = p.ID
	if a.DeliveryMode == "" {
		a.DeliveryMode = "claim"
		if principal == "controller" {
			a.DeliveryMode = "inject"
		}
		if p.Backend == "acp" {
			a.DeliveryMode = "prompt"
		}
	}
	if (p.Backend == "acp" && a.DeliveryMode != "prompt") || (p.Backend != "acp" && a.DeliveryMode != "inject" && a.DeliveryMode != "claim") {
		return model.Task{}, errors.New("delivery_mode 与目标 backend 不兼容")
	}
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
		recipient := s.paneLocked(a.ReportTo)
		if recipient == nil {
			return model.Task{}, errors.New("report_to pane 不存在")
		}
		a.ReportTo = recipient.ID
		if principal != "controller" && !s.hasEdgeLocked(principal, recipient.ID) {
			return model.Task{}, errors.New("没有发送到 report_to 的授权连线")
		}
	}
	if a.ParentTask != "" {
		parent := s.taskLocked(a.ParentTask)
		if parent == nil || (principal != "controller" && parent.From != principal && parent.To != principal) {
			return model.Task{}, errors.New("无权引用 parent_task")
		}
	}
	if a.DedupeKey != "" {
		for _, t := range s.state.Tasks {
			if t.From == principal && t.DedupeKey == a.DedupeKey {
				if t.To != a.To || t.Prompt != a.Prompt || t.DeliveryMode != a.DeliveryMode || t.OnComplete != a.OnComplete || t.ReportTo != a.ReportTo || t.ParentTask != a.ParentTask {
					return model.Task{}, errors.New("相同 dedupe_key 对应不同任务内容")
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
	if a.DeliveryMode == "inject" && !a.Queue {
		if err := s.readyLocked(p); err != nil {
			return model.Task{}, err
		}
	}
	now := time.Now()
	origin := principal
	if origin == "controller" {
		origin = "human"
	}
	t := model.Task{ID: newID("t-"), From: principal, Origin: origin, To: a.To, Prompt: a.Prompt, State: "queued", DeliveryMode: a.DeliveryMode, OnComplete: a.OnComplete, ReportTo: a.ReportTo, ParentTask: a.ParentTask, RetryOf: retryOf, DedupeKey: a.DedupeKey, CreatedAt: now, UpdatedAt: now}
	s.state.Tasks = append(s.state.Tasks, t)
	return t, s.saveLocked()
}

func (s *Server) nextTask(principal string, raw json.RawMessage) (any, error) {
	if principal == "controller" {
		return nil, errors.New("next 必须在接收任务的 pane 内运行")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.paneLocked(principal); p != nil && p.Backend == "acp" {
		return nil, errors.New("ACP 任务由 prompt 通道自动接收")
	}
	for _, t := range s.state.Tasks {
		if t.To == principal && (t.State == "running" || (t.State == "accepted" && taskMode(t) == "claim")) {
			return nil, errors.New("已有领取中的任务，请先提交 result")
		}
	}
	for i := range s.state.Tasks {
		t := &s.state.Tasks[i]
		if t.To != principal || t.State != "queued" || taskMode(*t) != "claim" {
			continue
		}
		if t.From != "controller" && !s.hasEdgeLocked(t.From, t.To) {
			continue
		}
		now := time.Now()
		t.State = "running"
		t.AcceptedAt = &now
		t.UpdatedAt = now
		return *t, s.saveLocked()
	}
	return nil, nil
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
	defer s.mu.Unlock()
	t := s.taskLocked(a.ID)
	if t == nil {
		return nil, errors.New("任务不存在")
	}
	if (!manual && t.To != principal) || (manual && principal != "controller") {
		return nil, errors.New("只有接收 pane 可以提交结果；人工完成需要控制端")
	}
	if t.State == "completed" {
		return nil, errors.New("任务已经完成，结果不能覆盖")
	}
	if !contains([]string{"running", "delivered", "accepted", "unknown", "cancelled", "failed", "cancelling"}, t.State) {
		return nil, fmt.Errorf("任务状态为 %s，不能提交结果", t.State)
	}
	t.State = "completed"
	if a.Failed {
		t.State = "failed"
	}
	t.Result = a.Result
	t.UpdatedAt = time.Now()
	t.Seen = false
	if t.AcceptedAt == nil && !manual {
		now := time.Now()
		t.AcceptedAt = &now
	}
	if manual {
		t.Origin = "human"
	}
	s.completeReportLocked(t)
	copy := *s.taskLocked(a.ID)
	return copy, s.saveLocked()
}

func (s *Server) getTasks(principal, method string, raw json.RawMessage) (any, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if method == "task.get" {
		t := s.taskLocked(a.ID)
		if t == nil {
			return nil, errors.New("任务不存在")
		}
		if principal != "controller" && t.From != principal && t.To != principal {
			return nil, errors.New("无权读取任务")
		}
		return *t, nil
	}
	tasks := []model.Task{}
	for _, t := range s.state.Tasks {
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
				now := time.Now()
				t.DeliveredAt = &now
				t.DeliveryStatus = "submitted"
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
			t.Result = result
			if err != nil {
				t.State = "failed"
				t.Result = err.Error()
				if agent.IsUncertain(err) {
					t.State = "unknown"
				} else if errors.Is(err, context.Canceled) {
					t.State = "cancelled"
				}
			}
			if len(t.Result) > 1<<20 {
				t.Result = short(t.Result, 250000)
				t.State = "failed"
				t.Result = "结果超过 1 MiB，已截断：\n" + t.Result
			}
			t.UpdatedAt = time.Now()
			if t.State == "completed" || t.State == "failed" {
				s.completeReportLocked(t)
			}
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
	if t.State != "queued" && taskMode(*t) == "claim" {
		s.mu.Unlock()
		return nil, errors.New("PTY 领取任务不能可靠取消；请停止 pane 并检查执行结果")
	}
	running := t.State != "queued"
	if running && a == nil && args.Confirmed && t.State != "cancelling" {
		s.mu.Unlock()
		return nil, errors.New("请先请求取消，再确认任务已经停止")
	}
	t.State = "cancelled"
	if running && (a != nil || !args.Confirmed) {
		t.State = "cancelling"
	}
	t.Result = "用户请求取消；已发生的操作不会撤销"
	t.UpdatedAt = time.Now()
	cancel := s.taskCancels[t.ID]
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
