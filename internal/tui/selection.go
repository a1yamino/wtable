package tui

import (
	"strings"

	"github.com/gdamore/tcell/v3"
)

type selectionCell struct {
	text  string
	style tcell.Style
	width int
}

type textSelection struct {
	paneID string
	area   rect
	cells  [][]selectionCell
	anchor int
	end    int
	moved  bool
}

// Capture the displayed cells so streaming output cannot move text under a drag.
// This also gives PTY and ACP panes the same selection behavior.
func (u *ui) startSelection(id string, r rect, x, y int) {
	area := rect{r.x, r.y + 1, r.w, r.h - 1}
	if area.w < 1 || area.h < 1 || !area.contains(x, y) {
		return
	}
	s := &textSelection{paneID: id, area: area, cells: make([][]selectionCell, area.h)}
	for row := range s.cells {
		s.cells[row] = make([]selectionCell, area.w)
		for col := 0; col < area.w; {
			text, style, width := u.screen.Get(area.x+col, area.y+row)
			width = max(1, width)
			if text == "" {
				text = " "
			}
			s.cells[row][col] = selectionCell{text, style, width}
			col += width
		}
	}
	s.anchor = s.position(x, y)
	s.end = s.anchor
	u.selection = s
}

func (s *textSelection) position(x, y int) int {
	return max(0, min(s.area.h-1, y-s.area.y))*s.area.w + max(0, min(s.area.w-1, x-s.area.x))
}

func (s *textSelection) update(x, y int) {
	s.end = s.position(x, y)
	s.moved = s.moved || s.end != s.anchor
}

func (s *textSelection) selected(x, y, width int) bool {
	start, end := min(s.anchor, s.end), max(s.anchor, s.end)
	position := y*s.area.w + x
	return s.moved && position <= end && position+width-1 >= start
}

func (s *textSelection) text() string {
	var lines []string
	start, end := min(s.anchor, s.end)/s.area.w, max(s.anchor, s.end)/s.area.w
	for y := start; y <= end; y++ {
		var line strings.Builder
		for x, c := range s.cells[y] {
			if c.width > 0 && s.selected(x, y, c.width) {
				line.WriteString(c.text)
			}
		}
		lines = append(lines, strings.TrimRight(line.String(), " "))
	}
	return strings.Join(lines, "\n")
}

func (u *ui) drawSelection() {
	s := u.selection
	if s == nil {
		return
	}
	for y, row := range s.cells {
		for x, c := range row {
			if c.width <= 0 {
				continue
			}
			style := c.style
			if s.selected(x, y, c.width) {
				style = style.Reverse(false).Foreground(tcell.ColorBlack).Background(tcell.NewHexColor(0xa6d8ea))
			}
			u.screen.Put(s.area.x+x, s.area.y+y, c.text, style)
		}
	}
	u.screen.HideCursor()
}
