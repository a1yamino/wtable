package model

import "time"

// Colors are RGB; -1 means the outer terminal's default color.
type Cell struct {
	Text  string `json:"t"`
	FG    int32  `json:"f"`
	BG    int32  `json:"b"`
	Attr  uint32 `json:"a,omitempty"`
	Width int    `json:"w"`
}

const (
	Bold uint32 = 1 << iota
	Dim
	Italic
	Underline
	Reverse
	Strike
)

type Frame struct {
	Cols           int      `json:"cols"`
	Rows           int      `json:"rows"`
	Cells          [][]Cell `json:"cells"`
	CursorX        int      `json:"cursor_x"`
	CursorY        int      `json:"cursor_y"`
	CursorVisible  bool     `json:"cursor_visible"`
	Alternate      bool     `json:"alternate"`
	BracketedPaste bool     `json:"bracketed_paste"`
	AppCursor      bool     `json:"app_cursor"`
	MouseMode      int      `json:"mouse_mode"`
	MouseSGR       bool     `json:"mouse_sgr"`
	HistoryLines   int      `json:"history_lines"`
	Revision       uint64   `json:"revision"`
}

type Message struct {
	Role string    `json:"role"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

type Pane struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Backend   string    `json:"backend"` // pty or acp
	Command   []string  `json:"command"`
	Cwd       string    `json:"cwd"`
	State     string    `json:"state"` // starting, running, exited, failed, interrupted
	TurnState string    `json:"turn_state,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	Error     string    `json:"error,omitempty"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Messages  []Message `json:"messages,omitempty"`
	Frame     *Frame    `json:"frame,omitempty"`
}

type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type Task struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Prompt    string    `json:"prompt"`
	State     string    `json:"state"` // queued, running, completed, failed, unknown, cancelled
	Result    string    `json:"result,omitempty"`
	DedupeKey string    `json:"dedupe_key,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PermissionOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type Permission struct {
	ID      string             `json:"id"`
	PaneID  string             `json:"pane_id"`
	Title   string             `json:"title"`
	Options []PermissionOption `json:"options"`
}

type Layer struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Repo   string `json:"repo"`
}

type Snapshot struct {
	Panes       []Pane       `json:"panes"`
	Tasks       []Task       `json:"tasks"`
	Edges       []Edge       `json:"edges"`
	Layers      []Layer      `json:"layers"`
	Permissions []Permission `json:"permissions"`
	Revision    uint64       `json:"revision"`
}

type SpawnParams struct {
	Name    string   `json:"name"`
	Backend string   `json:"backend"`
	Command []string `json:"command"`
	Cwd     string   `json:"cwd"`
	Cols    int      `json:"cols"`
	Rows    int      `json:"rows"`
}
