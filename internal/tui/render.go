package tui

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/gdamore/tcell/v3"
	"github.com/gdamore/tcell/v3/color"
	"github.com/rivo/uniseg"
	"wtable/internal/model"
)

var barStyle = tcell.StyleDefault.Background(color.NewHexColor(0x223344)).Foreground(color.White)
var focusStyle = tcell.StyleDefault.Background(color.NewHexColor(0x245c6a)).Foreground(color.White).Bold(true)
var idleTitleStyle = tcell.StyleDefault.Background(color.NewHexColor(0x1b2430)).Foreground(color.NewHexColor(0x9aa7b4))
var alertStyle = tcell.StyleDefault.Background(color.NewHexColor(0x4a3520)).Foreground(color.NewHexColor(0xffd7a8)).Bold(true)
var hintStyle = tcell.StyleDefault.Background(color.NewHexColor(0x223344)).Foreground(color.NewHexColor(0x8fa3b5))

func (u *ui) railWidth(w int) int {
	if w < 90 {
		if u.canvas.drawer && u.area == focusRail {
			return min(28, max(16, w/2))
		}
		return 4
	}
	if w >= 120 {
		return 28
	}
	return 26
}

func (u *ui) resize() {
	w, h := u.screen.Size()
	u.boxes = map[string]rect{}
	u.dividers = nil
	u.approvalRect = rect{}
	u.composerRect = rect{}
	u.mainRect = rect{}
	u.utilRect = rect{}
	u.railRect = rect{}
	if u.graph != nil || u.panel != nil || u.taskView {
		return
	}
	u.syncCanvas()
	if w < 8 || h < 4 {
		return
	}
	available := h - 1
	u.canvas.railW = u.railWidth(w)
	u.railRect = rect{0, 0, u.canvas.railW, available}
	content := rect{u.canvas.railW + 1, 0, w - u.canvas.railW - 1, available}
	if content.w < 4 {
		return
	}
	main := content
	var utility model.Pane
	if p := u.utilityPane(); p != nil {
		utility = *p
	}
	if utility.ID != "" && content.w >= 24 {
		usableW := content.w - 1
		usableH := content.h - 1
		if u.canvas.vertical {
			minimum := min(20, usableW/2)
			cut := int(float64(usableW)*u.canvas.ratio + .5)
			cut = max(minimum, min(usableW-minimum, cut))
			main = rect{content.x, content.y, cut, content.h}
			u.utilRect = rect{content.x + cut + 1, content.y, usableW - cut, content.h}
			u.dividers = append(u.dividers, divider{rect: rect{content.x + cut, content.y, 1, content.h}, vertical: true, area: content})
		} else {
			minimum := min(4, usableH/2)
			cut := int(float64(usableH)*u.canvas.ratio + .5)
			cut = max(minimum, min(usableH-minimum, cut))
			main = rect{content.x, content.y, content.w, cut}
			u.utilRect = rect{content.x, content.y + cut + 1, content.w, usableH - cut}
			u.dividers = append(u.dividers, divider{rect: rect{content.x, content.y + cut, content.w, 1}, vertical: false, area: content})
		}
	}
	u.mainRect = main
	if p := u.mainPane(); p != nil {
		u.boxes[p.ID] = main
		compH := u.composerHeight(*p)
		approvalH := u.approvalHeight()
		bottom := main.y + main.h
		top := main.y + 1 // keep the pane title readable on short terminals
		if approvalH > 0 {
			y := bottom - compH - approvalH
			if y < top {
				y = top
			}
			if h := bottom - compH - y; h > 0 {
				u.approvalRect = rect{main.x, y, main.w, h}
			}
		}
		if compH > 0 {
			y := bottom - compH
			if y < top {
				y = top
			}
			if h := bottom - y; h > 0 {
				u.composerRect = rect{main.x, y, main.w, h}
			}
		}
	}
	if utility.ID != "" {
		u.boxes[utility.ID] = u.utilRect
	}
	if s := u.selection; s != nil {
		r, ok := u.boxes[s.paneID]
		if !ok {
			u.selection = nil
		} else {
			area := rect{r.x, r.y + 1, r.w, r.h - 1}
			if p := u.paneByID(s.paneID); p != nil {
				area = u.contentRect(*p, r)
			}
			if area != s.area {
				u.selection = nil
			}
		}
	}
	for _, p := range u.snapshot.Panes {
		r, ok := u.boxes[p.ID]
		if !ok || p.Backend != "pty" {
			continue
		}
		content := u.contentRect(p, r)
		size := [2]int{min(240, max(1, content.w)), min(100, max(1, content.h))}
		if u.sizes[p.ID] != size && u.call("pane.resize", map[string]any{"id": p.ID, "cols": size[0], "rows": size[1]}, nil) {
			u.sizes[p.ID] = size
		}
	}
}

// contentRect is the area a pane's own surface (terminal or message stream)
// occupies: below the title, above the approval card and composer.
func (u *ui) contentRect(p model.Pane, r rect) rect {
	content := rect{r.x, r.y + 1, r.w, r.h - 1}
	if p.ID == u.canvas.main {
		content.h -= u.approvalHeight()
	}
	content.h -= u.composerHeight(p)
	if content.h < 1 {
		content.h = 1
	}
	return content
}

func (u *ui) composerRectFor(p model.Pane, r rect) rect {
	h := u.composerHeight(p)
	if h <= 0 {
		return rect{}
	}
	y := r.y + r.h - h
	if minY := r.y + 1; y < minY {
		y = minY
	}
	h = r.y + r.h - y
	if h < 1 || r.w < 2 {
		return rect{}
	}
	return rect{r.x, y, r.w, h}
}

func (u *ui) line(x, y, width int, text string, style tcell.Style) {
	if width <= 0 {
		return
	}
	for col := 0; col < width; col++ {
		u.screen.Put(x+col, y, " ", style)
	}
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		s := g.Str()
		sw := uniseg.StringWidth(s)
		if sw > width {
			break
		}
		u.screen.Put(x, y, s, style)
		x += sw
		width -= sw
	}
}

func (u *ui) draw() {
	w, h := u.screen.Size()
	if w <= 0 || h <= 0 {
		return
	}
	selectionKey := ""
	if s := u.selection; s != nil {
		selectionKey = fmt.Sprintf("%p:%d:%d:%t", s, s.anchor, s.end, s.moved)
	}
	key := fmt.Sprint(w, h, u.focus, u.prefix, u.help, u.copyMode, u.offsets, u.mode, u.editor.Display(), u.status, u.pendingPaste, selectionKey, u.taskView, u.taskCursor, u.taskDetail, u.taskScroll, u.reportView, u.reportCursor, u.reportDetail, u.taskScreen, u.taskDelivery, u.panelDrawKey(), u.overlayDrawKey(), u.acpDrawKey(), u.leftStatus(), u.boxes, u.canvas)
	if key == u.lastDrawKey && reflect.DeepEqual(u.snapshot, u.lastDrawSnapshot) {
		return
	}
	u.lastDrawKey = key
	u.lastDrawSnapshot = u.snapshot
	u.updateLegends()
	u.screen.Clear()
	u.screen.HideCursor()

	if u.taskView {
		u.drawTasks()
		u.drawStatusBar()
		u.drawTransientModals()
		u.screen.Show()
		return
	}
	if u.panel != nil {
		u.drawPanel()
		u.drawStatusBar()
		u.drawTransientModals()
		u.screen.Show()
		return
	}
	if u.graph != nil {
		u.drawGraph()
		u.drawStatusBar()
		u.drawTransientModals()
		u.screen.Show()
		return
	}

	u.drawRail(u.railRect)
	for y := 0; y < u.railRect.h; y++ {
		u.screen.Put(u.canvas.railW, y, "│", barStyle)
	}
	if p := u.mainPane(); p != nil {
		u.drawPane(*p, u.mainRect)
	}
	if p := u.utilityPane(); p != nil {
		u.drawPane(*p, u.utilRect)
	}
	for _, d := range u.dividers {
		glyph := "─"
		if d.vertical {
			glyph = "│"
		}
		for y := d.y; y < d.y+d.h; y++ {
			for x := d.x; x < d.x+d.w; x++ {
				u.screen.Put(x, y, glyph, barStyle)
			}
		}
	}
	if len(u.boxes) == 0 {
		u.line(u.canvas.railW+2, 1, max(0, w-u.canvas.railW-4), "还没有可见的 agent · Ctrl-a Space 打开命令面板 · Ctrl-a a 打开左栏", barStyle)
	}
	u.drawSelection()
	u.drawStatusBar()
	u.drawTransientModals()
	u.screen.Show()
}

func (u *ui) updateLegends() {
	if u.approvalCount() > 0 {
		u.legendOnce(glyphApproval)
	}
	if u.unreadTotal() > 0 {
		u.legendOnce(glyphUnread)
	}
	if u.queuedTotal() > 0 {
		u.legendOnce(glyphQueue)
	}
	for _, p := range u.snapshot.Panes {
		if p.State == "exited" || p.State == "failed" || p.State == "interrupted" {
			u.legendOnce(glyphExit)
			break
		}
	}
}

func (u *ui) drawPane(p model.Pane, r rect) {
	if r.w < 2 || r.h < 2 {
		return
	}
	focused := (u.area == focusMain && p.ID == u.canvas.main) || (u.area == focusUtility && p.ID == u.canvas.utility)
	style := idleTitleStyle
	marker := " "
	if p.ID == u.canvas.main {
		marker = "▌"
	}
	if p.ID == u.canvas.utility {
		marker = "▐"
	}
	if focused {
		style = focusStyle
	}
	glyph := stateGlyph(p.State, p.TurnState)
	title := fmt.Sprintf("%s %s · %s %s · %s", marker, p.Name, glyph, stateText(p), relativeAge(lastActivity(p)))
	if n := len(u.permitsFor(p.ID)); n > 0 {
		title += fmt.Sprintf(" · %s%d", glyphApproval, n)
	}
	if n := len(u.queuedForPane(p.ID)); n > 0 {
		title += fmt.Sprintf(" · %s%d", glyphQueue, n)
	}
	if p.Unread > 0 {
		title += fmt.Sprintf(" · %s%d", glyphUnread, p.Unread)
	}
	if n := u.offsets[p.ID]; n > 0 {
		title += fmt.Sprintf(" · ⟲ 回看 %d", n)
		style = style.Background(color.NewHexColor(0x59466d))
	} else if len(u.permitsFor(p.ID)) > 0 && !focused {
		style = alertStyle
	}
	u.line(r.x, r.y, r.w, title, style)

	content := u.contentRect(p, r)
	if p.Backend == "acp" {
		u.drawMessagesIn(p, content)
	} else if p.Frame == nil {
		if p.Error != "" {
			u.line(content.x, content.y, content.w, p.Error, tcell.StyleDefault)
		}
	} else {
		for y, row := range p.Frame.Cells {
			if y >= content.h {
				break
			}
			for x, c := range row {
				if x >= content.w {
					break
				}
				if c.Width <= 0 || x+c.Width > content.w {
					continue
				}
				text := c.Text
				if text == "" {
					text = " "
				}
				u.screen.Put(content.x+x, content.y+y, text, cellStyle(c))
			}
		}
		f := p.Frame
		if focused && !u.copyMode && u.offsets[p.ID] == 0 && f.CursorVisible && f.CursorX >= 0 && f.CursorX < content.w && f.CursorY >= 0 && f.CursorY < content.h {
			u.screen.ShowCursor(content.x+f.CursorX, content.y+f.CursorY)
		}
	}
	if restartable(p.State) && content.h > 0 {
		u.line(content.x, content.y, content.w, "[按 Enter 或点击此处重启] 将按原命令重新启动", focusStyle)
	}
	if p.ID == u.canvas.main {
		u.drawApprovalCard(u.approvalRect)
	}
	if p.Backend == "acp" {
		u.drawComposer(p, u.composerRectFor(p, r), focused)
	} else if len(u.queuedForPane(p.ID)) > 0 {
		u.line(r.x, r.y+r.h-1, r.w, u.queueBarText(p), barStyle)
	}
}

func (u *ui) drawComposer(p model.Pane, r rect, focused bool) {
	if r.w < 2 || r.h < 1 {
		return
	}
	input := u.acpInput(p.ID)
	line := 0
	if u.queueBarVisible(p) && r.h > 1 {
		line = 1
		u.line(r.x, r.y, r.w, u.queueBarText(p), barStyle)
	}
	text := input.Text()
	if focused {
		text = input.Display()
	}
	text = strings.ReplaceAll(text, "\n", " ↵ ")
	parts := wrap(text, max(1, r.w-2))
	visible := ""
	if len(parts) > 0 {
		visible = parts[len(parts)-1]
	}
	if focused {
		for _, part := range parts {
			if strings.Contains(part, "▏") {
				visible = part
				break
			}
		}
	}
	style := focusStyle
	if !focused {
		style = barStyle
	}
	u.line(r.x, r.y+line, r.w, "› "+visible, style)
	if r.h > line+1 {
		u.line(r.x, r.y+r.h-1, r.w, u.composerHint(p), barStyle)
	}
}

func (u *ui) queueBarText(p model.Pane) string {
	queued := u.queuedForPane(p.ID)
	parts := []string{}
	if len(queued) > 0 {
		parts = append(parts, fmt.Sprintf("%s 队列 %d", glyphQueue, len(queued)))
		for i, t := range queued {
			if i >= 2 {
				break
			}
			parts = append(parts, firstLineText(t.Prompt))
		}
	} else if t := u.lastSentChat(p.ID); t != nil {
		parts = append(parts, "· "+chatStateLabel(t.State), firstLineText(t.Prompt))
	}
	parts = append(parts, "点击这里，或按 Ctrl-a Space 打开队列")
	return strings.Join(parts, " · ")
}

func (u *ui) drawMessagesIn(p model.Pane, r rect) {
	if r.h < 1 {
		return
	}
	var lines []string
	if p.Backend == "acp" {
		lines = u.acpMessageLines(p, r.w)
	} else {
		lines = messageLines(p, r.w)
	}
	end := max(0, len(lines)-u.offsets[p.ID])
	start := max(0, end-r.h)
	for i, line := range lines[start:end] {
		u.line(r.x, r.y+i, r.w, line, tcell.StyleDefault)
	}
}

func (u *ui) drawStatusBar() {
	w, h := u.screen.Size()
	if h < 1 {
		return
	}
	y := h - 1
	left := u.leftStatus()
	hint := u.statusHint()
	u.line(0, y, w, "", barStyle)
	u.line(0, y, min(w, uniseg.StringWidth(left)+1), left, barStyle)
	if msg := u.status; msg != "" {
		mid := 1 + uniseg.StringWidth(left)
		avail := w - mid - uniseg.StringWidth(hint) - 2
		if avail > 8 {
			u.line(mid, y, avail, " "+msg, barStyle.Foreground(color.NewHexColor(0x9fe6c4)))
		}
	}
	if hintW := uniseg.StringWidth(hint); w > hintW+1 {
		u.line(w-hintW-1, y, hintW, hint, hintStyle)
	}
}

func (u *ui) drawTransientModals() {
	if u.help {
		u.modal(u.helpLines())
	}
	if u.pendingPaste != "" {
		u.modal([]string{"多行粘贴确认", "目标程序未启用 bracketed paste，粘贴可能执行多条命令。", "按 y 发送 · n / Esc 取消", fmt.Sprintf("共 %d 字节", len(u.pendingPaste))})
	}
	if u.mode != "" {
		u.drawInput()
	}
	if u.actions != nil {
		u.drawActions()
	}
	if u.palette != nil {
		u.drawPalette()
	}
	if u.create != nil {
		u.drawCreate()
	}
	if u.queueView != nil {
		u.drawQueue()
	}
}

func (u *ui) helpLines() []string {
	first := "主画布 · 直接输入即可"
	switch u.area {
	case focusRail:
		first = "左栏 · ↑↓ 选择 · Enter 打开 · Space 动作 · n 新建 · p 工具槽 · x 移除 · / 过滤 · Esc 返回"
	case focusApproval:
		first = "审批卡 · 1..9 选择 · r 拒绝 · d 稍后处理 · [ ] 切换 · Enter 详情 · Esc 收起"
	case focusUtility:
		first = "工具槽 · Ctrl-a h 回到主画布 · Ctrl-a s 关闭（进程继续运行）"
	}
	return []string{
		"快捷帮助 · " + first,
		"Ctrl-a j/k 切换 · 1..9 直达 · h/l 主/槽 · p 审批 · s 工具槽 · a 左栏 · Space 命令 · q 离开",
		"图例 ◌ 启动 · ● 空闲 · ◐ 执行 · ⏳ 审批 · ⏸ 排队 · ◉ 未读 · ✕ 退出 · Ctrl-a Ctrl-a 发送字面 ^A",
	}
}

func (u *ui) modal(lines []string) {
	w, h := u.screen.Size()
	width := min(90, max(1, w-4))
	height := min(len(lines)+2, max(1, h-2))
	x, y := (w-width)/2, (h-height)/2
	style := tcell.StyleDefault.Background(color.NewHexColor(0x263545)).Foreground(color.White)
	for row := 0; row < height; row++ {
		u.line(x, y+row, width, "", style)
	}
	for i, line := range lines {
		if i >= height-2 {
			break
		}
		u.line(x+1, y+i+1, max(0, width-2), line, style)
	}
	u.screen.HideCursor()
}

func messageLines(p model.Pane, width int) []string {
	var lines []string
	for _, m := range p.Messages {
		for _, line := range strings.Split(m.Role+": "+m.Text, "\n") {
			lines = append(lines, wrap(line, width)...)
		}
	}
	if p.Error != "" {
		lines = append(lines, wrap("错误："+p.Error, width)...)
	}
	return lines
}

func wrap(s string, width int) []string {
	if width < 1 {
		return nil
	}
	lines := []string{}
	line := ""
	n := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		sw := g.Width()
		if n+sw > width {
			lines = append(lines, line)
			line = ""
			n = 0
		}
		if sw <= width {
			line += g.Str()
			n += sw
		}
	}
	return append(lines, line)
}

func cellStyle(c model.Cell) tcell.Style {
	s := tcell.StyleDefault
	if c.FG >= 0 {
		s = s.Foreground(color.NewHexColor(c.FG))
	}
	if c.BG >= 0 {
		s = s.Background(color.NewHexColor(c.BG))
	}
	s = s.Bold(c.Attr&model.Bold != 0).Dim(c.Attr&model.Dim != 0).Italic(c.Attr&model.Italic != 0).Reverse(c.Attr&model.Reverse != 0).StrikeThrough(c.Attr&model.Strike != 0)
	if c.Attr&model.Underline != 0 {
		s = s.Underline(true)
	}
	return s
}

func (u *ui) drawInput() {
	w, h := u.screen.Size()
	width := max(1, min(90, max(1, w-4))-2)
	title := map[string]string{
		"command": "新终端命令（通过 /bin/zsh -lc 执行）", "task": "派发任务",
		"edge": "添加授权边：来源名称 目标名称", "unedge": "删除授权边：来源名称 目标名称",
		"stop": "停止当前 pane：输入 y 后 Enter 确认", "task-done": "人工标记完成：输入实际结果",
		"task-fail": "人工标记失败：输入原因", "task-redirect": "改派到其他 pane：输入名称（原任务保留）",
		"task-confirm-stop": "任务已停止？输入 y 确认",
		"task-raw":          "发送原始按键：<Enter> <Esc> <Ctrl-C> <Up> <Down>，按字面发送",
		"queue-edit":        "编辑排队任务（Enter 保存 · Esc 取消）",
		"rail-filter":       "过滤左栏列表（留空表示不过滤）",
	}[u.mode]
	if u.mode == "task" {
		if p := u.current(); p != nil {
			title = "派活 → " + p.Name
		}
	}
	lines := []string{title}
	if u.mode == "task" {
		if p := u.current(); p != nil {
			lines = append(lines, u.taskModeLine(p))
		}
	}
	lines = append(lines, "←→ 编辑 · ↑↓ 历史 · Enter 提交 · Alt-Enter 换行 · Esc 取消")
	var content []string
	cursorLine := 0
	for _, line := range strings.Split(u.editor.Display(), "\n") {
		for _, part := range wrap(line, width) {
			if strings.Contains(part, "▏") {
				cursorLine = len(content)
			}
			content = append(content, part)
		}
	}
	capacity := max(1, h-6)
	start := max(0, cursorLine-capacity+1)
	u.modal(append(lines, content[start:min(len(content), start+capacity)]...))
}
