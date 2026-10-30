package tui

import (
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/gdamore/tcell/v3"
	"github.com/rivo/uniseg"
	"wtable/internal/model"
)

var barStyle = tcell.StyleDefault.Background(tcell.NewHexColor(0x223344)).Foreground(tcell.ColorWhite)
var focusStyle = tcell.StyleDefault.Background(tcell.NewHexColor(0x245c6a)).Foreground(tcell.ColorWhite).Bold(true)

func (u *ui) resize() {
	w, h := u.screen.Size()
	u.boxes = map[string]rect{}
	u.dividers = nil
	available := h - 1
	if u.tasks {
		available -= min(6, max(0, h/3))
	}
	n := len(u.snapshot.Panes)
	if n == 0 || w < 2 || available < 2 {
		return
	}
	if u.zoom {
		p := u.current()
		if p != nil {
			u.boxes[p.ID] = rect{0, 0, w, available}
		}
	} else {
		cols, rows := 1, n
		switch u.layout {
		case 0:
			cols = int(math.Ceil(math.Sqrt(float64(n))))
			rows = (n + cols - 1) / cols
		case 1:
			cols = n
			rows = 1
		}
		key := [2]int{u.layout, n}
		if u.layoutKey != key || len(u.colCuts) != cols+1 || len(u.rowCuts) != rows+1 {
			u.colCuts, u.rowCuts = equalCuts(cols), equalCuts(rows)
			u.layoutKey = key
			u.drag = nil
		}
		xs, xgap := axisPositions(w, u.colCuts, 8)
		ys, ygap := axisPositions(available, u.rowCuts, 3)
		for i, p := range u.snapshot.Panes {
			cx, cy := i%cols, i/cols
			u.boxes[p.ID] = rect{xs[cx] + cx*xgap, ys[cy] + cy*ygap, xs[cx+1] - xs[cx], ys[cy+1] - ys[cy]}
		}
		if xgap > 0 {
			for i := 1; i < cols; i++ {
				u.dividers = append(u.dividers, divider{rect{xs[i] + i - 1, 0, 1, available}, true, i})
			}
		}
		if ygap > 0 {
			for i := 1; i < rows; i++ {
				u.dividers = append(u.dividers, divider{rect{0, ys[i] + i - 1, w, 1}, false, i})
			}
		}
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
	key := fmt.Sprint(w, h, u.focus, u.layout, u.zoom, u.tasks, u.prefix, u.help, u.copyMode, u.offsets, u.mode, u.editor.Display(), u.status, u.pendingPaste, u.colCuts, u.rowCuts, selectionKey, u.taskView, u.taskCursor, u.taskDetail, u.taskScroll, u.reportView, u.reportCursor, u.reportDetail, u.taskScreen, u.taskDelivery, u.taskQueue)
	if key == u.lastDrawKey && reflect.DeepEqual(u.snapshot, u.lastDrawSnapshot) {
		return
	}
	u.lastDrawKey = key
	u.lastDrawSnapshot = u.snapshot
	u.screen.Clear()
	u.screen.HideCursor()
	for i, p := range u.snapshot.Panes {
		r, ok := u.boxes[p.ID]
		if !ok || r.h < 1 {
			continue
		}
		style := barStyle
		marker := " "
		if i == u.focus {
			style = focusStyle
			marker = "●"
		}
		title := fmt.Sprintf("%s %s %s · %s", marker, p.ID, p.Name, p.State)
		if p.Backend == "acp" {
			title += " · ACP " + p.TurnState
		}
		if n := u.offsets[p.ID]; n > 0 {
			title += fmt.Sprintf(" · ⟲ 回看 %d", n)
			style = style.Background(tcell.NewHexColor(0x59466d))
		}
		u.line(r.x, r.y, r.w, title, style)
		if p.Backend == "acp" {
			u.drawMessages(p, r)
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
	status := "点击聚焦 · 滚轮回看 · 拖选复制 · Ctrl-a t 发任务 · k 任务列表 · ? 帮助"
	if u.status != "" {
		status = u.status + " · Ctrl-a ? 帮助"
	}
	if u.prefix {
		status = "Ctrl-a 前缀：? 帮助 n shell c 命令 o 聚焦 s 布局 z 放大 [ 回看 t 任务 d 离开"
	}
	if u.copyMode {
		status = "回看模式：↑↓ / j k 滚动 · PgUp/PgDn 翻页 · y 复制可见屏 · Esc 返回"
	}
	u.line(0, h-1, w, status, barStyle)
	if u.help {
		u.modal([]string{"wtable 快捷键（先按 Ctrl-a，再按下一键）", "?  帮助；任意键关闭帮助", "n  新建 $SHELL 终端", "c  输入 shell 命令（/bin/zsh -lc 执行）", "o  下一个 pane；s 网格 / 左右 / 上下；z 聚焦放大", "b  切换任务列表；t 给当前 pane 发送任务 / ACP prompt", "g  授权边：来源ID 目标ID；r 删除边", "k  任务列表：Enter 详情、Tab 报告；详情中重发 / 查看屏幕 / 人工处理", "t  发任务：Tab 切换 inject/claim，Ctrl-Q 切换显式排队", "R  原地重启已退出 pane；退出 pane 内 Enter 也可重启", "[  回看模式：↑↓、PgUp/PgDn；y 复制可见屏到剪贴板", "a / Ctrl-a  发送原 Ctrl-a；普通 Ctrl-Z/P 等直达终端", "d / q  离开界面（后台 pane 保持运行）", "输入框：←→ 编辑，Home/End 行首尾，Ctrl-U/W 删除，↑↓ 历史", "Enter 提交；任务 Alt-Enter 换行；Esc 取消", "鼠标：点击 pane 聚焦；拖动分隔线调整行 / 列大小", "拖选 pane 内文字，松开复制；Esc / 点击恢复实时画面", "滚轮回看；应用启用鼠标时转发滚轮，Shift+滚轮强制回看", "MVP：选区限可见屏；点击不转发到 pane 内应用"})
	}
	if u.taskView {
		u.drawTasks()
	}
	if u.mode != "" {
		u.drawInput()
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
	title := map[string]string{"command": "新终端命令（通过 /bin/zsh -lc 执行）", "task": "发送任务", "edge": "添加授权边：来源名称 目标名称", "unedge": "删除授权边：来源名称 目标名称", "stop": "停止当前 pane：输入 y 后 Enter 确认", "task-done": "人工标记完成：输入实际结果", "task-fail": "人工标记失败：输入原因", "task-redirect": "重发到其他 pane：输入名称（保留原任务）", "task-confirm-stop": "确认任务已停止：输入 y（不会自动判断）", "task-raw": "原始输入：<Enter> <Esc> <Ctrl-C> <Up> <Down>，不自动回车"}[u.mode]
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
