package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"wtable/internal/model"
)

func activeState(state string) bool {
	return contains([]string{"running", "delivered", "accepted", "cancelling"}, state)
}
func taskMode(t model.Task) string {
	if t.DeliveryMode == "" {
		return "claim"
	}
	return t.DeliveryMode
}
func (s *Server) inputLane(id string) *sync.Mutex {
	v, _ := s.inputLanes.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}
func deliveryConfig(p *model.Pane) model.DeliveryConfig {
	c := p.DeliveryConfig
	if c.TailLines == 0 {
		c.TailLines = 12
	}
	if c.SettleMS == 0 {
		c.SettleMS = 400
	}
	if c.VerifyMS == 0 {
		c.VerifyMS = 3000
	}
	if c.AckTimeoutMS == 0 {
		c.AckTimeoutMS = 90000
	}
	if c.BlockedRegex == "" && len(p.Command) > 0 {
		switch filepath.Base(p.Command[0]) {
		case "codex", "claude", "agy":
			c.BlockedRegex = `(?i)(log in|sign in|trust (this|the) (folder|directory)|allow (this|the) command|update available|permission required|would you like to run the following command\?|run this command\?|do you trust the contents of this project\?|allow access to this file\?|you are currently not signed in\.)`
		}
	}
	return c
}
func frameLines(f model.Frame) []string {
	lines := make([]string, 0, len(f.Cells))
	for _, row := range f.Cells {
		var b strings.Builder
		for _, c := range row {
			b.WriteString(c.Text)
		}
		if line := strings.TrimSpace(b.String()); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
func (s *Server) readyLocked(p *model.Pane) error {
	term := s.terminals[p.ID]
	if p.State != "running" || term == nil {
		return errors.New("not-ready: pane 进程未运行")
	}
	alive, last := term.Readiness()
	if !alive {
		return errors.New("not-ready: pane 进程已停止")
	}
	c := deliveryConfig(p)
	lines, _ := term.Tail(c.TailLines)
	if c.BlockedRegex != "" {
		re, err := regexp.Compile(c.BlockedRegex)
		if err != nil {
			return fmt.Errorf("blocked_regex: %w", err)
		}
		for _, line := range lines {
			if re.MatchString(line) {
				return fmt.Errorf("blocked:%s", line)
			}
		}
	}
	if time.Since(last) < time.Duration(c.SettleMS)*time.Millisecond {
		return errors.New("not-ready: 等待终端输出稳定")
	}
	for _, t := range s.state.Tasks {
		if t.To == p.ID && taskMode(t) == "inject" && (t.State == "delivered" || t.State == "accepted" || t.State == "cancelling") {
			return fmt.Errorf("busy:%s；用 --queue 显式排队", t.ID)
		}
	}
	return nil
}
func taskEnvelope(t model.Task) string {
	return fmt.Sprintf("[WTABLE TASK %s]\n%s\n[WTABLE END %s]\nAccept this task with: \"$WTABLE_CLI\" ack %s\nWhen finished, submit the full result with: \"$WTABLE_CLI\" result %s <result>\nDo not print credentials.\n", t.ID, t.Prompt, t.ID, t.ID, t.ID)
}
func (s *Server) deliverTask(id string) {
	s.mu.Lock()
	t := s.taskLocked(id)
	if t == nil {
		s.mu.Unlock()
		return
	}
	paneID := t.To
	s.mu.Unlock()
	lane := s.inputLane(paneID)
	lane.Lock()
	defer lane.Unlock()
	_ = s.deliverInLane(id, false)
}
func (s *Server) deliverInLane(id string, prechecked bool) error {
	s.mu.Lock()
	t := s.taskLocked(id)
	if t == nil || t.State != "queued" || taskMode(*t) != "inject" || s.storeErr != nil || s.ctx.Err() != nil {
		s.mu.Unlock()
		return errors.New("任务不能投递：状态已改变或服务不可用")
	}
	p := s.paneLocked(t.To)
	if p == nil {
		s.mu.Unlock()
		return errors.New("任务不能投递：状态已改变或服务不可用")
	}
	if t.From != "controller" && !s.hasEdgeLocked(t.From, t.To) {
		t.State = "cancelled"
		t.Result = "授权连线已移除"
		t.UpdatedAt = time.Now()
		t.Seen = false
		s.completeReportLocked(t)
		s.saveLocked()
		s.mu.Unlock()
		return errors.New("任务不能投递：状态已改变或服务不可用")
	}
	if err := s.readyLocked(p); err != nil && !prechecked {
		if t.DeliveryError != err.Error() {
			t.DeliveryError = err.Error()
			t.UpdatedAt = time.Now()
			s.saveLocked()
		}
		s.mu.Unlock()
		return err
	}
	term := s.terminals[p.ID]
	config := deliveryConfig(p)
	before := frameLines(term.Snapshot(0))
	envelope := taskEnvelope(*t)
	data := envelope
	if term.Snapshot(0).BracketedPaste {
		data = "\x1b[200~" + strings.TrimSuffix(envelope, "\n") + "\x1b[201~\r"
	} else {
		// One physical line is one submission. JSON retains prompt newlines without
		// turning each envelope instruction into a separate terminal command.
		payload, _ := json.Marshal(struct {
			TaskID       string `json:"task_id"`
			Prompt       string `json:"prompt"`
			Instructions string `json:"instructions"`
		}{t.ID, t.Prompt, fmt.Sprintf("Accept with \"$WTABLE_CLI\" ack %s; finish with \"$WTABLE_CLI\" result %s <result>. Do not print credentials.", t.ID, t.ID)})
		envelope = string(payload)
		data = envelope + "\r"
	}
	now := time.Now()
	t.State = "delivered"
	t.DeliveredAt = &now
	t.UpdatedAt = now
	t.DeliveryStatus = "verifying"
	t.DeliveryError = ""
	// Persist before IO: a crash may leave an unknown outcome, never a replayable queue entry.
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	err := term.Input([]byte(data))
	s.mu.Lock()
	t = s.taskLocked(id)
	if err != nil && t != nil && t.State == "delivered" {
		t.State = "unknown"
		t.DeliveryStatus = "write-unconfirmed"
		t.DeliveryError = err.Error()
		t.UpdatedAt = time.Now()
		t.Seen = false
		s.completeReportLocked(t)
		s.saveLocked()
	}
	s.mu.Unlock()
	if err == nil {
		go s.verifyDelivery(id, before, envelope, config)
	}
	return err
}
func compact(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}
func (s *Server) verifyDelivery(id string, before []string, envelope string, cfg model.DeliveryConfig) {
	timer := time.NewTimer(time.Duration(cfg.VerifyMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return
	case <-timer.C:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.taskLocked(id)
	if t == nil || t.State != "delivered" || t.DeliveryStatus != "verifying" {
		return
	}
	term := s.terminals[t.To]
	if term == nil {
		return
	}
	lines := frameLines(term.Snapshot(0))
	old := map[string]bool{}
	for _, line := range before {
		old[compact(line)] = true
	}
	echo := compact(envelope)
	echoVisible, changed := false, false
	for _, line := range lines {
		key := compact(line)
		if len(key) > 0 && strings.Contains(echo, key) {
			echoVisible = true
			continue
		}
		if !old[key] {
			changed = true
		}
	}
	t.DeliveryStatus = "submitted"
	if echoVisible && !changed {
		t.DeliveryStatus = "write-unconfirmed"
		t.DeliveryError = "终端仍显示注入文本，未观察到其他新输出；请人工检查"
	}
	t.UpdatedAt = time.Now()
	s.saveLocked()
}
func (s *Server) deliveryLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
		s.mu.Lock()
		if s.storeErr != nil {
			s.mu.Unlock()
			continue
		}
		var queued []string
		changed := false
		for i := range s.state.Tasks {
			t := &s.state.Tasks[i]
			if t.State == "queued" && taskMode(*t) == "inject" {
				queued = append(queued, t.ID)
			}
			if t.State == "delivered" && taskMode(*t) == "inject" && t.DeliveredAt != nil {
				p := s.paneLocked(t.To)
				if p != nil && time.Since(*t.DeliveredAt) >= time.Duration(deliveryConfig(p).AckTimeoutMS)*time.Millisecond {
					t.State = "unknown"
					t.DeliveryError = "ack_timeout: 未收到 ack 或 result，不会自动重投"
					t.UpdatedAt = time.Now()
					t.Seen = false
					s.completeReportLocked(t)
					changed = true
				}
			}
		}
		if changed {
			s.saveLocked()
		}
		s.mu.Unlock()
		for _, id := range queued {
			s.deliverTask(id)
		}
	}
}
func (s *Server) acceptACP(paneID, runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs[paneID] != runID || s.closing {
		return
	}
	for i := range s.state.Tasks {
		t := &s.state.Tasks[i]
		if t.To == paneID && t.State == "delivered" && t.DeliveryMode == "prompt" {
			now := time.Now()
			t.State = "accepted"
			t.AcceptedAt = &now
			t.UpdatedAt = now
			s.saveLocked()
			return
		}
	}
}
