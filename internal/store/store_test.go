package store

import (
	"path/filepath"
	"reflect"
	"testing"

	"wtable/internal/model"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return s
}

func TestSaveLoadRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		state State
	}{
		{
			name:  "empty",
			state: State{Version: 1},
		},
		{
			name: "populated",
			state: State{
				Version:   1,
				LastFocus: "pane-1",
				Panes: []model.Pane{
					{ID: "pane-1", Name: "editor", Command: []string{"sh"}},
				},
				Tasks: []model.Task{
					{ID: "task-1", Prompt: "inspect", Seen: true},
				},
				Edges: []model.Edge{{From: "pane-1", To: "pane-2"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			if err := s.Save(tt.state); err != nil {
				t.Fatalf("Save() error = %v", err)
			}
			got, err := s.Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.state) {
				t.Errorf("Load() = %#v, want %#v", got, tt.state)
			}
		})
	}
}

func TestLoadRejectsUnsupportedVersion(t *testing.T) {
	tests := []struct {
		name    string
		version int
	}{
		{name: "zero", version: 0},
		{name: "future", version: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			if err := s.Save(State{Version: tt.version}); err != nil {
				t.Fatalf("Save() error = %v", err)
			}
			if _, err := s.Load(); err == nil {
				t.Fatalf("Load() accepted version %d", tt.version)
			}
		})
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "truncated", body: `{"version":`},
		{name: "not JSON", body: `not-json`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			if _, err := s.db.Exec("INSERT INTO state(id, body) VALUES(1, ?)", tt.body); err != nil {
				t.Fatalf("insert malformed state: %v", err)
			}
			if _, err := s.Load(); err == nil {
				t.Fatal("Load() accepted malformed JSON")
			}
		})
	}
}
