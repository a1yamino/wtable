package server

import (
	"encoding/json"
	"errors"
	"time"

	"wtable/internal/model"
)

type permissionDenied struct{ message, target string }

func (e *permissionDenied) Error() string { return e.message }
func deniedError(message string, target ...string) error {
	e := &permissionDenied{message: message}
	if len(target) > 0 {
		e.target = target[0]
	}
	return e
}

// Store bounded metadata, never request payloads or credentials.
func (s *Server) recordDenied(principal, method, target string, raw json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	event := model.DeniedEvent{From: principal, Method: method, At: time.Now()}
	var args struct {
		To string `json:"to"`
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &args)
	if target == "" {
		target = args.To
	}
	if target == "" {
		if task := s.taskLocked(args.ID); task != nil {
			target = task.To
		}
		for _, report := range s.state.Reports {
			if report.ID == args.ID {
				target = report.To
				break
			}
		}
	}
	if p := s.paneLocked(target); p != nil {
		event.To = p.ID
	}
	if p := s.paneLocked(principal); p != nil {
		p.DeniedCount++
	}
	s.state.Denied = append(s.state.Denied, event)
	if len(s.state.Denied) > 256 {
		s.state.Denied = append([]model.DeniedEvent(nil), s.state.Denied[len(s.state.Denied)-256:]...)
	}
	return s.saveLocked()
}

func (s *Server) auditDenied(principal, method string, raw json.RawMessage, err error) error {
	var denied *permissionDenied
	if !errors.As(err, &denied) {
		return err
	}
	if saveErr := s.recordDenied(principal, method, denied.target, raw); saveErr != nil {
		return errors.Join(err, saveErr)
	}
	return err
}
