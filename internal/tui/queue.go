package tui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v3"
	"wtable/internal/model"
)

type queueView struct {
	paneID   string
	selected int
}

func (u *ui) openQueue(paneID string) {
	tasks := u.queuedForPane(paneID)
	if len(tasks) == 0 {
		u.status = "当前没有排队消息"
		return
	}
	u.queueView = &queueView{paneID: paneID}
	u.selection = nil
}

func (u *ui) queueTasks() []model.Task {
	if u.queueView == nil {
		return nil
	}
	return u.queuedForPane(u.queueView.paneID)
}

func (u *ui) queueKey(e *tcell.EventKey) {
	q := u.queueView
	if q == nil {
		return
	}
	tasks := u.queueTasks()
	if e.Key() == tcell.KeyEscape {
		u.queueView = nil
		return
	}
	if len(tasks) == 0 {
		u.queueView = nil
		return
	}
	q.selected = max(0, min(q.selected, len(tasks)-1))
	switch e.Key() {
	case tcell.KeyUp:
		q.selected = max(0, q.selected-1)
		return
	case tcell.KeyDown:
		q.selected = min(len(tasks)-1, q.selected+1)
		return
	case tcell.KeyEnter:
		t := tasks[q.selected]
		if u.call("task.promote", map[string]string{"id": t.ID}, nil) {
			u.status = "已提前 " + shortTask(t.ID)
			q.selected = 0
			_, _ = u.refresh()
		}
		return
	}
	if e.Key() == tcell.KeyRune {
		t := tasks[q.selected]
		switch e.Str() {
		case "x":
			if u.call("task.cancel", map[string]string{"id": t.ID}, nil) {
				u.status = "已取消排队消息 " + shortTask(t.ID)
				_, _ = u.refresh()
			}
			return
		case "e":
			u.taskActionID = t.ID
			u.queueView = nil
			u.openInput("queue-edit", t.Prompt)
			return
		case "r":
			_, _ = u.refresh()
			return
		}
	}
}

func (u *ui) drawQueue() {
	q := u.queueView
	if q == nil {
		return
	}
	tasks := u.queueTasks()
	pane := u.paneByID(q.paneID)
	name := q.paneID
	if pane != nil {
		name = pane.Name
	}
	lines := []string{fmt.Sprintf("队列 · %s · ↑↓ 选择 · Enter 提前 · e 编辑 · x 取消 · Esc 返回", name)}
	if len(tasks) == 0 {
		lines = append(lines, "队列已空")
	}
	q.selected = max(0, min(q.selected, max(0, len(tasks)-1)))
	for i, t := range tasks {
		marker := "  "
		if i == q.selected {
			marker = "› "
		}
		lines = append(lines, fmt.Sprintf("%s%d. %s  [%s]", marker, i+1, truncateColumns(strings.ReplaceAll(t.Prompt, "\n", " "), 54), chatStateLabel(t.State)))
	}
	lines = append(lines, "", "发送后会按顺序投递；中断请用 Ctrl-a Enter（两段确认）")
	u.modal(lines)
}
