package tui

import (
	"fmt"

	"github.com/gdamore/tcell/v3"
)

func (u *ui) wheel(e *tcell.EventMouse) {
	x, y := e.Position()
	delta := 3
	button := 64
	if e.Buttons()&tcell.WheelDown != 0 {
		delta = -3
		button = 65
	}
	for i, p := range u.snapshot.Panes {
		r, ok := u.boxes[p.ID]
		if !ok || !r.contains(x, y) {
			continue
		}
		u.selection = nil
		u.drag = nil
		u.prefix = false
		u.focus = i
		if p.ID == u.canvas.utility {
			u.area = focusUtility
		} else {
			u.area = focusMain
		}
		content := u.contentRect(p, r)
		// Shift always requests workbench scrollback, even for mouse-aware apps.
		if p.Backend == "pty" && p.Frame != nil && p.Frame.MouseMode != 0 && u.offsets[p.ID] == 0 && e.Modifiers()&tcell.ModShift == 0 && content.contains(x, y) {
			col, row := x-content.x+1, y-content.y+1
			if e.Modifiers()&tcell.ModAlt != 0 {
				button += 8
			}
			if e.Modifiers()&tcell.ModCtrl != 0 {
				button += 16
			}
			if p.Frame.MouseSGR {
				u.send(fmt.Sprintf("\x1b[<%d;%d;%dM", button, col, row))
			} else {
				u.send(string([]byte{27, '[', 'M', byte(button + 32), byte(min(223, col) + 32), byte(min(223, row) + 32)}))
			}
			u.copyMode = false
			return
		}
		limit := 0
		if p.Frame != nil {
			limit = p.Frame.HistoryLines
		} else {
			limit = max(0, len(u.acpMessageLines(p, content.w))-content.h)
		}
		u.offsets[p.ID] = max(0, min(limit, u.offsets[p.ID]+delta))
		u.copyMode = u.offsets[p.ID] > 0
		_, _ = u.refresh()
		return
	}
}
