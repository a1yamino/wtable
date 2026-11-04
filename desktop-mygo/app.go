package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

type SpawnPaneRequest struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Preset      string   `json:"preset"`
	Role        string   `json:"role"`
	Account     string   `json:"account"`
	Cwd         string   `json:"cwd"`
	Workspace   string   `json:"workspace"`
	Command     []string `json:"command"`
	InjectBrief bool     `json:"injectBrief"`
}

type UpdatePaneRequest struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Account   string `json:"account"`
	Workspace string `json:"workspace"`
}

type App struct {
	mu         sync.RWMutex
	cancel     context.CancelFunc
	terminals  map[*NativeTerminal]struct{}
	closing    bool
	ctx        context.Context
	control    *rpc.Client
	paths      config.Paths
	workspace  string
	startupErr error
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx, a.cancel = context.WithCancel(ctx)
	a.terminals = make(map[*NativeTerminal]struct{})
	a.startupErr = a.connect(a.ctx)
}

func (a *App) shutdown(context.Context) {
	a.mu.Lock()
	a.closing = true
	terms := make([]*NativeTerminal, 0, len(a.terminals))
	for t := range a.terminals {
		terms = append(terms, t)
	}
	a.mu.Unlock()
	// Stop readers and input workers before releasing the single UI writer.
	if a.cancel != nil {
		a.cancel()
	}
	for _, t := range terms {
		_ = t.Close()
	}
	if a.control != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = a.control.Call(ctx, "ui.detach", nil, nil)
		cancel()
		_ = a.control.Close()
	}
}

func (a *App) currentWorkspace() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workspace
}

func (a *App) setWorkspace(workspace string) {
	a.mu.Lock()
	a.workspace = workspace
	a.mu.Unlock()
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
	if err = control.Call(ctx, "ui.attach", nil, nil); err != nil {
		_ = control.Close()
		return fmt.Errorf("接入工作台失败：%w", err)
	}
	a.paths = paths
	a.control = control
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
	a.setWorkspace(workspace)
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
		workspace = a.currentWorkspace()
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	snapshot, err := a.snapshot(ctx, workspace)
	if err == nil {
		a.setWorkspace(workspace)
	}
	return snapshot, err
}

// GetAllSnapshot returns the complete pane and workspace inventory. The
// desktop rail needs every workspace even though terminal traffic remains
// scoped to the active workspace.
func (a *App) GetAllSnapshot() (model.Snapshot, error) {
	if a.startupErr != nil {
		return model.Snapshot{}, a.startupErr
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	var snapshot model.Snapshot
	err := a.control.Call(ctx, "state.get", map[string]any{
		"frames": false,
		"graph":  true,
	}, &snapshot)
	return snapshot, err
}

func (a *App) GetCatalog() (model.Catalog, error) {
	if a.startupErr != nil {
		return model.Catalog{}, a.startupErr
	}
	ctx, cancel := context.WithTimeout(a.ctx, 8*time.Second)
	defer cancel()
	var catalog model.Catalog
	err := a.control.Call(ctx, "preset.list", map[string]string{"kind": "worker"}, &catalog)
	return catalog, err
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
	a.setWorkspace(path)
	return a.snapshot(ctx, path)
}

func (a *App) CreateWorkspace(path string) (model.Snapshot, error) {
	if a.startupErr != nil {
		return model.Snapshot{}, a.startupErr
	}
	clean := filepath.Clean(path)
	if clean == "." || !filepath.IsAbs(clean) {
		return model.Snapshot{}, errors.New("工作区必须使用绝对路径")
	}
	if err := os.MkdirAll(clean, 0o755); err != nil {
		return model.Snapshot{}, fmt.Errorf("创建工作区目录：%w", err)
	}
	return a.OpenWorkspace(clean)
}

func (a *App) FocusPane(id string) error {
	if a.startupErr != nil {
		return a.startupErr
	}
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	return a.control.Call(ctx, "ui.focus", map[string]string{"id": id}, nil)
}

func (a *App) SpawnPane(args SpawnPaneRequest) (model.Pane, error) {
	if a.startupErr != nil {
		return model.Pane{}, a.startupErr
	}
	if a.currentWorkspace() == "" {
		return model.Pane{}, errors.New("请先选择工作区")
	}
	workspace := args.Workspace
	if workspace == "" {
		workspace = a.currentWorkspace()
	}
	kind := args.Kind
	if kind == "" {
		kind = model.PaneWorker
	}
	cwd := args.Cwd
	if cwd == "" {
		cwd = workspace
	}
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	var pane model.Pane
	command := args.Command
	if args.Preset != "" {
		command = nil
	}
	err := a.control.Call(ctx, "pane.spawn", model.SpawnParams{
		Name:        args.Name,
		Kind:        kind,
		Preset:      args.Preset,
		Role:        args.Role,
		Account:     args.Account,
		Cwd:         cwd,
		Workspace:   workspace,
		Command:     command,
		InjectBrief: args.InjectBrief,
		Cols:        120,
		Rows:        32,
	}, &pane)
	if err != nil {
		return model.Pane{}, err
	}
	_ = a.control.Call(ctx, "ui.focus", map[string]string{"id": pane.ID}, nil)
	return pane, nil
}

func (a *App) UpdatePane(args UpdatePaneRequest) (model.Pane, error) {
	if a.startupErr != nil {
		return model.Pane{}, a.startupErr
	}
	workspace := args.Workspace
	if workspace == "" {
		workspace = a.currentWorkspace()
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	var pane model.Pane
	err := a.control.Call(ctx, "pane.update", model.PaneUpdate{
		ID:        args.ID,
		Name:      args.Name,
		Role:      args.Role,
		Account:   args.Account,
		Workspace: workspace,
	}, &pane)
	return pane, err
}

func (a *App) RemovePane(id string) error {
	if a.startupErr != nil {
		return a.startupErr
	}
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	return a.control.Call(ctx, "pane.remove", map[string]string{
		"id":        id,
		"workspace": a.currentWorkspace(),
	}, nil)
}

func (a *App) AddEdge(from, to string) (model.Edge, error) {
	return a.edge("edge.add", from, to)
}

func (a *App) RemoveEdge(from, to string) (model.Edge, error) {
	return a.edge("edge.remove", from, to)
}

func (a *App) edge(method, from, to string) (model.Edge, error) {
	if a.startupErr != nil {
		return model.Edge{}, a.startupErr
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	var edge model.Edge
	err := a.control.Call(ctx, method, map[string]string{
		"from":      from,
		"to":        to,
		"workspace": a.currentWorkspace(),
	}, &edge)
	return edge, err
}

func (a *App) CreateShell(name string) (model.Pane, error) {
	if a.startupErr != nil {
		return model.Pane{}, a.startupErr
	}
	workspace := a.currentWorkspace()
	if workspace == "" {
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
		Cwd:       workspace,
		Workspace: workspace,
		Cols:      120,
		Rows:      32,
	}, &pane)
	if err != nil {
		return model.Pane{}, err
	}
	_ = a.control.Call(ctx, "ui.focus", map[string]string{"id": pane.ID}, nil)
	return pane, nil
}
