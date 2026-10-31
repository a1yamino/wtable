package server

import (
	"time"

	"wtable/internal/model"
)

// notifyNextWaiter wakes the (single) blocked next_task caller for this pane.
// The channel is buffered so a wake that races with registration is not lost.
func (s *Server) notifyNextWaiter(paneID string) {
	s.mu.Lock()
	ch := s.nextWaiters[paneID]
	s.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// registerNextWaiter installs one waiter per pane. A second concurrent next
// call is rejected by nextTask, so a plain slot is enough.
func (s *Server) registerNextWaiter(paneID string) (chan struct{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextWaiters == nil {
		s.nextWaiters = map[string]chan struct{}{}
	}
	if _, busy := s.nextWaiters[paneID]; busy {
		return nil, false
	}
	ch := make(chan struct{}, 1)
	s.nextWaiters[paneID] = ch
	return ch, true
}

func (s *Server) clearNextWaiter(paneID string, ch chan struct{}) {
	s.mu.Lock()
	if s.nextWaiters[paneID] == ch {
		delete(s.nextWaiters, paneID)
	}
	s.mu.Unlock()
}

// notifyTaskWaiter wakes a blocked task.wait for this task id.
func (s *Server) notifyTaskWaiter(taskID string) {
	s.mu.Lock()
	ch := s.taskWaiters[taskID]
	s.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
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
