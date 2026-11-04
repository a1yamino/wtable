package main

import (
	"fmt"
	"math"

	"github.com/egoist/mygo/ui"
	"wtable/internal/model"
)

const (
	boardNodeWidth  float32 = 260
	boardNodeHeight float32 = 80
	boardGap        float32 = 20
)

type boardState struct {
	PlaceNext                *NodeLayout
	preview                  *TopologyTemplate
	previewViewport          BoardViewport
	previewSelected          string
	previewFitPending        bool
	connecting, edgeA, edgeB string
	width, height            float32
}

// A board's camera is purely visual. Node sizes remain in logical DIPs so
// zooming a terminal never negotiates another PTY grid with the daemon.
func (w *Workbench) boardCamera() *BoardViewport {
	if w.preview != nil {
		if w.board.preview != w.preview {
			w.board.preview = w.preview
			w.board.previewViewport = BoardViewport{X: 32, Y: 32, Scale: 1}
			w.board.previewSelected = ""
			w.board.previewFitPending = true
			w.board.connecting, w.board.edgeA, w.board.edgeB = "", "", ""
		}
		return &w.board.previewViewport
	}
	w.board.preview = nil
	w.board.previewFitPending = false
	return &w.workspaceUI().Viewport
}

// The template inspector overlays the canvas. Only camera framing and floating
// controls reserve its width; terminal and world layout keep the full canvas.
func (w *Workbench) boardVisibleWidth() float32 {
	if w.forms.Kind == "templates" {
		return max(w.board.width-444, 280)
	}
	return w.board.width
}

func (w *Workbench) boardControlInset() float32 {
	if w.forms.Kind == "templates" {
		return 444
	}
	return 16
}

func (w *Workbench) boardPanes() []model.Pane {
	if w.preview == nil {
		return w.panes()
	}
	panes := make([]model.Pane, 0, len(w.preview.Agents))
	for _, a := range w.preview.Agents {
		panes = append(panes, model.Pane{ID: a.Key, Name: a.Name, Preset: a.Preset, Role: a.Role, Account: a.Account, Cwd: a.Cwd, Command: a.Command, Backend: "pty"})
	}
	return panes
}

func (w *Workbench) boardEdges() []model.Edge {
	if w.preview != nil {
		return w.preview.Links
	}
	return w.edges()
}

func (w *Workbench) boardLayout(id string) NodeLayout {
	if w.preview != nil {
		for _, a := range w.preview.Agents {
			if a.Key == id {
				return boardNormalize(a.Layout)
			}
		}
	} else if l, ok := w.workspaceUI().Layouts[id]; ok {
		return boardNormalize(l)
	}
	return NodeLayout{Width: 560, Height: 360}
}

func boardNormalize(l NodeLayout) NodeLayout {
	if l.Width < 340 {
		l.Width = 560
	}
	if l.Height < 200 {
		l.Height = 360
	}
	return l
}

func (w *Workbench) boardPut(id string, l NodeLayout) {
	if w.preview != nil {
		for i := range w.preview.Agents {
			if w.preview.Agents[i].Key == id {
				w.preview.Agents[i].Layout = l
				return
			}
		}
		return
	}
	w.workspaceUI().Layouts[id] = l
}

func (w *Workbench) boardSave() {
	if w.preview == nil {
		w.save()
	}
}
func boardSize(l NodeLayout) (float32, float32) {
	if l.Expanded {
		return l.Width, l.Height
	}
	return boardNodeWidth, boardNodeHeight
}

func (w *Workbench) boardCollision(id string, l NodeLayout, panes []model.Pane) bool {
	width, height := boardSize(l)
	for _, p := range panes {
		if p.ID == id {
			continue
		}
		// A pane without a live layout has not been placed yet. Treating its
		// fallback as an occupied origin would push the first node offscreen.
		if w.preview == nil {
			if _, placed := w.workspaceUI().Layouts[p.ID]; !placed {
				continue
			}
		}
		other := w.boardLayout(p.ID)
		ow, oh := boardSize(other)
		if l.X < other.X+ow+boardGap && l.X+width+boardGap > other.X && l.Y < other.Y+oh+boardGap && l.Y+height+boardGap > other.Y {
			return true
		}
	}
	return false
}

func (w *Workbench) boardVacant(id string, l NodeLayout, panes []model.Pane) NodeLayout {
	if !w.boardCollision(id, l, panes) {
		return l
	}
	// Automatic placement searches right and down from the requested point.
	// It never pushes a node into negative coordinates merely to clear a
	// neighbor; manually positioned negative coordinates remain supported.
	for ring := 1; ; ring++ {
		for row := 0; row <= ring; row++ {
			candidate := l
			candidate.X += float32(ring-row) * (boardNodeWidth + boardGap)
			candidate.Y += float32(row) * (boardNodeHeight + boardGap)
			if !w.boardCollision(id, candidate, panes) {
				return candidate
			}
		}
	}
}

// Expanding a node is an explicit layout change. Keep that node in view once
// after automatic placement, without changing zoom or constraining later pans.
func (w *Workbench) boardReveal(l NodeLayout) {
	if w.board.width <= 0 || w.board.height <= 0 {
		return
	}
	v := w.boardCamera()
	width, height := boardSize(l)
	left, top := v.X+l.X*v.Scale, v.Y+l.Y*v.Scale
	width, height = width*v.Scale, height*v.Scale
	const margin float32 = 20
	visibleWidth := w.boardVisibleWidth()
	if width > visibleWidth-2*margin || left < margin {
		v.X += margin - left
	} else if left+width > visibleWidth-margin {
		v.X -= left + width - (visibleWidth - margin)
	}
	if height > w.board.height-2*margin || top < margin {
		v.Y += margin - top
	} else if top+height > w.board.height-margin {
		v.Y -= top + height - (w.board.height - margin)
	}
}

func (w *Workbench) boardSelected() string {
	if w.preview != nil {
		return w.board.previewSelected
	}
	return w.workspaceUI().SelectedID
}
func (w *Workbench) boardSelect(id string) {
	if w.preview != nil {
		w.board.previewSelected = id
	} else {
		w.workspaceUI().SelectedID = id
	}
	if w.board.connecting != "" && w.board.connecting != id {
		w.board.edgeA, w.board.edgeB = w.board.connecting, id
		w.board.connecting = ""
	}
	w.boardSave()
}

func (w *Workbench) boardZoom(scale, x, y float32) {
	v := w.boardCamera()
	old := v.Scale
	if old <= 0 {
		old = 1
	}
	v.Scale = boardClamp(scale, .25, 2)
	v.X = x - (x-v.X)*v.Scale/old
	v.Y = y - (y-v.Y)*v.Scale/old
}
func boardClamp(v, low, high float32) float32 { return max(low, min(high, v)) }

func (w *Workbench) boardBounds(panes []model.Pane) ui.Rect {
	if len(panes) == 0 {
		return ui.Rect{W: boardNodeWidth, H: boardNodeHeight}
	}
	l := w.boardLayout(panes[0].ID)
	ww, hh := boardSize(l)
	x0, y0, x1, y1 := l.X, l.Y, l.X+ww, l.Y+hh
	for _, p := range panes[1:] {
		l = w.boardLayout(p.ID)
		ww, hh = boardSize(l)
		x0 = min(x0, l.X)
		y0 = min(y0, l.Y)
		x1 = max(x1, l.X+ww)
		y1 = max(y1, l.Y+hh)
	}
	return ui.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}
func (w *Workbench) boardFit(panes []model.Pane) {
	b := w.boardGraphBounds(panes)
	v := w.boardCamera()
	visibleWidth := w.boardVisibleWidth()
	v.Scale = boardClamp(min((visibleWidth-96)/b.W, (w.board.height-96)/b.H), .25, 1.25)
	v.X = (visibleWidth-b.W*v.Scale)/2 - b.X*v.Scale
	v.Y = (w.board.height-b.H*v.Scale)/2 - b.Y*v.Scale
	w.boardSave()
}

func (w *Workbench) boardView(c *ui.Context) {
	t := c.Theme()
	camera := w.boardCamera()
	if camera.Scale <= 0 {
		camera.Scale = 1
	}
	panes := w.boardPanes()
	if w.preview == nil {
		changed := false
		for i, p := range panes {
			if _, ok := w.workspaceUI().Layouts[p.ID]; ok {
				continue
			}
			l := NodeLayout{X: float32(i%3) * 300, Y: float32(i/3) * 120, Width: 560, Height: 360}
			w.boardPut(p.ID, w.boardVacant(p.ID, l, panes))
			changed = true
		}
		if changed {
			w.save()
		}
	}
	canvas := ui.Box(c.Key("canvas")).Fill().Clip().Background(deskBackground)
	if r := canvas.Bounds(); r.W > 0 && r.H > 0 {
		w.board.width, w.board.height = r.W, r.H
	}
	if w.board.previewFitPending {
		if w.board.width > 0 && w.board.height > 0 {
			w.boardFit(panes)
			w.board.previewFitPending = false
		} else {
			c.Invalidate()
		}
	}
	canvas.HandleInput(func(ev ui.InputEvent) bool {
		switch ev.Kind {
		case ui.InputScroll:
			if ev.Mods&(ui.Ctrl|ui.Super) != 0 {
				w.boardZoom(camera.Scale*float32(math.Exp(float64(-ev.DY)*.003)), ev.X, ev.Y)
			} else {
				camera.X -= ev.DX
				camera.Y -= ev.DY
			}
			w.boardSave()
			return true
		case ui.InputZoom:
			w.boardZoom(camera.Scale*ev.Scale, ev.X, ev.Y)
			w.boardSave()
			return true
		}
		return false
	})
	if dx, dy, ok := canvas.Dragged(); ok && (dx != 0 || dy != 0) {
		camera.X += dx
		camera.Y += dy
	}
	if _, _, ok := canvas.DragEnded(); ok {
		w.boardSave()
	}
	canvas.Draw(func(p *ui.Painter, r ui.Rect) { w.boardGrid(p, r); w.boardDrawEdges(p, r, panes, t) })
	canvas.Children(func() {
		ui.Box(c.Key("world")).Absolute().Left(0).Top(0).Size(1, 1).TransformOrigin(0, 0).Scale(camera.Scale, camera.Scale).Translate(camera.X, camera.Y).Children(func() {
			for _, pane := range panes {
				w.boardNode(c, pane, panes)
			}
		})
		for _, edge := range w.boardEdgeRoutes(panes) {
			anchor := edge.Curve.at(.5)
			if iconButton(c.Key("edge/"+edge.A+"/"+edge.B), "编辑授权关系", iconLink).Absolute().Left(camera.X+anchor.X*camera.Scale-14).Top(camera.Y+anchor.Y*camera.Scale-14).Size(28, 28).Background(deskSurface).Border(1, deskLine).Radius(6).Clicked() {
				w.board.edgeA, w.board.edgeB = edge.A, edge.B
			}
		}
		if len(panes) == 0 {
			ui.Column(c.Key("empty")).Absolute().Left(0).Top(0).Fill().Center().Gap(12).Children(func() {
				ui.Icon(c, iconBoard).Size(32, 32).TextColor(deskFaint)
				ui.Text(c, "从一个 Agent 开始").FontSize(18).FontWeight(600).TextColor(deskText)
				ui.Text(c, "使用顶部的新增按钮，或应用已有拓扑模板。").FontSize(12).TextColor(deskMuted)
			})
		}
		w.boardTools(c, panes)
		if w.board.connecting != "" {
			name := w.board.connecting
			for _, p := range panes {
				if p.ID == w.board.connecting {
					name = p.Name
					break
				}
			}
			ui.Row(c.Key("connecting")).Absolute().Left(16).Top(16).Padding(8, 10).Gap(10).AlignItems(ui.Center).Background(deskSurface).Border(1, deskAccent).Radius(8).Children(func() {
				ui.Icon(c, iconLink).Size(16, 16).TextColor(deskAccent)
				ui.Column(c).Gap(3).Children(func() {
					ui.Text(c, "选择目标 Agent").FontSize(12).FontWeight(600)
					ui.Text(c, "从 "+name+" 配置两个方向的授权").FontSize(11).TextColor(deskMuted)
				})
				if iconButton(c, "取消连接", iconClose).Clicked() {
					w.board.connecting = ""
				}
			})
		} else {
			ui.Text(c, "拖动空白平移 · 空白处 ⌘/Ctrl + 滚轮缩放").Absolute().Left(16).Bottom(12).FontSize(10).TextColor(deskFaint)
		}
		if w.workspaceUI().ShowMinimap {
			w.boardMinimap(c, panes)
		}
		if w.board.edgeA != "" && w.board.edgeB != "" {
			w.boardEdgePanel(c, panes)
		}
	})
}

func (w *Workbench) boardTools(c *ui.Context, panes []model.Pane) {
	v := w.boardCamera()
	ui.Row(c.Key("boardTools")).Absolute().Right(w.boardControlInset()).Top(16).Height(36).Padding(4).Gap(2).AlignItems(ui.Center).Background(deskSurface).Border(1, deskLine).Radius(8).Children(func() {
		if iconButton(c, "缩小", iconMinus).Clicked() {
			w.boardZoom(v.Scale/1.15, w.boardVisibleWidth()/2, w.board.height/2)
			w.boardSave()
		}
		ui.Text(c, fmt.Sprintf("%.0f%%", v.Scale*100)).Width(42).FontSize(11).TextAlign(ui.Center).TextColor(deskMuted)
		if iconButton(c, "放大", iconPlus).Clicked() {
			w.boardZoom(v.Scale*1.15, w.boardVisibleWidth()/2, w.board.height/2)
			w.boardSave()
		}
		ui.Box(c).Width(1).Height(16).Margin(0, 4).Background(deskLine)
		if iconButton(c, "适应全部", iconFit).Clicked() {
			w.boardFit(panes)
		}
		mapButton := iconButton(c, "切换 MiniMap", iconMap)
		if w.workspaceUI().ShowMinimap {
			mapButton.Background(deskSelected).TextColor(deskAccent)
		}
		if mapButton.Clicked() {
			w.workspaceUI().ShowMinimap = !w.workspaceUI().ShowMinimap
			w.save()
		}
	})
}

func (w *Workbench) boardGrid(p *ui.Painter, r ui.Rect) {
	v := w.boardCamera()
	spacing := 32 * v.Scale
	for spacing < 24 {
		spacing *= 2
	}
	ox, oy := float32(math.Mod(float64(v.X), float64(spacing))), float32(math.Mod(float64(v.Y), float64(spacing)))
	if ox < 0 {
		ox += spacing
	}
	if oy < 0 {
		oy += spacing
	}
	color := deskLine
	color.A = 75
	for y := oy; y < r.H; y += spacing {
		for x := ox; x < r.W; x += spacing {
			p.Fill(ui.Rect{X: r.X + x, Y: r.Y + y, W: 1, H: 1}, color, .5)
		}
	}
}

func (w *Workbench) boardNode(c *ui.Context, pane model.Pane, panes []model.Pane) {
	l := w.boardLayout(pane.ID)
	width, height := boardSize(l)
	selected := w.boardSelected() == pane.ID
	border := deskLine
	if selected {
		border = deskAccent
	}
	main := w.workspaceUI().MainID
	if w.preview != nil {
		main = w.preview.Main
	}
	node := ui.Column(c.Key("node/"+pane.ID)).Absolute().Left(l.X).Top(l.Y).Size(width, height).Background(deskSurface).Border(1, border).Radius(10).Clip()
	if selected {
		node.Shadow(0, 3, 12, 0, ui.RGBA(0, 0, 0, .16))
	}
	node.Children(func() {
		header := ui.Row(c.Key("drag")).FillWidth().Height(44).Shrink(0).Padding(7, 9).Gap(7).AlignItems(ui.Center).Cursor(ui.CursorMove).Label("拖动 Agent 节点")
		if header.Clicked() {
			w.boardSelect(pane.ID)
		}
		if dx, dy, ok := header.Dragged(); ok && (dx != 0 || dy != 0) {
			candidate := w.boardLayout(pane.ID)
			candidate.X += dx
			candidate.Y += dy
			if !w.boardCollision(pane.ID, candidate, panes) {
				w.boardPut(pane.ID, candidate)
			}
			if w.preview == nil {
				w.workspaceUI().SelectedID = pane.ID
			} else {
				w.board.previewSelected = pane.ID
			}
		}
		if _, _, ok := header.DragEnded(); ok {
			w.boardSave()
		}
		header.Children(func() {
			agentGlyph(c, pane, 24)
			name := pane.Name
			if name == "" {
				name = pane.ID
			}
			ui.Text(c, name).FontSize(12).FontWeight(600).Grow(1).MinWidth(0).SingleLine().Ellipsis("…").TextColor(deskText)
			if l.Expanded {
				if width >= 440 {
					smallBadge(c, backendLabel(pane), deskMuted)
				}
				if width >= 520 {
					status := paneStatus(pane)
					if w.preview != nil {
						status = "预览"
					}
					smallBadge(c, status, deskMuted)
				}
				if main == pane.ID {
					ui.Icon(c, iconPin).Size(12, 12).TextColor(deskAccent).Tooltip("主 Agent")
				}
				if selected {
					w.boardQuickActions(c, pane)
				}
			}
			if w.preview == nil {
				w.boardNodeMenu(c, pane, main)
			}
			label, glyph := "展开", iconExpand
			if l.Expanded {
				label, glyph = "收起", iconCollapse
			}
			if iconButton(c, label, glyph).Clicked() {
				l = w.boardLayout(pane.ID)
				l.Expanded = !l.Expanded
				if l.Expanded {
					l = w.boardVacant(pane.ID, l, panes)
				}
				w.boardPut(pane.ID, l)
				if l.Expanded {
					w.boardReveal(l)
				}
				w.boardSelect(pane.ID)
			}
		})
		if !l.Expanded {
			meta := ui.Row(c.Key("meta")).FillWidth().Grow(1).Padding(0, 10, 7).Gap(5).AlignItems(ui.Center).Label("选择 Agent")
			if meta.Clicked() {
				w.boardSelect(pane.ID)
			}
			meta.Children(func() {
				smallBadge(c, backendLabel(pane), deskMuted)
				status := paneStatus(pane)
				color := deskMuted
				if status == "工作中" {
					color = c.Theme().Success
				} else if status == "等待处理" {
					color = c.Theme().Warning
				} else if status == "异常" {
					color = c.Theme().Danger
				}
				if w.preview != nil {
					status = "预览"
				}
				ui.Text(c, status).FontSize(10).TextColor(color)
				if main == pane.ID {
					smallBadge(c, "主 Agent", deskAccent)
				}
				ui.Box(c).Grow(1)
				if selected {
					w.boardQuickActions(c, pane)
				}
			})
		} else {
			ui.Box(c.Key("terminal")).FillWidth().Grow(1).MinHeight(0).Clip().Background(deskTerminal).Children(func() {
				if w.preview != nil {
					ui.Column(c).Fill().Center().Gap(8).Children(func() {
						ui.Icon(c, iconTerminal).Size(24, 24).TextColor(deskFaint)
						ui.Text(c, "应用模板后启动终端").FontSize(12).TextColor(deskMuted)
						ui.Text(c, pane.Cwd).FontSize(10).TextColor(deskFaint)
					})
				} else {
					w.terminalView(c, pane, false)
				}
			})
			resize := ui.Box(c.Key("resize")).Absolute().Right(0).Bottom(0).Size(18, 18).Cursor(ui.CursorResizeNWSE).Label("调整节点尺寸").Draw(func(p *ui.Painter, r ui.Rect) {
				grip := new(ui.Path).MoveTo(r.X+7, r.Y+15).LineTo(r.X+15, r.Y+7).MoveTo(r.X+11, r.Y+15).LineTo(r.X+15, r.Y+11)
				p.StrokePath(grip, 1, deskMuted)
			})
			if dx, dy, ok := resize.Dragged(); ok && (dx != 0 || dy != 0) {
				candidate := w.boardLayout(pane.ID)
				candidate.Width = boardClamp(candidate.Width+dx, 340, 1200)
				candidate.Height = boardClamp(candidate.Height+dy, 200, 900)
				if !w.boardCollision(pane.ID, candidate, panes) {
					w.boardPut(pane.ID, candidate)
				}
			}
			if _, _, ok := resize.DragEnded(); ok {
				w.boardSave()
			}
		}
	})
}

func (w *Workbench) boardQuickActions(c *ui.Context, pane model.Pane) {
	if w.preview == nil && iconButton(c, "聚焦", iconFocus).Clicked() {
		w.focusPane(pane.ID)
		w.setMode("focus")
	}
	if iconButton(c, "授权", iconLink).Clicked() {
		w.board.connecting = pane.ID
	}
}
func (w *Workbench) boardNodeMenu(c *ui.Context, pane model.Pane, main string) {
	compactMenu(c, "Agent 操作", iconMore, func(m *ui.Menu) {
		if m.Item("编辑 Agent").Chosen() {
			w.openAgent(pane.ID)
		}
		if m.Item("设为主 Agent").Disabled(main == pane.ID).Chosen() {
			w.setMain(pane.ID)
		}
		if m.Item("在辅助终端中打开").Chosen() {
			w.setDock(pane.ID)
			w.setMode("focus")
		}
		m.Separator()
		if m.Item("删除 Agent…").Chosen() {
			w.requestDelete(pane.ID)
		}
	})
}

func boardPairs(edges []model.Edge) [][2]string {
	seen := map[[2]string]bool{}
	pairs := make([][2]string, 0)
	for _, e := range edges {
		if e.From == e.To {
			continue
		}
		a, b := e.From, e.To
		if a > b {
			a, b = b, a
		}
		key := [2]string{a, b}
		if !seen[key] {
			seen[key] = true
			pairs = append(pairs, key)
		}
	}
	return pairs
}
func boardHasEdge(edges []model.Edge, from, to string) bool {
	for _, e := range edges {
		if e.From == from && e.To == to {
			return true
		}
	}
	return false
}
func (w *Workbench) boardSetEdge(from, to string, on bool) {
	if w.preview == nil {
		w.setEdge(from, to, on)
		return
	}
	links := make([]model.Edge, 0, len(w.preview.Links)+1)
	for _, e := range w.preview.Links {
		if e.From != from || e.To != to {
			links = append(links, e)
		}
	}
	if on {
		links = append(links, model.Edge{From: from, To: to})
	}
	w.preview.Links = links
}
func (w *Workbench) boardEdgePanel(c *ui.Context, panes []model.Pane) {
	t := c.Theme()
	a, b := w.board.edgeA, w.board.edgeB
	names := map[string]string{}
	for _, p := range panes {
		names[p.ID] = p.Name
	}
	if _, ok := names[a]; !ok {
		w.board.edgeA = ""
		return
	}
	if _, ok := names[b]; !ok {
		w.board.edgeB = ""
		return
	}
	ui.Column(c.Key("edgePanel")).Absolute().Left(16).Bottom(36).Width(340).Padding(14).Gap(12).Background(deskSurface).Border(1, deskLine).Radius(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconLink).Size(15, 15).TextColor(deskAccent)
			ui.Text(c, "授权关系").FontWeight(600).Grow(1)
			if iconButton(c, "关闭授权关系", iconClose).Clicked() {
				w.board.edgeA, w.board.edgeB = "", ""
			}
		})
		ui.Text(c, names[a]+" 与 "+names[b]).FontSize(12).TextColor(deskText)
		ui.Text(c, "分别允许两个方向的消息发送。").FontSize(11).TextColor(t.TextMuted)
		for _, pair := range [][2]string{{a, b}, {b, a}} {
			on := boardHasEdge(w.boardEdges(), pair[0], pair[1])
			if ui.Checkbox(c.Key(pair[0]+"/"+pair[1]), &on, names[pair[0]]+" → "+names[pair[1]]).Changed() {
				w.boardSetEdge(pair[0], pair[1], on)
			}
		}
	})
}

func (w *Workbench) boardMinimap(c *ui.Context, panes []model.Pane) {
	t := c.Theme()
	b := w.boardGraphBounds(panes)
	v := w.boardCamera()
	const mw, mh float32 = 150, 100
	scale := min((mw-16)/max(b.W, 1), (mh-16)/max(b.H, 1))
	ox, oy := (mw-b.W*scale)/2-b.X*scale, (mh-b.H*scale)/2-b.Y*scale
	mini := ui.Box(c.Key("minimap")).Absolute().Right(w.boardControlInset()).Bottom(16).Size(mw, mh).Background(deskSurface).Border(1, deskLine).Radius(8).Clip().Label("MiniMap，点击定位画布")
	mini.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputPointerDown || ev.Kind == ui.InputPointerMove && ev.Button == 0 {
			x, y := (ev.X-ox)/scale, (ev.Y-oy)/scale
			v.X = w.boardVisibleWidth()/2 - x*v.Scale
			v.Y = w.board.height/2 - y*v.Scale
			return true
		}
		if ev.Kind == ui.InputPointerUp {
			w.boardSave()
			return true
		}
		return false
	})
	mini.Draw(func(p *ui.Painter, r ui.Rect) {
		for _, edge := range w.boardEdgeRoutes(panes) {
			curve := edge.Curve.mapped(scale, r.X+ox, r.Y+oy)
			p.StrokePath(curve.path(), 1, t.TextMuted.Alpha(.5))
		}
		for _, pane := range panes {
			l := w.boardLayout(pane.ID)
			ww, hh := boardSize(l)
			color := t.TextMuted
			if pane.ID == w.boardSelected() {
				color = t.Accent
			}
			p.Fill(ui.Rect{X: r.X + ox + l.X*scale, Y: r.Y + oy + l.Y*scale, W: ww * scale, H: max(3, hh*scale)}, color, 2)
		}
		p.Stroke(ui.Rect{X: r.X + ox - v.X/v.Scale*scale, Y: r.Y + oy - v.Y/v.Scale*scale, W: w.boardVisibleWidth() / v.Scale * scale, H: w.board.height / v.Scale * scale}, t.Accent, 2, 1)
	})
}
