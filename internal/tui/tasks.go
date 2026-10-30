package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v3"
	"wtable/internal/model"
)

func (u *ui) paneName(id string) string {
	if id == "controller" {
		return "你"
	}
	for _, p := range u.snapshot.Panes {
		if p.ID == id {
			return p.Name
		}
	}
	return id
}
func shortTask(id string) string {
	return strings.TrimPrefix(id, "t-")[:min(6, len(strings.TrimPrefix(id, "t-")))]
}
func (u *ui) orderedTasks() []model.Task {
	tasks := append([]model.Task{}, u.snapshot.Tasks...)
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].UpdatedAt.Equal(tasks[j].UpdatedAt) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt)
	})
	return tasks
}
func (u *ui) pickedTask(tasks []model.Task) int {
	for i, t := range tasks {
		if t.ID == u.taskCursor {
			return i
		}
	}
	return 0
}
func (u *ui) openTasks() {
	_, _ = u.refresh()
	u.taskView = true
	u.reportView = false
	u.reportDetail = nil
	u.taskScreen = nil
	u.taskDetail = nil
	u.taskScroll = 0
	u.selection = nil
	tasks := u.orderedTasks()
	if len(tasks) > 0 {
		u.taskCursor = tasks[0].ID
	}
}
func (u *ui) moveTask(delta int) {
	tasks := u.orderedTasks()
	if len(tasks) == 0 {
		return
	}
	i := max(0, min(len(tasks)-1, u.pickedTask(tasks)+delta))
	u.taskCursor = tasks[i].ID
}
func (u *ui) readTask(id string) {
	var t model.Task
	if u.call("task.get", map[string]string{"id": id}, &t) {
		u.taskDetail = &t
		u.call("task.seen", map[string]string{"id": id}, nil)
		u.taskCursor = id
		u.taskScroll = 0
	}
}
func (u *ui) cancelPickedTask() {
	tasks := u.orderedTasks()
	if len(tasks) == 0 {
		return
	}
	t := tasks[u.pickedTask(tasks)]
	if u.taskDetail != nil {
		t = *u.taskDetail
	}
	if t.State != "queued" && t.State != "running" && t.State != "delivered" && t.State != "accepted" && t.State != "unknown" && t.State != "cancelling" {
		u.status = "此任务当前不可取消：" + t.State
		return
	}
	var result model.Task
	if u.call("task.cancel", map[string]string{"id": t.ID}, &result) {
		u.status = "任务 " + shortTask(t.ID) + "：" + result.State
		if u.taskDetail != nil {
			u.taskDetail = &result
		}
		_, _ = u.refresh()
	}
}
func (u *ui) taskKey(e *tcell.EventKey) {
	if u.taskScreen != nil {
		if e.Key() == tcell.KeyEscape {
			u.taskScreen = nil
		}
		return
	}
	if e.Key() == tcell.KeyTab && u.taskDetail == nil && u.reportDetail == nil {
		u.reportView = !u.reportView
		u.reportCursor = 0
		u.taskScroll = 0
		return
	}
	if u.reportView {
		u.reportKey(e)
		return
	}
	if e.Key() == tcell.KeyEscape {
		if u.taskDetail != nil {
			u.taskDetail = nil
			u.taskScroll = 0
		} else {
			u.taskView = false
		}
		return
	}
	if e.Str() == "c" || e.Str() == "x" {
		u.cancelPickedTask()
		return
	}
	delta := 0
	switch e.Key() {
	case tcell.KeyUp:
		delta = -1
	case tcell.KeyDown:
		delta = 1
	case tcell.KeyPgUp:
		delta = -8
	case tcell.KeyPgDn:
		delta = 8
	}
	if u.taskDetail != nil {
		u.taskScroll = max(0, u.taskScroll+delta)
		u.taskAction(e.Str())
	} else {
		u.moveTask(delta)
		if e.Key() == tcell.KeyEnter {
			tasks := u.orderedTasks()
			if len(tasks) > 0 {
				u.readTask(tasks[u.pickedTask(tasks)].ID)
			}
		}
	}
}
func (u *ui) drawTasks() {
	if u.taskScreen != nil {
		u.drawTaskScreen()
		return
	}
	if u.reportView {
		u.drawReports()
		return
	}
	w, h := u.screen.Size()
	width := max(1, min(90, max(1, w-4))-2)
	capacity := max(1, h-6)
	lines := []string{"任务 · ↑↓ 选择 · Enter 详情 · c 取消 · Tab 报告 · Esc 返回"}
	u.taskRows = map[int]string{}
	if t := u.taskDetail; t != nil {
		body := []string{fmt.Sprintf("%s  %s → %s  [%s]", t.ID, u.paneName(t.From), u.paneName(t.To), t.State), "创建：" + t.CreatedAt.Format("01-02 15:04:05") + "  更新：" + t.UpdatedAt.Format("01-02 15:04:05"), "", "投递：" + t.DeliveryMode + " / " + t.DeliveryStatus, "投递说明：" + t.DeliveryError,
			"完成后：" + t.OnComplete + " → " + u.paneName(t.ReportTo), "上级：" + t.ParentTask + "  重试自：" + t.RetryOf,
			"r 重发 · a 改派 · v 屏幕 · i 原始输入 · m 确认送达", "d 标记完成 · f 标记失败 · c 请求取消 · C 确认已停止",
			"任务内容", t.Prompt, "", "结果", t.Result}
		var wrapped []string
		for _, line := range body {
			for _, part := range strings.Split(line, "\n") {
				wrapped = append(wrapped, wrap(part, width)...)
			}
		}
		u.taskScroll = min(u.taskScroll, max(0, len(wrapped)-capacity))
		lines = []string{fmt.Sprintf("任务详情 · ↑↓/PgUp/PgDn 滚动 · c 取消 · r 重发 · Esc 列表 (%d/%d)", u.taskScroll+1, max(1, len(wrapped)))}
		lines = append(lines, wrapped[u.taskScroll:min(len(wrapped), u.taskScroll+capacity)]...)
	} else {
		tasks := u.orderedTasks()
		if len(tasks) == 0 {
			lines = append(lines, "暂无任务：关闭列表，聚焦目标 pane，按 Ctrl-a t 发送")
		}
		selected := u.pickedTask(tasks)
		start := max(0, selected-capacity+1)
		for i := start; i < min(len(tasks), start+capacity); i++ {
			t := tasks[i]
			marker := "  "
			if !t.Seen && (t.State == "completed" || t.State == "failed" || t.State == "unknown") {
				marker = "• "
			}
			if i == selected {
				marker = "› "
			}
			prompt := strings.SplitN(t.Prompt, "\n", 2)[0]
			lines = append(lines, fmt.Sprintf("%s%s [%s] %s → %s  %s", marker, shortTask(t.ID), t.State, u.paneName(t.From), u.paneName(t.To), prompt))
		}
		top := (h-min(len(lines)+2, max(1, h-2)))/2 + 2
		for i := start; i < min(len(tasks), start+capacity); i++ {
			u.taskRows[top+i-start] = tasks[i].ID
		}
	}
	u.modal(lines)
}

func restartable(state string) bool {
	return state == "exited" || state == "failed" || state == "interrupted"
}
func (u *ui) restartCurrent() {
	p := u.current()
	if p == nil {
		return
	}
	if !restartable(p.State) {
		u.status = "当前 pane 未退出，无需重启"
		return
	}
	var result model.Pane
	if u.call("pane.restart", map[string]string{"id": p.ID}, &result) {
		delete(u.sizes, p.ID)
		u.offsets[p.ID] = 0
		u.copyMode = false
		u.selection = nil
		u.status = "已原地重启 " + p.Name + "（执行原启动命令）"
		_, _ = u.refresh()
		u.resize()
	}
}
