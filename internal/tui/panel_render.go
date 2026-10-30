package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v3"
	"github.com/rivo/uniseg"
	"wtable/internal/model"
)

func (u *ui) drawPanel() {
	p := u.panel
	w, h := u.screen.Size()
	width := max(1, min(90, w-4)-2)
	capacity := max(1, h-8)
	tabs := []string{}
	for i, name := range panelTabs {
		if i == p.tab {
			name = "[" + name + "]"
		}
		tabs = append(tabs, name)
	}
	lines := []string{strings.Join(tabs, "   ") + " · Tab 切页 · Esc 返回"}
	p.rows = map[int]int{}
	if p.busy {
		lines = append(lines, "处理中，请稍候；底层输出与权限请求仍可显示")
		if p.form != "" {
			lines = append(lines, "提交失败时会保留表单")
		}
		u.modal(lines)
		return
	}
	if p.preview != "" {
		lines = []string{"查看输出 / 身份说明 / 诊断 · ↑↓/PgUp/PgDn 滚动 · Esc 返回"}
		var body []string
		for _, line := range strings.Split(p.preview, "\n") {
			body = append(body, wrap(line, width)...)
		}
		p.scroll = min(p.scroll, max(0, len(body)-capacity))
		lines = append(lines, body[p.scroll:min(len(body), p.scroll+capacity)]...)
		u.modal(lines)
		return
	}
	if p.confirmKind != "" {
		if p.confirmKind == "pane" {
			lines = append(lines, "移除 "+u.paneName(p.confirmID)+"？进程将停止，相关排队任务会取消。", "任务、报告和身份说明历史保留。")
		} else if p.confirmKind == "role" {
			lines = append(lines, "删除角色模板 "+p.confirmID+"？已运行实例的角色快照保持不变。")
		} else {
			var e model.Edge
			_ = json.Unmarshal([]byte(p.confirmID), &e)
			lines = append(lines, "删除 "+u.paneName(e.From)+" → "+u.paneName(e.To)+"？", "该方向排队任务会取消，已执行操作不会撤销。")
		}
		lines = append(lines, "按 y 确认；n / Esc 取消；Enter 不确认")
		u.modal(lines)
		return
	}
	if p.form != "" {
		title := map[string]string{"agent": "添加 Agent", "edit": "编辑 Agent（命令和目录变更请重新创建）", "send": "派发任务", "edge": "添加单向授权边", "layer": "创建 Git Layer", "role": "编辑角色模板（启动时快照指令）"}[p.form]
		lines = append(lines, title+" · Tab 切字段 · ←→ 选项 · Space 多选 · Enter 提交")
		start := max(0, p.field-capacity+1)
		for i := start; i < min(len(p.fields), start+capacity); i++ {
			f := &p.fields[i]
			marker := "  "
			if i == p.field {
				marker = "› "
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
			available := max(1, width-uniseg.StringWidth(marker+f.label+": "))
			pieces := wrap(value, available)
			if len(pieces) > 1 {
				value = pieces[0]
				if i == p.field {
					for _, piece := range pieces {
						if strings.Contains(piece, "▏") {
							value = "…" + piece
							break
						}
					}
				}
			}
			lines = append(lines, marker+f.label+": "+value)
		}
		lines = append(lines, fmt.Sprintf("字段 %d/%d · Esc 取消 · 任务文本支持 Alt-Enter 换行", p.field+1, len(p.fields)))
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
		u.modal(lines)
		return
	}
	if p.roleView {
		u.drawRoles()
		return
	}
	actions := []string{"a 添加 · e 编辑 · d 移除 · Enter 单看 · p 驻留 · v peek · b 重发 · B 说明 · O 角色库", "a 添加边 · x 删除边 · t 当前 pane 过滤（人类能力不作为可删边）", "s 派活 · Enter 详情 · r 重发 · x 取消 · b 报告收件箱", "n 新建 Layer · Enter 设为新 pane 默认目录"}
	lines = append(lines, actions[p.tab])
	if p.tab == 0 && len(u.snapshot.Panes) > 0 {
		selected := u.snapshot.Panes[min(p.selected, len(u.snapshot.Panes)-1)]
		lines = append(lines, "输出："+truncateColumns(selected.LastOutput, 80), "活动："+selected.LastActivity.Local().Format("15:04:05")+" · 就绪："+selected.Readiness+" · "+selected.ReadinessReason,
			fmt.Sprintf("账号：%s · 端点：%s · 越权尝试 %d 次 · Role：%s", selected.Account, selected.Endpoint, selected.DeniedCount, selected.Role))
		lines = append(lines, u.conflictWarnings(selected.Cwd, selected.Layer, selected.Account)...)
		if selected.Endpoint != "" && selected.Endpoint != "official" {
			lines = append(lines, "非官方端点：工具调用 / 流式可能降级")
		}
		capacity = max(1, h-len(lines)-5)
	}
	var rows []string
	switch p.tab {
	case 0:
		for _, pane := range u.snapshot.Panes {
			brief := ""
			if pane.InjectBrief {
				brief = " · brief:"
				brief += "未发送"
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
			rows = append(rows, fmt.Sprintf("%s%s [%s] %s → %s %s", unread, shortTask(t.ID), t.State, u.paneName(t.From), u.paneName(t.To), strings.SplitN(t.Prompt, "\n", 2)[0]))
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
		marker := "  "
		if i == p.selected {
			marker = "› "
		}
		lines = append(lines, marker+rows[i])
	}
	if len(rows) == 0 {
		lines = append(lines, "暂无条目")
	}
	top := (h-min(len(lines)+2, max(1, h-2)))/2 + 1 + rowStart
	for i := start; i < min(len(rows), start+capacity); i++ {
		p.rows[top+i-start] = i
	}
	u.modal(lines)
}

func (u *ui) panelMouse(e *tcell.EventMouse, pressed bool) {
	p := u.panel
	if p.busy {
		return
	}
	if p.confirmKind != "" || p.form != "" {
		return
	}
	x, y := e.Position()
	w, _ := u.screen.Size()
	width := min(90, max(1, w-4))
	left := (w - width) / 2
	if x < left || x >= left+width {
		return
	}
	if p.preview != "" {
		if e.Buttons()&tcell.WheelUp != 0 {
			p.scroll = max(0, p.scroll-3)
		}
		if e.Buttons()&tcell.WheelDown != 0 {
			p.scroll += 3
		}
		return
	}
	if e.Buttons()&tcell.WheelUp != 0 {
		p.selected = max(0, p.selected-1)
	}
	if e.Buttons()&tcell.WheelDown != 0 {
		p.selected = min(max(0, u.panelCount()-1), p.selected+1)
	}
	if pressed {
		if index, ok := p.rows[y]; ok {
			p.selected = index
		}
	}
}
