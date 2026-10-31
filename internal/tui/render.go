package tui

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/gdamore/tcell/v3"
	"github.com/rivo/uniseg"
	"wtable/internal/model"
)

var barStyle = tcell.StyleDefault.Background(tcell.NewHexColor(0x223344)).Foreground(tcell.ColorWhite)
var focusStyle = tcell.StyleDefault.Background(tcell.NewHexColor(0x245c6a)).Foreground(tcell.ColorWhite).Bold(true)
var idleTitleStyle = tcell.StyleDefault.Background(tcell.NewHexColor(0x1b2430)).Foreground(tcell.NewHexColor(0x9aa7b4))
var alertStyle = tcell.StyleDefault.Background(tcell.NewHexColor(0x5a2f2f)).Foreground(tcell.NewHexColor(0xffd7d7)).Bold(true)
var hintStyle = tcell.StyleDefault.Background(tcell.NewHexColor(0x223344)).Foreground(tcell.NewHexColor(0x8fa3b5))

func (u *ui) resize() {
	w, h := u.screen.Size()
	u.boxes = map[string]rect{}
	u.dividers = nil
	if u.graph != nil {
		return
	}
	available := h - 1
	if u.tasks {
		available -= min(6, max(0, h/3))
	}
	u.syncViews()
	if w < 2 || available < 2 {
		return
	}
	t := u.tab()
	if u.zoom {
		if t.root.find(t.focus) != nil {
			u.boxes[t.focus] = rect{0, 0, w, available}
		}
	} else {
		u.layoutView(t.root, rect{0, 0, w, available})
	}

	if s := u.selection; s != nil {
		r, ok := u.boxes[s.paneID]
		if !ok || (rect{r.x, r.y + 1, r.w, r.h - 1}) != s.area {
			u.selection = nil
		}
	}
	for _, p := range u.snapshot.Panes {
		if r, ok := u.boxes[p.ID]; ok && p.Backend == "pty" {
			size := [2]int{min(240, max(1, r.w)), min(100, max(1, r.h-1))}
			if u.sizes[p.ID] != size && u.call("pane.resize", map[string]any{"id": p.ID, "cols": size[0], "rows": size[1]}, nil) {
				u.sizes[p.ID] = size
			}
		}
	}
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
	key := fmt.Sprint(w, h, u.focus, u.layout, u.zoom, u.tasks, u.prefix, u.help, u.copyMode, u.offsets, u.mode, u.editor.Display(), u.status, u.pendingPaste, u.colCuts, u.rowCuts, selectionKey, u.taskView, u.taskCursor, u.taskDetail, u.taskScroll, u.reportView, u.reportCursor, u.reportDetail, u.taskScreen, u.taskDelivery, u.taskQueue, u.panelDrawKey(), u.overlayDrawKey(), u.acpDrawKey(), u.viewStatus(), u.boxes)
	if key == u.lastDrawKey && reflect.DeepEqual(u.snapshot, u.lastDrawSnapshot) {
		return
	}
	u.lastDrawKey = key
	u.lastDrawSnapshot = u.snapshot
	u.screen.Clear()
	u.screen.HideCursor()
	if len(u.boxes) == 0 && u.graph == nil {
		u.line(1, 1, max(0, w-2), "当前 tab 没有可见 pane · Ctrl-a f 选择 · Ctrl-a a 管理", barStyle)
	}
	for i, p := range u.snapshot.Panes {
		r, ok := u.boxes[p.ID]
		if !ok || r.h < 1 {
			continue
		}
		style := barStyle
		focused := i == u.focus
		unread, attention := u.attention(p.ID)
		marker := " "
		if focused {
			style = focusStyle
			marker = "▌"
		}
		index := u.paneIndexOf(p.ID)
		badge := " "
		if index > 0 {
			badge = fmt.Sprintf("%d", index)
		}
		glyph := stateGlyph(p.State, p.TurnState, attention)
		title := fmt.Sprintf("%s%s %s%s · %s", marker, badge, glyph, p.Name, stateLabel(p.State, p.TurnState))
		if unread > 0 {
			title += fmt.Sprintf(" · %d 未读", unread)
		}
		if p.Backend == "acp" {
			title += " · ACP"
		}
		if n := u.offsets[p.ID]; n > 0 {
			title += fmt.Sprintf(" · ⟲ 回看 %d", n)
			style = style.Background(tcell.NewHexColor(0x59466d))
		} else if !focused {
			style = idleTitleStyle
		}
		if len(attention) > 0 && !focused {
			style = alertStyle
		}
		u.line(r.x, r.y, r.w, title, style)
		if p.Backend == "acp" {
			u.drawACP(p, r, i == u.focus)
			continue
		}
		if p.Frame == nil {
			u.line(r.x, r.y+1, r.w, p.Error, tcell.StyleDefault)
			continue
		}
		for y, row := range p.Frame.Cells {
			if y >= r.h-1 {
				break
			}
			for x, c := range row {
				if x >= r.w {
					break
				}
				if c.Width <= 0 {
					continue
				}
				if x+c.Width > r.w {
					continue
				}
				text := c.Text
				if text == "" {
					text = " "
				}
				u.screen.Put(r.x+x, r.y+1+y, text, cellStyle(c))
			}
		}
		f := p.Frame
		if i == u.focus && !u.copyMode && u.offsets[p.ID] == 0 && f.CursorVisible && f.CursorX >= 0 && f.CursorX < r.w && f.CursorY >= 0 && f.CursorY < r.h-1 {
			u.screen.ShowCursor(r.x+f.CursorX, r.y+1+f.CursorY)
		}
	}
	for _, p := range u.snapshot.Panes {
		if r, ok := u.boxes[p.ID]; ok && r.h > 1 && restartable(p.State) {
			u.line(r.x, r.y+1, r.w, "[Enter / 点击此行重启] 重新执行原命令", focusStyle)
		}
	}
	u.drawSelection()
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
	if u.tasks {
		lines := min(6, max(0, h/3))
		y := h - 1 - lines
		u.line(0, y, w, "任务（Ctrl-a k 查看 / 取消） · 授权边 "+edgeText(u.snapshot.Edges), barStyle)
		start := max(0, len(u.snapshot.Tasks)-(lines-1))
		for i, t := range u.snapshot.Tasks[start:] {
			u.line(0, y+1+i, w, fmt.Sprintf("%s %s → %s [%s] %s", shortTask(t.ID), u.paneName(t.From), u.paneName(t.To), t.State, strings.SplitN(t.Prompt, "\n", 2)[0]), tcell.StyleDefault)
		}
	}
	left := u.viewStatus()
	if queued := u.queuedTotal(); queued > 0 {
		left += fmt.Sprintf(" · ⏸ %d 等投递", queued)
	}
	if blocked := u.blockedDeliveries(); blocked > 0 {
		left += fmt.Sprintf(" · ⚠ %d 投递受阻", blocked)
	}
	hint := u.statusHint()
	u.line(0, h-1, w, "", barStyle)
	u.line(0, h-1, min(w, uniseg.StringWidth(left)+1), left, barStyle)
	if msg := u.status; msg != "" {
		mid := 1 + uniseg.StringWidth(left)
		avail := w - mid - uniseg.StringWidth(hint) - 2
		if avail > 8 {
			u.line(mid, h-1, avail, " "+msg, barStyle.Foreground(tcell.NewHexColor(0x9fe6c4)))
		}
	} else if avail := w - uniseg.StringWidth(left) - uniseg.StringWidth(hint) - 3; avail > 8 {
		u.line(uniseg.StringWidth(left)+1, h-1, avail, " Alt+←→ 切 pane · Alt+1..9 直达 · Alt+z 单看", hintStyle)
	}
	if hintW := uniseg.StringWidth(hint); w > hintW+1 {
		u.line(w-hintW-1, h-1, hintW, hint, hintStyle)
	}
	if u.picker != nil {
		u.drawPicker()
	}
	if u.help {
		u.modal([]string{
			"快捷键 · 方向键优先（Alt 组合不传给 pane，普通按键照常进终端）",
			"",
			"动作    选中任意条目 → Enter 打开动作列表 → ↑↓ 选择 → Enter 执行",
			"        列表里的每一项都有说明，不需要记字母键；Esc 返回",
			"",
			"移动    Alt+←/→/↑/↓（或 Alt+hjkl）切到该方向的 pane",
			"        Alt+1..9 直达第 N 个 pane（标题左边就是编号）",
			"        Alt+z 单看 / 还原；Ctrl-a f 从列表里挑",
			"        Alt+←→ 到头不绕圈，按 Ctrl-a o 才是循环",
			"",
			"工作区  Ctrl-a T 新 tab · { } 切 tab · W 关 tab · , 重命名",
			"        Ctrl-a | 左右分屏 · - 上下分屏 · s 一键重排",
			"        Ctrl-a ! 跳到下一个需要你的人",
			"",
			"面板    Ctrl-a a 打开：Agents / Edges / Tasks / Layers",
			"        ↑↓ 选择 · Tab 换页 · Enter 打开 · x 删除 · Esc 返回",
			"        a 加 agent · e 编辑 · r 重发说明 · l 看日志 · R 重启",
			"",
			"任务    Ctrl-a t 派活（可多选目标）· k 任务列表 · b 底部任务条",
			"        列表里 Enter 详情 · r 重发 · x 取消 · Tab 切报告",
			"",
			"协作    Ctrl-a G 协作图：↑↓ 选节点 · Enter 打开 · e 选边 · x 删边",
			"",
			"终端    Ctrl-a [ 回看：↑↓ 滚动 · PgUp/PgDn 翻页 · y 复制 · Esc 返回",
			"        滚轮即可回看；拖选复制；Shift+滚轮强制回看",
			"        应用启用鼠标时，鼠标事件转发给它，Shift 让位给本地",
			"",
			"输入框  ←→ 编辑 · Home/End 行首尾 · Ctrl-U/W 删除 · ↑↓ 历史",
			"        Enter 提交 · Alt-Enter 换行 · Esc 取消",
			"",
			"退出    Ctrl-a d 离开（后台继续跑）· Ctrl-a ? 关掉本页",
		})
	}
	if u.graph != nil {
		u.drawGraph()
	}
	if u.panel != nil {
		u.drawPanel()
	}
	if u.taskView {
		u.drawTasks()
	}
	if u.mode != "" {
		u.drawInput()
	}
	if u.actions != nil {
		u.drawActions()
	}
	if u.pendingPaste != "" {
		u.modal([]string{"多行粘贴确认", "目标程序未启用 bracketed paste，粘贴可能执行多条命令。", "按 y 发送；n / Esc 取消（Enter 不会发送）", fmt.Sprintf("共 %d 字节", len(u.pendingPaste))})
	}
	if len(u.snapshot.Permissions) > 0 {
		p := u.snapshot.Permissions[0]
		lines := []string{"权限请求 · " + p.PaneID, p.Title, "仅按数字选择；Esc 拒绝 / 取消；Enter 不会批准"}
		for i, o := range p.Options {
			if i < 9 {
				lines = append(lines, fmt.Sprintf("%d  %s [%s]", i+1, o.Name, o.Kind))
			}
		}
		u.modal(lines)
	}
	u.screen.Show()
}
func (u *ui) modal(lines []string) {
	w, h := u.screen.Size()
	width := min(90, max(1, w-4))
	height := min(len(lines)+2, max(1, h-2))
	x, y := (w-width)/2, (h-height)/2
	style := tcell.StyleDefault.Background(tcell.NewHexColor(0x263545)).Foreground(tcell.ColorWhite)
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

func (u *ui) drawMessages(p model.Pane, r rect) {
	lines := messageLines(p, r.w)
	end := max(0, len(lines)-u.offsets[p.ID])
	start := max(0, end-(r.h-1))
	for i, line := range lines[start:end] {
		u.line(r.x, r.y+1+i, r.w, line, tcell.StyleDefault)
	}
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
func edgeText(edges []model.Edge) string {
	parts := []string{}
	for _, e := range edges {
		parts = append(parts, e.From+"→"+e.To)
	}
	if len(parts) == 0 {
		return "无"
	}
	return strings.Join(parts, " ")
}
func cellStyle(c model.Cell) tcell.Style {
	s := tcell.StyleDefault
	if c.FG >= 0 {
		s = s.Foreground(tcell.NewHexColor(c.FG))
	}
	if c.BG >= 0 {
		s = s.Background(tcell.NewHexColor(c.BG))
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
	title := map[string]string{"tab-name": "tab 名称", "command": "新终端命令（通过 /bin/zsh -lc 执行）", "task": "发送任务", "edge": "添加授权边：来源名称 目标名称", "unedge": "删除授权边：来源名称 目标名称", "stop": "停止当前 pane：输入 y 后 Enter 确认", "task-done": "人工标记完成：输入实际结果", "task-fail": "人工标记失败：输入原因", "task-redirect": "重发到其他 pane：输入名称（保留原任务）", "task-confirm-stop": "确认任务已停止：输入 y（不会自动判断）", "task-raw": "原始输入：<Enter> <Esc> <Ctrl-C> <Up> <Down>，不自动回车"}[u.mode]
	if u.mode == "task" {
		if p := u.current(); p != nil {
			title += " → " + p.Name
			mode := u.taskDelivery
			if mode == "" {
				mode = "inject"
				if p.Backend == "acp" {
					mode = "prompt"
				}
			}
			title += fmt.Sprintf(" [%s] 排队:%t · Tab 切换 · Ctrl-Q 排队", mode, u.taskQueue)
		}
	}
	lines := []string{title, "←→ 编辑 · ↑↓ 历史 · Enter 提交 · Alt-Enter 换行 · Esc 取消"}
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
