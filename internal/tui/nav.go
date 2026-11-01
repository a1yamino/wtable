package tui

import (
	"fmt"
	"time"

	"wtable/internal/model"
)

// One glyph, one meaning. Approval, unread and exit each get their own symbol
// so no single ⚠ has to carry three jobs.
const (
	glyphStarting = "◌"
	glyphIdle     = "●"
	glyphRunning  = "◐"
	glyphApproval = "⏳"
	glyphQueue    = "⏸"
	glyphUnread   = "◉"
	glyphExit     = "✕"
)

func stateGlyph(state, turnState string) string {
	switch {
	case state == "failed":
		return glyphExit
	case state == "exited" || state == "interrupted":
		return glyphExit
	case turnState == "running":
		return glyphRunning
	case state == "running":
		return glyphIdle
	default:
		return glyphStarting
	}
}

// railSignal is the one signal a rail row shows: approval > exit > unread > queue.
func (u *ui) railSignal(p model.Pane) (string, string) {
	if len(u.permitsFor(p.ID)) > 0 {
		return glyphApproval, "等待审批"
	}
	if p.State == "failed" || p.State == "exited" || p.State == "interrupted" {
		return glyphExit, "已退出"
	}
	if p.Unread > 0 {
		return glyphUnread, "有未读"
	}
	if n := len(u.queuedForPane(p.ID)); n > 0 {
		return glyphQueue, fmt.Sprintf("%d 条排队", n)
	}
	switch p.TurnState {
	case "running":
		return glyphRunning, "执行中"
	case "waiting-permission":
		return glyphApproval, "等待审批"
	}
	if p.State == "running" {
		return glyphIdle, "空闲"
	}
	return glyphStarting, "启动中"
}

// stateText is the human word for a pane's state; no protocol names.
func stateText(p model.Pane) string {
	if p.TurnState == "running" {
		return "执行中"
	}
	switch p.State {
	case "running":
		return "运行中"
	case "starting":
		return "启动中"
	case "failed":
		return "失败"
	case "exited":
		return "已退出"
	case "interrupted":
		return "已中断"
	case "stopping":
		return "停止中"
	}
	return p.State
}

func relativeAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// legendOnce explains a symbol the first time the current session shows it.
func (u *ui) legendOnce(glyph string) {
	if u.seenGlyphs == nil {
		u.seenGlyphs = map[string]bool{}
	}
	if u.seenGlyphs[glyph] {
		return
	}
	u.seenGlyphs[glyph] = true
	switch glyph {
	case glyphApproval:
		u.status = "⏳ 等待审批：按 Ctrl-a p 处理；平时仍可直接输入，Esc 收起卡片"
	case glyphUnread:
		u.status = "◉ 有未读消息：打开该 agent 后自动清除"
	case glyphExit:
		u.status = "✕ 已退出：在左栏选中后按 Space 重启或移除"
	case glyphQueue:
		u.status = "⏸ 有排队中的消息：点击队列条，或按 Ctrl-a Space 打开队列"
	}
}

func (u *ui) queuedTotal() int {
	count := 0
	for _, t := range u.snapshot.Tasks {
		if t.State == "queued" && t.Kind != "chat" && t.Kind != model.TaskKindBrief {
			count++
		}
	}
	return count
}

func (u *ui) approvalCount() int { return len(u.snapshot.Permissions) }

func (u *ui) unreadTotal() int {
	total := 0
	for _, p := range u.snapshot.Panes {
		total += p.Unread
	}
	for _, t := range u.snapshot.Tasks {
		if t.From == "controller" && t.To != "controller" && !t.Seen && (t.State == "completed" || t.State == "failed" || t.State == "unknown") && t.Kind != "chat" && t.Kind != model.TaskKindBrief {
			total++
		}
	}
	for _, r := range u.snapshot.Reports {
		if r.To == "controller" && !r.Seen {
			total++
		}
	}
	return total
}

// statusHint is the persistent, context-aware key hint on the right.
func (u *ui) statusHint() string {
	switch {
	case u.prefix:
		return "Ctrl-a 前缀：j/k 切 agent · p 审批 · s 工具槽 · a 左栏 · Space 命令面板"
	case u.panel != nil:
		return "←→ 侧栏/内容 · ↑↓ 选择 · Enter 操作 · Tab 换页 · Esc 返回"
	case u.taskView:
		return "↑↓ 选择 · Enter 详情 · Esc 返回"
	case u.graph != nil:
		return "↑↓ 选择 · Enter 操作 · e 节点/边 · Esc 返回"
	case u.palette != nil:
		return "输入筛选 · ↑↓ 选择 · Enter 执行 · Esc 取消"
	case u.create != nil:
		return "↑↓ 选择 · Enter 创建 · Esc 取消"
	case u.queueView != nil:
		return "↑↓ 选择 · Enter 提到最前 · e 编辑 · x 取消 · Esc 返回"
	case u.area == focusRail:
		return "↑↓ 选择 · Enter 打开 · Space 动作 · n 新建 · p 工具槽 · x 移除 · Esc 返回"
	case u.area == focusApproval:
		return "1..9 选择 · r 拒绝 · d 稍后处理 · [ ] 切换 · Enter 详情 · Esc 收起"
	case u.copyMode:
		return "回看模式：↑↓ 滚动 · PgUp/PgDn 翻页 · y 复制 · Esc 返回"
	}
	if p := u.current(); p != nil && p.Backend == "acp" && u.area == focusMain {
		return "Enter 发送或排队 · Ctrl-J 换行 · Ctrl-a p 审批 · Ctrl-a Space 命令面板"
	}
	return "直接输入即可 · Ctrl-a j/k 切 agent · Ctrl-a s 工具槽 · Ctrl-a Space 命令面板"
}

// leftStatus is the fixed left part of the bar: the only global counters that
// matter at a glance.
func (u *ui) leftStatus() string {
	panes := u.railOrder()
	index := 0
	for i, p := range panes {
		if p.ID == u.canvas.main {
			index = i + 1
			break
		}
	}
	text := fmt.Sprintf("[rail] agent %d/%d", index, len(panes))
	if n := u.approvalCount(); n > 0 {
		text += fmt.Sprintf(" · ⏳%d", n)
	}
	if n := u.unreadTotal(); n > 0 {
		text += fmt.Sprintf(" · ◉%d", n)
	}
	return text
}
