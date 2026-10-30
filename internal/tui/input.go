package tui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v3"
)

// encodeKey uses the xterm VT input protocol. The inner emulator currently
// advertises that protocol; no output escape sequences are parsed here.
func encodeKey(e *tcell.EventKey, appCursor bool) string {
	k, m := e.Key(), e.Modifiers()
	alt := m&tcell.ModAlt != 0
	prefix := ""
	if alt {
		prefix = "\x1b"
	}
	if k == tcell.KeyRune {
		s := e.Str()
		if m&tcell.ModCtrl != 0 && len(s) == 1 {
			c := strings.ToUpper(s)[0]
			if c >= '@' && c <= '_' {
				s = string([]byte{c & 31})
			} else if c == ' ' || c == '2' {
				s = "\x00"
			} else if c == '?' || c == '8' {
				s = "\x7f"
			}
		}
		return prefix + s
	}
	if k >= tcell.KeyCtrlA && k <= tcell.KeyCtrlZ {
		return prefix + string([]byte{byte(k - tcell.KeyCtrlA + 1)})
	}
	if k == tcell.KeyBacktab || (k == tcell.KeyTab && m&tcell.ModShift != 0) {
		return "\x1b[Z"
	}
	if k >= tcell.KeyNUL && k <= tcell.KeyUS {
		return prefix + string([]byte{byte(k)})
	}
	if k == tcell.KeyBackspace2 {
		return prefix + "\x7f"
	}
	modifier := 1
	if m&tcell.ModShift != 0 {
		modifier++
	}
	if alt {
		modifier += 2
	}
	if m&tcell.ModCtrl != 0 {
		modifier += 4
	}
	final := map[tcell.Key]string{tcell.KeyUp: "A", tcell.KeyDown: "B", tcell.KeyRight: "C", tcell.KeyLeft: "D", tcell.KeyHome: "H", tcell.KeyEnd: "F"}[k]
	if final != "" {
		if modifier > 1 {
			return fmt.Sprintf("\x1b[1;%d%s", modifier, final)
		}
		if appCursor {
			return "\x1bO" + final
		}
		return "\x1b[" + final
	}
	if k >= tcell.KeyF1 && k <= tcell.KeyF4 {
		final := string(rune('P' + k - tcell.KeyF1))
		if modifier > 1 {
			return fmt.Sprintf("\x1b[1;%d%s", modifier, final)
		}
		return "\x1bO" + final
	}
	number := map[tcell.Key]int{tcell.KeyInsert: 2, tcell.KeyDelete: 3, tcell.KeyPgUp: 5, tcell.KeyPgDn: 6, tcell.KeyF5: 15, tcell.KeyF6: 17, tcell.KeyF7: 18, tcell.KeyF8: 19, tcell.KeyF9: 20, tcell.KeyF10: 21, tcell.KeyF11: 23, tcell.KeyF12: 24}[k]
	if number > 0 {
		if modifier > 1 {
			return fmt.Sprintf("\x1b[%d;%d~", number, modifier)
		}
		return fmt.Sprintf("\x1b[%d~", number)
	}
	return ""
}
