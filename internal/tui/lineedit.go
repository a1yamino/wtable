package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v3"
	"github.com/rivo/uniseg"
)

// lineEdit is a zero-value-ready editor. cursor is a byte offset on a grapheme
// boundary; histories are kept separately for each input kind.
type lineEdit struct {
	text         string
	cursor       int
	kind         string
	history      map[string][]string
	historyIndex int
	browsing     bool
	draft        string
	draftCursor  int
}

func (e *lineEdit) Set(s string) {
	e.text, e.cursor = s, len(s)
	e.browsing = false
}

func (e *lineEdit) Text() string { return e.text }

func (e *lineEdit) Display() string {
	return e.text[:e.cursor] + "▏" + e.text[e.cursor:]
}

// Begin starts a new editing session without discarding any input history.
func (e *lineEdit) Begin(kind, initial string) {
	e.kind = kind
	e.Set(initial)
}

func (e *lineEdit) Insert(s string) {
	if s == "" {
		return
	}
	pos := e.cursor + len(s)
	e.text = e.text[:e.cursor] + s + e.text[e.cursor:]
	e.cursor = e.boundaryAtOrAfter(pos)
	e.browsing = false
}

// Remember stores nonempty input, suppressing consecutive duplicates.
func (e *lineEdit) Remember(kind string) {
	if e.text == "" {
		return
	}
	if e.history == nil {
		e.history = make(map[string][]string)
	}
	items := e.history[kind]
	if len(items) == 0 || items[len(items)-1] != e.text {
		items = append(items, e.text)
		if len(items) > 100 {
			items = append([]string(nil), items[len(items)-100:]...)
		}
		e.history[kind] = items
	}
	if kind == e.kind {
		e.browsing = false
	}
}

// Handle returns true only for keys consumed by the editor. Submission and
// cancellation remain the caller's responsibility.
func (e *lineEdit) Handle(event *tcell.EventKey) bool {
	key := event.Key()
	if key == tcell.KeyRune && event.Modifiers()&tcell.ModCtrl != 0 {
		switch strings.ToLower(event.Str()) {
		case "a":
			key = tcell.KeyCtrlA
		case "e":
			key = tcell.KeyCtrlE
		case "u":
			key = tcell.KeyCtrlU
		case "w":
			key = tcell.KeyCtrlW
		}
	}
	switch key {
	case tcell.KeyLeft:
		e.cursor = e.previousBoundary(e.cursor)
	case tcell.KeyRight:
		e.cursor = e.nextBoundary(e.cursor)
	case tcell.KeyHome, tcell.KeyCtrlA:
		e.cursor = e.lineStart()
	case tcell.KeyEnd, tcell.KeyCtrlE:
		e.cursor = e.lineEnd()
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		e.remove(e.previousBoundary(e.cursor), e.cursor)
	case tcell.KeyDelete:
		e.remove(e.cursor, e.nextBoundary(e.cursor))
	case tcell.KeyCtrlU:
		e.remove(e.lineStart(), e.cursor)
	case tcell.KeyCtrlW:
		start := e.cursor
		for start > 0 {
			previous := e.previousBoundary(start)
			if !clusterSpace(e.text[previous:start]) {
				break
			}
			start = previous
		}
		for start > 0 {
			previous := e.previousBoundary(start)
			if clusterSpace(e.text[previous:start]) {
				break
			}
			start = previous
		}
		e.remove(start, e.cursor)
	case tcell.KeyUp:
		e.moveHistory(-1)
	case tcell.KeyDown:
		e.moveHistory(1)
	case tcell.KeyRune:
		if event.Modifiers()&(tcell.ModCtrl|tcell.ModAlt|tcell.ModMeta) != 0 {
			return false
		}
		e.Insert(event.Str())
	default:
		return false
	}
	return true
}

func (e *lineEdit) remove(start, end int) {
	if start == end {
		return
	}
	e.text = e.text[:start] + e.text[end:]
	e.cursor = e.boundaryAtOrAfter(start)
	e.browsing = false
}

func (e *lineEdit) boundaryAtOrAfter(pos int) int {
	if pos <= 0 {
		return 0
	}
	g := uniseg.NewGraphemes(e.text)
	for g.Next() {
		_, end := g.Positions()
		if end >= pos {
			return end
		}
	}
	return len(e.text)
}

func (e *lineEdit) previousBoundary(pos int) int {
	g := uniseg.NewGraphemes(e.text)
	for g.Next() {
		start, end := g.Positions()
		if end >= pos {
			return start
		}
	}
	return 0
}

func (e *lineEdit) nextBoundary(pos int) int {
	g := uniseg.NewGraphemes(e.text)
	for g.Next() {
		_, end := g.Positions()
		if end > pos {
			return end
		}
	}
	return len(e.text)
}

func (e *lineEdit) lineStart() int {
	start := 0
	g := uniseg.NewGraphemes(e.text)
	for g.Next() {
		_, end := g.Positions()
		if end > e.cursor {
			break
		}
		if strings.ContainsAny(g.Str(), "\r\n") {
			start = end
		}
	}
	return start
}

func (e *lineEdit) lineEnd() int {
	g := uniseg.NewGraphemes(e.text)
	for g.Next() {
		start, _ := g.Positions()
		if start >= e.cursor && strings.ContainsAny(g.Str(), "\r\n") {
			return start
		}
	}
	return len(e.text)
}

func clusterSpace(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsSpace(r)
}

func (e *lineEdit) moveHistory(delta int) {
	items := e.history[e.kind]
	if len(items) == 0 {
		return
	}
	if !e.browsing {
		if delta > 0 {
			return
		}
		e.draft, e.draftCursor = e.text, e.cursor
		e.historyIndex = len(items)
		e.browsing = true
	}
	next := e.historyIndex + delta
	if next < 0 || next > len(items) {
		return
	}
	e.historyIndex = next
	if next == len(items) {
		e.text, e.cursor = e.draft, e.draftCursor
		e.browsing = false
		return
	}
	e.text = items[next]
	e.cursor = len(e.text)
}
