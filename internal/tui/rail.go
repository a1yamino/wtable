package tui

import (
	"fmt"

	"github.com/gdamore/tcell/v3"
	"github.com/gdamore/tcell/v3/color"
	"wtable/internal/model"
)

type railState struct {
	sel        string // "i:<inbox id>" or "p:<pane id>"
	filter     string
	scroll     int
	dismissed  map[string]bool
	pendingDel string
}

type railRow struct {
	header string
	kind   string // inbox, pane
	id     string
	item   model.InboxItem
	pane   model.Pane
}

func (u *ui) inboxItems() []model.InboxItem {
	items := []model.InboxItem{}
	for _, item := range u.snapshot.Inbox {
		if u.rail.dismissed[item.ID] {
			continue
		}
		items = append(items, item)
	}
	return items
}

func (u *ui) railRows() []railRow {
	rows := []railRow{}
	inbox := u.inboxItems()
	if len(inbox) > 0 {
		rows = append(rows, railRow{header: "需要你"})
		for _, item := range inbox {
			rows = append(rows, railRow{kind: "inbox", id: item.ID, item: item})
		}
	}
	foreground, workers := []railRow{}, []railRow{}
	for _, p := range u.railOrder() {
		row := railRow{kind: "pane", id: p.ID, pane: p}
		if p.Kind == model.PaneForeground {
			foreground = append(foreground, row)
		} else {
			workers = append(workers, row)
		}
	}
	if len(foreground) > 0 {
		rows = append(rows, railRow{header: "前台"})
		rows = append(rows, foreground...)
	}
	if len(workers) > 0 {
		rows = append(rows, railRow{header: "Workers"})
		rows = append(rows, workers...)
	}
	return rows
}

func (u *ui) selectableRows() []railRow {
	rows := []railRow{}
	for _, row := range u.railRows() {
		if row.kind != "" {
			rows = append(rows, row)
		}
	}
	return rows
}

func (u *ui) ensureRailSelection() {
	rows := u.selectableRows()
	if len(rows) == 0 {
		u.rail.sel = ""
		return
	}
	for _, row := range rows {
		if railKeyOf(row) == u.rail.sel {
			return
		}
	}
	u.rail.sel = railKeyOf(rows[0])
}

func (u *ui) railSelectFirst() {
	rows := u.selectableRows()
	if len(rows) == 0 {
		u.rail.sel = ""
		return
	}
	u.rail.sel = railKeyOf(rows[0])
}

func railKeyOf(row railRow) string {
	if row.kind == "inbox" {
		return "i:" + row.id
	}
	return "p:" + row.id
}

func (u *ui) railSelectionIndex(rows []railRow) int {
	for i, row := range rows {
		if railKeyOf(row) == u.rail.sel {
			return i
		}
	}
	return 0
}

func (u *ui) focusRail() {
	u.ensureRailSelection()
	u.area = focusRail
	if narrowRail(u.canvas.railW) {
		u.canvas.drawer = true
	}
	u.resize()
}

func narrowRail(width int) bool { return width <= 4 }

func (u *ui) railKey(e *tcell.EventKey) {
	rows := u.selectableRows()
	if e.Key() == tcell.KeyEscape {
		u.rail.pendingDel = ""
		u.area = focusMain
		if u.canvas.drawer {
			u.canvas.drawer = false
			u.resize()
		}
		return
	}
	if u.rail.pendingDel != "" {
		if e.Str() == "y" {
			id := u.rail.pendingDel
			u.rail.pendingDel = ""
			u.removePaneID(id)
		} else {
			u.rail.pendingDel = ""
			u.status = "已取消，未移除"
		}
		return
	}
	if len(rows) == 0 {
		return
	}
	index := u.railSelectionIndex(rows)
	switch e.Key() {
	case tcell.KeyUp:
		index = max(0, index-1)
		u.rail.sel = railKeyOf(rows[index])
		return
	case tcell.KeyDown:
		index = min(len(rows)-1, index+1)
		u.rail.sel = railKeyOf(rows[index])
		return
	case tcell.KeyHome:
		u.rail.sel = railKeyOf(rows[0])
		return
	case tcell.KeyEnd:
		u.rail.sel = railKeyOf(rows[len(rows)-1])
		return
	case tcell.KeyEnter:
		u.activateRailRow(rows[index])
		return
	}
	row := rows[index]
	if e.Key() == tcell.KeyRune {
		switch e.Str() {
		case "j":
			u.rail.sel = railKeyOf(rows[min(len(rows)-1, index+1)])
			return
		case "k":
			u.rail.sel = railKeyOf(rows[max(0, index-1)])
			return
		case " ":
			u.openRailActions(row)
			return
		case "d":
			if row.kind == "inbox" {
				if u.rail.dismissed == nil {
					u.rail.dismissed = map[string]bool{}
				}
				u.rail.dismissed[row.id] = true
				u.ensureRailSelection()
				u.status = "已暂缓；下次打开时仍在收件箱"
				return
			}
		case "n":
			if u.foregroundPane() == nil {
				u.openCreate(model.PaneForeground)
			} else {
				u.openCreate(model.PaneWorker)
			}
			return
		case "e":
			if row.kind == "pane" {
				u.editPane(row.pane)
			}
			return
		case "p":
			if row.kind == "pane" {
				u.putUtility(row.pane.ID)
			}
			return
		case "x":
			if row.kind == "pane" {
				u.rail.pendingDel = row.pane.ID
				u.status = "要移除 " + row.pane.Name + " 吗？按 y 确认，其他键取消"
			}
			return
		case "/":
			u.openInput("rail-filter", u.rail.filter)
			return
		}
	}
}

func (u *ui) foregroundPane() *model.Pane {
	for i := range u.snapshot.Panes {
		if u.snapshot.Panes[i].Kind == model.PaneForeground {
			return &u.snapshot.Panes[i]
		}
	}
	return nil
}

func (u *ui) removePaneID(id string) {
	p := u.paneByID(id)
	if p == nil {
		return
	}
	name := p.Name
	if u.call("pane.remove", map[string]string{"id": id}, nil) {
		if u.canvas.utility == id {
			u.canvas.utility = ""
		}
		u.status = "已移除 " + name + "；任务与报告历史保留"
		_, _ = u.refresh()
		u.resize()
	}
}

func (u *ui) editPane(p model.Pane) {
	u.ensurePanel(0)
	for i, other := range u.snapshot.Panes {
		if other.ID == p.ID {
			u.panel.selected = i
		}
	}
	if len(u.panel.catalog.Presets) == 0 {
		u.panel.afterLoad = "edit"
	}
	u.startAgentForm(true)
}

func (u *ui) activateRailRow(row railRow) {
	if row.kind == "pane" {
		u.selectMain(row.pane.ID)
		return
	}
	item := row.item
	switch item.Kind {
	case "permission":
		u.selectMain(item.PaneID)
		u.focusPermission(item.PermissionID)
	case "task", "delivery":
		u.selectMain(item.PaneID)
		u.openTasks()
		u.readTask(item.TaskID)
	case "report":
		u.openTasks()
		u.reportView = true
		var report model.Report
		if u.call("report.get", map[string]string{"id": item.ReportID}, &report) {
			u.reportDetail = &report
			u.call("report.seen", map[string]string{"id": report.ID}, nil)
			u.taskScroll = 0
		}
	default:
		u.selectMain(item.PaneID)
	}
}

func (u *ui) openRailActions(row railRow) {
	if row.kind == "pane" {
		p := row.pane
		u.openPaneActions(&p)
		return
	}
	item := row.item
	items := []actionItem{
		{label: "打开", hint: item.Title, run: func() { u.activateRailRow(row) }},
		{label: "稍后处理", hint: "本次会话内先隐藏", run: func() {
			if u.rail.dismissed == nil {
				u.rail.dismissed = map[string]bool{}
			}
			u.rail.dismissed[item.ID] = true
			u.ensureRailSelection()
		}},
		{label: "复制标题", run: func() { u.copyText(item.Title, "已复制") }},
	}
	u.showActions(item.Title, "inbox:"+item.ID, items)
}

func (u *ui) drawRail(r rect) {
	style := tcell.StyleDefault.Background(color.NewHexColor(0x151d27)).Foreground(color.NewHexColor(0xc7d3df))
	dim := style.Foreground(color.NewHexColor(0x7d8b9a))
	selStyle := tcell.StyleDefault.Background(color.NewHexColor(0x2b4a5e)).Foreground(color.White).Bold(true)
	for y := r.y; y < r.y+r.h; y++ {
		for x := r.x; x < r.x+r.w; x++ {
			u.screen.Put(x, y, " ", style)
		}
	}
	u.railHits = map[int]railRow{}
	rows := u.railRows()
	if narrowRail(r.w) {
		u.drawRailStrip(r, rows)
		return
	}
	selectable := u.selectableRows()
	selIndex := u.railSelectionIndex(selectable)
	// scroll and the visible slice index into rows (headers included), so the
	// selected row's position in rows is what must stay on screen.
	index := 0
	if len(selectable) > 0 {
		key := railKeyOf(selectable[selIndex])
		for i, row := range rows {
			if row.kind != "" && railKeyOf(row) == key {
				index = i
				break
			}
		}
	}
	if index < u.rail.scroll {
		u.rail.scroll = index
	}
	capacity := r.h
	if index >= u.rail.scroll+capacity {
		u.rail.scroll = index - capacity + 1
	}
	if u.rail.scroll > len(rows)-capacity {
		u.rail.scroll = max(0, len(rows)-capacity)
	}
	if u.rail.scroll < 0 {
		u.rail.scroll = 0
	}
	visible := rows[u.rail.scroll:min(len(rows), u.rail.scroll+capacity)]
	y := r.y
	for _, row := range visible {
		if row.kind == "" {
			label := row.header
			if row.header == "需要你" {
				label = fmt.Sprintf("%s (%d)", row.header, len(u.inboxItems()))
			}
			u.line(r.x, y, r.w, " "+label, dim.Bold(true))
			y++
			continue
		}
		key := railKeyOf(row)
		lineStyle := style
		marker := " "
		if key == u.rail.sel {
			lineStyle = selStyle
			marker = "›"
		}
		text := ""
		if row.kind == "inbox" {
			glyph := "!"
			if row.item.Severity == model.InboxBlocking {
				glyph = glyphApproval
			} else if row.item.Severity == model.InboxInfo {
				glyph = "·"
			}
			if key == u.rail.sel {
				text = fmt.Sprintf("%s %s %s · %s", marker, glyph, truncateColumns(row.item.Title, max(1, r.w-12)), relativeAge(row.item.CreatedAt))
			} else {
				text = fmt.Sprintf("%s %s %s %s", marker, glyph, truncateColumns(row.item.Title, max(1, r.w-10)), relativeAge(row.item.CreatedAt))
			}
			if row.item.Severity == model.InboxBlocking && key != u.rail.sel {
				lineStyle = lineStyle.Foreground(color.NewHexColor(0xffd7a8)).Bold(true)
			}
			u.line(r.x, y, r.w, text, lineStyle)
		} else {
			p := row.pane
			glyph, signal := u.railSignal(p)
			canvasMark := " "
			switch p.ID {
			case u.canvas.main:
				canvasMark = "▌"
			case u.canvas.utility:
				canvasMark = "▐"
			}
			text = fmt.Sprintf("%s%s %s %s", marker, canvasMark, glyph, truncateColumns(p.Name, max(1, r.w-8)))
			if key == u.rail.sel {
				detail := p.Backend + " · " + relativeAge(lastActivity(p))
				if n := len(u.queuedForPane(p.ID)); n > 0 {
					detail += fmt.Sprintf(" · %s%d", glyphQueue, n)
				}
				text = fmt.Sprintf("%s%s %s %s · %s", marker, canvasMark, glyph, truncateColumns(p.Name, max(1, r.w-20)), detail)
				if signal == "等待审批" && lineStyle == style {
					lineStyle = lineStyle.Foreground(color.NewHexColor(0xffd7a8))
				}
			}
			_ = signal
			u.line(r.x, y, r.w, text, lineStyle)
		}
		u.railHits[y] = row
		y++
	}
	if len(rows) == 0 {
		u.line(r.x, y, r.w, " 还没有 agent", dim)
	}
	if u.rail.filter != "" {
		u.line(r.x, r.y+r.h-1, r.w, " /"+truncateColumns(u.rail.filter, max(1, r.w-4)), dim)
	}
}

// drawRailStrip is the narrow-terminal form: four columns of glyphs, still
// permanent, never hidden. Ctrl-a a expands it into a drawer.
func (u *ui) drawRailStrip(r rect, rows []railRow) {
	style := tcell.StyleDefault.Background(color.NewHexColor(0x151d27))
	y := r.y
	// One glyph per selectable row, inbox entries first, matching the wide
	// rail. Blocking permissions collapse into the leading ⏳ row.
	glyphRows := []railRow{}
	var blockingItem model.InboxItem
	hasBlocking := false
	for _, row := range rows {
		if row.kind == "" {
			continue
		}
		if row.kind == "inbox" && row.item.Severity == model.InboxBlocking {
			if !hasBlocking {
				blockingItem = row.item
				hasBlocking = true
			}
			continue
		}
		glyphRows = append(glyphRows, row)
	}
	capacity := r.h
	if hasBlocking {
		capacity--
	}
	capacity = max(0, capacity)
	start := max(0, min(u.rail.scroll, len(glyphRows)-capacity))
	if hasBlocking && y < r.y+r.h {
		u.line(r.x, y, r.w, glyphApproval, style.Foreground(color.NewHexColor(0xffd7a8)).Bold(true))
		u.railHits[y] = railRow{kind: "inbox", id: blockingItem.ID, item: blockingItem}
		y++
	}
	for _, row := range glyphRows[start:min(len(glyphRows), start+capacity)] {
		if y >= r.y+r.h {
			break
		}
		lineStyle := style.Foreground(color.White)
		text := ""
		if row.kind == "inbox" {
			glyph := "!"
			if row.item.Severity == model.InboxInfo {
				glyph = "·"
			}
			text = " " + glyph
		} else {
			glyph, _ := u.railSignal(row.pane)
			text = " " + glyph
			switch row.pane.ID {
			case u.canvas.main:
				lineStyle = lineStyle.Bold(true)
				text = "▌" + glyph
			case u.canvas.utility:
				lineStyle = lineStyle.Foreground(color.NewHexColor(0x8fd0ff))
			}
		}
		u.line(r.x, y, r.w, text, lineStyle)
		u.railHits[y] = row
		y++
	}
}

// railRowAt maps a click to a rail row (headers are not clickable).
func (u *ui) railRowAt(y int) (railRow, bool) {
	row, ok := u.railHits[y]
	return row, ok
}
