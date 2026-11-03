package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"wtable/internal/config"
	"wtable/internal/daemon"
	"wtable/internal/model"
	"wtable/internal/rpc"
)

type Bootstrap struct {
	Workspace string         `json:"workspace"`
	Snapshot  model.Snapshot `json:"snapshot"`
	Home      string         `json:"home"`
}

type TerminalChunk struct {
	Data   string `json:"data"`
	Cursor uint64 `json:"cursor"`
}

type TerminalAttach struct {
	History  string `json:"history"`
	Snapshot string `json:"snapshot"`
	Cursor   uint64 `json:"cursor"`
}

type App struct {
	ctx        context.Context
	control    *rpc.Client
	stream     *rpc.Client
	paths      config.Paths
	workspace  string
	startupErr error
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.startupErr = a.connect(ctx)
}

func (a *App) shutdown(context.Context) {
	if a.stream != nil {
		_ = a.stream.Close()
	}
	if a.control != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = a.control.Call(ctx, "ui.detach", nil, nil)
		cancel()
		_ = a.control.Close()
	}
}

func (a *App) connect(ctx context.Context) error {
	paths, err := config.Resolve()
	if err != nil {
		return err
	}
	if err = daemon.Ensure(ctx, paths); err != nil {
		return err
	}
	token, err := paths.ClientToken()
	if err != nil {
		return err
	}
	control, err := rpc.Dial(ctx, paths.Socket, token)
	if err != nil {
		return err
	}
	stream, err := rpc.Dial(ctx, paths.Socket, token)
	if err != nil {
		_ = control.Close()
		return err
	}
	if err = control.Call(ctx, "ui.attach", nil, nil); err != nil {
		_ = stream.Close()
		_ = control.Close()
		return fmt.Errorf("接入工作台失败：%w", err)
	}
	a.paths = paths
	a.control = control
	a.stream = stream
	return nil
}

func (a *App) Bootstrap() (Bootstrap, error) {
	if a.startupErr != nil {
		return Bootstrap{}, a.startupErr
	}
	var all model.Snapshot
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	if err := a.control.Call(ctx, "state.get", map[string]any{"frames": false, "graph": true}, &all); err != nil {
		return Bootstrap{}, err
	}
	workspace := chooseWorkspace(all)
	snapshot, err := a.snapshot(ctx, workspace)
	if err != nil {
		return Bootstrap{}, err
	}
	a.workspace = workspace
	return Bootstrap{Workspace: workspace, Snapshot: snapshot, Home: a.paths.Home}, nil
}

func chooseWorkspace(snapshot model.Snapshot) string {
	if workspace := os.Getenv("WTABLE_WORKSPACE"); workspace != "" {
		if absolute, err := filepath.Abs(workspace); err == nil {
			return absolute
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		for _, workspace := range snapshot.Workspaces {
			if workspace.Path == cwd {
				return cwd
			}
		}
	}
	for _, pane := range snapshot.Panes {
		if pane.ID == snapshot.LastFocus && pane.Workspace != "" {
			return pane.Workspace
		}
	}
	if len(snapshot.Workspaces) > 0 {
		return snapshot.Workspaces[0].Path
	}
	cwd, _ := os.Getwd()
	return cwd
}

func (a *App) GetSnapshot(workspace string) (model.Snapshot, error) {
	if a.startupErr != nil {
		return model.Snapshot{}, a.startupErr
	}
	if workspace == "" {
		workspace = a.workspace
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	snapshot, err := a.snapshot(ctx, workspace)
	if err == nil {
		a.workspace = workspace
	}
	return snapshot, err
}

func (a *App) snapshot(ctx context.Context, workspace string) (model.Snapshot, error) {
	var snapshot model.Snapshot
	err := a.control.Call(ctx, "state.get", map[string]any{
		"frames":    false,
		"graph":     true,
		"workspace": workspace,
	}, &snapshot)
	return snapshot, err
}

func (a *App) OpenWorkspace(path string) (model.Snapshot, error) {
	if a.startupErr != nil {
		return model.Snapshot{}, a.startupErr
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	if err := a.control.Call(ctx, "workspace.open", map[string]string{"path": path}, nil); err != nil {
		return model.Snapshot{}, err
	}
	a.workspace = path
	return a.snapshot(ctx, path)
}

func (a *App) FocusPane(id string) error {
	if a.startupErr != nil {
		return a.startupErr
	}
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	return a.control.Call(ctx, "ui.focus", map[string]string{"id": id}, nil)
}

func (a *App) CreateShell(name string) (model.Pane, error) {
	if a.startupErr != nil {
		return model.Pane{}, a.startupErr
	}
	if a.workspace == "" {
		return model.Pane{}, errors.New("请先选择工作区")
	}
	if name == "" {
		name = "shell"
	}
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	var pane model.Pane
	err := a.control.Call(ctx, "pane.spawn", model.SpawnParams{
		Name:      name,
		Backend:   "pty",
		Cwd:       a.workspace,
		Workspace: a.workspace,
		Cols:      120,
		Rows:      32,
	}, &pane)
	if err != nil {
		return model.Pane{}, err
	}
	_ = a.control.Call(ctx, "ui.focus", map[string]string{"id": pane.ID}, nil)
	return pane, nil
}

func (a *App) SendInput(id, data string) error {
	if a.startupErr != nil {
		return a.startupErr
	}
	if id == "" || data == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	return a.control.Call(ctx, "pane.input", map[string]string{
		"id": id, "workspace": a.workspace, "data": data,
	}, nil)
}

func (a *App) ResizeTerminal(id string, cols, rows int) error {
	if a.startupErr != nil {
		return a.startupErr
	}
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	return a.control.Call(ctx, "pane.resize", map[string]any{
		"id": id, "workspace": a.workspace, "cols": cols, "rows": rows,
	}, nil)
}

func (a *App) AttachTerminal(id string) (TerminalAttach, error) {
	if a.startupErr != nil {
		return TerminalAttach{}, a.startupErr
	}
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	var attachment struct {
		History  []byte `json:"history"`
		Snapshot []byte `json:"snapshot"`
		Cursor   uint64 `json:"cursor"`
	}
	err := a.control.Call(ctx, "pane.attach", map[string]string{
		"id": id, "workspace": a.workspace,
	}, &attachment)
	if err != nil {
		return TerminalAttach{}, err
	}
	return TerminalAttach{
		History:  base64.StdEncoding.EncodeToString(attachment.History),
		Snapshot: base64.StdEncoding.EncodeToString(attachment.Snapshot),
		Cursor:   attachment.Cursor,
	}, nil
}

func (a *App) ReadTerminal(id string, cursor uint64) (TerminalChunk, error) {
	if a.startupErr != nil {
		return TerminalChunk{}, a.startupErr
	}
	if id == "" {
		return TerminalChunk{Cursor: cursor}, nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	var output struct {
		Data   []byte `json:"data"`
		Cursor uint64 `json:"cursor"`
	}
	err := a.stream.Call(ctx, "pane.output", map[string]any{
		"id": id, "workspace": a.workspace, "cursor": cursor, "max_bytes": outputChunkSize, "wait_ms": 650,
	}, &output)
	if err != nil {
		return TerminalChunk{}, err
	}
	return TerminalChunk{
		Data:   base64.StdEncoding.EncodeToString(output.Data),
		Cursor: output.Cursor,
	}, nil
}

const outputChunkSize = 64 << 10
