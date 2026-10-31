package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rivo/uniseg"
	"wtable/internal/model"
)

// canvas is the whole layout: one main pane, one optional utility pane and a
// persistent rail. There is deliberately no tree, tab, park or zoom state.
type canvas struct {
	main     string
	utility  string
	vertical bool // true: utility to the right; false: below
	ratio    float64
	railW    int
	drawer   bool // narrow terminal: rail expanded as a drawer
}

type focusArea int

const (
	focusMain focusArea = iota
	focusUtility
	focusRail
	focusApproval
)

func (u *ui) paneByID(id string) *model.Pane {
	for i := range u.snapshot.Panes {
		if u.snapshot.Panes[i].ID == id {
			return &u.snapshot.Panes[i]
		}
	}
	return nil
}

func (u *ui) mainPane() *model.Pane { return u.paneByID(u.canvas.main) }

func (u *ui) utilityPane() *model.Pane {
	if u.canvas.utility == "" {
		return nil
	}
	return u.paneByID(u.canvas.utility)
}

// current is the pane that receives plain keys: the utility while it is
// focused, otherwise the main pane.
func (u *ui) current() *model.Pane {
	if u.area == focusUtility {
		if p := u.utilityPane(); p != nil {
			return p
		}
	}
	return u.mainPane()
}

func (u *ui) focusID(id string) {
	for i, p := range u.snapshot.Panes {
		if p.ID == id {
			u.focus = i
			return
		}
	}
}

func (u *ui) focusedPaneID() string {
	if p := u.current(); p != nil {
		return p.ID
	}
	return ""
}

// syncCanvas keeps the two canvas slots pointing at live panes. It never
// changes focus behind the user's back unless the focused pane disappeared.
func (u *ui) syncCanvas() {
	if u.canvas.main != "" && u.paneByID(u.canvas.main) == nil {
		u.canvas.main = ""
	}
	if u.canvas.utility != "" {
		if p := u.paneByID(u.canvas.utility); p == nil || restartable(p.State) || u.canvas.utility == u.canvas.main {
			// A dead utility collapses back to the canvas; the process stays in
			// the rail with a failure badge.
			u.canvas.utility = ""
			u.utilityLast = ""
			if u.area == focusUtility {
				u.area = focusMain
			}
		}
	}
	if u.canvas.main == "" {
		u.canvas.main = u.initialMain()
	}
	if u.canvas.ratio <= 0 || u.canvas.ratio >= 1 {
		u.canvas.ratio = .35
	}
	if u.area == focusUtility && u.canvas.utility == "" {
		u.area = focusMain
	}
	if u.area == focusApproval && len(u.permitsFor(u.canvas.main)) == 0 {
		u.area = focusMain
	}
	if u.canvas.main != "" {
		u.focusID(u.canvas.main)
	}
}

// initialMain prefers the server's LastFocus when it is still running, then any
// running pane, then the foreground, then the last focused pane (so an exit
// page is still visible), then creation order.
func (u *ui) initialMain() string {
	if id := u.snapshot.LastFocus; id != "" {
		if p := u.paneByID(id); p != nil && p.State == "running" {
			return id
		}
	}
	for _, p := range u.snapshot.Panes {
		if p.State == "running" {
			return p.ID
		}
	}
	if id := u.snapshot.LastFocus; id != "" && u.paneByID(id) != nil {
		return id
	}
	for _, p := range u.snapshot.Panes {
		if p.Kind == model.PaneForeground {
			return p.ID
		}
	}
	if len(u.snapshot.Panes) > 0 {
		return u.snapshot.Panes[0].ID
	}
	return ""
}

// selectMain replaces the main canvas. Selecting the pane that currently sits
// in the utility slot swaps the two; it never creates a split.
func (u *ui) selectMain(id string) {
	if u.paneByID(id) == nil {
		return
	}
	if u.canvas.utility == id {
		u.canvas.utility, u.canvas.main = u.canvas.main, u.canvas.utility
	} else {
		u.canvas.main = id
	}
	u.area = focusMain
	u.canvas.drawer = false
	u.copyMode = false
	u.selection = nil
	u.offsets[id] = 0
	u.syncCanvas()
	u.markFocusedSeen(u.canvas.main)
	u.resize()
}

func (u *ui) selectUtility(id string) {
	if u.paneByID(id) == nil || id == u.canvas.main {
		return
	}
	u.canvas.utility = id
	u.area = focusUtility
	u.canvas.drawer = false
	u.utilityLast = id
	u.selection = nil
	u.markFocusedSeen(id)
	u.resize()
}

func (u *ui) closeUtility() {
	if u.canvas.utility == "" {
		return
	}
	u.utilityLast = u.canvas.utility
	u.canvas.utility = ""
	u.area = focusMain
	u.resize()
}

// toggleUtility closes the slot, or opens the last utility pane when it still
// exists. A brand-new shell follows the main pane's directory.
func (u *ui) toggleUtility() {
	if u.canvas.utility != "" {
		u.closeUtility()
		u.status = "工具槽已关闭；进程继续在左栏运行"
		return
	}
	if u.utilityLast != "" && u.utilityLast != u.canvas.main && u.paneByID(u.utilityLast) != nil {
		u.selectUtility(u.utilityLast)
		return
	}
	cwd := u.cwd
	if p := u.mainPane(); p != nil && p.Cwd != "" {
		cwd = p.Cwd
	}
	shell := model.SpawnParams{
		Name:    u.autoAgentName("shell"),
		Kind:    model.PaneWorker,
		Backend: "pty",
		Cwd:     cwd,
		Layer:   u.activeLayer,
		Cols:    80,
		Rows:    24,
	}
	var pane model.Pane
	if !u.callWithin(10e9, "pane.spawn", shell, &pane) {
		return
	}
	_, _ = u.refresh()
	u.selectUtility(pane.ID)
}

// putUtility toggles a pane between the canvas and the background.
func (u *ui) putUtility(id string) {
	if u.canvas.utility == id {
		u.closeUtility()
		u.status = "已移出工具槽；进程继续在左栏运行"
		return
	}
	if id == u.canvas.main {
		if u.canvas.utility == "" {
			u.status = "它已经在主画布；先打开工具槽再交换"
			return
		}
		u.canvas.utility, u.canvas.main = u.canvas.main, u.canvas.utility
		u.area = focusMain
		u.resize()
		return
	}
	u.selectUtility(id)
}

// railOrder is the stable order used by the rail and Ctrl-a 1..9: the
// foreground group first, then workers, each in creation order.
func (u *ui) railOrder() []model.Pane {
	foreground, workers := []model.Pane{}, []model.Pane{}
	for _, p := range u.snapshot.Panes {
		if !matchesFilter(p, u.rail.filter) {
			continue
		}
		if p.Kind == model.PaneForeground {
			foreground = append(foreground, p)
		} else {
			workers = append(workers, p)
		}
	}
	return append(foreground, workers...)
}

func matchesFilter(p model.Pane, filter string) bool {
	if filter == "" {
		return true
	}
	filter = strings.ToLower(filter)
	return strings.Contains(strings.ToLower(p.Name), filter) ||
		strings.Contains(strings.ToLower(p.ID), filter) ||
		strings.Contains(strings.ToLower(p.Backend), filter)
}

func (u *ui) nextPane(delta int) {
	panes := u.railOrder()
	if len(panes) == 0 {
		return
	}
	index := 0
	for i, p := range panes {
		if p.ID == u.canvas.main {
			index = i
			break
		}
	}
	index = (index + delta + len(panes)) % len(panes)
	u.selectMain(panes[index].ID)
}

func (u *ui) jumpToNth(n int) {
	panes := u.railOrder()
	if n < 1 || n > len(panes) {
		return
	}
	u.selectMain(panes[n-1].ID)
}

// permitsFor returns the pending requests of one pane in stable order.
func (u *ui) permitsFor(paneID string) []model.Permission {
	out := []model.Permission{}
	for _, p := range u.snapshot.Permissions {
		if p.PaneID == paneID {
			out = append(out, p)
		}
	}
	return out
}

func (u *ui) queuedForPane(paneID string) []model.Task {
	tasks := []model.Task{}
	for _, t := range u.snapshot.Tasks {
		if t.To == paneID && t.State == "queued" {
			tasks = append(tasks, t)
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].CreatedAt.Equal(tasks[j].CreatedAt) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})
	return tasks
}

// markFocusedSeen clears unread only for the pane the human actually opened.
// Visibility alone is not consent: panes merely drawn on the canvas do not
// clear their inbox entries.
func (u *ui) markFocusedSeen(id string) {
	if id == "" {
		return
	}
	needsFocus := u.lastFocusSent != id
	if !needsFocus {
		for _, t := range u.snapshot.Tasks {
			if t.To == id && !t.Seen && (t.State == "completed" || t.State == "failed" || t.State == "unknown") {
				needsFocus = true
				break
			}
		}
	}
	if !needsFocus {
		for _, r := range u.snapshot.Reports {
			if r.To == id && !r.Seen {
				needsFocus = true
				break
			}
		}
	}
	if !needsFocus {
		return
	}
	if !u.call("ui.focus", map[string]string{"id": id}, nil) {
		return
	}
	u.lastFocusSent = id
	// ui.focus already marks these seen server-side in the same commit.
	// Mirror that locally instead of issuing one RPC per item: each call
	// rewrites the whole state and bumps UpdatedAt, which reorders the task
	// list and stalls input on a large backlog.
	for i := range u.snapshot.Tasks {
		t := &u.snapshot.Tasks[i]
		if t.To == id && (t.State == "completed" || t.State == "failed" || t.State == "unknown") {
			t.Seen = true
		}
	}
	for i := range u.snapshot.Reports {
		if u.snapshot.Reports[i].To == id {
			u.snapshot.Reports[i].Seen = true
		}
	}
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
	visibility := "后台"
	switch p.ID {
	case u.canvas.main:
		visibility = "主画布"
	case u.canvas.utility:
		visibility = "工具槽"
	}
	activity := "尚无输出"
	if !p.LastActivity.IsZero() {
		activity = p.LastActivity.Local().Format("15:04:05")
	}
	queue := ""
	if n := len(u.queuedForPane(p.ID)); n > 0 {
		queue = fmt.Sprintf(" · ⏸%d", n)
	}
	return fmt.Sprintf("%s · %s · %s · 未读 %d %s%s · %s · %s", p.Name, p.State, visibility, unread, strings.Join(attention, ","), queue, truncateColumns(p.LastOutput, 80), activity)
}

func lastActivity(p model.Pane) time.Time {
	if !p.LastActivity.IsZero() {
		return p.LastActivity
	}
	return p.CreatedAt
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

// openPaneFromOverlay closes every full-screen surface before replacing the
// main canvas, matching the old "open never leaves other views behind" rule.
func (u *ui) openPaneFromOverlay(id string) {
	u.graph = nil
	u.panel = nil
	u.taskView = false
	u.actions = nil
	u.selectMain(id)
}
