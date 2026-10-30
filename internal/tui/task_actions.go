package tui

import (
	"fmt"
	"strings"

	"wtable/internal/model"
)

func (u *ui) taskAction(key string) {
	t := u.taskDetail
	if t == nil {
		return
	}
	u.taskActionID = t.ID
	switch key {
	case "r":
		var retry model.Task
		if u.call("task.retry", map[string]string{"id": t.ID}, &retry) {
			u.status = "已创建重试任务 " + shortTask(retry.ID)
			u.readTask(retry.ID)
		}
	case "a":
		u.openInput("task-redirect", "")
	case "d":
		u.openInput("task-done", "")
	case "f":
		u.openInput("task-fail", "")
	case "C":
		u.openInput("task-confirm-stop", "")
	case "i":
		u.openInput("task-raw", "")
	case "m":
		var updated model.Task
		if u.call("task.confirm", map[string]string{"id": t.ID}, &updated) {
			u.taskDetail = &updated
			u.status = "已人工确认送达，仍等待 ack / result"
		}
	case "v":
		var frame model.Frame
		if u.call("pane.screen", map[string]string{"id": t.To}, &frame) {
			u.taskScreen = &frame
		}
	}
}

func (u *ui) submitTaskAction(mode, input string) bool {
	id := u.taskActionID
	var t model.Task
	switch mode {
	case "task-done", "task-fail":
		if strings.TrimSpace(input) == "" {
			u.status = "请输入实际结果或原因"
			return false
		}
		if !u.call("task.done", map[string]any{"id": id, "result": input, "failed": mode == "task-fail"}, &t) {
			return false
		}
	case "task-redirect":
		if strings.TrimSpace(input) == "" {
			u.status = "请输入目标 pane 名称"
			return false
		}
		if !u.call("task.retry", map[string]string{"id": id, "to": strings.TrimSpace(input)}, &t) {
			return false
		}
	case "task-confirm-stop":
		if input != "y" {
			u.status = "确认已停止后输入 y；Esc 返回"
			return false
		}
		if !u.call("task.cancel", map[string]any{"id": id, "confirmed": true}, &t) {
			return false
		}
	case "task-raw":
		if !u.call("task.get", map[string]string{"id": id}, &t) {
			return false
		}
		data := strings.NewReplacer("<Enter>", "\r", "<Esc>", "\x1b", "<Ctrl-C>", "\x03", "<Up>", "\x1b[A", "<Down>", "\x1b[B", "<Left>", "\x1b[D", "<Right>", "\x1b[C").Replace(input)
		if !u.call("pane.input", map[string]string{"id": t.To, "data": data}, nil) {
			return false
		}
		u.status = "已发送原始输入；任务状态未自动更改"
		return true
	}
	u.taskDetail = &t
	u.taskCursor = t.ID
	u.status = fmt.Sprintf("任务 %s：%s", shortTask(t.ID), t.State)
	_, _ = u.refresh()
	return true
}

func (u *ui) drawTaskScreen() {
	w, h := u.screen.Size()
	lines := []string{"目标终端的一次性快照 · Esc 返回（不据此判定完成）"}
	for _, row := range u.taskScreen.Cells {
		var b strings.Builder
		for _, c := range row {
			if c.Width > 0 {
				b.WriteString(c.Text)
			}
		}
		lines = append(lines, wrap(strings.TrimRight(b.String(), " "), max(1, min(90, w-4)-2))...)
	}
	maxLines := max(1, h-5)
	if len(lines) > maxLines {
		lines = append(lines[:1], lines[len(lines)-maxLines+1:]...)
	}
	u.modal(lines)
}
