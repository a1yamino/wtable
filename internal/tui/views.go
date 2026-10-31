package tui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v3"
	"github.com/rivo/uniseg"
	"wtable/internal/model"
)

// Layouts belong to the attached UI, independently of daemon pane lifetimes.
type viewNode struct {
	pane          string
	vertical      bool
	ratio         float64
	first, second *viewNode
}
type viewTab struct {
	name, focus string
	root        *viewNode
	zoom        bool
}
type panePicker struct {
	selected int
	split    string
	anchor   string
}

func (n *viewNode) leaves() []string {
	if n == nil {
		return nil
	}
	if n.pane != "" {
		return []string{n.pane}
	}
	return append(n.first.leaves(), n.second.leaves()...)
}
func (n *viewNode) find(id string) *viewNode {
	if n == nil {
		return nil
	}
	if n.pane == id {
		return n
	}
	if found := n.first.find(id); found != nil {
		return found
	}
	return n.second.find(id)
}
func pruneView(n *viewNode, keep func(string) bool) *viewNode {
	if n == nil {
		return nil
	}
	if n.pane != "" {
		if keep(n.pane) {
			return n
		}
		return nil
	}
	n.first, n.second = pruneView(n.first, keep), pruneView(n.second, keep)
	if n.first == nil {
		return n.second
	}
	if n.second == nil {
		return n.first
	}
	return n
}
func (u *ui) tab() *viewTab {
	if len(u.views) == 0 {
		u.views = []viewTab{{name: "tab-1", zoom: true}}
		if p := u.current(); p != nil {
			u.views[0].root = &viewNode{pane: p.ID}
			u.views[0].focus = p.ID
		}
	}
	return &u.views[u.viewIndex]
}
func (u *ui) paneByID(id string) *model.Pane {
	for i := range u.snapshot.Panes {
		if u.snapshot.Panes[i].ID == id {
			return &u.snapshot.Panes[i]
		}
	}
	return nil
}
func (u *ui) focusID(id string) {
	for i, p := range u.snapshot.Panes {
		if p.ID == id {
			u.focus = i
			u.tab().focus = id
			return
		}
	}
}
func (u *ui) syncViews() {
	if u.parked == nil {
		u.parked = map[string]bool{}
	}
	u.tab()
	for i := range u.views {
		t := &u.views[i]
		t.root = pruneView(t.root, func(id string) bool { return u.paneByID(id) != nil && !u.parked[id] })
		if t.root.find(t.focus) == nil {
			t.focus = ""
			if ids := t.root.leaves(); len(ids) > 0 {
				t.focus = ids[0]
			}
		}
	}
	if id := u.tab().focus; id != "" {
		u.focusID(id)
	}
}
func (u *ui) openPane(id string, zoom bool) {
	if u.paneByID(id) == nil {
		return
	}
	// Opening a pane always leaves full-screen views behind.
	u.graph = nil
	u.taskView = false
	u.actions = nil
	u.syncViews()
	delete(u.parked, id)
	t := u.tab()
	if t.root.find(id) == nil {
		if leaf := t.root.find(t.focus); leaf != nil {
			*leaf = viewNode{pane: id}
		} else {
			t.root = &viewNode{pane: id}
		}
	}
	u.focusID(id)
	t.zoom = zoom
	u.zoom = zoom
	u.copyMode = false
	u.selection = nil
	u.resize()
	u.markVisibleSeen()
}
func (u *ui) togglePark(id string) {
	u.syncViews()
	if u.parked[id] || u.tab().root.find(id) == nil {
		u.openPane(id, true)
		return
	}
	u.parked[id] = true
	u.syncViews()
	u.zoom = u.tab().zoom
	u.resize()
	u.status = "已驻留 " + u.paneName(id) + "；进程继续运行，终端尺寸保持不变"
}
func (u *ui) newTab() {
	t := viewTab{name: fmt.Sprintf("tab-%d", u.nextTab+2), zoom: true}
	u.nextTab++
	if p := u.current(); p != nil {
		delete(u.parked, p.ID)
		t.root = &viewNode{pane: p.ID}
		t.focus = p.ID
	}
	u.views = append(u.views, t)
	u.viewIndex = len(u.views) - 1
	u.zoom = true
	u.resize()
}
func (u *ui) switchTab(delta int) {
	u.tab()
	u.tab().zoom = u.zoom
	u.viewIndex = (u.viewIndex + delta + len(u.views)) % len(u.views)
	u.zoom = u.tab().zoom
	u.syncViews()
	u.resize()
	u.markVisibleSeen()
}
func (u *ui) closeTab() {
	if len(u.views) < 2 {
		u.status = "至少保留一个 tab"
		return
	}
	u.views = append(u.views[:u.viewIndex], u.views[u.viewIndex+1:]...)
	u.viewIndex = min(u.viewIndex, len(u.views)-1)
	u.zoom = u.tab().zoom
	u.syncViews()
	u.resize()
	u.markVisibleSeen()
}
func (u *ui) arrangeView() {
	ids := u.tab().root.leaves()
	var build func([]string, int) *viewNode
	build = func(ids []string, depth int) *viewNode {
		if len(ids) == 0 {
			return nil
		}
		if len(ids) == 1 {
			return &viewNode{pane: ids[0]}
		}
		mid := len(ids) / 2
		vertical := u.layout == 1 || (u.layout == 0 && depth%2 == 0)
		return &viewNode{vertical: vertical, ratio: float64(mid) / float64(len(ids)), first: build(ids[:mid], depth+1), second: build(ids[mid:], depth+1)}
	}
	u.tab().root = build(ids, 0)
	u.zoom = false
	u.tab().zoom = false
}
func (u *ui) pickPane(split string) {
	u.picker = &panePicker{split: split}
	if p := u.current(); p != nil {
		u.picker.selected = u.focus
		u.picker.anchor = p.ID
	}
	u.selection = nil
	u.copyMode = false
}
func (u *ui) pickerKey(e *tcell.EventKey) {
	p := u.picker
	n := len(u.snapshot.Panes)
	if e.Key() == tcell.KeyEscape {
		u.picker = nil
		return
	}
	if n == 0 {
		return
	}
	switch {
	case e.Key() == tcell.KeyDown || e.Str() == "j":
		p.selected = (p.selected + 1) % n
	case e.Key() == tcell.KeyUp || e.Str() == "k":
		p.selected = (p.selected + n - 1) % n
	case e.Key() == tcell.KeyHome:
		p.selected = 0
	case e.Key() == tcell.KeyEnd:
		p.selected = n - 1
	case e.Key() == tcell.KeyEnter:
		id := u.snapshot.Panes[p.selected].ID
		if p.split == "" {
			u.picker = nil
			u.openPane(id, true)
			return
		}
		if id == p.anchor {
			u.status = "分屏请选择另一个 pane"
			return
		}
		t := u.tab()
		t.root = pruneView(t.root, func(existing string) bool { return existing != id })
		leaf := t.root.find(p.anchor)
		if leaf == nil {
			u.status = "原分屏对象已移除，请重新选择"
			u.picker = nil
			return
		}
		delete(u.parked, id)
		*leaf = viewNode{vertical: p.split == "|", ratio: .5, first: &viewNode{pane: p.anchor}, second: &viewNode{pane: id}}
		u.picker = nil
		u.focusID(id)
		t.zoom = false
		u.zoom = false
		u.resize()
		u.markVisibleSeen()
	}
}
func (u *ui) drawPicker() {
	p := u.picker
	_, h := u.screen.Size()
	capacity := max(1, h/2-3)
	title := "选择 pane · j/k 或 ↑↓ · Enter 单看 · Esc 取消"
	if p.split != "" {
		title = "分屏目标 · j/k 或 ↑↓ · Enter 确认 · Esc 取消"
	}
	lines := []string{title}
	p.selected = min(p.selected, max(0, len(u.snapshot.Panes)-1))
	start := max(0, p.selected-capacity+1)
	for i := start; i < min(len(u.snapshot.Panes), start+capacity); i++ {
		pane := u.snapshot.Panes[i]
		mark := "  "
		if i == p.selected {
			mark = "› "
		}
		lines = append(lines, mark+u.agentSummary(pane))
	}
	if len(u.snapshot.Panes) == 0 {
		lines = append(lines, "没有可选 pane")
	}
	u.modal(lines)
}
func (u *ui) layoutView(n *viewNode, r rect) {
	if n == nil || r.w < 1 || r.h < 1 {
		return
	}
	if n.pane != "" {
		u.boxes[n.pane] = r
		return
	}
	if n.ratio <= 0 || n.ratio >= 1 {
		n.ratio = .5
	}
	total, minimum := r.h, 3
	if n.vertical {
		total = r.w
		minimum = 8
	}
	if total < 3 {
		u.layoutView(n.first, r)
		return
	}
	usable := total - 1
	minimum = min(minimum, usable/2)
	cut := max(minimum, min(usable-minimum, int(float64(usable)*n.ratio+.5)))
	a, b := r, r
	d := divider{vertical: n.vertical, node: n, area: r}
	if n.vertical {
		a.w = cut
		b.x = r.x + cut + 1
		b.w = usable - cut
		d.rect = rect{r.x + cut, r.y, 1, r.h}
	} else {
		a.h = cut
		b.y = r.y + cut + 1
		b.h = usable - cut
		d.rect = rect{r.x, r.y + cut, r.w, 1}
	}
	u.dividers = append(u.dividers, d)
	u.layoutView(n.first, a)
	u.layoutView(n.second, b)
}
func (u *ui) attention(id string) (int, []string) {
	if u.snapshot.Graph != nil {
		for _, n := range u.snapshot.Graph.Nodes {
			if n.ID == id {
				return n.Unread, n.Attention
			}
		}
	}
	return 0, nil
}
func truncateColumns(text string, limit int) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\n", " "), "\r", "")
	var b strings.Builder
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		width := g.Width()
		if width > limit {
			break
		}
		b.WriteString(g.Str())
		limit -= width
	}
	return b.String()
}
func (u *ui) agentSummary(p model.Pane) string {
	unread, attention := u.attention(p.ID)
	visibility := "parked"
	if _, ok := u.boxes[p.ID]; ok {
		visibility = "visible"
	}
	activity := "尚无输出"
	if !p.LastActivity.IsZero() {
		activity = p.LastActivity.Local().Format("15:04:05")
	}
	queue := ""
	if n := u.queuedTasks(p.ID); n > 0 {
		queue = fmt.Sprintf(" · ⏸%d", n)
	}
	return fmt.Sprintf("%s · %s · %s · 未读 %d %s%s · %s · %s", p.Name, p.State, visibility, unread, strings.Join(attention, ","), queue, truncateColumns(p.LastOutput, 80), activity)
}
func (u *ui) viewStatus() string {
	t := u.tab()
	parked, waiting := 0, 0
	for _, p := range u.snapshot.Panes {
		if _, ok := u.boxes[p.ID]; !ok {
			parked++
		}
		_, attention := u.attention(p.ID)
		if len(attention) > 0 || needsReadinessAttention(p.Readiness) {
			waiting++
		}
	}
	position := 0
	for i, id := range t.root.leaves() {
		if p := u.current(); p != nil && p.ID == id {
			position = i + 1
		}
	}
	return fmt.Sprintf("[%s %d/%d] %d/%d · %d parked · %d waiting", t.name, u.viewIndex+1, len(u.views), position, len(t.root.leaves()), parked, waiting)
}
func (u *ui) nextAttention() {
	n := len(u.snapshot.Panes)
	for step := 1; step <= n; step++ {
		p := u.snapshot.Panes[(u.focus+step)%n]
		_, a := u.attention(p.ID)
		if len(a) > 0 || needsReadinessAttention(p.Readiness) {
			u.openPane(p.ID, true)
			return
		}
	}
	u.status = "没有需要处理的 pane"
}
func needsReadinessAttention(readiness string) bool {
	return readiness == "unconfirmed" || readiness == "blocked" || readiness == "failed"
}
func (u *ui) markVisibleSeen() {
	if u.graph != nil {
		return
	}
	for _, t := range u.snapshot.Tasks {
		if _, ok := u.boxes[t.To]; ok && !t.Seen && (t.State == "completed" || t.State == "failed" || t.State == "unknown") {
			u.call("task.seen", map[string]string{"id": t.ID}, nil)
		}
	}
	for _, r := range u.snapshot.Reports {
		if _, ok := u.boxes[r.To]; ok && !r.Seen {
			u.call("report.seen", map[string]string{"id": r.ID}, nil)
		}
	}
}
func (u *ui) peekPane(p model.Pane) {
	u.ensurePanel(0)
	var result struct {
		Lines []string `json:"lines"`
	}
	if u.call("pane.peek", map[string]any{"id": p.ID, "lines": 20}, &result) {
		u.panel.preview = "最近 20 行 · " + p.Name + "（后台尺寸保持不变）\n" + strings.Join(result.Lines, "\n")
		u.panel.scroll = 0
	}
}
func (u *ui) conflictWarnings(cwd, layer, account string) []string {
	location, shared := 0, 0
	for _, p := range u.snapshot.Panes {
		if p.State != "running" {
			continue
		}
		if (cwd != "" && p.Cwd == cwd) || (layer != "" && p.Layer == layer) {
			location++
		}
		if account != "" && p.Account == account {
			shared++
		}
	}
	lines := []string{}
	if location > 0 {
		lines = append(lines, fmt.Sprintf("⚠ 同目录 / Layer 已有 %d 个运行实例；选项字段按 L 另开 Layer", location))
	}
	if shared > 0 {
		lines = append(lines, fmt.Sprintf("⚠ 同账号 %d 个运行实例，共享额度", shared))
	}
	return lines
}
