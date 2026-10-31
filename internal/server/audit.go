package server

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"wtable/internal/model"
)

// recordDecisionLocked appends one permission outcome to the bounded audit log.
// decision is derived from the option kind unless an explicit one is supplied.
func (s *Server) recordDecisionLocked(a *approval, optionID, decision string, batch bool) {
	kind := ""
	for _, o := range a.permission.Options {
		if o.ID == optionID {
			kind = o.Kind
			break
		}
	}
	if decision == "" {
		switch {
		case optionID == "":
			decision = "cancel"
		case strings.HasPrefix(kind, "allow"):
			decision = "allow"
		case strings.HasPrefix(kind, "reject"):
			decision = "deny"
		default:
			// Servers are not required to use the allow*/reject* prefixes.
			// Never claim an approval we cannot prove from the option kind.
			decision = "unknown"
		}
	}
	entry := model.PermissionAudit{
		PermissionID: a.permission.ID,
		PaneID:       a.permission.PaneID,
		Decision:     decision,
		OptionID:     optionID,
		OptionKind:   kind,
		Title:        cleanInboxText(a.permission.Title, 200),
		Batch:        batch,
		At:           time.Now(),
	}
	s.state.Decisions = append(s.state.Decisions, entry)
	if len(s.state.Decisions) > 256 {
		s.state.Decisions = append([]model.PermissionAudit(nil), s.state.Decisions[len(s.state.Decisions)-256:]...)
	}
}

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
