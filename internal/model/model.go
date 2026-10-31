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

// PaneCaps is what a pane declares it can receive. The zero value means
// "derive from the backend", so older state files keep working.
type PaneCaps struct {
	ReceiveTasks  bool   `json:"receive_tasks"`
	ReturnResults bool   `json:"return_results"`
	PushIdle      bool   `json:"push_idle"`
	PushSteer     bool   `json:"push_steer"`
	Prompt        bool   `json:"prompt"`
	Pull          bool   `json:"pull"`
	Source        string `json:"source,omitempty"` // preset, user or probe
}

// EffectiveCaps returns the declared caps or the backend default.
func (p *Pane) EffectiveCaps() PaneCaps {
	if p.Caps != nil {
		return *p.Caps
	}
	if p.Backend == "acp" {
		return PaneCaps{ReceiveTasks: true, ReturnResults: true, Prompt: true, Source: "backend"}
	}
	return PaneCaps{ReceiveTasks: true, ReturnResults: true, PushIdle: true, Pull: true, Source: "backend"}
}

// DeliveryConfig controls the heuristic PTY gate; zero values select defaults.
type DeliveryConfig struct {
	BlockedRegex string `json:"blocked_regex,omitempty"`
	TailLines    int    `json:"tail_lines,omitempty"`
	SettleMS     int    `json:"settle_ms,omitempty"`
	VerifyMS     int    `json:"verify_ms,omitempty"`
	AckTimeoutMS int    `json:"ack_timeout_ms,omitempty"`
}

type Preset struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Backend   string   `json:"backend"`
	Command   []string `json:"command"`
	Available bool     `json:"available"`
	Version   string   `json:"version,omitempty"`
	Detail    string   `json:"detail,omitempty"`
}

type Catalog struct {
	Roles           []RoleTemplate `json:"roles"`
	AccountProfiles []AccountInfo  `json:"account_profiles"`
	Presets         []Preset       `json:"presets"`
	Accounts        []string       `json:"accounts"`
}

type PaneUpdate struct {
	RoleTemplate *string `json:"role_template,omitempty"`
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Role         string  `json:"role"`
	Account      string  `json:"account"`
}

type Pane struct {
	RoleTemplate     string         `json:"role_template,omitempty"`
	RoleInstructions string         `json:"role_instructions,omitempty"`
	RoleHash         string         `json:"role_hash,omitempty"`
	Endpoint         string         `json:"endpoint,omitempty"`
	LastOutput       string         `json:"last_output,omitempty"`
	LastActivity     time.Time      `json:"last_activity,omitempty"`
	Readiness        string         `json:"readiness,omitempty"`
	ReadinessReason  string         `json:"readiness_reason,omitempty"`
	DeniedCount      int            `json:"denied_count,omitempty"`
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Backend          string         `json:"backend"` // pty or acp
	Command          []string       `json:"command"`
	Cwd              string         `json:"cwd"`
	State            string         `json:"state"` // starting, running, exited, failed, interrupted
	TurnState        string         `json:"turn_state,omitempty"`
	SessionID        string         `json:"session_id,omitempty"`
	Preset           string         `json:"preset,omitempty"`
	Role             string         `json:"role,omitempty"`
	Account          string         `json:"account,omitempty"`
	Layer            string         `json:"layer,omitempty"`
	InjectBrief      bool           `json:"inject_brief,omitempty"`
	BriefPath        string         `json:"brief_path,omitempty"`
	BriefTask        string         `json:"brief_task,omitempty"`
	ExitSummary      string         `json:"exit_summary,omitempty"`
	LogPath          string         `json:"log_path,omitempty"`
	Error            string         `json:"error,omitempty"`
	ExitCode         *int           `json:"exit_code,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	Messages         []Message      `json:"messages,omitempty"`
	Frame            *Frame         `json:"frame,omitempty"`
	DeliveryConfig   DeliveryConfig `json:"delivery_config,omitempty"`
	Caps             *PaneCaps      `json:"caps,omitempty"`
}

type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Graph is a display-only projection; its virtual edges never grant access.
type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

type GraphNode struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Preset    string   `json:"preset"`
	Backend   string   `json:"backend"`
	Layer     string   `json:"layer"`
	State     string   `json:"state"`
	Attention []string `json:"attention"`
	Unread    int      `json:"unread"`
}

type GraphEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Kind   string `json:"kind"` // grant or virtual
	Active int    `json:"active"`
}

type Task struct {
	ID             string     `json:"id"`
	From           string     `json:"from"`
	To             string     `json:"to"`
	Prompt         string     `json:"prompt"`
	State          string     `json:"state"` // queued, delivered, accepted, completed, failed, unknown, cancelling, cancelled
	DeliveryMode   string     `json:"delivery_mode,omitempty"`
	DeliveryStatus string     `json:"delivery_status,omitempty"`
	DeliveryError  string     `json:"delivery_error,omitempty"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
	AcceptedAt     *time.Time `json:"accepted_at,omitempty"`
	OnComplete     string     `json:"on_complete,omitempty"`
	ReportTo       string     `json:"report_to,omitempty"`
	ParentTask     string     `json:"parent_task,omitempty"`
	Origin         string     `json:"origin,omitempty"`
	RetryOf        string     `json:"retry_of,omitempty"`
	ReportID       string     `json:"report_id,omitempty"`
	Seen           bool       `json:"seen"`
	Result         string     `json:"result,omitempty"`
	DedupeKey      string     `json:"dedupe_key,omitempty"`
	IdempotencyKey string     `json:"idempotency_key,omitempty"`
	QueueReason    string     `json:"queue_reason,omitempty"`
	DeliveredVia   string     `json:"delivered_via,omitempty"`
	SteerOf        string     `json:"steer_of,omitempty"`
	ResultPushed   bool       `json:"result_pushed,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type SendParams struct {
	To             string `json:"to"`
	Prompt         string `json:"prompt"`
	DedupeKey      string `json:"dedupe_key,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	DeliveryMode   string `json:"delivery_mode,omitempty"`
	Queue          bool   `json:"queue,omitempty"`
	OnComplete     string `json:"on_complete,omitempty"`
	ReportTo       string `json:"report_to,omitempty"`
	ParentTask     string `json:"parent_task,omitempty"`
	Steer          bool   `json:"steer,omitempty"`
}

type Report struct {
	TaskState   string    `json:"task_state,omitempty"`
	ID          string    `json:"id"`
	ParentTask  string    `json:"parent_task"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	State       string    `json:"state"` // delivered or undeliverable
	Mode        string    `json:"mode"`  // notify or handoff
	Summary     string    `json:"summary"`
	Error       string    `json:"error,omitempty"`
	HandoffTask string    `json:"handoff_task,omitempty"`
	Seen        bool      `json:"seen"`
	CreatedAt   time.Time `json:"created_at"`
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
	Reports     []Report     `json:"reports"`
	Graph       *Graph       `json:"graph,omitempty"`
	Revision    uint64       `json:"revision"`
}

type SpawnParams struct {
	RoleTemplate   string         `json:"role_template,omitempty"`
	Preset         string         `json:"preset,omitempty"`
	Role           string         `json:"role,omitempty"`
	Account        string         `json:"account,omitempty"`
	Layer          string         `json:"layer,omitempty"`
	InjectBrief    bool           `json:"inject_brief,omitempty"`
	ConnectTo      []string       `json:"connect_to,omitempty"`
	Name           string         `json:"name"`
	Backend        string         `json:"backend"`
	Command        []string       `json:"command"`
	Cwd            string         `json:"cwd"`
	Cols           int            `json:"cols"`
	Rows           int            `json:"rows"`
	DeliveryConfig DeliveryConfig `json:"delivery_config,omitempty"`
}

// RoleTemplate defaults apply only when the caller leaves a value unspecified.
type RoleTemplate struct {
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
	Preset       string `json:"preset,omitempty"`
	Backend      string `json:"backend,omitempty"`
	Cwd          string `json:"cwd,omitempty"`
}
type AccountInfo struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
}
type DeniedEvent struct {
	From   string    `json:"from"`
	To     string    `json:"to,omitempty"`
	Method string    `json:"method"`
	At     time.Time `json:"at"`
}
