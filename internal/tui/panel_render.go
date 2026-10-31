package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v3"
	"wtable/internal/model"
)

func (u *ui) drawPanel() {
	p := u.panel
	_, h := u.screen.Size()
	capacity := max(1, h-8)
	p.rows = map[int]int{}
	p.fieldRows = map[int]int{}

	if p.busy {
		lines := []string{"处理中，请稍候；底层输出与权限请求仍可显示"}
		if p.form != "" {
			lines = append(lines, "提交失败时会保留表单")
		}
		u.panelFrame("面板", lines)
		return
	}

	if p.preview != "" {
		lines := []string{"↑↓ / PgUp/PgDn 滚动 · Esc 返回"}
		width := max(20, u.screenWidth()-26)
		var body []string
		for _, line := range strings.Split(p.preview, "\n") {
			body = append(body, wrap(line, width)...)
		}
		p.scroll = min(p.scroll, max(0, len(body)-capacity))
		lines = append(lines, body[p.scroll:min(len(body), p.scroll+capacity)]...)
		u.panelFrame("查看", lines)
		return
	}

	if p.confirmKind != "" {
		var question []string
		if p.confirmKind == "pane" {
			question = []string{"移除 " + u.paneName(p.confirmID) + "？进程将停止，相关排队任务会取消。", "任务、报告和身份说明历史保留。"}
		} else if p.confirmKind == "role" {
			question = []string{"删除角色模板 " + p.confirmID + "？已运行实例的角色快照保持不变。"}
		} else {
			var e model.Edge
			_ = json.Unmarshal([]byte(p.confirmID), &e)
			question = []string{"删除 " + u.paneName(e.From) + " → " + u.paneName(e.To) + "？", "该方向排队任务会取消，已执行操作不会撤销。"}
		}
		lines := append([]string{}, question...)
		lines = append(lines, "", "  [ 确认 ]        [ 取消 ]", "", "y 确认 · n/Esc 取消 · Enter 不确认（安全考虑）；也可以直接点上面的按钮")
		top := u.panelFrame("确认", lines)
		buttons := top + len(question) + 2
		p.rows[buttons] = -1 // [ 确认 ]
		p.confirmCancelRow = buttons
		return
	}

	if p.form == "preset" {
		entries := u.presetEntries()
		order := u.presetOrder()
		lines := []string{}
		rowIndex := map[int]int{} // line index → 可选索引（len(order) 表示"自定义命令"）
		lines = append(lines, "选一个预设，Enter 直接创建；e 或 Tab 进入详细设置；Esc 返回")
		lines = append(lines, "")
		availableAt := -1
		for cursor, index := range order {
			preset := entries[index]
			if !preset.Available {
				continue
			}
			if availableAt < 0 {
				availableAt = len(lines)
				lines = append(lines, "可用")
			}
			marker := "   "
			if cursor == p.presetCursor {
				marker = " › "
			}
			rowIndex[len(lines)] = cursor
			lines = append(lines, fmt.Sprintf("%s%-4s %-22s %s", marker, strings.ToUpper(preset.Backend), preset.Name, preset.Version))
		}
		missingAt := -1
		for cursor, index := range order {
			preset := entries[index]
			if preset.Available {
				continue
			}
			if missingAt < 0 {
				missingAt = len(lines)
				lines = append(lines, "", "未安装 / 不可用（Enter 会说明原因）")
			}
			marker := "   "
			if cursor == p.presetCursor {
				marker = " › "
			}
			rowIndex[len(lines)] = cursor
			lines = append(lines, fmt.Sprintf("%s%-4s %-22s %s", marker, strings.ToUpper(preset.Backend), preset.Name, preset.Detail))
		}
		lines = append(lines, "")
		customMarker := "   "
		if p.presetCursor >= len(order) {
			customMarker = " › "
		}
		rowIndex[len(lines)] = len(order)
		lines = append(lines, customMarker+"自定义命令…")
		lines = append(lines, "", "默认：名字自动编号 · 目录跟随当前 Layer/工作区 · 注入身份说明 · 与当前 pane 建边")
		top := u.panelFrame("添加 Agent", lines)
		for line, cursor := range rowIndex {
			p.rows[top+line] = cursor
		}
		return
	}

	if p.form != "" {
		title := map[string]string{"agent": "添加 Agent", "edit": "编辑 Agent（命令与目录变更请重新创建）", "send": "派发任务", "edge": "添加单向授权边", "layer": "创建 Git Layer", "role": "编辑角色模板"}[p.form]
		lines := []string{}
		start := max(0, p.field-capacity+1)
		fieldLine := map[int]int{}
		for i := start; i < min(len(p.fields), start+capacity); i++ {
			f := &p.fields[i]
			marker := "   "
			if i == p.field {
				marker = " › "
			}
			value := f.edit.Text()
			if i == p.field {
				value = f.edit.Display()
			}
			if len(f.options) > 0 {
				value = f.options[f.choice].label
				if f.multi {
					mark := "□"
					if f.checked[f.value()] {
						mark = "☑"
					}
					value = fmt.Sprintf("%s %s（已选 %d）", mark, value, len(f.values()))
					if p.form == "edge" && f.key == "targets" {
						from := p.get("from").value()
						if from == f.value() {
							value += " · 自身不可选"
						}
						for _, e := range u.snapshot.Edges {
							if e.From == from && e.To == f.value() {
								value += " · 已授权"
								break
							}
						}
					}
				} else {
					value = "‹ " + value + " ›"
				}
			} else if f.multi {
				value = "没有其他 pane"
			}
			value = strings.ReplaceAll(value, "\n", " ↵ ")
			fieldLine[len(lines)] = i
			lines = append(lines, marker+f.label+": "+value)
		}
		lines = append(lines, "", fmt.Sprintf("字段 %d/%d · ↑↓ 换字段 · ←→ 改值 · Enter 下一步 · Esc 取消", p.field+1, len(p.fields)))
		if p.form == "agent" {
			lines = append(lines, u.conflictWarnings(p.get("cwd").value(), p.get("layer").value(), p.get("account").value())...)
			for _, account := range p.catalog.AccountProfiles {
				if account.Name == p.get("account").value() && account.Endpoint != "" && account.Endpoint != "official" {
					lines = append(lines, "非官方端点 "+account.Endpoint+"：工具调用 / 流式可能降级")
				}
			}
			for _, preset := range p.catalog.Presets {
				if preset.ID == p.get("preset").value() {
					lines = append(lines, preset.Version+" · "+preset.Detail)
					break
				}
			}
		}
		top := u.panelFrame(title, lines)
		for line, field := range fieldLine {
			p.fieldRows[top+line] = field
		}
		return
	}

	if p.roleView {
		u.drawRoles()
		return
	}

	var lines []string
	actions := []string{
		"a 添加 Agent · e 编辑 · d 移除 · Enter 单看 · p 驻留 · v peek · b 重发说明",
		"a 添加边 · x 删除边 · t 只看当前 pane 相关（you→pane 是人类能力，不可删）",
		"s 派活 · Enter 详情 · r 重发 · x 取消 · b 报告收件箱",
		"n 新建 Layer · Enter 设为新 pane 的默认目录",
		"a 新建角色 · e 编辑 · x 删除",
	}
	lines = append(lines, actions[min(p.tab, len(actions)-1)])
	if p.tab == 0 && len(u.snapshot.Panes) > 0 {
		selected := u.snapshot.Panes[min(p.selected, len(u.snapshot.Panes)-1)]
		lines = append(lines,
			"输出："+truncateColumns(selected.LastOutput, 80),
			"活动："+selected.LastActivity.Local().Format("15:04:05")+" · 就绪："+selected.Readiness+" · "+selected.ReadinessReason,
			fmt.Sprintf("账号：%s · 端点：%s · 越权尝试 %d 次 · Role：%s", selected.Account, selected.Endpoint, selected.DeniedCount, selected.Role))
		lines = append(lines, u.conflictWarnings(selected.Cwd, selected.Layer, selected.Account)...)
		if selected.Endpoint != "" && selected.Endpoint != "official" {
			lines = append(lines, "非官方端点：工具调用 / 流式可能降级")
		}
	}
	lines = append(lines, "")

	var rows []string
	switch p.tab {
	case 0:
		for _, pane := range u.snapshot.Panes {
			brief := ""
			if pane.InjectBrief {
				brief = " · brief:未发送"
				for _, t := range u.snapshot.Tasks {
					if t.ID == pane.BriefTask {
						brief = " · brief:" + t.State
						break
					}
				}
			}
			rows = append(rows, u.agentSummary(pane)+brief)
		}
	case 1:
		for _, e := range u.panelEdges() {
			rows = append(rows, u.paneName(e.From)+" → "+u.paneName(e.To))
		}
	case 2:
		for _, t := range u.orderedTasks() {
			unread := ""
			if !t.Seen && (t.State == "completed" || t.State == "failed" || t.State == "unknown") {
				unread = "• "
			}
			line := fmt.Sprintf("%s%s [%s] %s → %s %s", unread, shortTask(t.ID), t.State, u.paneName(t.From), u.paneName(t.To), strings.SplitN(t.Prompt, "\n", 2)[0])
			if t.State == "queued" && t.QueueReason != "" {
				line += " · 等待：" + truncateColumns(t.QueueReason, 40)
			}
			rows = append(rows, line)
		}
	case 3:
		mark := ""
		if u.activeLayer == "" {
			mark = " ✓"
		}
		rows = append(rows, "工作区："+u.cwd+mark)
		for _, l := range u.snapshot.Layers {
			mark = ""
			if l.Path == u.activeLayer {
				mark = " ✓"
			}
			rows = append(rows, l.Name+" · "+l.Path+mark)
		}
	}
	p.selected = min(p.selected, max(0, len(rows)-1))
	start := max(0, p.selected-capacity+1)
	rowStart := len(lines)
	for i := start; i < min(len(rows), start+capacity); i++ {
		marker := "   "
		if i == p.selected && !p.focusSide {
			marker = " › "
		}
		lines = append(lines, marker+rows[i])
	}
	if len(rows) == 0 {
		lines = append(lines, "暂无条目")
	}
	title := "面板"
	if p.tab < len(panelTabs) {
		title = panelTabs[p.tab]
	}
	top := u.panelFrame(title, lines)
	for i := start; i < min(len(rows), start+capacity); i++ {
		p.rows[top+rowStart+(i-start)] = i
	}
}

func (u *ui) screenWidth() int {
	w, _ := u.screen.Size()
	return w
}

func (u *ui) panelMouse(e *tcell.EventMouse, pressed bool) {
	p := u.panel
	if p.busy {
		return
	}
	x, y := e.Position()
	w, _ := u.screen.Size()
	sideW := min(24, max(16, w/5))
	wheelUp := e.Buttons()&tcell.WheelUp != 0
	wheelDown := e.Buttons()&tcell.WheelDown != 0

	// 侧栏：滚轮换页，点击切页
	if x <= sideW {
		if wheelUp || wheelDown {
			step := 1
			if wheelUp {
				step = -1
			}
			p.tab = (p.tab + step + len(panelTabs)) % len(panelTabs)
			p.selected = 0
			return
		}
		if pressed {
			if tab, ok := p.sideRows[y]; ok {
				p.tab = tab
				p.selected = 0
				p.focusSide = true
			}
		}
		return
	}

	if p.preview != "" {
		switch {
		case wheelUp:
			p.scroll = max(0, p.scroll-3)
		case wheelDown:
			p.scroll += 3
		}
		return
	}

	// 确认框：点 [确认] / [取消]
	if p.confirmKind != "" {
		if !pressed {
			return
		}
		if y == p.confirmCancelRow {
			if x < sideW+18 {
				u.panelKey(tcell.NewEventKey(tcell.KeyRune, "y", tcell.ModNone))
			} else {
				u.panelKey(tcell.NewEventKey(tcell.KeyEscape, "", tcell.ModNone))
			}
		}
		return
	}

	// 预设选择器：点击选中，再点一次直接创建
	if p.form == "preset" {
		count := len(u.presetOrder()) + 1
		switch {
		case wheelUp:
			p.presetCursor = max(0, p.presetCursor-1)
		case wheelDown:
			p.presetCursor = min(count-1, p.presetCursor+1)
		}
		if pressed {
			if cursor, ok := p.rows[y]; ok {
				if cursor == p.presetCursor {
					u.panelKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
					return
				}
				p.presetCursor = cursor
			}
		}
		return
	}

	// 表单：点字段聚焦；点选项的值区域直接换值
	if p.form != "" {
		if !pressed {
			return
		}
		index, ok := p.fieldRows[y]
		if !ok {
			return
		}
		p.field = index
		p.focusSide = false
		f := &p.fields[index]
		if len(f.options) > 0 && x > sideW+34 {
			if f.multi {
				f.checked[f.value()] = !f.checked[f.value()]
			} else if shift := e.Modifiers()&tcell.ModShift != 0; shift {
				f.choice = (f.choice + len(f.options) - 1) % len(f.options)
			} else {
				f.choice = (f.choice + 1) % len(f.options)
			}
		}
		return
	}

	// 列表：滚轮移动，点击选中，再点一次等同 Enter
	switch {
	case wheelUp:
		p.selected = max(0, p.selected-1)
	case wheelDown:
		p.selected = min(max(0, u.panelCount()-1), p.selected+1)
	}
	if pressed {
		if index, ok := p.rows[y]; ok {
			if index == p.selected {
				u.panelKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
				return
			}
			p.selected = index
			p.focusSide = false
		}
	}
}

// panelFrame draws the panel chrome: full-screen frame, a clickable section
// sidebar on the left, the content column on the right and one hint footer.
// It returns the first content row so callers can map rows to clicks.
func (u *ui) panelFrame(title string, lines []string) int {
	p := u.panel
	w, h := u.screen.Size()
	base := tcell.StyleDefault.Background(tcell.NewHexColor(0x1a2230)).Foreground(tcell.ColorWhite)
	for y := 0; y < h; y++ {
		u.line(0, y, w, "", base)
	}
	head := " " + title
	if p.focusSide {
		head += "   ←/→ 切换侧栏"
	}
	u.line(0, 0, w, head, barStyle.Bold(true))

	sideW := min(24, max(16, w/5))
	p.sideRows = map[int]int{}
	y := 2
	for i, name := range panelTabs {
		marker, style := "  ", idleTitleStyle
		if i == p.tab {
			marker = "▸ "
			if p.focusSide {
				style = focusStyle
			} else {
				style = barStyle
			}
		}
		u.line(1, y, sideW-1, fmt.Sprintf("%s%s %d", marker, name, u.panelCountOf(i)), style)
		p.sideRows[y] = i
		y++
	}
	u.line(sideW, 1, 1, "│", idleTitleStyle)
	for row := 2; row < h-1; row++ {
		u.line(sideW, row, 1, "│", idleTitleStyle)
	}
	hint := " ←→ 侧栏/内容 · ↑↓ 选择 · Tab 换页 · Enter 操作 · Esc 返回"
	if p.form != "" {
		hint = " ←→ 侧栏/内容 · ↑↓ 换字段 · Enter 下一步 / 末项提交 · Esc 取消"
	}
	u.line(0, h-1, w, hint, hintStyle)

	top := 2
	for i, line := range lines {
		if top+i >= h-1 {
			break
		}
		u.line(sideW+2, top+i, max(1, w-sideW-3), line, base)
	}
	return top
}

// panelCountOf is the number shown beside each sidebar section.
func (u *ui) panelCountOf(tab int) int {
	switch tab {
	case 0:
		return len(u.snapshot.Panes)
	case 1:
		return len(u.panelEdges())
	case 2:
		return len(u.snapshot.Tasks)
	case 3:
		return len(u.snapshot.Layers)
	default:
		return len(u.panel.catalog.Roles)
	}
}
