package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"wtable/internal/model"
)

func (w *Workbench) view(c *ui.Context) {
	defer w.releaseRetiredTerminals(c)
	defer w.reconcileTerminals()
	c.SetTheme(desktopTheme())
	if !w.ready {
		w.launchView(c)
		return
	}
	for _, mods := range []ui.Modifiers{ui.Cmd, ui.Ctrl} {
		c.OnShortcut(mods, ui.Key1, func() { w.setMode("focus") })
		c.OnShortcut(mods, ui.Key2, func() { w.setMode("board") })
		c.OnShortcut(mods, ui.KeyT, w.openTemplates)
		c.OnShortcut(mods, ui.KeyK, func() { w.forms.SearchFocus = true })
	}
	if w.forms.Kind == "templates" && !w.forms.ConfirmOpen {
		c.OnShortcut(0, ui.KeyEscape, w.dismissDrawer)
	}
	ui.Row(c).Fill().Background(deskBackground).AlignItems(ui.Stretch).Children(func() {
		w.railView(c.Key("rail"))
		ui.Column(c.Key("workbench")).Grow(1).MinWidth(0).Children(func() {
			w.workbenchHeader(c.Key("header"))
			ui.Column(c.Key("content")).Grow(1).MinHeight(0).Clip().Children(func() {
				if w.workspaceUI().Mode == "board" || w.preview != nil {
					w.boardView(c.Key("board"))
				} else {
					w.focusView(c.Key("focus"))
				}
			})
		})
	})
	if w.forms.Kind != "" {
		w.drawerView(c.Key("drawer"))
	}
	w.confirmView(c.Key("confirmation"))
	w.statusView(c.Key("notifications"))
	// A drawer may dismiss after the terminal was built in this frame. Request
	// one more frame only once its target is ready to consume the focus request.
	if w.forms.Kind == "" && !w.forms.ConfirmOpen && w.pendingTerminalFocus != "" {
		if slot := w.terminals[w.pendingTerminalFocus]; slot != nil && slot.terminal != nil {
			c.After(0)
		}
	}
}

func (w *Workbench) launchView(c *ui.Context) {
	ui.Column(c).Fill().Background(deskBackground).AlignItems(ui.Center).Justify(ui.Center).Gap(14).Children(func() {
		ui.Box(c).Size(48, 48).Radius(12).Background(deskSurface).Center().Children(func() {
			ui.Icon(c, iconTerminal).Size(24, 24).TextColor(deskAccent)
		})
		ui.Text(c, "wtable").FontSize(23).Bold()
		if w.failure != "" {
			ui.Text(c, "无法打开本地工作台").FontSize(15)
			ui.Text(c, w.failure).Width(440).TextColor(c.Theme().Danger)
			actionButton(c, "重试连接", iconTerminal).Disabled(w.busy).OnClick(w.retryStartup)
		} else {
			step := w.launchStep
			if step == "" {
				step = "正在连接本地工作台…"
			}
			ui.Text(c, step).FontSize(13).TextColor(deskMuted)
		}
	})
}

func (w *Workbench) statusView(c *ui.Context) {
	if w.notice != "" {
		c.AddToast(ui.Toast{ID: "workbench-success", Title: w.notice, Type: "success", Timeout: 3 * time.Second})
		w.notice = ""
	}
	if w.failure != "" {
		if w.forms.Kind != "" {
			w.forms.Error = w.failure
		}
		c.AddToast(ui.Toast{ID: "workbench-error", Title: w.failure, Type: "error", Timeout: -1})
		w.failure = ""
	}
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.Padding(16).AlignItems(ui.End).Gap(8)
		if w.forms.Kind != "" {
			viewport.Padding(16, 16, 16, 248).AlignItems(ui.Start)
		}
		for _, toast := range toasts {
			parts := ui.ToastBase(c, toast)
			parts.Root.Width(360).Padding(11, 12).Radius(8).Background(deskRaised).Border(1, deskLine).Children(func() {
				ui.Row(c).Gap(9).AlignItems(ui.Center).Children(func() {
					icon, color := iconCheck, deskAccent
					if toast.Type == "error" {
						icon, color = iconAlert, c.Theme().Danger
					}
					ui.Icon(c, icon).Size(16, 16).Shrink(0).TextColor(color)
					ui.Text(c, toast.Title).FontSize(12).Grow(1).MinWidth(0)
					parts.CloseButton().Size(22, 22).Padding(4).Radius(4).Label("关闭通知").Tooltip("关闭通知").Children(func() {
						ui.Icon(c, iconClose).Size(13, 13).TextColor(deskMuted)
					})
				})
			})
		}
	})
}

func (w *Workbench) workbenchHeader(c *ui.Context) {
	bar := c.TitleBar()
	ui.Row(c).Height(max(44, bar.Height)).Shrink(0).Padding(6, bar.Right+12, 6, 16).Gap(12).AlignItems(ui.Center).Background(deskSurface).Border(1, deskLine).BorderWidth(0, 0, 1, 0).DragWindow().Children(func() {
		ui.Row(c).Grow(1).MinWidth(0).Gap(7).AlignItems(ui.Center).Tooltip(w.active).Children(func() {
			ui.Icon(c, iconFolder).Size(14, 14).TextColor(deskFaint)
			ui.Text(c, filepath.Base(filepath.Dir(w.active))).FontSize(12).TextColor(deskFaint).SingleLine().Ellipsis("…").MaxWidth(140)
			ui.Text(c, "/").FontSize(12).TextColor(deskFaint)
			ui.Text(c, filepath.Base(w.active)).FontSize(13).TextColor(deskText).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
		})
		ui.Row(c).Gap(2).Padding(2).Radius(6).Background(deskRail).Children(func() {
			for _, item := range []struct {
				mode, label string
				icon        *ui.SVG
			}{{"focus", "Focus", iconFocus}, {"board", "Board", iconBoard}} {
				button := quietButton(c.Key(item.mode), item.label, item.icon).Height(26).Padding(4, 10)
				if w.workspaceUI().Mode == item.mode {
					button.Background(deskRaised).TextColor(deskText)
				} else {
					button.TextColor(deskMuted)
				}
				button.OnClick(func() { w.setMode(item.mode) })
			}
		})
		actionButton(c, "新增 Agent", iconPlus).Disabled(w.busy).OnClick(func() { w.openAgent("") })
		compactMenu(c, "工作区", iconMore, func(m *ui.Menu) {
			if m.Item("授权关系").Chosen() {
				w.forms.Kind, w.forms.Error, w.forms.DrawerOpen = "links", "", true
			}
			if m.Item("拓扑模板  ⌘T").Chosen() {
				w.openTemplates()
			}
			m.Separator()
			if m.Item("工作区设置").Chosen() {
				w.openWorkspace(false)
			}
		})
	})
}

func (w *Workbench) railView(c *ui.Context) {
	if w.forms.Collapsed == nil {
		w.forms.Collapsed = make(map[string]bool)
	}
	bar := c.TitleBar()
	ui.Column(c).Width(232).Shrink(0).Background(deskRail).Border(1, deskLine).BorderWidth(0, 1, 0, 0).Children(func() {
		ui.Row(c).Height(max(44, bar.Height)).Padding(6, 9, 6, bar.Left+10).Gap(8).AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Text(c, "wtable").FontSize(14).Bold().Grow(1).TextColor(deskText)
			iconButton(c, "新建工作区", iconFolderPlus).Disabled(w.busy).OnClick(func() { w.openWorkspace(true) })
		})
		ui.Row(c.Key("search")).Margin(4, 10, 10).Padding(0, 8).Height(30).Radius(6).Border(1, deskLine).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Icon(c, iconSearch).Size(13, 13).Shrink(0).TextColor(deskFaint)
			search := ui.TextInputBase(c, &w.forms.Search).Grow(1).MinWidth(0).FontSize(12).Placeholder("搜索 Agent").Label("搜索 Agent").Height(28)
			if w.forms.SearchFocus {
				search.Focus()
				w.forms.SearchFocus = false
			}
			if w.forms.Search != "" {
				iconButton(c, "清除搜索", iconClose).Size(18, 18).OnClick(func() { w.forms.Search = "" })
			} else {
				ui.Text(c, "⌘K").FontSize(10).TextColor(deskFaint)
			}
		})
		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).Padding(0, 8).Gap(9).Children(func() {
				workspaces := append([]model.WorkspaceInfo(nil), w.data.Workspaces...)
				known := make(map[string]bool)
				for _, ws := range workspaces {
					known[ws.Path] = true
				}
				for _, pane := range w.data.Panes {
					if pane.Workspace != "" && !known[pane.Workspace] {
						known[pane.Workspace] = true
						workspaces = append(workspaces, model.WorkspaceInfo{Path: pane.Workspace})
					}
				}
				if w.active != "" && !known[w.active] {
					workspaces = append(workspaces, model.WorkspaceInfo{Path: w.active})
				}
				matches := 0
				for _, workspace := range workspaces {
					matches += w.workspaceGroup(c.Key(workspace.Path), workspace)
				}
				if w.forms.Search != "" && matches == 0 {
					ui.Text(c, "没有匹配的 Agent").FontSize(12).TextColor(deskMuted).Padding(16, 8)
				}
			})
		})
		ui.Row(c).Padding(8, 10).Border(1, deskLine).BorderWidth(1, 0, 0, 0).Children(func() {
			quietButton(c, "工作区设置", iconSettings).Grow(1).OnClick(func() { w.openWorkspace(false) })
		})
	})
}

func (w *Workbench) workspaceGroup(c *ui.Context, workspace model.WorkspaceInfo) int {
	path := workspace.Path
	query := strings.ToLower(strings.TrimSpace(w.forms.Search))
	panes := make([]model.Pane, 0)
	for _, pane := range w.data.Panes {
		if pane.Workspace == path && (query == "" || strings.Contains(strings.ToLower(pane.Name+" "+backendLabel(pane)+" "+pane.Role), query)) {
			panes = append(panes, pane)
		}
	}
	if query != "" && len(panes) == 0 {
		return 0
	}
	sort.Slice(panes, func(i, j int) bool { return panes[i].Name < panes[j].Name })
	collapsed := w.forms.Collapsed[path] && query == ""
	ui.Column(c).Gap(2).Children(func() {
		ui.Row(c.Key("heading")).Height(28).Gap(2).AlignItems(ui.Center).Children(func() {
			chevron := iconChevronDown
			if collapsed {
				chevron = iconChevronRight
			}
			iconButton(c.Key("collapse"), "展开或折叠工作区", chevron).Size(20, 26).OnClick(func() { w.forms.Collapsed[path] = !w.forms.Collapsed[path] })
			button := ui.ButtonBase(c.Key("workspace")).Grow(1).MinWidth(0).Height(28).Padding(2, 4).Radius(4).Label(filepath.Base(path) + " 工作区").Tooltip(path).Disabled(w.busy)
			if button.Hovered() {
				button.Background(deskSurface)
			}
			button.Children(func() {
				ui.Row(c).Grow(1).MinWidth(0).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, iconFolder).Size(13, 13).Shrink(0).TextColor(deskFaint)
					color := deskMuted
					if w.active == path {
						color = deskText
					}
					ui.Text(c, filepath.Base(path)).FontSize(12).TextColor(color).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
					ui.Textf(c, "%d", len(panes)).FontSize(10).TextColor(deskFaint)
				})
			})
			button.OnClick(func() { w.switchWorkspace(path) })
		})
		if collapsed {
			return
		}
		if workspace.Missing {
			ui.Text(c, "项目目录不存在").FontSize(11).TextColor(c.Theme().Warning).Padding(4, 12)
		}
		if len(panes) == 0 {
			ui.Text(c, "还没有 Agent").TextColor(deskFaint).FontSize(11).Padding(8, 28)
		}
		for _, pane := range panes {
			selected := path == w.active && pane.ID == w.workspaceUI().FocusID
			row := ui.ButtonBase(c.Key(pane.ID)).FillWidth().Height(44).Padding(6, 10).Radius(5).Selected(selected).TextColor(deskText).Label(pane.Name).Tooltip(fmt.Sprintf("%s · %s · %s", pane.Name, backendLabel(pane), paneStatus(pane)))
			if selected {
				row.Background(deskSelected)
			} else if row.Hovered() {
				row.Background(deskSurface)
			}
			row.Children(func() {
				if selected {
					ui.Box(c).Absolute().Left(0).Top(7).Bottom(7).Width(2).Radius(1).Background(deskAccent)
				}
				ui.Row(c).Grow(1).MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
					agentGlyph(c, pane, 20)
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
							ui.Text(c, pane.Name).FontSize(13).TextColor(deskText).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
							if state := w.state.Workspaces[path]; state != nil && state.MainID == pane.ID {
								ui.Icon(c, iconPin).Size(10, 10).TextColor(deskAccent).Tooltip("主 Agent")
							}
						})
						secondary := backendLabel(pane)
						if pane.Role != "" {
							secondary += " · " + pane.Role
						}
						ui.Text(c, secondary).FontSize(11).TextColor(deskMuted).SingleLine().Ellipsis("…")
					})
					statusDot(c, pane)
				})
			})
			row.OnClick(func() { w.focusPane(pane.ID) })
		}
	})
	return len(panes)
}

func statusDot(c *ui.Context, pane model.Pane) {
	color := deskFaint
	switch paneStatus(pane) {
	case "异常":
		color = c.Theme().Danger
	case "等待处理":
		color = c.Theme().Warning
	case "工作中":
		color = deskAccent
	}
	ui.Box(c).Size(6, 6).Shrink(0).Radius(3).Background(color).Label(paneStatus(pane)).Tooltip(paneStatus(pane))
}

func (w *Workbench) focusView(c *ui.Context) {
	state := w.workspaceUI()
	pane, dock, hasPane, hasDock := w.focusPanes()
	if !hasPane {
		ui.Column(c).Fill().AlignItems(ui.Center).Justify(ui.Center).Gap(12).Children(func() {
			ui.Icon(c, iconTerminal).Size(34, 34).TextColor(deskFaint)
			ui.Text(c, "工作区已就绪").FontSize(20).Bold()
			ui.Text(c, "添加 Agent 开始工作，或使用已有拓扑模板。").FontSize(13).TextColor(deskMuted)
			ui.Row(c).Gap(8).Margin(6, 0).Children(func() {
				quietButton(c, "新增 Agent", iconPlus).OnClick(func() { w.openAgent("") })
				quietButton(c, "拓扑模板", iconTemplate).OnClick(w.openTemplates)
			})
			ui.Text(c, "⌘1 Focus   ·   ⌘2 Board   ·   ⌘K 搜索").FontSize(11).TextColor(deskFaint)
		})
		return
	}
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		ui.Column(c.Key("main-" + pane.ID)).Grow(1).MinWidth(0).Children(func() {
			w.focusTerminalHeader(c.Key("heading"), pane, false)
			ui.Column(c.Key("terminal")).Grow(1).MinHeight(0).Children(func() { w.terminalView(c.Key(pane.ID), pane, true) })
		})
		if hasDock {
			width, _ := c.Size()
			grip := ui.Box(c.Key("dock-grip")).Width(4).Shrink(0).Background(deskLine).Cursor(ui.CursorResizeEW).Focusable().Role(ui.RoleSplitter).Label("调整辅助终端宽度")
			if grip.Hovered() || grip.Pressed() {
				grip.Background(deskAccent)
			}
			if dx, _, dragged := grip.Dragged(); dragged && dx != 0 {
				state.DockWidth = max(280, min(min(720, width*0.48), state.DockWidth-dx))
				w.save()
			}
			grip.OnShortcut(0, ui.KeyLeft, func() { state.DockWidth = min(720, state.DockWidth+16); w.save() })
			grip.OnShortcut(0, ui.KeyRight, func() { state.DockWidth = max(280, state.DockWidth-16); w.save() })
			ui.Column(c.Key("dock-" + dock.ID)).Width(state.DockWidth).Shrink(0).MinWidth(0).Children(func() {
				w.focusTerminalHeader(c.Key("heading"), dock, true)
				ui.Column(c.Key("terminal")).Grow(1).MinHeight(0).Children(func() { w.terminalView(c.Key(dock.ID), dock, false) })
			})
		}
	})
}

func (w *Workbench) focusTerminalHeader(c *ui.Context, pane model.Pane, dock bool) {
	ui.Row(c).Height(38).Shrink(0).Padding(4, 10).Gap(7).AlignItems(ui.Center).Background(deskSurface).Border(1, deskLine).BorderWidth(0, 0, 1, 0).Children(func() {
		agentGlyph(c, pane, 20)
		ui.Text(c, pane.Name).FontSize(13).Bold().MaxWidth(180).MinWidth(0).SingleLine().Ellipsis("…").Tooltip(pane.Name)
		if pane.ID == w.workspaceUI().MainID {
			ui.Icon(c, iconPin).Size(11, 11).TextColor(deskAccent).Tooltip("主 Agent").Label("主 Agent")
		}
		ui.Text(c, filepath.Base(pane.Cwd)).FontSize(11).TextColor(deskFaint).Grow(1).MinWidth(0).SingleLine().Ellipsis("…").Tooltip(pane.Cwd)
		statusDot(c, pane)
		ui.Text(c, paneStatus(pane)).FontSize(11).TextColor(deskMuted).Shrink(0)
		if !dock {
			compactMenu(c, "辅助终端", iconSplit, func(m *ui.Menu) {
				for _, candidate := range w.panes() {
					if candidate.ID != pane.ID && m.Item(candidate.Name).Checked(w.workspaceUI().DockID == candidate.ID).Chosen() {
						w.setDock(candidate.ID)
					}
				}
				if len(w.panes()) < 2 {
					m.Item("请先新增另一个 Agent").Disabled(true)
				}
			})
		}
		compactMenu(c, "操作", iconMore, func(m *ui.Menu) {
			if m.Item("在 Board 中打开").Chosen() {
				w.workspaceUI().SelectedID = pane.ID
				w.setMode("board")
			}
			if m.Item("编辑 Agent").Chosen() {
				w.openAgent(pane.ID)
			}
			if m.Item("设为主 Agent").Checked(w.workspaceUI().MainID == pane.ID).Chosen() {
				w.setMain(pane.ID)
			}
			m.Separator()
			if m.Item("删除 Agent…").Disabled(w.busy).Chosen() {
				w.requestDelete(pane.ID)
			}
		})
		if dock {
			iconButton(c, "关闭辅助终端", iconClose).OnClick(func() { w.setDock("") })
		}
	})
}
