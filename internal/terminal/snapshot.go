package terminal

import (
	"fmt"
	"strings"

	"wtable/internal/model"
)

// frameANSI redraws a detached frame into a fresh xterm-compatible screen.
// It intentionally renders the full visible grid and then restores cursor and
// terminal modes; callers may replay raw history before this snapshot.
func frameANSI(frame model.Frame) []byte {
	var out strings.Builder
	out.Grow(frame.Cols * frame.Rows * 2)
	if frame.Alternate {
		out.WriteString("\x1b[?1049h")
	} else {
		out.WriteString("\x1b[?1049l")
	}
	out.WriteString("\x1b[0m\x1b[H\x1b[2J")
	writeMode(&out, 2004, frame.BracketedPaste)
	writeMode(&out, 1, frame.AppCursor)
	for _, mode := range []int{1000, 1002, 1003} {
		writeMode(&out, mode, frame.MouseMode == mode)
	}
	writeMode(&out, 1006, frame.MouseSGR)

	lastStyle := ""
	for y, row := range frame.Cells {
		fmt.Fprintf(&out, "\x1b[%d;1H", y+1)
		for _, cell := range row {
			if cell.Width == 0 {
				continue
			}
			style := cellStyle(cell)
			if style != lastStyle {
				out.WriteString(style)
				lastStyle = style
			}
			if cell.Text == "" {
				out.WriteByte(' ')
			} else {
				out.WriteString(cell.Text)
			}
		}
	}

	out.WriteString("\x1b[0m")
	cursorY := clamp(frame.CursorY, 0, max(0, frame.Rows-1))
	cursorX := clamp(frame.CursorX, 0, max(0, frame.Cols-1))
	fmt.Fprintf(&out, "\x1b[%d;%dH", cursorY+1, cursorX+1)
	writeMode(&out, 25, frame.CursorVisible)
	return []byte(out.String())
}

func writeMode(out *strings.Builder, mode int, enabled bool) {
	if enabled {
		fmt.Fprintf(out, "\x1b[?%dh", mode)
	} else {
		fmt.Fprintf(out, "\x1b[?%dl", mode)
	}
}

func cellStyle(cell model.Cell) string {
	codes := []string{"0"}
	for _, attr := range []struct {
		flag uint32
		code string
	}{
		{model.Bold, "1"},
		{model.Dim, "2"},
		{model.Italic, "3"},
		{model.Underline, "4"},
		{model.Reverse, "7"},
		{model.Strike, "9"},
	} {
		if cell.Attr&attr.flag != 0 {
			codes = append(codes, attr.code)
		}
	}
	if cell.FG >= 0 {
		codes = append(codes, trueColor("38", cell.FG))
	}
	if cell.BG >= 0 {
		codes = append(codes, trueColor("48", cell.BG))
	}
	return "\x1b[" + strings.Join(codes, ";") + "m"
}

func trueColor(kind string, value int32) string {
	return fmt.Sprintf("%s;2;%d;%d;%d", kind, value>>16&0xff, value>>8&0xff, value&0xff)
}

func clamp(value, low, high int) int {
	return max(low, min(value, high))
}
