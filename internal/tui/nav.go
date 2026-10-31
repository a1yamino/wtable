package tui

import (
	"sort"

	"github.com/gdamore/tcell/v3"
)

// Arrow-first navigation. Plain keys always belong to the focused pane; the
// workbench only claims Alt combinations, so nothing an agent's TUI needs is
// stolen. Ctrl-a stays available as the legacy prefix.

// visibleIDs returns the on-screen panes in reading order (top-left first) so
// that Alt+1..9 is predictable.
func (u *ui) visibleIDs() []string {
	ids := make([]string, 0, len(u.boxes))
	for id := range u.boxes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := u.boxes[ids[i]], u.boxes[ids[j]]
		if a.y != b.y {
			return a.y < b.y
		}
		if a.x != b.x {
			return a.x < b.x
		}
		return ids[i] < ids[j]
	})
	return ids
}

// paneIndexOf returns the 1-based Alt+N label for a pane, or 0 when hidden.
func (u *ui) paneIndexOf(id string) int {
	for i, candidate := range u.visibleIDs() {
		if candidate == id {
			return i + 1
		}
	}
	return 0
}

func (u *ui) navKey(e *tcell.EventKey) bool {
	if e.Modifiers()&tcell.ModAlt == 0 {
		return false
	}
	if u.zoom {
		// In single-pane view the only useful moves are leaving it or switching.
		if e.Str() == "z" {
			u.toggleZoom()
			return true
		}
	}
	switch e.Key() {
	case tcell.KeyLeft:
		return u.focusDir(-1, 0)
	case tcell.KeyRight:
		return u.focusDir(1, 0)
	case tcell.KeyUp:
		return u.focusDir(0, -1)
	case tcell.KeyDown:
		return u.focusDir(0, 1)
	}
	s := e.Str()
	switch s {
	case "h":
		return u.focusDir(-1, 0)
	case "l":
		return u.focusDir(1, 0)
	case "k":
		return u.focusDir(0, -1)
	case "j":
		return u.focusDir(0, 1)
	case "z":
		u.toggleZoom()
		return true
	}
	if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
		return u.focusNth(int(s[0] - '1'))
	}
	return false
}

func rectCenter(r rect) (int, int) { return r.x + r.w/2, r.y + r.h/2 }

// focusDir moves focus to the nearest pane in the given direction, preferring
// alignment over raw distance so that a grid behaves like a spreadsheet.
func (u *ui) focusDir(dx, dy int) bool {
	p := u.current()
	if p == nil {
		return false
	}
	cur, ok := u.boxes[p.ID]
	if !ok {
		return false
	}
	cx, cy := rectCenter(cur)
	best, bestScore := "", 1<<30
	for id, r := range u.boxes {
		if id == p.ID {
			continue
		}
		x, y := rectCenter(r)
		var primary, perpendicular int
		if dx != 0 {
			delta := (x - cx) * dx
			if delta <= 0 {
				continue
			}
			primary, perpendicular = delta, abs(y-cy)
		} else {
			delta := (y - cy) * dy
			if delta <= 0 {
				continue
			}
			primary, perpendicular = delta, abs(x-cx)
		}
		// Two points of progress along the axis are worth one point of drift.
		if score := primary*2 + perpendicular; score < bestScore {
			best, bestScore = id, score
		}
	}
	if best == "" {
		return false
	}
	u.jumpTo(best)
	return true
}

func (u *ui) focusNth(n int) bool {
	ids := u.visibleIDs()
	if n < 0 || n >= len(ids) {
		return false
	}
	if p := u.current(); p != nil && p.ID == ids[n] {
		return false
	}
	u.jumpTo(ids[n])
	return true
}

// jumpTo focuses an on-screen pane without changing the layout.
func (u *ui) jumpTo(id string) {
	u.focusID(id)
	if p := u.current(); p != nil {
		u.offsets[p.ID] = 0
	}
	u.copyMode = false
	u.selection = nil
	u.resize()
}

func (u *ui) toggleZoom() {
	u.zoom = !u.zoom
	u.tab().zoom = u.zoom
	u.resize()
}

// stateGlyph is the one-character status badge shown before a pane name.
func stateGlyph(state, turnState string, attention []string) string {
	switch {
	case len(attention) > 0:
		return "⚠"
	case state == "failed":
		return "✕"
	case state == "exited" || state == "interrupted":
		return "○"
	case turnState == "running":
		return "◐"
	case state == "running":
		return "●"
	default:
		return "◌"
	}
}

func stateLabel(state, turnState string) string {
	if turnState != "" && turnState != "idle" {
		return state + "/" + turnState
	}
	return state
}

// statusHint is the persistent, mode-aware key hint on the right of the bar.
func (u *ui) statusHint() string {
	switch {
	case u.prefix:
		return "Ctrl-a 前缀：a 面板 G 图 t 任务 ? 帮助 d 离开"
	case u.copyMode:
		return "回看：↑↓ 滚动 PgUp/PgDn 翻页 y 复制 Esc 返回"
	case u.picker != nil:
		return "↑↓ 选择 · Enter 操作 · Esc 返回"
	case u.panel != nil:
		return "↑↓ 选择 · Enter 操作 · Tab 换页 · Esc 返回"
	case u.taskView:
		return "↑↓ 选择 Enter 详情 Esc 返回"
	case u.graph != nil:
		return "↑↓ 选择 · Enter 操作 · e 节点/边 · Esc 返回"
	default:
		return "Alt+←→ pane · Alt+1..9 直达 · Ctrl-a a 面板 · ? 帮助"
	}
}

// queuedTasks counts tasks the scheduler is still holding for one pane.
func (u *ui) queuedTasks(paneID string) int {
	count := 0
	for _, task := range u.snapshot.Tasks {
		if task.To == paneID && task.State == "queued" {
			count++
		}
	}
	return count
}

// queuedTotal is the queue depth across panes, for the status bar.
func (u *ui) queuedTotal() int {
	count := 0
	for _, task := range u.snapshot.Tasks {
		if task.State == "queued" {
			count++
		}
	}
	return count
}

// blockedDeliveries counts tasks whose write was refused or unconfirmed, so the
// bar can say "someone is stuck" without opening the task list.
func (u *ui) blockedDeliveries() int {
	count := 0
	for _, task := range u.snapshot.Tasks {
		if task.DeliveryError == "" {
			continue
		}
		if task.State == "queued" || task.State == "unknown" || task.DeliveryStatus == "write-unconfirmed" {
			count++
		}
	}
	return count
}
