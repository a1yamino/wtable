package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v3"
	"wtable/internal/model"
)

func (u *ui) reports() []model.Report {
	reports := append([]model.Report{}, u.snapshot.Reports...)
	sort.SliceStable(reports, func(i, j int) bool { return reports[i].CreatedAt.After(reports[j].CreatedAt) })
	return reports
}
func (u *ui) reportKey(e *tcell.EventKey) {
	if e.Key() == tcell.KeyEscape {
		if u.reportDetail != nil {
			u.reportDetail = nil
			u.taskScroll = 0
		} else {
			u.taskView = false
		}
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
	if u.reportDetail != nil {
		u.taskScroll = max(0, u.taskScroll+delta)
		if e.Key() == tcell.KeyEnter {
			u.readTask(u.reportDetail.ParentTask)
			if u.taskDetail != nil {
				u.reportDetail = nil
				u.reportView = false
			}
		}
		return
	}
	reports := u.reports()
	if len(reports) == 0 {
		return
	}
	u.reportCursor = max(0, min(len(reports)-1, u.reportCursor+delta))
	if e.Key() == tcell.KeyEnter {
		var report model.Report
		if u.call("report.get", map[string]string{"id": reports[u.reportCursor].ID}, &report) {
			u.reportDetail = &report
			u.taskScroll = 0
			u.call("report.seen", map[string]string{"id": report.ID}, nil)
		}
	}
}
func (u *ui) drawReports() {
	w, h := u.screen.Size()
	capacity := max(1, h-6)
	width := max(1, min(90, w-4)-2)
	lines := []string{"报告收件箱 · 通知不执行 · ↑↓ 选择 · Enter 详情 · Tab 任务 · Esc 返回"}
	if r := u.reportDetail; r != nil {
		body := []string{fmt.Sprintf("%s → %s [%s / %s]", u.paneName(r.From), u.paneName(r.To), r.Mode, r.State), "原任务：" + r.ParentTask, "后续任务：" + r.HandoffTask, r.Error, "", r.Summary}
		var wrapped []string
		for _, line := range body {
			for _, part := range strings.Split(line, "\n") {
				wrapped = append(wrapped, wrap(part, width)...)
			}
		}
		u.taskScroll = min(u.taskScroll, max(0, len(wrapped)-capacity))
		lines = []string{"报告详情 · Enter 查看原任务（需权限） · ↑↓ 滚动 · Esc 返回"}
		lines = append(lines, wrapped[u.taskScroll:min(len(wrapped), u.taskScroll+capacity)]...)
	} else {
		reports := u.reports()
		u.reportCursor = min(u.reportCursor, max(0, len(reports)-1))
		if len(reports) == 0 {
			lines = append(lines, "暂无报告")
		}
		start := max(0, u.reportCursor-capacity+1)
		for i := start; i < min(len(reports), start+capacity); i++ {
			r := reports[i]
			marker := "  "
			if !r.Seen {
				marker = "• "
			}
			if i == u.reportCursor {
				marker = "› "
			}
			lines = append(lines, fmt.Sprintf("%s%s → %s [%s / %s] %s", marker, u.paneName(r.From), u.paneName(r.To), r.Mode, r.State, strings.SplitN(r.Summary, "\n", 2)[0]))
		}
	}
	u.modal(lines)
}
