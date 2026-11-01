package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v3"
	"github.com/gdamore/tcell/v3/color"
	"wtable/internal/model"
)

type paletteView struct {
	filter string
	cursor int
}

type paletteItem struct {
	label string
	hint  string
	run   func()
}

// openPalette is the only entry that needs memorising; every low-frequency
// action lives here with its old key (if it still has one) shown beside it.
func (u *ui) openPalette() {
	u.palette = &paletteView{}
}

func (u *ui) paletteItems() []paletteItem {
	hasForeground := u.foregroundPane() != nil
	items := []paletteItem{
		{label: "新建前台 agent", hint: "使用 PTY 预设", run: func() {
			u.palette = nil
			u.openCreate(model.PaneForeground)
		}},
		{label: "新建 worker", hint: "PTY 或 ACP；新建到左栏", run: func() {
			u.palette = nil
			u.openCreate(model.PaneWorker)
		}},
		{label: "编辑当前 agent", hint: "名字 / 角色 / 账号", run: func() {
			u.palette = nil
			if p := u.current(); p != nil {
				u.editPane(*p)
			}
		}},
		{label: "派活给当前 agent", hint: "队列 / 插话 / 中断后发送", run: func() {
			u.palette = nil
			u.openTaskComposer()
		}},
		{label: "任务列表", hint: "Ctrl-a b · 委派任务与报告", run: func() {
			u.palette = nil
			u.openTasks()
		}},
		{label: "队列", hint: "排队消息：提到最前 / 编辑 / 取消", run: func() {
			u.palette = nil
			if p := u.current(); p != nil {
				u.openQueue(p.ID)
			}
		}},
		{label: "协作图", hint: "Ctrl-a G · 谁连着谁", run: func() {
			u.palette = nil
			u.openGraph()
		}},
		{label: "管理面板", hint: "Agents / Edges / Tasks / Layers", run: func() {
			u.palette = nil
			u.openPanel(0)
		}},
		{label: "工具槽开关", hint: "Ctrl-a s · 默认 shell", run: func() {
			u.palette = nil
			u.toggleUtility()
		}},
		{label: "工具槽换方向", hint: "右侧 / 下方", run: func() {
			u.palette = nil
			u.canvas.vertical = !u.canvas.vertical
			u.resize()
			u.status = "工具槽已换到另一侧"
		}},
		{label: "回看当前 agent 的输出", hint: "滚轮也可以", run: func() {
			u.palette = nil
			u.copyMode = true
		}},
		{label: "重启当前 agent", hint: "进程已退出时可用", run: func() {
			u.palette = nil
			u.restartCurrent()
		}},
		{label: "停止当前 agent", hint: "输入 y 确认", run: func() {
			u.palette = nil
			u.openInput("stop", "")
		}},
		{label: "显示 / 隐藏对话任务", hint: "任务列表默认不含对话", run: func() {
			u.palette = nil
			u.allowChat = !u.allowChat
			if u.allowChat {
				u.status = "任务列表现在包含对话任务"
			} else {
				u.status = "任务列表只显示委派任务"
			}
		}},
		{label: "本页帮助", hint: "Ctrl-a ? · 当前上下文", run: func() {
			u.palette = nil
			u.help = true
		}},
		{label: "离开（后台继续跑）", hint: "Ctrl-a q", run: func() {
			u.palette = nil
			u.quit = true
		}},
	}
	if hasForeground {
		items[0].hint = "已有前台 agent；请先移除再新建"
	}
	return items
}

func (u *ui) visiblePaletteItems() []paletteItem {
	items := u.paletteItems()
	filter := strings.ToLower(strings.TrimSpace(u.palette.filter))
	if filter == "" {
		return items
	}
	out := []paletteItem{}
	for _, item := range items {
		if strings.Contains(strings.ToLower(item.label), filter) {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		// Hints are a fallback only: matching them first would let a hint word
		// like "队列" shadow the real "队列" command.
		for _, item := range items {
			if strings.Contains(strings.ToLower(item.hint), filter) {
				out = append(out, item)
			}
		}
	}
	return out
}

func (u *ui) paletteKey(e *tcell.EventKey) {
	p := u.palette
	if p == nil {
		return
	}
	items := u.visiblePaletteItems()
	switch e.Key() {
	case tcell.KeyEscape:
		u.palette = nil
		return
	case tcell.KeyUp:
		p.cursor = max(0, p.cursor-1)
		return
	case tcell.KeyDown:
		if len(items) > 0 {
			p.cursor = min(len(items)-1, p.cursor+1)
		}
		return
	case tcell.KeyEnter:
		if len(items) == 0 {
			return
		}
		item := items[max(0, min(p.cursor, len(items)-1))]
		u.palette = nil
		if item.run != nil {
			item.run()
		}
		return
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if p.filter != "" {
			_, size := utf8.DecodeLastRuneInString(p.filter)
			p.filter = p.filter[:len(p.filter)-size]
			p.cursor = 0
		}
		return
	case tcell.KeyCtrlU:
		p.filter = ""
		p.cursor = 0
		return
	case tcell.KeyRune:
		if e.Modifiers()&(tcell.ModCtrl|tcell.ModAlt) != 0 {
			return
		}
		p.filter += e.Str()
		p.cursor = 0
		return
	}
}

func (u *ui) drawPalette() {
	p := u.palette
	if p == nil {
		return
	}
	items := u.visiblePaletteItems()
	w, h := u.screen.Size()
	width := min(80, max(30, w-8))
	height := min(len(items)+6, max(6, h-4))
	x0 := (w - width) / 2
	y0 := (h - height) / 2
	base := tcell.StyleDefault.Background(color.NewHexColor(0x1d2733)).Foreground(color.White)
	dim := base.Foreground(color.NewHexColor(0x8fa3b8))
	sel := tcell.StyleDefault.Background(color.NewHexColor(0x2f4a63)).Foreground(color.White)
	for row := 0; row < height; row++ {
		u.line(x0, y0+row, width, "", base)
	}
	u.line(x0, y0, width, " 命令面板 ", base.Bold(true))
	u.line(x0, y0+1, width, " > "+truncateColumns(p.filter+"▏", width-4), base)
	u.line(x0, y0+2, width, strings.Repeat("─", width), dim)
	visible := height - 5
	p.cursor = max(0, min(p.cursor, max(0, len(items)-1)))
	top := max(0, p.cursor-visible+1)
	top = min(top, max(0, len(items)-visible))
	if len(items) == 0 {
		u.line(x0+1, y0+3, width-2, "没有匹配的命令", dim)
	}
	for i := top; i < min(len(items), top+visible); i++ {
		item := items[i]
		style := base
		marker := "   "
		if i == p.cursor {
			marker = " › "
			style = sel
		}
		line := fmt.Sprintf("%s%s", marker, item.label)
		if item.hint != "" {
			line += "  · " + item.hint
		}
		u.line(x0+1, y0+3+(i-top), width-2, truncateColumns(line, width-4), style)
	}
	u.line(x0, y0+height-1, width, " 输入筛选 · ↑↓ 选择 · Enter 执行 · Esc 取消 ", dim)
	u.screen.HideCursor()
}
