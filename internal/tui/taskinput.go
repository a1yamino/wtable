package tui

import (
	"fmt"
	"strings"

	"wtable/internal/model"
)

// The PTY task form uses the same one-line mode bar as the ACP composer:
// Enter always does what the bar says, and only the destructive interrupt path
// needs a second confirmation.
func (u *ui) openTaskComposer() {
	p := u.current()
	if p == nil {
		u.status = "没有可派活的目标"
		return
	}
	u.taskDelivery = ""
	u.interruptConfirm = false
	u.openInput("task", "")
	u.status = "派活 → " + p.Name
}

func (u *ui) paneBusyForTask(id string) bool {
	if p := u.paneByID(id); p != nil && p.TurnState == "running" {
		return true
	}
	for _, t := range u.snapshot.Tasks {
		if t.Kind == model.TaskKindBrief {
			continue
		}
		if t.To == id && (t.State == "delivered" || t.State == "accepted" || t.State == "running" || t.State == "cancelling") {
			return true
		}
	}
	return false
}

func (u *ui) taskModeOptions(p *model.Pane) []string {
	busy := u.paneBusyForTask(p.ID)
	out := []string{""}
	if p.EffectiveCaps().PushSteer && busy {
		out = append(out, "steer")
	}
	if busy {
		out = append(out, "interrupt")
	}
	return out
}

func (u *ui) taskModeLabel(p *model.Pane, mode string) string {
	switch mode {
	case "steer":
		return "插话（追加到当前 turn）"
	case "interrupt":
		return "中断后发送"
	default:
		// An idle pane takes the task immediately; only a busy one actually
		// leaves it in the queue, so do not promise "排队" when it will not.
		if p != nil && u.paneBusyForTask(p.ID) {
			return "排队"
		}
		return "发送"
	}
}

func (u *ui) cycleTaskMode(back bool) {
	p := u.current()
	if p == nil {
		return
	}
	modes := u.taskModeOptions(p)
	index := 0
	for i, m := range modes {
		if m == u.taskDelivery {
			index = i
			break
		}
	}
	step := 1
	if back {
		step = -1
	}
	u.taskDelivery = modes[(index+step+len(modes))%len(modes)]
	u.interruptConfirm = false
}

func (u *ui) taskModeLine(p *model.Pane) string {
	if u.interruptConfirm {
		return "将中断当前 turn，未完成的工作可能丢失；Enter 确认 · Esc 取消"
	}
	parts := []string{}
	for _, mode := range u.taskModeOptions(p) {
		label := u.taskModeLabel(p, mode)
		if mode == u.taskDelivery {
			label = "[" + label + "]"
		}
		parts = append(parts, label)
	}
	return "模式 " + strings.Join(parts, " / ") + " · Tab 切换 · Enter 发送 · Ctrl-J 换行 · Esc 取消"
}

func (u *ui) submitTaskComposer(input string) {
	p := u.current()
	if p == nil || strings.TrimSpace(input) == "" {
		u.status = "请输入任务内容"
		return
	}
	if u.taskDelivery == "interrupt" {
		if !u.interruptConfirm {
			u.interruptConfirm = true
			u.status = "将中断当前 turn，未完成的工作可能丢失；Enter 确认 · Esc 取消"
			return
		}
		u.interruptConfirm = false
		var task model.Task
		if !u.call("task.interrupt_send", map[string]string{"to": p.ID, "prompt": input, "kind": "delegation"}, &task) {
			return
		}
		u.editor.Remember("task")
		u.mode = ""
		u.editor.Set("")
		u.status = "已中断当前 turn，并排队新任务"
		_, _ = u.refresh()
		u.resize()
		return
	}
	args := model.SendParams{To: p.ID, Prompt: input, Kind: "delegation", Queue: true}
	if u.taskDelivery == "steer" {
		args.DeliveryMode = "steer"
	}
	var task model.Task
	if !u.call("task.send", args, &task) {
		return
	}
	u.editor.Remember("task")
	u.mode = ""
	u.editor.Set("")
	if task.State == "queued" {
		position := 1
		for _, other := range u.queuedForPane(p.ID) {
			if other.ID != task.ID && other.CreatedAt.Before(task.CreatedAt) {
				position++
			}
		}
		u.status = fmt.Sprintf("已排队：第 %d 位 → %s", position, p.Name)
	} else {
		u.status = "任务 " + shortTask(task.ID) + " → " + p.Name + "：" + task.State
	}
	_, _ = u.refresh()
	u.resize()
}
