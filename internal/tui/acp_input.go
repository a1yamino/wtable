package tui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v3"
	"wtable/internal/model"
)

func (u *ui) acpInput(id string) *lineEdit {
	if u.acpInputs == nil {
		u.acpInputs = map[string]*lineEdit{}
	}
	if u.acpInputs[id] == nil {
		u.acpInputs[id] = &lineEdit{}
	}
	return u.acpInputs[id]
}

// queueBarVisible is true while the pane has queued work or the just-sent chat
// turn has not reached a final state.
func (u *ui) queueBarVisible(p model.Pane) bool {
	if len(u.queuedForPane(p.ID)) > 0 {
		return true
	}
	if t := u.lastSentChat(p.ID); t != nil {
		return !finalTaskState(t.State)
	}
	return false
}

func finalTaskState(state string) bool {
	return state == "completed" || state == "failed" || state == "cancelled" || state == "unknown"
}

func (u *ui) composerHeight(p model.Pane) int {
	if p.Backend != "acp" {
		// PTY panes keep their own terminal, but a queue bar still appears
		// while work is waiting, using the same component as ACP.
		if len(u.queuedForPane(p.ID)) > 0 {
			return 1
		}
		return 0
	}
	if u.queueBarVisible(p) {
		return 3
	}
	return 2
}

func (u *ui) lastSentChat(paneID string) *model.Task {
	id := u.lastSent[paneID]
	if id == "" {
		return nil
	}
	t, ok := u.taskByID(id)
	if !ok {
		return nil
	}
	return &t
}

func chatStateLabel(state string) string {
	switch state {
	case "queued":
		return "排队中"
	case "delivered":
		return "已投递"
	case "accepted", "running":
		return "已接手"
	case "completed":
		return "完成"
	case "failed":
		return "失败"
	case "cancelled":
		return "已取消"
	case "unknown":
		return "结果未确认"
	}
	return state
}

// composerHint is the one-line answer to "what does Enter do right now?".
func (u *ui) composerHint(p model.Pane) string {
	if u.interruptConfirm {
		return "将中断当前 turn，未完成的工作可能丢失；Enter 确认 · Esc 取消"
	}
	if restartable(p.State) {
		return "未运行 · Enter 重启 · Esc 清空 · Ctrl-a Space 命令"
	}
	queue := len(u.queuedForPane(p.ID))
	if p.TurnState == "waiting-permission" {
		return "等待审批：Enter 不发送 · Ctrl-a p 处理 · Ctrl-J 换行 · Esc 清空"
	}
	if queue > 0 || p.TurnState == "running" {
		return fmt.Sprintf("Enter 排队（第 %d 位）· Ctrl-J 换行 · Ctrl-a Enter 中断后发送", queue+1)
	}
	return "Enter 立即发送 · Ctrl-J 换行 · Esc 清空草稿"
}

func (u *ui) composerKey(e *tcell.EventKey, p *model.Pane) {
	input := u.acpInput(p.ID)
	// Ctrl-J and legacy Alt-Enter insert a newline; Enter alone submits.
	altEnter := e.Modifiers()&tcell.ModAlt != 0 && (e.Key() == tcell.KeyEnter || (e.Key() == tcell.KeyRune && e.Str() == "m" && e.Modifiers()&tcell.ModCtrl != 0))
	ctrlJ := e.Key() == tcell.KeyCtrlJ || (e.Key() == tcell.KeyRune && e.Str() == "j" && e.Modifiers()&tcell.ModCtrl != 0)
	if altEnter || ctrlJ {
		input.Insert("\n")
		return
	}
	if e.Key() == tcell.KeyEnter {
		if restartable(p.State) {
			u.restartCurrent()
			return
		}
		if p.TurnState == "waiting-permission" {
			u.status = "等待审批：请先按 Ctrl-a p 处理"
			return
		}
		u.sendChat(p)
		return
	}
	if e.Key() == tcell.KeyEscape {
		input.Set("")
		u.interruptConfirm = false
		return
	}
	input.Handle(e)
}

func (u *ui) sendChat(p *model.Pane) {
	input := u.acpInput(p.ID)
	text := input.Text()
	if strings.TrimSpace(text) == "" {
		return
	}
	if p.State != "running" {
		u.status = "pane 未运行；Enter 重启"
		return
	}
	if p.TurnState == "waiting-permission" {
		u.status = "等待审批：Ctrl-a p 处理"
		return
	}
	position := len(u.queuedForPane(p.ID)) + 1
	var task model.Task
	if !u.call("task.send", model.SendParams{To: p.ID, Prompt: text, Kind: "chat", DeliveryMode: "prompt", Queue: true}, &task) {
		return
	}
	input.Remember("acp")
	input.Begin("acp", "")
	if u.lastSent == nil {
		u.lastSent = map[string]string{}
	}
	u.lastSent[p.ID] = task.ID
	if task.State == "queued" {
		u.status = fmt.Sprintf("已排队：第 %d 位", position)
	} else {
		u.status = "已发送 → " + p.Name + "：" + chatStateLabel(task.State)
	}
	_, _ = u.refresh()
}

// interruptCurrent implements Ctrl-a Enter: first press explains, second
// confirms. The server performs cancel-then-queue atomically.
func (u *ui) interruptCurrent() {
	if u.area != focusMain && u.area != focusUtility {
		return
	}
	p := u.current()
	if p == nil {
		return
	}
	if p.Backend != "acp" {
		u.status = "中断后发送用于 ACP worker；PTY 请用 Ctrl-a t 的任务表单"
		return
	}
	if !u.interruptConfirm {
		u.interruptConfirm = true
		u.status = "将中断当前 turn，未完成的工作可能丢失；Enter 确认 · Esc 取消"
		return
	}
	u.interruptConfirm = false
	input := u.acpInput(p.ID)
	text := input.Text()
	var task model.Task
	if !u.call("task.interrupt_send", map[string]string{"to": p.ID, "prompt": text, "kind": "chat"}, &task) {
		return
	}
	input.Begin("acp", "")
	if task.ID != "" {
		u.lastSent[p.ID] = task.ID
		u.status = "已中断当前 turn，新消息已排队"
	} else {
		u.status = "已中断当前 turn"
	}
	_, _ = u.refresh()
}

// acpMessageLines is the structured chat stream plus one synthetic delivery
// chip for the most recent user message.
func (u *ui) acpMessageLines(p model.Pane, width int) []string {
	lines := messageLines(p, width)
	if t := u.lastSentChat(p.ID); t != nil {
		lines = append(lines, "", "· "+chatStateLabel(t.State)+" · "+firstLineText(t.Prompt))
	}
	return lines
}

func firstLineText(text string) string {
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		text = text[:i]
	}
	return strings.TrimSpace(text)
}

func (u *ui) acpDrawKey() string {
	var b strings.Builder
	for _, p := range u.snapshot.Panes {
		if p.Backend == "acp" {
			fmt.Fprint(&b, p.ID, u.acpInput(p.ID).Display())
		}
	}
	return b.String()
}

func (u *ui) panelDrawKey() string {
	p := u.panel
	if p == nil {
		return ""
	}
	return fmt.Sprint(p.tab, p.selected, p.field, p.scroll, p.form, p.targetID, p.confirmKind, p.confirmID, p.preview, p.busy, p.related, p.catalog, p.fields, p.roleView, p.layerReturn, p.presetCursor, p.focusSide, u.activeLayer)
}

// overlayDrawKey covers the modal views drawn on top of the panes. Their
// cursors and viewports are plain struct fields, so the draw cache must observe
// the values, not just pointer identity.
func (u *ui) overlayDrawKey() string {
	var b strings.Builder
	if a := u.actions; a != nil {
		fmt.Fprintf(&b, "actions:%s:%d:%d;", a.title, a.cursor, len(a.items))
	}
	if g := u.graph; g != nil {
		fmt.Fprintf(&b, "graph:%+v;", *g)
	}
	if p := u.palette; p != nil {
		fmt.Fprintf(&b, "palette:%s:%d;", p.filter, p.cursor)
	}
	if c := u.create; c != nil {
		fmt.Fprintf(&b, "create:%s:%d:%s;", c.mode, c.cursor, c.reason)
	}
	if q := u.queueView; q != nil {
		fmt.Fprintf(&b, "queue:%s:%d;", q.paneID, q.selected)
	}
	fmt.Fprintf(&b, "rail:%s:%s:%t:%v;area:%d;approval:%+v;canvas:%+v;interrupt:%t;chat:%t;",
		u.rail.sel, u.rail.filter, u.canvas.drawer, u.rail.dismissed, u.area, u.approval, u.canvas, u.interruptConfirm, u.allowChat)
	return b.String()
}
