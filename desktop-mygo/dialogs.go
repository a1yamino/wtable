package main

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"wtable/internal/model"
)

type formState struct {
	Kind, EditID, Name, Preset, Role, Account, Cwd    string
	Main                                              bool
	Path                                              string
	Creating                                          bool
	TemplateName, TemplateDescription                 string
	LinkFrom, LinkTo                                  string
	Error                                             string
	Collapsed                                         map[string]bool
	ConfirmOpen                                       bool
	Confirm                                           confirmForm
	DrawerOpen, Advanced, SearchFocus, TemplateSaving bool
	Search                                            string
	TemplateExpanded                                  map[string]bool
}

type confirmForm struct {
	Title, Message, Label string
	Danger                bool
	Action                func()
}

func (w *Workbench) openAgent(id string) {
	f := &w.forms
	f.Kind, f.EditID, f.Error = "agent", id, ""
	f.DrawerOpen, f.Advanced = true, id != ""
	f.Name, f.Preset, f.Role, f.Account, f.Cwd = "", "", "", "", w.active
	f.Main = len(w.panes()) == 0
	if id != "" {
		if pane, ok := w.pane(id); ok {
			f.Name, f.Preset, f.Role, f.Account, f.Cwd = pane.Name, pane.Preset, pane.Role, pane.Account, pane.Cwd
			f.Main = w.workspaceUI().MainID == id
		}
	} else {
		for _, preset := range w.catalog.Presets {
			if preset.Available {
				f.Preset = preset.ID
				break
			}
		}
	}
}

func (w *Workbench) openWorkspace(creating bool) {
	w.forms.Kind, w.forms.Creating, w.forms.Error = "workspace", creating, ""
	w.forms.DrawerOpen = true
	w.forms.Path = w.active
	if creating {
		w.forms.Path = ""
	}
}

func (w *Workbench) openTemplates() {
	w.forms.Kind, w.forms.Error = "templates", ""
	w.forms.DrawerOpen = true
}

func (w *Workbench) closeForms() {
	w.forms.Kind, w.forms.EditID, w.forms.Error = "", "", ""
	w.forms.DrawerOpen = false
	w.board.PlaceNext = nil
}

func (w *Workbench) dismissDrawer() {
	if w.forms.Kind == "templates" && w.preview != nil {
		w.cancelPreview()
	}
	w.closeForms()
	w.restoreTerminalFocus()
}

func (w *Workbench) drawerView(c *ui.Context) {
	title := map[string]string{"agent": "配置新 Agent", "workspace": "工作区设置", "templates": "拓扑模板", "links": "授权关系"}[w.forms.Kind]
	if w.forms.Kind == "agent" && w.forms.EditID != "" {
		title = "编辑 Agent"
	}
	if w.forms.Kind == "workspace" && w.forms.Creating {
		title = "新建工作区"
	}
	content := func() {
		ui.Row(c).Height(56).Shrink(0).Padding(12, 20).Gap(10).AlignItems(ui.Center).Border(1, deskLine).BorderWidth(0, 0, 1, 0).Children(func() {
			ui.Text(c, title).FontSize(16).FontWeight(600).Grow(1)
			iconButton(c, "关闭配置", iconClose).Disabled(w.busy).OnClick(w.dismissDrawer)
		})
		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).Padding(20).Gap(16).Children(func() {
				switch w.forms.Kind {
				case "agent":
					w.agentForm(c.Key("agent"))
				case "workspace":
					w.workspaceForm(c.Key("workspace"))
				case "templates":
					w.templatesForm(c.Key("templates"))
				case "links":
					w.linksForm(c.Key("links"))
				}
			})
		})
		if w.forms.Error != "" {
			ui.Row(c).Margin(0, 20, 12).Padding(10).Gap(8).Radius(6).Background(c.Theme().Danger.Alpha(.08)).AlignItems(ui.Start).Children(func() {
				ui.Icon(c, iconAlert).Size(14, 14).Shrink(0).TextColor(c.Theme().Danger)
				ui.Text(c, w.forms.Error).Grow(1).MinWidth(0).FontSize(12).TextColor(c.Theme().Danger).Label("表单错误")
			})
		}
		w.drawerFooter(c.Key("footer"))
	}
	if w.forms.Kind == "templates" {
		// This is a nonmodal inspector: the preview's Board remains usable.
		ui.Overlay(c, func() {
			ui.Column(c.Key("template-inspector")).Absolute().Top(52).Right(12).Bottom(12).Width(420).Background(deskSurface).Border(1, deskLine).Radius(9).Shadow(0, 8, 32, 0, ui.RGBA(0, 0, 0, .32)).Label(title).Children(content)
		})
		return
	}
	panel := ui.DialogBase(c, &w.forms.DrawerOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, .42))
		panel.Absolute().Top(0).Right(0).Bottom(0).Width(420).Background(deskSurface).Border(1, deskLine).BorderWidth(0, 0, 0, 1).Label(title)
		content()
	})
	if panel.Dismissed() {
		if w.busy {
			w.forms.DrawerOpen = true
		} else {
			w.dismissDrawer()
		}
	}
}

func (w *Workbench) drawerFooter(c *ui.Context) {
	f := &w.forms
	if f.Kind == "links" || (f.Kind == "templates" && w.preview == nil) {
		return
	}
	ui.Row(c).Padding(14, 20).Gap(8).AlignItems(ui.Center).Border(1, deskLine).BorderWidth(1, 0, 0, 0).Children(func() {
		switch f.Kind {
		case "agent":
			if f.EditID != "" {
				iconButton(c, "删除 Agent…", iconTrash).TextColor(c.Theme().Danger).Disabled(w.busy).OnClick(func() { w.requestDelete(f.EditID) })
			}
			ui.Spacer(c)
			quietButton(c, "取消", nil).Disabled(w.busy).OnClick(w.dismissDrawer)
			label := "创建 Agent"
			if f.EditID != "" {
				label = "保存设置"
			}
			if w.busy {
				label = "正在保存…"
			}
			actionButton(c, label, iconCheck).Disabled(w.busy).OnClick(w.submitAgentForm)
		case "workspace":
			ui.Spacer(c)
			quietButton(c, "取消", nil).Disabled(w.busy).OnClick(w.dismissDrawer)
			label := "打开此工作区"
			if f.Creating {
				label = "创建工作区"
			}
			if w.busy {
				label = "正在打开…"
			}
			actionButton(c, label, iconFolder).Disabled(w.busy || strings.TrimSpace(f.Path) == "").OnClick(func() { w.createWorkspace(strings.TrimSpace(f.Path)) })
		case "templates":
			quietButton(c, "取消预览", nil).Disabled(w.busy).OnClick(w.cancelPreview)
			ui.Spacer(c)
			label := "应用模板…"
			if w.busy {
				label = "正在应用…"
			}
			actionButton(c, label, iconTemplate).Disabled(w.busy).OnClick(w.requestApplyTemplate)
		}
	})
}

func formField(c *ui.Context, label string, build func()) {
	ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(deskMuted)
		build()
	})
}

func (w *Workbench) agentForm(c *ui.Context) {
	f := &w.forms
	editing := f.EditID != ""
	pane, _ := w.pane(f.EditID)
	accountLocked := editing && (pane.State == "running" || pane.State == "starting" || pane.State == "stopping")
	formField(c, "名称 · 必填", func() {
		ui.TextInput(c, &f.Name).FillWidth().Height(34).Placeholder("例如 architect").Label("Agent 名称").AutoFocus().Disabled(w.busy)
	})
	formField(c, "Agent 底座 · 必选", func() {
		w.presetSelect(c, &f.Preset, editing || w.busy)
		if editing {
			ui.Text(c, "底座需要重新创建 Agent 才能修改。").FontSize(11).TextColor(deskMuted)
		}
	})
	formField(c, "角色说明", func() {
		ui.TextInput(c, &f.Role).FillWidth().Height(34).Placeholder("例如：负责架构与最终集成").Label("角色说明").Disabled(w.busy)
	})
	ui.Checkbox(c, &f.Main, "设为当前 Workspace 的主 Agent").Disabled(w.busy)
	ui.Divider(c)
	chevron := iconChevronRight
	if f.Advanced {
		chevron = iconChevronDown
	}
	quietButton(c, "高级设置", chevron).FillWidth().Justify(ui.Start).OnClick(func() { f.Advanced = !f.Advanced })
	if !f.Advanced {
		summary := "当前项目目录 · 程序默认登录"
		if f.Account != "" {
			summary = "当前项目目录 · " + f.Account
		}
		if f.Cwd != w.active {
			summary = strings.Replace(summary, "当前项目目录", "自定义工作目录", 1)
		}
		ui.Text(c, summary).FontSize(11).TextColor(deskFaint).SingleLine().Ellipsis("…").Tooltip(f.Cwd)
		return
	}
	formField(c, "账号配置", func() {
		ui.TextInput(c, &f.Account).FillWidth().Height(34).Placeholder("留空使用程序默认登录").Disabled(accountLocked || w.busy).Label("账号配置")
		if len(w.catalog.Accounts) > 0 && !accountLocked {
			ui.MenuButton(c, "已配置账号", func(m *ui.Menu) {
				if m.Item("程序默认登录").Checked(f.Account == "").Chosen() {
					f.Account = ""
				}
				m.Separator()
				for _, name := range w.catalog.Accounts {
					if m.Item(name).Checked(f.Account == name).Chosen() {
						f.Account = name
					}
				}
			})
		}
		if accountLocked {
			ui.Text(c, "运行中的 Agent 不能切换账号；停止后可以修改。").FontSize(11).TextColor(deskMuted)
		}
	})
	formField(c, "工作目录", func() {
		ui.TextInput(c, &f.Cwd).FillWidth().Height(34).Disabled(editing || w.busy).Label("工作目录")
		if editing {
			ui.Text(c, "目录需要重新创建 Agent 才能修改。").FontSize(11).TextColor(deskMuted)
		}
	})
}

func (w *Workbench) submitAgentForm() {
	f := &w.forms
	name, cwd, role := strings.TrimSpace(f.Name), strings.TrimSpace(f.Cwd), strings.TrimSpace(f.Role)
	if name == "" || cwd == "" {
		f.Error = "请填写 Agent 名称和工作目录。"
		return
	}
	if !utf8.ValidString(name) || len(name) > 80 || strings.ContainsFunc(name, unicode.IsControl) ||
		!utf8.ValidString(role) || len(role) > 256 || strings.ContainsFunc(role, unicode.IsControl) {
		f.Error = "名称最多 80 字节、角色最多 256 字节，均须为有效 UTF-8，且不得包含控制字符。"
		return
	}
	account := strings.TrimSpace(f.Account)
	f.Error = ""
	if f.EditID != "" {
		if pane, ok := w.pane(f.EditID); ok && (pane.State == "running" || pane.State == "starting" || pane.State == "stopping") {
			account = pane.Account
		}
		w.updatePane(UpdatePaneRequest{ID: f.EditID, Name: name, Role: role, Account: account, Workspace: w.active}, f.Main)
		return
	}
	var selected *model.Preset
	for i := range w.catalog.Presets {
		if w.catalog.Presets[i].ID == f.Preset {
			selected = &w.catalog.Presets[i]
			break
		}
	}
	if selected == nil || !selected.Available {
		f.Error = "请选择已安装且可用的 Agent 底座。"
		return
	}
	w.spawnPane(SpawnPaneRequest{
		Name: name, Kind: "worker", Preset: selected.ID, Role: role, Account: account,
		Cwd: cwd, Workspace: w.active, Command: append([]string(nil), selected.Command...), InjectBrief: false,
	}, f.Main)
}

// SelectBase lets unavailable installed-agent presets stay visible without
// presenting them as runnable choices.
func (w *Workbench) presetSelect(c *ui.Context, selected *string, disabled bool) ui.Element {
	sel := ui.SelectBase(c, selected)
	label := *selected
	for _, preset := range w.catalog.Presets {
		if preset.ID == *selected {
			label = preset.Name
			break
		}
	}
	if label == "" {
		label = "请选择 Agent 底座"
	}
	sel.Trigger.FillWidth().Height(34).Padding(7, 10).Background(deskBackground).Border(1, deskLine).Radius(5).Disabled(disabled).Label("选择 Agent 底座").Children(func() {
		ui.Text(c, label).Grow(1).MinWidth(0).SingleLine().Ellipsis("…").FontSize(12)
		ui.Icon(c, iconChevronDown).Size(13, 13).TextColor(deskMuted)
	})
	sel.Popup(func(panel ui.Element) {
		panel.Width(360).Padding(4).Background(deskSurface).Border(1, deskLine).Radius(6)
		for _, preset := range w.catalog.Presets {
			item := sel.Item(preset.ID).Padding(8, 9).Radius(5).Disabled(!preset.Available)
			if item.Highlighted() {
				item.Background(deskLine)
			}
			item.Children(func() {
				ui.Column(c).Gap(3).Children(func() {
					text := preset.Name
					if !preset.Available {
						text += " · 未安装 / 不可用"
					}
					ui.Text(c, text).FontSize(12)
					if !preset.Available && preset.Detail != "" {
						ui.Text(c, preset.Detail).FontSize(10).TextColor(deskMuted)
					}
				})
			})
		}
	})
	if len(w.catalog.Presets) == 0 {
		ui.Text(c, "没有可用的 Agent 底座；请安装后重新打开应用。").FontSize(11).TextColor(c.Theme().Warning)
	}
	return sel.Trigger
}

func (w *Workbench) workspaceForm(c *ui.Context) {
	f := &w.forms
	message := "工作区名称由项目目录决定。打开其他目录会切换当前工作区。"
	if f.Creating {
		message = "新工作区从空白开始。如果目录不存在，wtable 会创建它。"
	}
	ui.Text(c, message).FontSize(12).TextColor(deskMuted)
	formField(c, "项目目录", func() {
		ui.TextInput(c, &f.Path).FillWidth().Height(34).AutoFocus().Placeholder("/Users/you/Projects/workspace").Label("项目目录").Disabled(w.busy)
	})
}

func (w *Workbench) templatesForm(c *ui.Context) {
	f := &w.forms
	if w.preview != nil {
		w.templatePreviewForm(c.Key("preview"))
		ui.Divider(c)
	}
	if len(w.panes()) > 0 {
		quietButton(c, "保存当前拓扑为模板", iconPlus).FillWidth().Justify(ui.Start).Disabled(w.busy).OnClick(func() { f.TemplateSaving = !f.TemplateSaving })
		if f.TemplateSaving {
			ui.Column(c.Key("save-template")).Padding(12).Gap(10).Radius(6).Background(deskBackground).Children(func() {
				ui.TextInput(c, &f.TemplateName).FillWidth().Height(32).Placeholder("模板名称").Label("模板名称").AutoFocus()
				ui.TextInput(c, &f.TemplateDescription).FillWidth().Height(32).Placeholder("用途说明（可选）").Label("模板用途")
				quietButton(c, "保存为全局模板", iconCheck).FillWidth().Disabled(w.busy || strings.TrimSpace(f.TemplateName) == "").OnClick(func() {
					w.saveTemplate(strings.TrimSpace(f.TemplateName), strings.TrimSpace(f.TemplateDescription))
					f.TemplateName, f.TemplateDescription, f.TemplateSaving = "", "", false
				})
			})
		}
	}
	ui.Text(c, "模板库").FontSize(11).TextColor(deskFaint)
	if len(w.state.Templates) == 0 {
		ui.Text(c, "还没有保存的模板。").FontSize(12).TextColor(deskMuted)
		ui.Text(c, "模板会保留 Agent 配置、节点布局和授权方向。").FontSize(11).TextColor(deskFaint)
	}
	for _, template := range w.state.Templates {
		ui.Column(c.Key(template.ID)).Padding(12).Gap(8).Background(deskBackground).Border(1, deskLine).Radius(6).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconTemplate).Size(15, 15).TextColor(deskMuted)
				ui.Text(c, template.Name).FontSize(13).FontWeight(600).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
				iconButton(c, "删除模板", iconTrash).Disabled(w.busy).OnClick(func() {
					w.askConfirmation("删除模板「"+template.Name+"」？", "只删除保存的全局模板，当前工作区的 Agent 与授权关系不受影响。", "删除模板", true, func() { w.deleteTemplate(template.ID) })
				})
			})
			if template.Description != "" {
				ui.Text(c, template.Description).FontSize(11).TextColor(deskMuted)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Textf(c, "%d 个 Agent · %d 条授权", len(template.Agents), len(template.Links)).FontSize(11).TextColor(deskFaint).Grow(1)
				quietButton(c, "预览并配置", iconBoard).Disabled(w.busy).OnClick(func() { w.previewTemplate(template.ID) })
			})
		})
	}
}

func (w *Workbench) templatePreviewForm(c *ui.Context) {
	f := &w.forms
	if f.TemplateExpanded == nil {
		f.TemplateExpanded = make(map[string]bool)
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "预览："+w.preview.Name).FontSize(14).FontWeight(600).Grow(1).MinWidth(0)
		smallBadge(c, "尚未应用", deskAccent)
	})
	ui.Text(c, "背景画板可继续拖动和缩放。展开 Agent 可调整底座、账号和目录。").FontSize(12).TextColor(deskMuted)
	for i := range w.preview.Agents {
		agent := &w.preview.Agents[i]
		ui.Column(c.Key(agent.Key)).Padding(10).Gap(10).Background(deskBackground).Border(1, deskLine).Radius(6).Children(func() {
			chevron := iconChevronRight
			if f.TemplateExpanded[agent.Key] {
				chevron = iconChevronDown
			}
			quietButton(c, agent.Name, chevron).FillWidth().Justify(ui.Start).OnClick(func() { f.TemplateExpanded[agent.Key] = !f.TemplateExpanded[agent.Key] })
			if !f.TemplateExpanded[agent.Key] {
				return
			}
			w.presetSelect(c, &agent.Preset, w.busy).OnChange(func() {
				for _, preset := range w.catalog.Presets {
					if preset.ID == agent.Preset {
						agent.Command = append([]string(nil), preset.Command...)
						break
					}
				}
			})
			formField(c, "账号配置", func() {
				ui.TextInput(c, &agent.Account).FillWidth().Height(32).Placeholder("留空使用程序默认登录").Label(agent.Name + "账号").Disabled(w.busy)
			})
			formField(c, "工作目录", func() {
				ui.TextInput(c, &agent.Cwd).FillWidth().Height(32).Label(agent.Name + "工作目录").Disabled(w.busy)
			})
		})
	}
	if len(w.panes()) > 0 {
		ui.Textf(c, "应用后将替换当前 %d 个 Agent，无法自动恢复。", len(w.panes())).FontSize(12).TextColor(c.Theme().Warning)
	}
}

func (w *Workbench) requestApplyTemplate() {
	if w.preview == nil {
		return
	}
	for _, agent := range w.preview.Agents {
		available := false
		for _, preset := range w.catalog.Presets {
			available = available || (preset.ID == agent.Preset && preset.Available)
		}
		if !available {
			w.forms.Error = "「" + agent.Name + "」的底座不可用，请先选择已安装的底座。"
			return
		}
	}
	message := fmt.Sprintf("将创建 %d 个 Agent 和 %d 条定向授权。", len(w.preview.Agents), len(w.preview.Links))
	if count := len(w.panes()); count > 0 {
		message = fmt.Sprintf("当前工作区的 %d 个 Agent 会停止并被模板替换，原授权关系也会删除。这个操作无法自动恢复。", count)
	}
	w.askConfirmation("应用「"+w.preview.Name+"」？", message, "应用模板", len(w.panes()) > 0, w.applyTemplate)
}

func (w *Workbench) linksForm(c *ui.Context) {
	panes := w.panes()
	if len(panes) < 2 {
		ui.Text(c, "至少需要两个 Agent 才能设置授权关系。").TextColor(deskMuted)
		return
	}
	f := &w.forms
	ui.Text(c, "授权按方向生效。A → B 表示 A 可向 B 发送消息；双向通信需要两个方向都开启。").TextColor(deskMuted)
	formField(c, "发送方", func() { paneSelect(c.Key("from"), &f.LinkFrom, panes) })
	formField(c, "接收方", func() { paneSelect(c.Key("to"), &f.LinkTo, panes) })
	label := "添加单向授权"
	if w.busy {
		label = "正在同步…"
	}
	actionButton(c, label, iconLink).FillWidth().Disabled(w.busy || f.LinkFrom == "" || f.LinkTo == "" || f.LinkFrom == f.LinkTo).OnClick(func() { w.setEdge(f.LinkFrom, f.LinkTo, true) })
	ui.Divider(c)
	edges := w.edges()
	if len(edges) == 0 {
		ui.Text(c, "当前没有授权关系。").TextColor(deskMuted)
	}
	for _, edge := range edges {
		from, fromOK := w.pane(edge.From)
		to, toOK := w.pane(edge.To)
		if !fromOK || !toOK {
			continue
		}
		ui.Row(c.Key(edge.From + "→" + edge.To)).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, from.Name+" → "+to.Name).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			iconButton(c, "移除授权", iconClose).Disabled(w.busy).OnClick(func() { w.setEdge(edge.From, edge.To, false) })
		})
	}
}

func paneSelect(c *ui.Context, selected *string, panes []model.Pane) {
	label := "请选择 Agent"
	for _, pane := range panes {
		if pane.ID == *selected {
			label = pane.Name
		}
	}
	sel := ui.SelectBase(c, selected)
	sel.Trigger.FillWidth().Height(34).Padding(7, 10).Background(deskBackground).Border(1, deskLine).Radius(5).Children(func() {
		ui.Text(c, label).FontSize(12).Grow(1)
		ui.Icon(c, iconChevronDown).Size(13, 13).TextColor(deskMuted)
	})
	sel.Popup(func(panel ui.Element) {
		panel.Width(300).Padding(4).Background(deskSurface).Border(1, deskLine).Radius(7)
		for _, pane := range panes {
			item := sel.Item(pane.ID).Padding(8, 9).Radius(5)
			if item.Highlighted() {
				item.Background(deskLine)
			}
			item.Children(func() { ui.Text(c, pane.Name) })
		}
	})
}

func (w *Workbench) askConfirmation(title, message, label string, danger bool, action func()) {
	w.forms.Confirm = confirmForm{Title: title, Message: message, Label: label, Danger: danger, Action: action}
	w.forms.ConfirmOpen = true
}

func (w *Workbench) requestDelete(id string) {
	pane, ok := w.pane(id)
	if !ok {
		return
	}
	w.askConfirmation("删除「"+pane.Name+"」？", "该 Agent 会停止运行，并从工作区移除。与它相关的授权关系也会一并删除。", "删除 Agent", true, func() { w.removePane(id) })
}

func (w *Workbench) confirmView(c *ui.Context) {
	f := &w.forms
	if !f.ConfirmOpen {
		return
	}
	confirm := f.Confirm
	ui.Modal(c, &f.ConfirmOpen, func() {
		ui.Column(c).Width(400).Gap(17).Children(func() {
			ui.Text(c, confirm.Title).FontSize(19).Bold()
			ui.Text(c, confirm.Message).TextColor(deskMuted)
			ui.Row(c).Gap(9).Justify(ui.End).Children(func() {
				ui.Button(c, "取消").OnClick(func() { f.ConfirmOpen = false })
				button := ui.PrimaryButton(c, confirm.Label).Disabled(w.busy)
				if confirm.Danger {
					button.Background(c.Theme().Danger).TextColor(ui.Hex("#171a1e"))
				}
				button.OnClick(func() {
					f.ConfirmOpen = false
					if confirm.Action != nil {
						confirm.Action()
					}
				})
			})
		})
	})
}
