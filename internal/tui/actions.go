package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v3"
	"github.com/rivo/uniseg"
	"wtable/internal/model"
)

// An actionMenu answers one question: "I picked this, what can I do with it?"
// Everything is a list entry, so nothing has to be memorised. Keys are shown as
// hints only; the same item is reachable with arrows and Enter.
type actionMenu struct {
	title  string
	object string // kind:id, used to close when the object disappears
	items  []actionItem
	cursor int
}

type actionItem struct {
	label  string
	hint   string
	danger bool
	run    func()
}

// openActions builds the menu for the current selection of a view.
func (u *ui) openActions() {
	switch {
	case u.graph != nil:
		u.openGraphActions()
	case u.taskView:
		tasks := u.orderedTasks()
		if len(tasks) == 0 {
			u.status = "还没有任务"
			return
		}
		u.openTaskActions(tasks[u.pickedTask(tasks)].ID)
	case u.panel != nil:
		switch u.panel.tab {
		case 0:
			if p := u.panelPane(); p != nil {
				u.openPaneActions(p)
			}
		case 1:
			edges := u.panelEdges()
			if len(edges) > 0 {
				u.openEdgeActions(edges[min(u.panel.selected, len(edges)-1)])
			}
		case 2:
			tasks := u.orderedTasks()
			if len(tasks) > 0 {
				u.openTaskActions(tasks[min(u.panel.selected, len(tasks)-1)].ID)
			}
		case 3:
			if u.panel.selected == 0 {
				u.openLayerActions(model.Layer{Name: "工作区", Path: u.cwd})
			} else if l := u.panelLayer(); l != nil {
				u.openLayerActions(*l)
			}
		}
	}
}

func (u *ui) panelPane() *model.Pane {
	if len(u.snapshot.Panes) == 0 {
		return nil
	}
	index := min(max(0, u.panel.selected), len(u.snapshot.Panes)-1)
	return &u.snapshot.Panes[index]
}

func (u *ui) panelLayer() *model.Layer {
	if len(u.snapshot.Layers) == 0 {
		return nil
	}
	index := min(max(0, u.panel.selected-1), len(u.snapshot.Layers)-1)
	return &u.snapshot.Layers[index]
}

func (u *ui) showActions(title, object string, items []actionItem) {
	if len(items) == 0 {
		u.status = "这里没有可用的操作"
		return
	}
	u.actions = &actionMenu{title: title, object: object, items: items}
}

func (u *ui) openPaneActions(p *model.Pane) {
	pane := *p
	parked := false
	if _, ok := u.boxes[pane.ID]; !ok {
		parked = true
	}
	park := "驻留（移到后台）"
	if parked {
		park = "取消驻留（放回主区域）"
	}
	items := []actionItem{
		{label: "打开", hint: "看它现在在做什么", run: func() {
			u.panel = nil
			u.openPane(pane.ID, true)
		}},
		{label: "编辑", hint: "名字 / 角色 / 账号", run: func() {
			u.panel.tab = 0
			for i, other := range u.snapshot.Panes {
				if other.ID == pane.ID {
					u.panel.selected = i
				}
			}
			u.startAgentForm(true)
		}},
		{label: "回看输出", hint: "滚动看历史", run: func() { u.peekPane(pane) }},
		{label: park, run: func() { u.togglePark(pane.ID) }},
		{label: "派活给 " + pane.Name, hint: "开一张任务单", run: func() {
			u.startSendForm()
			u.preselectTarget(pane.ID)
		}},
		{label: "从它建一条边", hint: "授权它向另一个 pane 派活", run: func() {
			u.startEdgeForm()
			if f := u.panel.get("from"); f != nil {
				for i, o := range f.options {
					if o.value == pane.ID {
						f.choice = i
					}
				}
			}
		}},
		{label: "重发身份说明", hint: "brief 再投递一次", run: func() {
			var task model.Task
			if u.call("pane.brief.send", map[string]string{"id": pane.ID}, &task) {
				u.status = "身份说明已排队：" + shortTask(task.ID)
				_, _ = u.refresh()
			}
		}},
		{label: "重启", hint: "进程已退出时可用", run: func() {
			for i, other := range u.snapshot.Panes {
				if other.ID == pane.ID {
					u.focus = i
				}
			}
			u.restartCurrent()
		}},
		{label: "复制 pane ID", run: func() { u.copyText(pane.ID, "已复制 "+pane.ID) }},
		{label: "移除", hint: "停止进程，任务与报告历史保留", danger: true, run: func() {
			u.openPanel(0)
			for i, other := range u.snapshot.Panes {
				if other.ID == pane.ID {
					u.panel.selected = i
				}
			}
			u.panel.confirmKind = "pane"
			u.panel.confirmID = pane.ID
		}},
	}
	u.showActions("对 "+pane.Name+" 做什么？", "pane:"+pane.ID, items)
}

func (u *ui) openEdgeActions(e model.Edge) {
	edge := e
	items := []actionItem{
		{label: "查看这条边的任务", hint: "只看这个方向", run: func() {
			u.panel.tab = 2
			u.panel.related = true
			for i, other := range u.snapshot.Panes {
				if other.ID == edge.From {
					u.focus = i
				}
			}
		}},
		{label: "打开来源 pane", run: func() {
			u.panel = nil
			u.openPane(edge.From, true)
		}},
		{label: "打开目标 pane", run: func() {
			u.panel = nil
			u.openPane(edge.To, true)
		}},
		{label: "复制", hint: "来源 → 目标", run: func() {
			u.copyText(u.paneName(edge.From)+" → "+u.paneName(edge.To), "已复制这条边")
		}},
		{label: "删除这条边", hint: "该方向排队任务会取消", danger: true, run: func() {
			u.openPanel(1)
			b, _ := json.Marshal(edge)
			u.panel.confirmKind = "edge"
			u.panel.confirmID = string(b)
		}},
	}
	u.showActions("边 "+u.paneName(edge.From)+" → "+u.paneName(edge.To)+" 能做什么？", "edge:"+edge.From+">"+edge.To, items)
}

func (u *ui) openTaskActions(id string) {
	task, ok := u.taskByID(id)
	if !ok {
		return
	}
	items := []actionItem{
		{label: "查看详情", hint: task.State + " · " + shortTask(task.ID), run: func() {
			u.openTasks()
			u.readTask(task.ID)
		}},
		{label: "打开目标 pane", hint: u.paneName(task.To), run: func() {
			u.taskView = false
			u.panel = nil
			u.openPane(task.To, true)
		}},
		{label: "打开来源", hint: u.paneName(task.From), run: func() {
			u.taskView = false
			u.panel = nil
			u.openPane(task.From, true)
		}},
		{label: "重发", hint: "只对失败 / 未知 / 写入未确认", run: func() {
			var out model.Task
			if u.call("task.retry", map[string]string{"id": task.ID}, &out) {
				u.status = "已重发：" + shortTask(out.ID)
				_, _ = u.refresh()
			}
		}},
		{label: "复制任务 ID", run: func() { u.copyText(task.ID, "已复制 "+task.ID) }},
		{label: "取消", hint: "先请求停止，再确认", danger: true, run: func() {
			var out model.Task
			if !u.call("task.cancel", map[string]string{"id": task.ID}, &out) {
				return
			}
			if out.State == "cancelling" {
				u.status = "已请求取消；在任务列表里再选一次「取消」并确认"
				return
			}
			u.status = "任务已取消"
		}},
	}
	u.showActions("任务 "+shortTask(task.ID)+" 能做什么？", "task:"+task.ID, items)
}

func (u *ui) openLayerActions(l model.Layer) {
	layer := l
	items := []actionItem{
		{label: "设为新 Agent 的默认目录", run: func() {
			u.activeLayer = layer.Path
			u.activeCwd = layer.Path
			u.status = "新 Agent 将创建在 " + layer.Name
		}},
		{label: "打开目录", hint: layer.Path, run: func() { u.status = "目录：" + layer.Path }},
		{label: "复制路径", run: func() { u.copyText(layer.Path, "已复制路径") }},
	}
	u.showActions("Layer "+layer.Name+" 能做什么？", "layer:"+layer.Path, items)
}

func (u *ui) openGraphActions() {
	v := u.graph
	if v.edges {
		edges := u.graphEdges()
		for _, e := range edges {
			if e.From == v.edge.From && e.To == v.edge.To {
				u.openGraphEdgeActions(e)
				return
			}
		}
		v.edges = false // stale edge selection: fall back to the node
	}
	for _, p := range u.snapshot.Panes {
		if p.ID == v.node {
			u.openPaneActions(&p)
			return
		}
	}
	if v.node == "you" {
		u.showActions("你（人类控制端）能做什么？", "you", []actionItem{
			{label: "新建 Agent", run: func() { u.graph = nil; u.openPanel(0); u.startPresetPicker() }},
			{label: "建一条边", hint: "两个 agent 之间授权", run: func() { u.graph = nil; u.openPanel(1); u.startEdgeForm() }},
		})
	}
}

func (u *ui) openGraphEdgeActions(e model.GraphEdge) {
	edge := model.Edge{From: e.From, To: e.To}
	items := []actionItem{
		{label: "查看这个方向的任务", run: func() { u.graph = nil; u.openPanel(2) }},
		{label: "打开来源 pane", run: func() { u.graph = nil; u.openPane(edge.From, true) }},
		{label: "打开目标 pane", run: func() { u.graph = nil; u.openPane(edge.To, true) }},
	}
	if e.Kind != "virtual" {
		items = append(items, actionItem{label: "删除这条授权边", danger: true, run: func() {
			u.graph = nil
			u.openPanel(1)
			b, _ := json.Marshal(edge)
			u.panel.confirmKind = "edge"
			u.panel.confirmID = string(b)
		}})
	}
	u.showActions("边 "+u.paneName(edge.From)+" → "+u.paneName(edge.To)+" 能做什么？", "graph-edge:"+edge.From+">"+edge.To, items)
}

func (u *ui) taskByID(id string) (model.Task, bool) {
	for _, t := range u.snapshot.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return model.Task{}, false
}

// preselectTarget ticks a pane in an already-open send form.
func (u *ui) preselectTarget(id string) {
	if f := u.panel.get("targets"); f != nil {
		f.checked[id] = true
	}
}

func (u *ui) actionKey(e *tcell.EventKey) {
	m := u.actions
	switch e.Key() {
	case tcell.KeyEscape:
		u.actions = nil
		return
	case tcell.KeyUp:
		m.cursor = (m.cursor + len(m.items) - 1) % len(m.items)
		return
	case tcell.KeyDown:
		m.cursor = (m.cursor + 1) % len(m.items)
		return
	case tcell.KeyHome:
		m.cursor = 0
		return
	case tcell.KeyEnd:
		m.cursor = len(m.items) - 1
		return
	case tcell.KeyEnter:
		u.runAction(m.cursor)
		return
	}
	if e.Key() == tcell.KeyRune {
		if n := int(e.Str()[0] - '0'); n >= 1 && n <= 9 && n <= len(m.items) {
			u.runAction(n - 1)
			return
		}
		if e.Str() == "j" {
			m.cursor = (m.cursor + 1) % len(m.items)
			return
		}
		if e.Str() == "k" {
			m.cursor = (m.cursor + len(m.items) - 1) % len(m.items)
			return
		}
	}
}

func (u *ui) runAction(index int) {
	m := u.actions
	if m == nil || index < 0 || index >= len(m.items) {
		return
	}
	item := m.items[index]
	u.actions = nil
	if item.run != nil {
		item.run()
	}
}

func (u *ui) actionMouse(e *tcell.EventMouse, pressed bool) {
	m := u.actions
	w, h := u.screen.Size()
	width := min(78, max(30, w-6))
	x0 := (w - width) / 2
	height := min(len(m.items)+6, max(4, h-2))
	y0 := (h - height) / 2
	_, y := e.Position()
	switch {
	case e.Buttons()&tcell.WheelUp != 0:
		m.cursor = (m.cursor + len(m.items) - 1) % len(m.items)
		return
	case e.Buttons()&tcell.WheelDown != 0:
		m.cursor = (m.cursor + 1) % len(m.items)
		return
	}
	if !pressed {
		return
	}
	x, _ := e.Position()
	if x < x0 || x >= x0+width {
		u.actions = nil
		return
	}
	first := y0 + 3
	if y < first {
		return
	}
	index := y - first
	if index >= 0 && index < len(m.items) {
		u.runAction(index)
	}
}

func (u *ui) drawActions() {
	m := u.actions
	w, h := u.screen.Size()
	width := min(78, max(30, w-6))
	height := min(len(m.items)+6, max(4, h-2))
	x0 := (w - width) / 2
	y0 := (h - height) / 2
	base := tcell.StyleDefault.Background(tcell.NewHexColor(0x22303f)).Foreground(tcell.ColorWhite)
	dim := base.Foreground(tcell.NewHexColor(0x8fa3b8))
	_ = dim
	for row := 0; row < height; row++ {
		u.line(x0, y0+row, width, "", base)
	}
	u.line(x0, y0, width, " "+truncateColumns(m.title, width-2), base.Bold(true))
	u.line(x0, y0+1, width, strings.Repeat("─", width), dim)
	// Keep the cursor inside the visible window.
	visible := height - 5
	top := max(0, m.cursor-visible+1)
	top = min(top, max(0, len(m.items)-visible))
	for i := top; i < min(len(m.items), top+visible); i++ {
		item := m.items[i]
		marker := "   "
		style := base
		if i == m.cursor {
			marker = " › "
			style = tcell.StyleDefault.Background(tcell.NewHexColor(0x2f4a63)).Foreground(tcell.ColorWhite)
		}
		if item.danger {
			style = style.Foreground(tcell.NewHexColor(0xff9a8a))
		}
		line := fmt.Sprintf("%s%d. %s", marker, i+1, item.label)
		if item.hint != "" {
			line += "  · " + item.hint
		}
		u.line(x0+1, y0+3+(i-top), width-2, line, style)
	}
	u.line(x0, y0+height-2, width, strings.Repeat("─", width), dim)
	hint := " ↑↓ 选择 · Enter 执行 · 1..9 直达 · Esc 返回 "
	if pad := width - uniseg.StringWidth(hint); pad > 0 {
		hint = strings.Repeat(" ", pad) + hint
	}
	u.line(x0, y0+height-1, width, hint, dim)
}
