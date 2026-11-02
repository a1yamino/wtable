package server

import (
	"encoding/json"
	"testing"
	"time"

	"wtable/internal/model"
)

func TestPaneOutputCursorAndImmediateRead(t *testing.T) {
	s := onboardingServer(t)
	pane, err := s.spawn(model.SpawnParams{
		Name: "output", Cwd: s.paths.Home, Command: []string{"/bin/cat"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.stop(pane.ID, "")
	if err = s.terminals[pane.ID].Input([]byte("hello")); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	result, err := s.dispatch(connection{principal: "controller"}, "pane.output", rawTask(map[string]any{
		"id": pane.ID, "cursor": 0, "max_bytes": 64, "wait_ms": 500,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 400*time.Millisecond {
		t.Fatal("pane.output did not wake for new PTY data")
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var chunk struct {
		Data   []byte `json:"data"`
		Cursor uint64 `json:"cursor"`
	}
	if err = json.Unmarshal(data, &chunk); err != nil {
		t.Fatal(err)
	}
	if string(chunk.Data) != "hello" || chunk.Cursor != 5 {
		t.Fatalf("unexpected output chunk: %q cursor=%d", chunk.Data, chunk.Cursor)
	}

	start = time.Now()
	_, err = s.dispatch(connection{principal: "controller"}, "pane.output", rawTask(map[string]any{
		"id": pane.ID, "cursor": chunk.Cursor, "max_bytes": 64, "wait_ms": 0,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("wait_ms=0 must return without blocking")
	}
}
