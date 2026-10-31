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
	// Chat turns belong to the conversation stream, not the delegation inbox.
	if t.Kind == "chat" {
		return
	}
	if t.OnComplete == "" || t.OnComplete == "none" || !contains([]string{"unknown", "cancelled", "completed", "failed"}, t.State) {
		return
	}
	id := "report:" + t.ID + ":1"
	index := -1
	finalReported := false
	r := model.Report{ID: id, ParentTask: t.ID, From: t.To, To: t.ReportTo, Mode: t.OnComplete, CreatedAt: time.Now()}
	for i, existing := range s.state.Reports {
		if existing.ID == id {
			index = i
			r = existing
			// Before task_state existed, reports were only created for final results.
			finalReported = existing.TaskState == "" || existing.TaskState == "completed" || existing.TaskState == "failed" || existing.HandoffTask != ""
			break
		}
	}
	t.ReportID = id
	final := t.State == "completed" || t.State == "failed"
	if finalReported && !final {
		return
	}
	r.TaskState = t.State
	r.State = "delivered"
	r.Error = ""
	r.Seen = false
	summary := t.Result
	if !final {
		summary = fmt.Sprintf("任务状态：%s；尚无最终结果。", t.State)
		if t.Result != "" {
			summary += "\n" + t.Result
		}
		if t.DeliveryError != "" {
			// Reports can be addressed to a different pane than the target.
			// Never carry the target's matched screen line across that boundary,
			// even when the task was sent by the controller.
			summary += "\n" + sanitizedBlockedText(t.DeliveryError)
		}
	}
	r.Summary = reportSummary(summary)
	recipient := s.paneLocked(t.ReportTo)
	if recipient == nil || (t.From != "controller" && !s.hasEdgeLocked(t.From, t.ReportTo)) {
		r.State = "undeliverable"
		r.Error = "报告目标不存在或原发送方的授权连线已撤销"
	} else if t.OnComplete == "handoff" && final && !finalReported {
		if len(s.state.Tasks) >= 2000 {
			r.State = "undeliverable"
			r.Error = "任务记录已达上限"
		} else if mode, modeErr := resolveDelivery(recipient, ""); modeErr != nil {
			// The follow-up is execution, not notification: its target must
			// accept tasks. Never bypass a pane's declared capabilities.
			r.State = "undeliverable"
			r.Error = "handoff 目标不接受后续任务：" + modeErr.Error()
		} else {
			now := time.Now()
			next := model.Task{ID: newID("t-"), From: t.From, Origin: t.Origin, To: recipient.ID, Prompt: fmt.Sprintf("Task %s (%s) finished with state %s.\n%s", t.ID, t.To, t.State, reportSummary(t.Result)), Kind: "handoff", State: "queued", DeliveryMode: mode, ParentTask: t.ID, OnComplete: "none", CreatedAt: now, UpdatedAt: now}
			if s.taskContentBytesLocked()+len(next.Prompt) > maxTaskContentBytes {
				r.State = "undeliverable"
				r.Error = "任务内容总量已达上限，无法创建 handoff 任务"
			} else {
				r.HandoffTask = next.ID
				s.state.Tasks = append(s.state.Tasks, next)
				// Safe while holding s.mu since the pump has its own lock; push and
				// prompt both make progress immediately instead of waiting a tick.
				s.wake(next.To)
			}
		}
	}
	if index < 0 {
		s.state.Reports = append(s.state.Reports, r)
	} else {
		s.state.Reports[index] = r
	}
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
				return nil, deniedError("无权读取报告")
			}
			if method == "report.seen" {
				if principal != "controller" && principal != r.To {
					return nil, deniedError("只有报告接收方可以标记已读")
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
			return nil, deniedError("只有接收 pane 可以 ack")
		}
		if t.State == "accepted" {
			return *t, nil
		}
		mode := taskMode(*t)
		if (mode != "push" && mode != "steer" && mode != "pull") || (t.State != "delivered" && t.State != "unknown") {
			return nil, errors.New("只有已注入或未知的任务可以 ack")
		}
		now := time.Now()
		t.State = "accepted"
		t.AcceptedAt = &now
		t.DeliveryStatus = "submitted"
		t.DeliveryError = ""
	case "task.confirm":
		if principal != "controller" {
			return nil, deniedError("人工确认需要控制端")
		}
		mode := taskMode(*t)
		if (mode != "push" && mode != "steer" && mode != "pull") || (t.State != "delivered" && t.State != "unknown") {
			return nil, errors.New("任务当前不能确认投递")
		}
		t.DeliveryStatus = "submitted"
		t.DeliveryError = ""
	case "task.seen":
		// Unread belongs to the recipient. Letting the sender clear it would
		// hide a completed result from the recipient's inbox.
		if principal != "controller" && principal != t.To {
			return nil, deniedError("只有任务接收方可以标记已读")
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
	// accidentally elevate an agent's report/handoff authorization. A steer
	// belongs to a turn that has already ended, so a retry is ordinary work.
	delivery := old.DeliveryMode
	if taskMode(old) == "steer" {
		delivery = "push"
	}
	params := model.SendParams{To: a.To, Prompt: old.Prompt, Kind: old.Kind, DeliveryMode: delivery, Queue: a.Queue, OnComplete: old.OnComplete, ReportTo: old.ReportTo, ParentTask: old.ParentTask}
	if p := s.paneLocked(a.To); p != nil {
		mode, err := resolveDelivery(p, params.DeliveryMode)
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		params.DeliveryMode = mode
	}
	s.mu.Unlock()
	s.mu.Lock()
	original = s.taskLocked(old.ID)
	priorTask := *original
	var priorReports []model.Report
	reportedUnknown := false
	if original.DeliveryStatus == "write-unconfirmed" && original.State == "delivered" {
		// createTaskLocked persists the retry and this status report together.
		// Restore both if validation rejects the retry.
		priorReports = append([]model.Report(nil), s.state.Reports...)
		original.State = "unknown"
		original.UpdatedAt = time.Now()
		original.Seen = false
		s.completeReportLocked(original)
		reportedUnknown = true
	}
	t, err := s.createTaskLocked(old.From, params, old.ID)
	if err != nil {
		original = s.taskLocked(old.ID)
		*original = priorTask
		if reportedUnknown {
			s.state.Reports = priorReports
		}
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	s.wake(t.To)
	return s.awaitFirstHop(t.ID, 1200*time.Millisecond), nil
}
