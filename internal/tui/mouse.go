package tui

import (
	"github.com/gdamore/tcell/v3"
)

type divider struct {
	rect
	vertical bool
	area     rect // the content area whose ratio this divider moves
}

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

func (u *ui) mouse(e *tcell.EventMouse) {
	x, y := e.Position()
	down := e.Buttons()&tcell.Button1 != 0
	wasDown := u.mouseDown
	pressed := down && !wasDown
	u.mouseDown = down
	// Mouse input must never change the target of an open prompt or approve it.
	if u.graph != nil || u.help || u.mode != "" || u.pendingPaste != "" || u.pasting ||
		u.palette != nil || u.create != nil || u.queueView != nil || u.workspacePicker != nil {
		u.drag = nil
		u.selection = nil
		return
	}
	if u.actions != nil {
		u.drag = nil
		u.selection = nil
		u.actionMouse(e, pressed)
		return
	}
	if u.panel != nil && !u.taskView {
		u.drag = nil
		u.selection = nil
		u.panelMouse(e, pressed)
		return
	}
	if u.taskView {
		u.drag = nil
		u.selection = nil
		u.taskViewMouse(e, pressed)
		return
	}
	if e.Buttons()&(tcell.WheelUp|tcell.WheelDown) != 0 {
		if u.railRect.contains(x, y) {
			u.railWheel(e)
			return
		}
		u.wheel(e)
		return
	}
	if !down {
		u.drag = nil
		if s := u.selection; s != nil && e.Buttons() == tcell.ButtonNone && wasDown {
			s.update(x, y)
			if s.moved {
				u.copyText(s.text(), "已复制选区 · Esc 或点击返回实时画面")
			} else {
				u.selection = nil
			}
		}
		return
	}
	if pressed {
		u.selection = nil
		u.status = ""
		u.draw()
	} else if u.selection != nil {
		u.selection.update(x, y)
		return
	}
	if u.drag != nil {
		u.dragDivider(*u.drag, x, y)
		return
	}
	if !pressed {
		return
	}
	u.prefix = false
	u.interruptConfirm = false

	// Rail: a click selects and opens; the rail itself gets keyboard focus via
	// Ctrl-a a, so there is no hidden double-click mode.
	if u.railRect.contains(x, y) {
		if row, ok := u.railRowAt(y); ok {
			u.rail.sel = railKeyOf(row)
			u.area = focusRail
			if narrowRail(u.canvas.railW) {
				u.canvas.drawer = true
			}
			u.resize()
			if row.kind == "pane" {
				u.selectMain(row.pane.ID)
			} else {
				u.activateRailRow(row)
			}
		}
		return
	}

	// Approval card: a click only focuses it. Decisions stay deliberate.
	if u.approvalRect.w > 0 && u.approvalRect.contains(x, y) {
		if len(u.permitsFor(u.canvas.main)) > 0 {
			u.area = focusApproval
			u.status = "审批卡已聚焦：1..9 选择 · r 拒绝 · Esc 收起"
			u.resize()
		}
		return
	}

	// Composer: click on the queue bar opens the queue, elsewhere focuses input.
	if u.composerRect.w > 0 && u.composerRect.contains(x, y) {
		u.area = focusMain
		if p := u.mainPane(); p != nil && y == u.composerRect.y && u.queueBarVisible(*p) && len(u.queuedForPane(p.ID)) > 0 {
			u.openQueue(p.ID)
		}
		return
	}

	for i := range u.dividers {
		if u.dividers[i].contains(x, y) {
			copy := u.dividers[i]
			u.drag = &copy
			return
		}
	}

	for _, p := range u.snapshot.Panes {
		r, ok := u.boxes[p.ID]
		if !ok || !r.contains(x, y) {
			continue
		}
		if p.ID == u.canvas.utility {
			u.area = focusUtility
			u.markFocusedSeen(p.ID)
		} else {
			u.area = focusMain
			if u.canvas.main != p.ID {
				u.selectMain(p.ID)
			} else {
				u.markFocusedSeen(p.ID)
			}
		}
		if restartable(p.State) {
			content := u.contentRect(p, r)
			if y == content.y {
				u.restartCurrent()
				return
			}
		}
		u.startSelection(p.ID, r, x, y)
		return
	}
}

func (u *ui) dragDivider(d divider, x, y int) {
	coordinate, total := x-d.area.x, d.area.w
	if !d.vertical {
		coordinate, total = y-d.area.y, d.area.h
	}
	usable := total - 1
	if usable < 2 {
		return
	}
	ratio := float64(max(1, min(usable-1, coordinate))) / float64(usable)
	ratio = max(.2, min(.8, ratio))
	u.canvas.ratio = ratio
	u.resize()
}

func (u *ui) railWheel(e *tcell.EventMouse) {
	rows := u.selectableRows()
	if len(rows) == 0 {
		return
	}
	u.ensureRailSelection()
	index := u.railSelectionIndex(rows)
	if e.Buttons()&tcell.WheelUp != 0 {
		index = max(0, index-1)
	} else {
		index = min(len(rows)-1, index+1)
	}
	u.rail.sel = railKeyOf(rows[index])
	u.area = focusRail
}

// taskViewMouse holds the task/report list's existing row and wheel behavior.
func (u *ui) taskViewMouse(e *tcell.EventMouse, pressed bool) {
	if u.reportView {
		delta := 0
		if e.Buttons()&tcell.WheelUp != 0 {
			delta = -1
		}
		if e.Buttons()&tcell.WheelDown != 0 {
			delta = 1
		}
		if u.reportDetail != nil {
			u.taskScroll = max(0, u.taskScroll+delta*3)
		} else {
			u.reportCursor = max(0, u.reportCursor+delta)
		}
		return
	}
	if u.taskScreen != nil {
		return
	}
	if e.Buttons()&tcell.WheelUp != 0 {
		if u.taskDetail != nil {
			u.taskScroll = max(0, u.taskScroll-3)
		} else {
			u.moveTask(-1)
		}
	} else if e.Buttons()&tcell.WheelDown != 0 {
		if u.taskDetail != nil {
			u.taskScroll += 3
		} else {
			u.moveTask(1)
		}
	} else if pressed && u.taskDetail == nil {
		w, _ := u.screen.Size()
		width := min(90, max(1, w-4))
		left := (w - width) / 2
		x, y := e.Position()
		if id := u.taskRows[y]; id != "" && x >= left && x < left+width {
			u.readTask(id)
		}
	}
}
