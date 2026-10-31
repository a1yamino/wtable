package server

import (
	"sort"
	"time"

	"wtable/internal/model"
)

// pumpState is the per-pane single-flight guard. One goroutine may write into a
// pane at a time; events that arrive while it is running only mark pending.
type pumpState struct {
	running bool
	pending bool
}

// wake asks the pump for one pane to look at its queue. It never blocks and
// never writes by itself. Pump state carries its own lock so callers may wake
// while holding s.mu — report completion does exactly that — and the reverse
// order never occurs, which is what keeps that path from deadlocking.
func (s *Server) wake(paneID string) {
	if paneID == "" {
		return
	}
	s.pumpMu.Lock()
	if s.pumps == nil {
		s.pumps = map[string]*pumpState{}
	}
	st := s.pumps[paneID]
	if st == nil {
		st = &pumpState{}
		s.pumps[paneID] = st
	}
	if st.running {
		st.pending = true
		s.pumpMu.Unlock()
		return
	}
	st.running = true
	s.pumpMu.Unlock()
	go s.pumpLoop(paneID, st)
}

func (s *Server) pumpLoop(paneID string, st *pumpState) {
	for {
		s.pumpStep(paneID)
		s.pumpMu.Lock()
		// s.closing is intentionally not read here: it is guarded by s.mu, and
		// pumpStep re-checks it under that lock before touching any state.
		again := st.pending && s.ctx.Err() == nil
		if again {
			st.pending = false
			s.pumpMu.Unlock()
			continue
		}
		st.running = false
		s.pumpMu.Unlock()
		return
	}
}

// pumpStep delivers at most one queued task for this pane, in FIFO order, and
// only when the pane has no task in flight. A failed gate leaves the task
// queued with a reason; the next event decides whether to try again.
func (s *Server) pumpStep(paneID string) {
	s.mu.Lock()
	if s.storeErr != nil || s.closing || s.ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	p := s.paneLocked(paneID)
	if p == nil || p.State != "running" {
		s.mu.Unlock()
		return
	}
	activeID := ""
	for i := range s.state.Tasks {
		t := &s.state.Tasks[i]
		if t.To == paneID && activeState(t.State) {
			activeID = t.ID
			break
		}
	}
	var pick *model.Task
	var mode string
	order := make([]*model.Task, 0, len(s.state.Tasks))
	for i := range s.state.Tasks {
		t := &s.state.Tasks[i]
		if t.To == paneID && t.State == "queued" && (t.From == "controller" || s.hasEdgeLocked(t.From, paneID)) {
			order = append(order, t)
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].CreatedAt.Before(order[j].CreatedAt) })
	caps := p.EffectiveCaps()
	steerBusy := false
	for i := range s.state.Tasks {
		t := &s.state.Tasks[i]
		if t.To == paneID && taskMode(*t) == "steer" && activeState(t.State) {
			steerBusy = true
			break
		}
	}
	for _, t := range order {
		m := taskMode(*t)
		// A steer that was queued for a turn which has already ended (or a
		// capability that was revoked) must not wait forever. Turn it into
		// ordinary work so it cannot be injected into a later unrelated turn.
		if m == "steer" && (!caps.PushSteer || !s.paneBusyLocked(paneID)) {
			t.DeliveryMode = "push"
			if t.QueueReason != "steer_expired: 目标已空闲或不再支持 steer，已改为普通投递" {
				t.QueueReason = "steer_expired: 目标已空闲或不再支持 steer，已改为普通投递"
				t.UpdatedAt = time.Now()
				s.saveLocked()
			}
			m = "push"
		}
		if m == "prompt" && (!caps.Prompt || p.Backend != "acp") {
			if t.QueueReason != "caps: 目标已不再接受 prompt 投递" {
				t.QueueReason = "caps: 目标已不再接受 prompt 投递"
				t.UpdatedAt = time.Now()
				s.saveLocked()
			}
			continue
		}
		if m == "steer" && caps.PushSteer && !steerBusy && (p.TurnState == "running" || activeID != "") {
			// Steering exists to reach a pane that is already working, so it
			// bypasses the serial check below.
			pick, mode = t, "steer"
			activeID = ""
			break
		}
		if (m == "push" || m == "prompt") && pick == nil {
			pick, mode = t, m
		}
	}
	if pick == nil {
		s.mu.Unlock()
		return
	}
	id := pick.ID
	if activeID != "" {
		// Serial per pane: explain the wait instead of leaving the queue silent.
		if pick.QueueReason != "busy:"+activeID {
			pick.QueueReason = "busy:" + activeID
			pick.UpdatedAt = time.Now()
			s.saveLocked()
		}
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	if mode == "prompt" {
		s.runQueue(paneID)
		return
	}
	lane := s.inputLane(paneID)
	lane.Lock()
	err := s.deliverInLane(id, false)
	lane.Unlock()
	if err == nil {
		return
	}
	// Keep the task queued and remember why, so the UI can explain the wait.
	// The reason is peer-visible, so never copy a raw target screen line here.
	s.mu.Lock()
	if t := s.taskLocked(id); t != nil && t.State == "queued" {
		reason := sanitizedDeliveryReason(err, t.From)
		if t.QueueReason != reason {
			t.QueueReason = reason
			t.UpdatedAt = time.Now()
			s.saveLocked()
		}
	}
	s.mu.Unlock()
}

// paneBusyLocked reports whether the pane is in the middle of work. ACP panes
// expose that through turn_state; a PTY pane is busy while a push task is in
// flight, which is the signal steering needs.
func (s *Server) paneBusyLocked(paneID string) bool {
	if p := s.paneLocked(paneID); p != nil && p.TurnState == "running" {
		return true
	}
	for i := range s.state.Tasks {
		t := &s.state.Tasks[i]
		if t.To == paneID && (taskMode(*t) == "push" || taskMode(*t) == "steer") && activeState(t.State) {
			return true
		}
	}
	return false
}

// wakeAll restarts pumps for panes that already have queued work (daemon start,
// pane restart, queue change).
func (s *Server) wakeQueued() {
	s.mu.Lock()
	panes := map[string]bool{}
	for i := range s.state.Tasks {
		t := &s.state.Tasks[i]
		if t.State == "queued" {
			panes[t.To] = true
		}
	}
	s.mu.Unlock()
	for id := range panes {
		s.wake(id)
	}
}
