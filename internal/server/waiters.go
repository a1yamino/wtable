package server

import (
	"strings"
	"time"

	"wtable/internal/model"
)

func nextWaiterKey(paneID, runID string) string { return paneID + "\x00" + runID }

// notifyNextWaiter wakes every run's blocked next caller for this pane. Old
// runs then fail their generation check; only the current run may claim.
func (s *Server) notifyNextWaiter(paneID string) {
	s.mu.Lock()
	channels := []chan struct{}{}
	prefix := paneID + "\x00"
	for key, ch := range s.nextWaiters {
		if strings.HasPrefix(key, prefix) {
			channels = append(channels, ch)
		}
	}
	s.mu.Unlock()
	for _, ch := range channels {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// registerNextWaiter installs one waiter per pane and run. A replaced run can
// finish without preventing the newly authenticated instance from polling.
func (s *Server) registerNextWaiter(paneID, runID string) (chan struct{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextWaiters == nil {
		s.nextWaiters = map[string]chan struct{}{}
	}
	key := nextWaiterKey(paneID, runID)
	if _, busy := s.nextWaiters[key]; busy {
		return nil, false
	}
	ch := make(chan struct{}, 1)
	s.nextWaiters[key] = ch
	return ch, true
}

func (s *Server) clearNextWaiter(paneID, runID string, ch chan struct{}) {
	s.mu.Lock()
	key := nextWaiterKey(paneID, runID)
	if s.nextWaiters[key] == ch {
		delete(s.nextWaiters, key)
	}
	s.mu.Unlock()
}

// notifyFinishedTaskWaitersLocked is called by every successful state Save.
// Looking at committed task states here, rather than notifying at individual
// mutation sites, covers ACK timeouts, process/pane lifecycle events, delivery
// failures and late ACP outcomes through one path. The caller holds s.mu.
func (s *Server) notifyFinishedTaskWaitersLocked() {
	for key, ch := range s.taskWaiters {
		taskID, _, _ := strings.Cut(key, "\x00")
		t := s.taskLocked(taskID)
		if t == nil || !finished(t.State) {
			continue
		}
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// awaitFirstHop waits briefly for a freshly queued task to leave queued, so
// task.send can report delivered when the pane was ready. It never fails the
// call: a task still waiting is a normal outcome.
func (s *Server) awaitFirstHop(id string, timeout time.Duration) model.Task {
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		t := s.taskLocked(id)
		if t == nil {
			s.mu.Unlock()
			return model.Task{}
		}
		state := t.State
		reason := t.QueueReason
		copy := *t
		s.mu.Unlock()
		if state != "queued" {
			return copy
		}
		if timeout <= 0 || time.Now().After(deadline) {
			if reason != "" {
				copy.QueueReason = reason
			}
			return copy
		}
		time.Sleep(20 * time.Millisecond)
	}
}
