package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	ghostty "github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"wtable/internal/model"
)

// The UI owns this state. Background work only publishes results with post;
// terminal I/O and daemon requests never run in a view callback.
type Workbench struct {
	service *App
	win     *mygo.Window
	ctx     context.Context
	cancel  context.CancelFunc
	workers sync.WaitGroup

	data       model.Snapshot
	catalog    model.Catalog
	active     string
	state      DesktopState
	ready      bool
	busy       bool
	launchStep string
	failure    string
	notice     string
	preview    *TopologyTemplate
	forms      formState
	board      boardState

	terminals            map[string]*terminalSlot
	retiredTerminals     terminalRetirements
	lastTerminalID       string
	pendingTerminalFocus string
	saves                chan []byte
}

type terminalSlot struct {
	terminal *NativeTerminal
	cancel   context.CancelFunc
	err      error
}

func (w *Workbench) restoreTerminalFocus() {
	w.pendingTerminalFocus = ""
	if id := w.lastTerminalID; w.visibleTerminalIDs()[id] {
		w.pendingTerminalFocus = id
	}
}

// focusPanes is shared by rendering and terminal ownership, including the
// first-pane fallback used before a valid focus has been restored.
func (w *Workbench) focusPanes() (pane, dock model.Pane, hasPane, hasDock bool) {
	state := w.workspaceUI()
	pane, hasPane = w.pane(state.FocusID)
	if !hasPane {
		if panes := w.panes(); len(panes) > 0 {
			pane, hasPane = panes[0], true
		}
	}
	dock, hasDock = w.pane(state.DockID)
	hasDock = hasPane && hasDock && dock.ID != pane.ID
	return
}

func (w *Workbench) visibleTerminalIDs() map[string]bool {
	visible := make(map[string]bool)
	if !w.ready || w.preview != nil {
		return visible
	}
	state := w.workspaceUI()
	if state.Mode == "board" {
		for _, pane := range w.panes() {
			if pane.Backend == "pty" && state.Layouts[pane.ID].Expanded {
				visible[pane.ID] = true
			}
		}
		return visible
	}
	pane, dock, hasPane, hasDock := w.focusPanes()
	if hasPane && pane.Backend == "pty" {
		visible[pane.ID] = true
	}
	if hasDock && dock.Backend == "pty" {
		visible[dock.ID] = true
	}
	return visible
}

func (w *Workbench) removeTerminalSlot(id string, slot *terminalSlot) {
	if w.terminals[id] != slot {
		return
	}
	delete(w.terminals, id)
	slot.cancel()
}

func (w *Workbench) reconcileTerminals() {
	visible := w.visibleTerminalIDs()
	for id, slot := range w.terminals {
		if !visible[id] {
			w.removeTerminalSlot(id, slot)
		}
	}
	if !visible[w.pendingTerminalFocus] {
		w.pendingTerminalFocus = ""
	}
}

func newWorkbench() *Workbench {
	ctx, cancel := context.WithCancel(context.Background())
	return &Workbench{
		service: NewApp(), ctx: ctx, cancel: cancel,
		state:     DesktopState{Workspaces: make(map[string]*WorkspaceUI)},
		terminals: make(map[string]*terminalSlot), saves: make(chan []byte, 1),
	}
}

func (w *Workbench) background(fn func()) {
	w.workers.Add(1)
	go func() {
		defer w.workers.Done()
		fn()
	}()
}

func (w *Workbench) post(fn func()) {
	if w.ctx.Err() != nil {
		return
	}
	w.win.Update(func() {
		if w.ctx.Err() == nil {
			fn()
		}
	})
}

func (w *Workbench) start() {
	w.background(func() {
		w.post(func() { w.launchStep = "正在接入后台会话…" })
		w.service.startup(w.ctx)
		boot, err := w.service.Bootstrap()
		if err != nil {
			w.post(func() { w.failure = err.Error() })
			return
		}
		statePath := filepath.Join(boot.Home, "desktop.json")
		state, stateErr := loadDesktopState(statePath)
		if stateErr != nil {
			w.post(func() { w.failure = fmt.Sprintf("无法读取 %s：%v", statePath, stateErr) })
			return
		}
		w.post(func() { w.launchStep = "正在检测可用的 Agent…" })
		catalog, err := w.service.GetCatalog()
		if err != nil {
			w.post(func() { w.failure = err.Error() })
			return
		}
		w.post(func() { w.launchStep = "正在恢复工作区…" })
		all, err := w.service.GetAllSnapshot()
		if err != nil {
			w.post(func() { w.failure = err.Error() })
			return
		}
		w.background(func() { w.persist(statePath) })
		w.post(func() {
			w.state, w.catalog, w.active = state, catalog, boot.Workspace
			w.ready = true
			w.acceptSnapshot(all)
		})
		w.poll()
	})
}

func (w *Workbench) retryStartup() {
	if w.ready || w.busy {
		return
	}
	w.busy, w.failure = true, ""
	previous := w.service
	w.background(func() {
		previous.shutdown(context.Background())
		w.post(func() {
			w.service, w.busy = NewApp(), false
			w.start()
		})
	})
}

func (w *Workbench) poll() {
	timer := time.NewTicker(850 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-timer.C:
			snapshot, err := w.service.GetAllSnapshot()
			w.post(func() {
				if err != nil {
					w.failure = "连接后台失败：" + err.Error()
					return
				}
				w.acceptSnapshot(snapshot)
			})
		}
	}
}

func (w *Workbench) stop() {
	w.cancel()
	w.workers.Wait()
	w.service.shutdown(context.Background())
}

func (w *Workbench) persist(path string) {
	for {
		var data []byte
		select {
		case data = <-w.saves:
		case <-w.ctx.Done():
			select {
			case data = <-w.saves:
			default:
				return
			}
		}
		if err := writeDesktopState(path, data); err != nil {
			w.post(func() { w.failure = "保存桌面布局失败：" + err.Error() })
			fmt.Fprintln(os.Stderr, "wtable desktop preferences:", err)
		}
	}
}

func (w *Workbench) save() {
	if !w.ready {
		return
	}
	data, err := json.MarshalIndent(w.state, "", "  ")
	if err != nil {
		w.failure = err.Error()
		return
	}
	// Only the most recent complete state needs to be written.
	select {
	case <-w.saves:
	default:
	}
	w.saves <- data
}

func (w *Workbench) workspaceUI() *WorkspaceUI {
	state := w.state.Workspaces[w.active]
	if state == nil {
		state = defaultWorkspaceUI()
		w.state.Workspaces[w.active] = state
	}
	if state.Layouts == nil {
		state.Layouts = make(map[string]NodeLayout)
	}
	if state.Viewport.Scale <= 0 {
		state.Viewport = BoardViewport{X: 32, Y: 32, Scale: 1}
	}
	if state.DockWidth < 240 {
		state.DockWidth = 360
	}
	return state
}

func (w *Workbench) panes() []model.Pane {
	panes := make([]model.Pane, 0)
	for _, pane := range w.data.Panes {
		if pane.Workspace == w.active {
			panes = append(panes, pane)
		}
	}
	return panes
}

func (w *Workbench) pane(id string) (model.Pane, bool) {
	for _, pane := range w.data.Panes {
		if pane.ID == id && pane.Workspace == w.active {
			return pane, true
		}
	}
	return model.Pane{}, false
}

func (w *Workbench) edges() []model.Edge {
	ids := make(map[string]bool)
	for _, pane := range w.panes() {
		ids[pane.ID] = true
	}
	edges := make([]model.Edge, 0)
	for _, edge := range w.data.Edges {
		if ids[edge.From] && ids[edge.To] {
			edges = append(edges, edge)
		}
	}
	return edges
}

func (w *Workbench) acceptSnapshot(snapshot model.Snapshot) {
	if snapshot.Revision < w.data.Revision {
		return
	}
	w.data = snapshot
	state, panes := w.workspaceUI(), w.panes()
	if _, ok := w.pane(state.MainID); !ok {
		state.MainID = ""
	}
	if _, ok := w.pane(state.FocusID); !ok {
		state.FocusID = state.MainID
		for _, workspace := range snapshot.Workspaces {
			if workspace.Path == w.active && state.FocusID == "" {
				if _, ok := w.pane(workspace.LastFocus); ok {
					state.FocusID = workspace.LastFocus
				}
			}
		}
		if state.FocusID == "" && len(panes) > 0 {
			state.FocusID = panes[0].ID
		}
	}
	if _, ok := w.pane(state.DockID); !ok || state.DockID == state.FocusID {
		state.DockID = ""
	}
	w.reconcileTerminals()
}

// Commands capture their arguments on the UI thread and execute serially.
// A refresh follows every command, including partially successful operations.
func (w *Workbench) run(command func() (func(), error)) {
	if !w.ready || w.busy {
		return
	}
	w.busy, w.failure, w.notice = true, "", ""
	w.background(func() {
		complete, err := command()
		snapshot, refreshErr := w.service.GetAllSnapshot()
		w.post(func() {
			w.busy = false
			if complete != nil {
				complete()
			}
			if refreshErr == nil {
				w.acceptSnapshot(snapshot)
			}
			if err != nil {
				w.failure = err.Error()
			} else if refreshErr != nil {
				w.failure = refreshErr.Error()
			}
			w.save()
		})
	})
}

func (w *Workbench) setMode(mode string) {
	w.workspaceUI().Mode = mode
	w.preview = nil
	w.save()
}

func (w *Workbench) focusPane(id string) {
	index := slices.IndexFunc(w.data.Panes, func(pane model.Pane) bool { return pane.ID == id })
	if index < 0 || w.busy {
		return
	}
	workspace, changed := w.data.Panes[index].Workspace, w.data.Panes[index].Workspace != w.active
	w.run(func() (func(), error) {
		if changed {
			if _, err := w.service.OpenWorkspace(workspace); err != nil {
				return nil, err
			}
		}
		err := w.service.FocusPane(id)
		if err != nil {
			if changed {
				return func() { w.changeWorkspace(workspace) }, err
			}
			return nil, err
		}
		return func() {
			if changed {
				w.changeWorkspace(workspace)
			}
			state := w.workspaceUI()
			if state.DockID == id {
				state.DockID = state.FocusID
			}
			state.FocusID, state.SelectedID = id, id
			state.Mode, w.preview = "focus", nil
		}, nil
	})
}

func (w *Workbench) setDock(id string) {
	state := w.workspaceUI()
	if id == state.FocusID {
		return
	}
	state.DockID = id
	w.save()
}

func (w *Workbench) setMain(id string) {
	w.workspaceUI().MainID = id
	w.save()
}

func (w *Workbench) switchWorkspace(path string) {
	if path == w.active {
		return
	}
	w.run(func() (func(), error) {
		_, err := w.service.OpenWorkspace(path)
		if err != nil {
			return nil, err
		}
		return func() { w.changeWorkspace(path) }, nil
	})
}

func (w *Workbench) changeWorkspace(path string) {
	for id, slot := range w.terminals {
		w.removeTerminalSlot(id, slot)
	}
	w.pendingTerminalFocus = ""
	w.active, w.preview = path, nil
	w.board = boardState{}
	w.closeForms()
}

func (w *Workbench) createWorkspace(path string) {
	path = filepath.Clean(strings.TrimSpace(path))
	w.run(func() (func(), error) {
		_, err := w.service.CreateWorkspace(path)
		if err != nil {
			return nil, err
		}
		return func() { w.changeWorkspace(path) }, nil
	})
}

func (w *Workbench) spawnPane(args SpawnPaneRequest, main bool) {
	args.Workspace = w.active
	w.run(func() (func(), error) {
		pane, err := w.service.SpawnPane(args)
		if err != nil {
			return nil, err
		}
		return func() {
			state := w.workspaceUI()
			state.FocusID, state.SelectedID = pane.ID, pane.ID
			if w.board.PlaceNext != nil {
				state.Layouts[pane.ID] = *w.board.PlaceNext
				w.board.PlaceNext = nil
			}
			if main || state.MainID == "" {
				state.MainID = pane.ID
			}
			w.closeForms()
			w.notice = "Agent 已创建"
		}, nil
	})
}

func (w *Workbench) updatePane(args UpdatePaneRequest, main bool) {
	args.Workspace = w.active
	w.run(func() (func(), error) {
		_, err := w.service.UpdatePane(args)
		if err != nil {
			return nil, err
		}
		return func() {
			state := w.workspaceUI()
			if main {
				state.MainID = args.ID
			} else if state.MainID == args.ID {
				state.MainID = ""
			}
			w.closeForms()
			w.notice = "Agent 已更新"
		}, nil
	})
}

func (w *Workbench) removePane(id string) {
	w.run(func() (func(), error) {
		err := w.service.RemovePane(id)
		if err != nil {
			return nil, err
		}
		return func() {
			delete(w.workspaceUI().Layouts, id)
			w.closeForms()
			w.notice = "Agent 已删除"
		}, nil
	})
}

func (w *Workbench) setEdge(from, to string, enabled bool) {
	w.run(func() (func(), error) {
		var err error
		if enabled {
			_, err = w.service.AddEdge(from, to)
		} else {
			_, err = w.service.RemoveEdge(from, to)
		}
		return nil, err
	})
}

func (w *Workbench) saveTemplate(name, description string) {
	name = strings.TrimSpace(name)
	if name == "" || len(w.panes()) == 0 {
		w.failure = "请输入模板名称，并至少创建一个 Agent"
		return
	}
	state := w.workspaceUI()
	template := TopologyTemplate{
		ID: fmt.Sprintf("template-%d", time.Now().UnixNano()), Name: name, Description: description,
	}
	keys := make(map[string]string)
	for index, pane := range w.panes() {
		key := fmt.Sprintf("agent-%d", index)
		keys[pane.ID] = key
		preset := pane.Preset
		if preset == "" && backendLabel(pane) == "Shell" {
			preset = "shell"
		}
		template.Agents = append(template.Agents, TemplateAgent{
			Key: key, Name: pane.Name, Preset: preset, Role: pane.Role,
			Account: pane.Account, Cwd: pane.Cwd, Command: slices.Clone(pane.Command),
			Layout: state.Layouts[pane.ID],
		})
	}
	for _, edge := range w.edges() {
		template.Links = append(template.Links, model.Edge{From: keys[edge.From], To: keys[edge.To]})
	}
	template.Main = keys[state.MainID]
	w.state.Templates = slices.DeleteFunc(w.state.Templates, func(t TopologyTemplate) bool { return t.Name == name })
	w.state.Templates = append([]TopologyTemplate{template}, w.state.Templates...)
	w.notice = "拓扑模板已保存"
	w.save()
}

func (w *Workbench) previewTemplate(id string) {
	for _, template := range w.state.Templates {
		if template.ID == id {
			copy := template
			copy.Agents = slices.Clone(template.Agents)
			copy.Links = slices.Clone(template.Links)
			w.preview = &copy
			w.workspaceUI().Mode = "board"
			return
		}
	}
}

func (w *Workbench) cancelPreview() {
	w.preview = nil
	w.save()
}

func (w *Workbench) deleteTemplate(id string) {
	w.state.Templates = slices.DeleteFunc(w.state.Templates, func(t TopologyTemplate) bool { return t.ID == id })
	if w.preview != nil && w.preview.ID == id {
		w.preview = nil
	}
	w.save()
}

func (w *Workbench) applyTemplate() {
	if w.preview == nil {
		return
	}
	template := *w.preview
	template.Agents, template.Links = slices.Clone(template.Agents), slices.Clone(template.Links)
	panes, workspace := w.panes(), w.active
	w.run(func() (func(), error) {
		keys := make(map[string]string)
		layouts := make(map[string]NodeLayout)
		var created []string
		catalog, err := w.service.GetCatalog()
		if err != nil {
			return nil, err
		}
		if err := validateTemplate(&template, catalog, workspace); err != nil {
			return nil, err
		}
		for _, pane := range panes {
			if err := w.service.RemovePane(pane.ID); err != nil {
				return nil, fmt.Errorf("替换模板时删除 %s：%w", pane.Name, err)
			}
		}
		applyErr := func() error {
			for _, agent := range template.Agents {
				pane, err := w.service.SpawnPane(SpawnPaneRequest{
					Name: agent.Name, Kind: model.PaneWorker, Preset: agent.Preset,
					Role: agent.Role, Account: agent.Account, Cwd: agent.Cwd,
					Workspace: workspace, Command: slices.Clone(agent.Command),
				})
				if err != nil {
					return err
				}
				created = append(created, pane.ID)
				keys[agent.Key], layouts[pane.ID] = pane.ID, agent.Layout
			}
			for _, edge := range template.Links {
				if keys[edge.From] == "" || keys[edge.To] == "" {
					return errors.New("模板中有不存在的 Agent 授权")
				}
				if _, err := w.service.AddEdge(keys[edge.From], keys[edge.To]); err != nil {
					return err
				}
			}
			return nil
		}()
		if applyErr != nil {
			for _, id := range created {
				if err := w.service.RemovePane(id); err != nil {
					applyErr = errors.Join(applyErr, fmt.Errorf("清理 %s：%w", id, err))
				}
			}
			return nil, fmt.Errorf("模板应用失败，原 Agent 不会自动恢复：%w", applyErr)
		}
		return func() {
			state := w.workspaceUI()
			state.Layouts, state.MainID = layouts, keys[template.Main]
			state.FocusID, state.DockID, state.Mode = state.MainID, "", "focus"
			if state.FocusID == "" && len(created) > 0 {
				state.FocusID = created[0]
			}
			w.preview = nil
			w.closeForms()
			w.notice = "模板已应用到当前工作区"
		}, nil
	})
}

func (w *Workbench) terminalView(c *ui.Context, pane model.Pane, autoFocus bool) {
	if pane.Backend != "pty" {
		ui.Column(c).Fill().Center().Gap(8).Children(func() {
			ui.Text(c, "此 Agent 使用结构化会话").Bold()
			ui.Text(c, "终端视图仅适用于 PTY；会话仍由后台管理。")
		})
		return
	}
	slot := w.terminals[pane.ID]
	if slot == nil {
		ctx, cancel := context.WithCancel(w.ctx)
		slot = &terminalSlot{cancel: cancel}
		w.terminals[pane.ID] = slot
		w.background(func() {
			term, err := w.service.OpenTerminal(ctx, pane)
			if err != nil {
				w.post(func() {
					if w.terminals[pane.ID] == slot && ctx.Err() == nil {
						slot.err = err
					}
				})
				return
			}
			defer w.closeTerminal(term)
			w.post(func() {
				if w.terminals[pane.ID] != slot || ctx.Err() != nil || !w.visibleTerminalIDs()[pane.ID] {
					cancel()
					return
				}
				slot.terminal = term
			})
			<-ctx.Done()
		})
	}
	if slot.err != nil {
		ui.Column(c).Fill().Center().Gap(12).Padding(20).Children(func() {
			ui.Text(c, "终端连接失败").Bold()
			ui.Text(c, slot.err.Error()).Wrap()
			ui.Button(c, "重新连接").OnClick(func() {
				w.removeTerminalSlot(pane.ID, slot)
			})
		})
		return
	}
	if slot.terminal == nil {
		ui.Column(c).Fill().Center().Children(func() { ui.Text(c, "正在连接终端…") })
		return
	}
	ui.Column(c.Key(pane.ID)).Fill().Children(func() {
		if err := slot.terminal.Err(); err != nil {
			ui.Row(c).Gap(8).Padding(6).Children(func() {
				ui.Text(c, "终端连接中断："+err.Error()).Grow(1).FontSize(11).Wrap()
				ui.Button(c, "重连终端").OnClick(func() {
					w.removeTerminalSlot(pane.ID, slot)
				})
			})
		}
		element := ghostty.View(c, slot.terminal.Terminal).Fill()
		if element.Focused() {
			w.lastTerminalID = pane.ID
		}
		if autoFocus {
			element.AutoFocus()
		}
		if w.pendingTerminalFocus == pane.ID && w.forms.Kind == "" && !w.forms.ConfirmOpen {
			element.Focus()
			w.pendingTerminalFocus = ""
		}
	})
}
