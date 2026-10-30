package tui

import (
	"math"

	"github.com/gdamore/tcell/v3"
)

type divider struct {
	rect
	vertical bool
	index    int
}

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

func equalCuts(n int) []float64 {
	cuts := make([]float64, n+1)
	for i := range cuts {
		cuts[i] = float64(i) / float64(n)
	}
	return cuts
}

// Cuts describe proportions of usable space, excluding one-cell dividers.
// Small terminals relax minimum sizes rather than letting panes overlap.
func axisPositions(total int, cuts []float64, minimum int) ([]int, int) {
	n := len(cuts) - 1
	gap := 1
	if total < 2*n-1 {
		gap = 0
	}
	usable := max(0, total-(n-1)*gap)
	minimum = min(minimum, usable/n)
	positions := make([]int, n+1)
	positions[n] = usable
	for i := 1; i < n; i++ {
		positions[i] = max(positions[i-1]+minimum, min(usable-(n-i)*minimum, int(math.Round(cuts[i]*float64(usable)))))
	}
	return positions, gap
}

func (u *ui) mouse(e *tcell.EventMouse) {
	x, y := e.Position()
	down := e.Buttons()&tcell.Button1 != 0
	wasDown := u.mouseDown
	pressed := down && !wasDown
	u.mouseDown = down
	// Mouse input must never change the target of an open prompt or approve it.
	if u.help || u.mode != "" || u.pendingPaste != "" || len(u.snapshot.Permissions) > 0 || u.pasting {
		u.drag = nil
		u.selection = nil
		return
	}
	if u.taskView {
		u.drag = nil
		u.selection = nil
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
			if id := u.taskRows[y]; id != "" && x >= left && x < left+width {
				u.readTask(id)
			}
		}
		return
	}
	if e.Buttons()&(tcell.WheelUp|tcell.WheelDown) != 0 {
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
		d := *u.drag
		cuts, coordinate, minimum := u.rowCuts, y, 3
		if d.vertical {
			cuts, coordinate, minimum = u.colCuts, x, 8
		}
		// A divider spans the other axis; obtain the current layout extent.
		w, h := u.screen.Size()
		if u.tasks {
			h -= min(6, max(0, h/3))
		}
		total := h - 1
		if d.vertical {
			total = w
		}
		positions, gap := axisPositions(total, cuts, minimum)
		n := len(cuts) - 1
		usable := positions[n]
		if gap == 0 || usable == 0 {
			return
		}
		minimum = min(minimum, usable/n)
		position := max(positions[d.index-1]+minimum, min(positions[d.index+1]-minimum, coordinate-(d.index-1)))
		cuts[d.index] = float64(position) / float64(usable)
		u.resize()
		return
	}
	if !pressed {
		return
	}
	u.prefix = false
	for _, d := range u.dividers {
		if d.contains(x, y) {
			copy := d
			u.drag = &copy
			return
		}
	}
	for i, p := range u.snapshot.Panes {
		if r, ok := u.boxes[p.ID]; ok && r.contains(x, y) {
			if previous := u.current(); previous != nil && u.focus != i {
				u.offsets[previous.ID] = 0
				u.copyMode = false
			}
			u.focus = i
			if restartable(p.State) && y == r.y+1 {
				u.restartCurrent()
				return
			}
			u.startSelection(p.ID, r, x, y)
			return
		}
	}
}
