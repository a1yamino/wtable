package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"wtable/internal/model"
)

// reportSummary is byte-bounded and never cuts a UTF-8 sequence.
func reportSummary(text string) string {
	if len(text) <= 4096 {
		return text
	}
	text = text[:4093]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text + "…"
}

// Called under the same state lock and Save as completion. Appending a handoff
// may reallocate Tasks, so callers must reacquire task pointers afterwards.
func (s *Server) completeReportLocked(t *model.Task) {
	if t.OnComplete == "" || t.OnComplete == "none" || t.ReportID != "" {
		return
	}
	id := "report:" + t.ID + ":1"
	for _, r := range s.state.Reports {
		if r.ID == id {
			t.ReportID = id
			return
		}
	}
	t.ReportID = id
	r := model.Report{ID: id, ParentTask: t.ID, From: t.To, To: t.ReportTo, Mode: t.OnComplete, State: "delivered", Summary: reportSummary(t.Result), CreatedAt: time.Now()}
	recipient := s.paneLocked(t.ReportTo)
	if recipient == nil || (t.From != "controller" && !s.hasEdgeLocked(t.From, t.ReportTo)) {
		r.State = "undeliverable"
		r.Error = "报告目标不存在或原发送方的授权连线已撤销"
	} else if t.OnComplete == "handoff" {
		if len(s.state.Tasks) >= 2000 {
			r.State = "undeliverable"
			r.Error = "任务记录已达上限"
		} else {
			mode := "claim"
			if t.From == "controller" {
				mode = "inject"
			}
			if recipient.Backend == "acp" {
				mode = "prompt"
			}
			now := time.Now()
			next := model.Task{ID: newID("t-"), From: t.From, Origin: t.Origin, To: recipient.ID, Prompt: fmt.Sprintf("Task %s (%s) finished with state %s.\n%s", t.ID, t.To, t.State, reportSummary(t.Result)), State: "queued", DeliveryMode: mode, ParentTask: t.ID, OnComplete: "none", CreatedAt: now, UpdatedAt: now}
			r.HandoffTask = next.ID
			s.state.Tasks = append(s.state.Tasks, next)
			if mode == "prompt" {
				go s.runQueue(next.To)
			}
		}
	}
	s.state.Reports = append(s.state.Reports, r)
}

func (s *Server) reports(principal, method string, raw json.RawMessage) (any, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	visible := func(r model.Report) bool {
		parent := s.taskLocked(r.ParentTask)
		return principal == "controller" || (r.To == principal && r.State == "delivered") || r.From == principal || (parent != nil && parent.From == principal)
	}
	if method != "report.list" {
		for i := range s.state.Reports {
			r := &s.state.Reports[i]
			if r.ID != a.ID {
				continue
			}
			if !visible(*r) {
				return nil, errors.New("无权读取报告")
			}
			if method == "report.seen" {
				if principal != "controller" && principal != r.To {
					return nil, errors.New("只有报告接收方可以标记已读")
				}
				r.Seen = true
				return *r, s.saveLocked()
			}
			return *r, nil
		}
		return nil, errors.New("报告不存在")
	}
	result := []model.Report{}
	for _, r := range s.state.Reports {
		if visible(r) {
			result = append(result, r)
		}
	}
	return result, nil
}

func (s *Server) taskMark(principal, method string, raw json.RawMessage) (any, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.taskLocked(a.ID)
	if t == nil {
		return nil, errors.New("任务不存在")
	}
	switch method {
	case "task.ack":
		if principal != t.To {
			return nil, errors.New("只有接收 pane 可以 ack")
		}
		if t.State == "accepted" {
			return *t, nil
		}
		if taskMode(*t) != "inject" || (t.State != "delivered" && t.State != "unknown") {
			return nil, errors.New("只有已注入或未知的任务可以 ack")
		}
		now := time.Now()
		t.State = "accepted"
		t.AcceptedAt = &now
		t.DeliveryStatus = "submitted"
		t.DeliveryError = ""
	case "task.confirm":
		if principal != "controller" {
			return nil, errors.New("人工确认需要控制端")
		}
		if taskMode(*t) != "inject" || (t.State != "delivered" && t.State != "unknown") {
			return nil, errors.New("任务当前不能确认投递")
		}
		t.DeliveryStatus = "submitted"
		t.DeliveryError = ""
	case "task.seen":
		if principal != "controller" && principal != t.From && principal != t.To {
			return nil, errors.New("无权读取任务")
		}
		t.Seen = true
	}
	t.UpdatedAt = time.Now()
	return *t, s.saveLocked()
}

func (s *Server) retryTask(raw json.RawMessage) (any, error) {
	var a struct {
		ID    string `json:"id"`
		To    string `json:"to"`
		Queue bool   `json:"queue"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	s.mu.Lock()
	original := s.taskLocked(a.ID)
	if original == nil {
		s.mu.Unlock()
		return nil, errors.New("任务不存在")
	}
	if !contains([]string{"failed", "unknown", "cancelled"}, original.State) && !(original.State == "delivered" && original.DeliveryStatus == "write-unconfirmed") {
		s.mu.Unlock()
		return nil, errors.New("只有失败、未知、取消或写入未确认的任务可以重发")
	}
	old := *original
	if a.To == "" {
		a.To = old.To
	}
	// Retrying preserves the original sender's grants; a controller cannot
	// accidentally elevate an agent's report/handoff authorization.
	params := model.SendParams{To: a.To, Prompt: old.Prompt, DeliveryMode: old.DeliveryMode, Queue: a.Queue, OnComplete: old.OnComplete, ReportTo: old.ReportTo, ParentTask: old.ParentTask}
	if params.DeliveryMode == "" {
		params.DeliveryMode = "claim"
	}
	if p := s.paneLocked(a.To); p != nil && p.Backend == "acp" {
		params.DeliveryMode = "prompt"
	} else if params.DeliveryMode == "prompt" {
		params.DeliveryMode = "inject"
	}
	s.mu.Unlock()
	laneID := params.To
	s.mu.Lock()
	if p := s.paneLocked(laneID); p != nil {
		laneID = p.ID
	}
	s.mu.Unlock()
	lane := s.inputLane(laneID)
	lane.Lock()
	defer lane.Unlock()
	s.mu.Lock()
	original = s.taskLocked(old.ID)
	priorState := original.State
	if original.DeliveryStatus == "write-unconfirmed" && original.State == "delivered" {
		original.State = "unknown"
	}
	t, err := s.createTaskLocked(old.From, params, old.ID)
	if err != nil {
		original = s.taskLocked(old.ID)
		original.State = priorState
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if t.DeliveryMode == "inject" && !a.Queue {
		if err := s.deliverInLane(t.ID, true); err != nil {
			return t, err
		}
		s.mu.Lock()
		t = *s.taskLocked(t.ID)
		s.mu.Unlock()
	} else if t.DeliveryMode == "prompt" {
		go s.runQueue(t.To)
	}
	return t, nil
}
