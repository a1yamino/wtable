package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v3"
	"github.com/rivo/uniseg"
	"wtable/internal/model"
)

// graphView keeps selection by identity so polling cannot retarget an action.
type graphView struct {
	node        string
	edge        model.Edge
	edges       bool
	confirm     *model.Edge
	x, y, tasks int
	follow      bool
}

type graphLayout struct {
	nodes         []model.GraphNode
	boxes         map[string]rect
	levels        map[string]int
	width, height int
}

// Collapse strongly connected components before computing longest-path levels.
func layoutGraph(g *model.Graph) graphLayout {
	l := graphLayout{boxes: map[string]rect{}, levels: map[string]int{}}
	if g == nil {
		return l
	}
	adj := map[string][]string{}
	for _, n := range g.Nodes {
		adj[n.ID] = nil
	}
	for _, e := range g.Edges {
		if _, ok := adj[e.From]; !ok {
			continue
		}
		if _, ok := adj[e.To]; !ok {
			continue
		}
		adj[e.From] = append(adj[e.From], e.To)
	}
	index, low, component := map[string]int{}, map[string]int{}, map[string]int{}
	on := map[string]bool{}
	stack := []string{}
	next, count := 0, 0
	var visit func(string)
	visit = func(id string) {
		next++
		index[id], low[id] = next, next
		stack = append(stack, id)
		on[id] = true
		for _, to := range adj[id] {
			if index[to] == 0 {
				visit(to)
				low[id] = min(low[id], low[to])
			} else if on[to] {
				low[id] = min(low[id], index[to])
			}
		}
		if low[id] == index[id] {
			for {
				v := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				on[v] = false
				component[v] = count
				if v == id {
					break
				}
			}
			count++
		}
	}
	for _, n := range g.Nodes {
		if index[n.ID] == 0 {
			visit(n.ID)
		}
	}
	parents := make([][]int, count)
	for from, tos := range adj {
		for _, to := range tos {
			if component[from] != component[to] {
				parents[component[to]] = append(parents[component[to]], component[from])
			}
		}
	}
	levels := map[int]int{}
	var level func(int) int
	level = func(c int) int {
		if v, ok := levels[c]; ok {
			return v
		}
		v := 0
		for _, p := range parents[c] {
			v = max(v, level(p)+1)
		}
		levels[c] = v
		return v
	}
	l.nodes = append(l.nodes, g.Nodes...)
	for _, n := range l.nodes {
		l.levels[n.ID] = level(component[n.ID])
	}
	sort.Slice(l.nodes, func(i, j int) bool {
		a, b := l.nodes[i], l.nodes[j]
		if l.levels[a.ID] != l.levels[b.ID] {
			return l.levels[a.ID] < l.levels[b.ID]
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	columns := map[int]int{}
	for _, n := range l.nodes {
		lv := l.levels[n.ID]
		r := rect{2 + columns[lv]*34, 1 + lv*9, 28, 5}
		columns[lv]++
		l.boxes[n.ID] = r
		l.width = max(l.width, r.x+r.w+4)
		l.height = max(l.height, r.y+r.h+2)
	}
	return l
}

func (u *ui) graphEdges() []model.GraphEdge {
	var edges []model.GraphEdge
	if u.snapshot.Graph != nil {
		for _, e := range u.snapshot.Graph.Edges {
			if e.Kind == "grant" {
				edges = append(edges, e)
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.From != b.From {
			return a.From < b.From
		}
		return a.To < b.To
	})
	return edges
}

func (u *ui) openGraph() {
	u.graph = &graphView{node: "you", follow: true}
	if p := u.current(); p != nil {
		u.graph.node = p.ID
	}
	u.selection = nil
	u.copyMode = false
	u.resize()
	if _, err := u.refresh(); err != nil {
		u.status = err.Error()
	}
}

func (u *ui) graphKey(e *tcell.EventKey) {
	v := u.graph
	if v.confirm != nil {
		if e.Str() == "y" {
			edge := *v.confirm
			if u.call("edge.remove", edge, nil) {
				v.confirm = nil
				u.status = "已删除授权边，该方向排队任务已取消"
				_, _ = u.refresh()
			}
		} else if e.Str() == "n" || e.Key() == tcell.KeyEscape {
			v.confirm = nil
		}
		return
	}
	if e.Key() == tcell.KeyEscape || e.Str() == "q" {
		u.graph = nil
		u.resize()
		_, _ = u.refresh()
		return
	}
	if e.Str() == "e" {
		v.edges = !v.edges
		v.follow = true
		return
	}
	if e.Key() == tcell.KeyPgDn {
		v.tasks++
		return
	}
	if e.Key() == tcell.KeyPgUp {
		v.tasks = max(0, v.tasks-1)
		return
	}
	switch e.Key() {
	case tcell.KeyLeft:
		v.x = max(0, v.x-8)
		v.follow = false
		return
	case tcell.KeyRight:
		v.x += 8
		v.follow = false
		return
	case tcell.KeyUp:
		v.y = max(0, v.y-3)
		v.follow = false
		return
	case tcell.KeyDown:
		v.y += 3
		v.follow = false
		return
	}
	delta := 0
	if e.Str() == "j" {
		delta = 1
	}
	if e.Str() == "k" {
		delta = -1
	}
	if v.edges {
		edges := u.graphEdges()
		if len(edges) == 0 {
			return
		}
		idx := 0
		for i, a := range edges {
			if a.From == v.edge.From && a.To == v.edge.To {
				idx = i
			}
		}
		idx = (idx + delta + len(edges)) % len(edges)
		edge := edges[idx]
		v.edge = model.Edge{From: edge.From, To: edge.To}
		if delta != 0 {
			v.follow = true
			v.node = edge.From
			v.tasks = 0
		}
		if e.Str() == "x" {
			copy := v.edge
			v.confirm = &copy
		}
		return
	}
	l := layoutGraph(u.snapshot.Graph)
	if len(l.nodes) == 0 {
		return
	}
	idx := 0
	for i, n := range l.nodes {
		if n.ID == v.node {
			idx = i
		}
	}
	if delta != 0 {
		idx = (idx + delta + len(l.nodes)) % len(l.nodes)
		v.node = l.nodes[idx].ID
		v.follow = true
		v.tasks = 0
	}
	if e.Key() == tcell.KeyEnter {
		for i, p := range u.snapshot.Panes {
			if p.ID == v.node {
				u.graph = nil
				u.openPane(u.snapshot.Panes[i].ID, true)
				_, _ = u.refresh()
				return
			}
		}
	}
}

func (u *ui) drawGraph() {
	w, h := u.screen.Size()
	v := u.graph
	u.screen.Clear()
	u.screen.HideCursor()
	l := layoutGraph(u.snapshot.Graph)
	if _, ok := l.boxes[v.node]; !ok && len(l.nodes) > 0 {
		v.node = l.nodes[0].ID
		v.follow = true
	}
	edges := u.graphEdges()
	if v.edges && len(edges) > 0 {
		found := false
		for _, e := range edges {
			if e.From == v.edge.From && e.To == v.edge.To {
				found = true
			}
		}
		if !found {
			v.edge = model.Edge{From: edges[0].From, To: edges[0].To}
			v.node = v.edge.From
			v.follow = true
		}
	}
	taskHeight := min(7, max(2, h/3))
	bottom := max(2, h-taskHeight-1)
	viewH := max(0, bottom-2)
	if v.follow {
		r := l.boxes[v.node]
		v.x = max(0, r.x-max(0, (w-r.w)/2))
		v.y = max(0, r.y-max(0, (viewH-r.h)/2))
		v.follow = false
	}
	v.x = min(v.x, max(0, l.width+6-w))
	v.y = min(v.y, max(0, l.height-viewH))
	put := func(x, y int, ch string, style tcell.Style) {
		x -= v.x
		y = y - v.y + 2
		if x >= 0 && x < w && y >= 2 && y < bottom {
			u.screen.Put(x, y, ch, style)
		}
	}
	path := func(x1, y1, x2, y2 int, dashed bool, style tcell.Style) {
		if x1 == x2 {
			for y := min(y1, y2); y <= max(y1, y2); y++ {
				ch := "│"
				if dashed {
					ch = "┆"
				}
				put(x1, y, ch, style)
			}
		} else {
			for x := min(x1, x2); x <= max(x1, x2); x++ {
				ch := "─"
				if dashed {
					ch = "┄"
				}
				put(x, y1, ch, style)
			}
		}
	}
	drawEdge := func(e model.GraphEdge) {
		a, ok := l.boxes[e.From]
		if !ok {
			return
		}
		b, ok := l.boxes[e.To]
		if !ok {
			return
		}
		dashed := e.Kind == "virtual"
		style := tcell.StyleDefault.Foreground(tcell.ColorGray)
		if dashed {
			style = style.Dim(true)
		} else if v.edges && e.From == v.edge.From && e.To == v.edge.To {
			style = focusStyle
		}
		if a.y == b.y && abs(a.x-b.x) == 34 {
			if a.x < b.x {
				path(a.x+a.w, a.y+2, b.x-1, b.y+2, dashed, style)
				put(b.x-1, b.y+2, "▶", style)
			} else {
				path(b.x+b.w, b.y+2, a.x-1, a.y+2, dashed, style)
				put(b.x+b.w, b.y+2, "◀", style)
			}
			return
		}
		if b.y > a.y {
			x1, x2 := a.x+a.w/2, b.x+b.w/2
			y := a.y + a.h + 1
			path(x1, a.y+a.h, x1, y, dashed, style)
			path(x1, y, x2, y, dashed, style)
			path(x2, y, x2, b.y-1, dashed, style)
			put(x2, b.y-1, "▼", style)
		} else {
			// A fixed right-side rail is intentional in v1; no crossing avoidance.
			rail := l.width
			y1, y2 := a.y+3, b.y+1
			path(a.x+a.w, y1, rail, y1, dashed, style)
			path(rail, y1, rail, y2, dashed, style)
			path(b.x+b.w, y2, rail, y2, dashed, style)
			put(b.x+b.w, y2, "◀", style)
		}
	}
	if u.snapshot.Graph != nil {
		for _, e := range u.snapshot.Graph.Edges {
			if !(v.edges && e.Kind == "grant" && e.From == v.edge.From && e.To == v.edge.To) {
				drawEdge(e)
			}
		}
		for _, e := range edges {
			if v.edges && e.From == v.edge.From && e.To == v.edge.To {
				drawEdge(e)
			}
		}
	}
	for _, n := range l.nodes {
		r := l.boxes[n.ID]
		style := barStyle
		if n.ID == v.node {
			style = focusStyle
		}
		text := []string{"┌" + strings.Repeat("─", r.w-2) + "┐", n.Name, n.Backend + " · " + n.State, fmt.Sprintf("未读 %d  %s", n.Unread, strings.Join(n.Attention, ",")), "└" + strings.Repeat("─", r.w-2) + "┘"}
		for row, s := range text {
			y := r.y + row - v.y + 2
			if y < 2 || y >= bottom {
				continue
			}
			// Use grapheme-aware line rendering, clipped on both sides of the viewport.
			cells := wrap(s, r.w-2)
			if row == 0 || row == 4 {
				cells = []string{s}
			}
			line := ""
			if len(cells) > 0 {
				line = cells[0]
			}
			if row > 0 && row < 4 {
				line = "│" + line + strings.Repeat(" ", max(0, r.w-2-uniseg.StringWidth(line))) + "│"
			}
			drawGraphLine(u, r.x-v.x, y, w, line, style)
		}
	}
	mode := "节点"
	if v.edges {
		mode = "真实边"
	}
	u.line(0, 0, w, "协作 Graph · "+mode+" · j/k 选择 · Enter 打开 · e 节点/边 · x 删边 · Esc 返回", barStyle)
	detail := "虚线 you → pane 为人类入口，不可选删 · 方向键移动视口 · PgUp/PgDn 任务"
	if v.edges {
		detail = "没有真实授权边"
		if len(edges) > 0 {
			for _, edge := range edges {
				if edge.From == v.edge.From && edge.To == v.edge.To {
					detail = fmt.Sprintf("选中边：%s → %s · active %d · x 删除（需确认）", u.paneName(edge.From), u.paneName(edge.To), edge.Active)
					break
				}
			}
		}
	}
	u.line(0, 1, w, detail, tcell.StyleDefault)
	related := u.graphItems(v.node)
	capacity := max(0, h-bottom-2)
	v.tasks = min(v.tasks, max(0, len(related)-capacity))
	u.line(0, bottom, w, fmt.Sprintf("关联任务 · %s · %d 条 · PgUp/PgDn 滚动", u.paneName(v.node), len(related)), barStyle)
	for i := 0; i < capacity && i+v.tasks < len(related); i++ {
		t := related[i+v.tasks]
		unread := ""
		style := tcell.StyleDefault
		if t.unread {
			unread = " · 未读"
			style = style.Bold(true)
		}
		u.line(0, bottom+1+i, w, fmt.Sprintf("%s%s  %s → %s  [%s]%s", t.kind, t.id, u.paneName(t.from), u.paneName(t.to), t.state, unread), style)
	}
	if len(related) == 0 {
		u.line(0, bottom+1, w, "暂无关联任务", tcell.StyleDefault)
	}
	u.line(0, h-1, w, u.status, barStyle)
	if v.confirm != nil {
		u.modal([]string{"删除授权边：" + u.paneName(v.confirm.From) + " → " + u.paneName(v.confirm.To) + "？", "该方向排队任务会取消，已执行操作不会撤销。", "按 y 确认；n / Esc 取消；Enter 不确认", u.status})
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Clip entire graphemes so panning never writes half a wide character.
func drawGraphLine(u *ui, x, y, width int, text string, style tcell.Style) {
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		n := g.Width()
		if x >= 0 && x+n <= width {
			u.screen.Put(x, y, g.Str(), style)
		}
		x += n
	}
}

// Reports remain distinct entries: their sender is the worker, and their
// recipient can differ from both original task participants.
type graphItem struct {
	id, kind, from, to, state string
	unread                    bool
	created                   time.Time
}

func (u *ui) graphItems(node string) []graphItem {
	if node == "you" {
		node = "controller"
	}
	items := []graphItem{}
	for _, t := range u.snapshot.Tasks {
		if t.From != node && t.To != node {
			continue
		}
		unread := t.To == node && !t.Seen && (t.State == "completed" || t.State == "failed" || t.State == "unknown")
		items = append(items, graphItem{id: t.ID, from: t.From, to: t.To, state: t.State, unread: unread, created: t.CreatedAt})
	}
	for _, r := range u.snapshot.Reports {
		if r.From != node && r.To != node {
			continue
		}
		items = append(items, graphItem{id: r.ID, kind: "report ", from: r.From, to: r.To, state: r.State, unread: r.To == node && !r.Seen, created: r.CreatedAt})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].created.Equal(items[j].created) {
			return items[i].id < items[j].id
		}
		return items[i].created.After(items[j].created)
	})
	return items
}
