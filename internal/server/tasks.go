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
	var a struct {
		To        string `json:"to"`
		Prompt    string `json:"prompt"`
		DedupeKey string `json:"dedupe_key"`
	}
	if err := decode(raw, &a); err != nil {
		return model.Task{}, err
	}
	if strings.TrimSpace(a.Prompt) == "" || len(a.Prompt) > 1<<20 {
		return model.Task{}, errors.New("任务内容不能为空且不得超过 1 MiB")
	}
	if len(a.DedupeKey) > 256 {
		return model.Task{}, errors.New("dedupe_key 最长 256 字节")
	}
	s.mu.Lock()
	p := s.paneLocked(a.To)
	if p == nil {
		s.mu.Unlock()
		return model.Task{}, errors.New("目标 pane 不存在")
	}
	if principal != "controller" && !s.hasEdgeLocked(principal, p.ID) {
		s.mu.Unlock()
		return model.Task{}, errors.New("没有发送到此 pane 的授权连线")
	}
	if a.DedupeKey != "" {
		for _, t := range s.state.Tasks {
			if t.From == principal && t.DedupeKey == a.DedupeKey {
				s.mu.Unlock()
				if t.To != p.ID || t.Prompt != a.Prompt {
					return model.Task{}, errors.New("相同 dedupe_key 对应不同任务内容")
				}
				return t, nil
			}
		}
	}
	if p.State != "running" {
		s.mu.Unlock()
		return model.Task{}, errors.New("目标 pane 没有运行，请先 restart")
	}
	if len(s.state.Tasks) >= 2000 {
		s.mu.Unlock()
		return model.Task{}, errors.New("MVP 任务记录上限为 2,000 条，请使用新的 WTABLE_HOME")
	}
	t := model.Task{ID: newID("t-"), From: principal, To: p.ID, Prompt: a.Prompt, State: "queued", DedupeKey: a.DedupeKey, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	s.state.Tasks = append(s.state.Tasks, t)
	err := s.saveLocked()
	backend := p.Backend
	s.mu.Unlock()
	if err == nil && backend == "acp" {
		go s.runQueue(t.To)
	}
	return t, err
}

func (s *Server) nextTask(principal string, raw json.RawMessage) (any, error) {
	if principal == "controller" {
		return nil, errors.New("next 必须在接收任务的 pane 内运行")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.state.Tasks {
		if t.To == principal && t.State == "running" {
			return nil, errors.New("已有领取中的任务，请先提交 result")
		}
	}
	for i := range s.state.Tasks {
		t := &s.state.Tasks[i]
		if t.To != principal || t.State != "queued" {
			continue
		}
		if t.From != "controller" && !s.hasEdgeLocked(t.From, t.To) {
			continue
		}
		t.State = "running"
		t.UpdatedAt = time.Now()
		copy := *t
		return copy, s.saveLocked()
	}
	return nil, nil
}

func (s *Server) taskResult(principal string, raw json.RawMessage) (any, error) {
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
	if t.To != principal {
		return nil, errors.New("只有接收 pane 可以提交结果")
	}
	state := "completed"
	if a.Failed {
		state = "failed"
	}
	if t.State == "completed" || t.State == "failed" {
		if t.State == state && t.Result == a.Result {
			return *t, nil
		}
		return nil, errors.New("任务已经完成，结果不能覆盖")
	}
	if t.State != "running" {
		return nil, fmt.Errorf("任务状态为 %s，不能提交结果", t.State)
	}
	t.State = state
	t.Result = a.Result
	t.UpdatedAt = time.Now()
	copy := *t
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
			if t.To == paneID && t.State == "queued" && (t.From == "controller" || s.hasEdgeLocked(t.From, paneID)) {
				t.State = "running"
				t.UpdatedAt = time.Now()
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
		if s.closing {
			s.mu.Unlock()
			return
		}
		t := s.taskLocked(taskID)
		p = s.paneLocked(paneID)
		if t != nil && (t.State == "running" || t.State == "cancelling" || t.State == "unknown") {
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
		}
		if p != nil && s.runs[paneID] == runID && p.State == "running" {
			p.TurnState = "idle"
		}
		s.saveLocked()
		s.mu.Unlock()
	}
}

func (s *Server) cancelTask(raw json.RawMessage) (any, error) {
	var args struct {
		ID string `json:"id"`
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
	if t.State != "queued" && t.State != "running" {
		copy := *t
		s.mu.Unlock()
		return copy, nil
	}
	a := s.agents[t.To]
	if t.State == "running" && a == nil {
		s.mu.Unlock()
		return nil, errors.New("PTY 任务不能可靠取消；请停止 pane 并检查执行结果")
	}
	running := t.State == "running"
	cancel := s.taskCancels[t.ID]
	t.State = "cancelled"
	if running {
		t.State = "cancelling"
	}
	t.Result = "用户请求取消；已发生的操作不会撤销"
	t.UpdatedAt = time.Now()
	copy := *t
	err := s.saveLocked()
	s.mu.Unlock()
	if running && cancel != nil {
		cancel()
	}
	return copy, err
}
