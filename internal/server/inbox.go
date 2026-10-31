package server

import (
	"sort"
	"strings"
	"time"

	"wtable/internal/model"
	"wtable/internal/store"
)

// migratePaneKinds assigns foreground/worker once for state written before the
// distinction existed. ACP panes are always workers; the newest PTY pane becomes
// the foreground when none is marked, and everything else stays a worker.
func migratePaneKinds(state *store.State) {
	if state.KindMigrated {
		return
	}
	hasForeground := false
	var newest *model.Pane
	for i := range state.Panes {
		p := &state.Panes[i]
		if p.Kind == model.PaneForeground {
			hasForeground = true
		}
		if p.Backend != "pty" {
			p.Kind = model.PaneWorker
			continue
		}
		if p.Kind == "" && (newest == nil || p.CreatedAt.After(newest.CreatedAt)) {
			newest = p
		}
	}
	if !hasForeground && newest != nil {
		newest.Kind = model.PaneForeground
		hasForeground = true
	}
	for i := range state.Panes {
		p := &state.Panes[i]
		if p.Kind == "" && !hasForeground && p.Backend == "pty" {
			p.Kind = model.PaneForeground
			hasForeground = true
			continue
		}
		if p.Kind == "" {
			p.Kind = model.PaneWorker
		}
	}
	state.KindMigrated = true
}

func paneLabel(p model.Pane) string {
	if strings.TrimSpace(p.Name) != "" {
		return p.Name
	}
	return p.ID
}

func cleanInboxText(text string, limit int) string {
	text = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return ' '
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			return ' '
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	return short(text, limit)
}

// deriveInbox projects durable pane/task/permission state into the single list
// that answers "what needs me now?". It is a view, never a new authority.
func deriveInbox(s model.Snapshot) []model.InboxItem {
	items := []model.InboxItem{}
	panes := map[string]model.Pane{}
	for _, p := range s.Panes {
		panes[p.ID] = p
	}
	name := func(id string) string {
		if p, ok := panes[id]; ok {
			return paneLabel(p)
		}
		return id
	}
	pendingPermission := map[string]bool{}
	for _, perm := range s.Permissions {
		pendingPermission[perm.PaneID] = true
		detail := perm.Preview
		if detail == "" {
			detail = perm.Title
		}
		items = append(items, model.InboxItem{
			ID:           "perm:" + perm.ID,
			Kind:         "permission",
			Severity:     model.InboxBlocking,
			PaneID:       perm.PaneID,
			PermissionID: perm.ID,
			Title:        name(perm.PaneID) + " 请求批准",
			Detail:       cleanInboxText(detail, 160),
			CreatedAt:    perm.CreatedAt,
		})
	}
	for _, p := range s.Panes {
		if p.State == "exited" || p.State == "failed" || p.State == "interrupted" {
			detail := p.Error
			if detail == "" {
				detail = p.ExitSummary
			}
			items = append(items, model.InboxItem{
				ID:        "pane:" + p.ID + ":" + p.State,
				Kind:      "pane",
				Severity:  model.InboxAction,
				PaneID:    p.ID,
				Title:     paneLabel(p) + " 已退出",
				Detail:    cleanInboxText(detail, 160),
				CreatedAt: lastActivity(p),
			})
			continue
		}
		if !pendingPermission[p.ID] && (p.Readiness == "blocked" || p.Readiness == "failed") {
			items = append(items, model.InboxItem{
				ID:        "ready:" + p.ID + ":" + p.Readiness,
				Kind:      "pane",
				Severity:  model.InboxAction,
				PaneID:    p.ID,
				Title:     paneLabel(p) + " 需要输入",
				Detail:    cleanInboxText(p.ReadinessReason, 160),
				CreatedAt: lastActivity(p),
			})
		}
		if p.Unread > 0 {
			items = append(items, model.InboxItem{
				ID:        "unread:" + p.ID,
				Kind:      "unread",
				Severity:  model.InboxInfo,
				PaneID:    p.ID,
				Title:     paneLabel(p) + " 有新回复",
				Detail:    cleanInboxText(p.LastOutput, 160),
				CreatedAt: lastActivity(p),
			})
		}
	}
	seenTask := map[string]bool{}
	for _, t := range s.Tasks {
		// Chat and briefs are not delegated work: they never ask the human to
		// act or report back.
		if t.Kind == model.TaskKindChat || t.Kind == model.TaskKindBrief {
			continue
		}
		blocked := t.DeliveryError != "" && (t.State == "queued" || t.State == "unknown" || t.DeliveryStatus == "write-unconfirmed")
		if blocked {
			items = append(items, model.InboxItem{
				ID:        "delivery:" + t.ID,
				Kind:      "delivery",
				Severity:  model.InboxAction,
				PaneID:    t.To,
				TaskID:    t.ID,
				Title:     "投递受阻：" + name(t.To),
				Detail:    cleanInboxText(t.DeliveryError, 160),
				CreatedAt: t.UpdatedAt,
			})
		}
		if !contains([]string{"completed", "failed", "unknown"}, t.State) {
			continue
		}
		human := t.From == "controller"
		severity := ""
		if human {
			if t.State == "completed" {
				severity = model.InboxInfo
			} else {
				severity = model.InboxAction
			}
		} else if t.State != "completed" {
			severity = model.InboxInfo
		}
		if severity == "" || t.Seen || seenTask[t.ID] {
			continue
		}
		seenTask[t.ID] = true
		items = append(items, model.InboxItem{
			ID:        "task:" + t.ID,
			Kind:      "task",
			Severity:  severity,
			PaneID:    t.To,
			TaskID:    t.ID,
			Title:     name(t.To) + " 任务" + taskStateLabel(t.State),
			Detail:    cleanInboxText(firstLine(t.Prompt), 160),
			CreatedAt: t.UpdatedAt,
			Seen:      t.Seen,
		})
	}
	for _, r := range s.Reports {
		if r.Seen || r.To != "controller" {
			continue
		}
		items = append(items, model.InboxItem{
			ID:        "report:" + r.ID,
			Kind:      "report",
			Severity:  model.InboxAction,
			ReportID:  r.ID,
			Title:     name(r.From) + " 回了报告",
			Detail:    cleanInboxText(r.Summary, 160),
			CreatedAt: r.CreatedAt,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		ab, bb := a.Severity == model.InboxBlocking, b.Severity == model.InboxBlocking
		if ab != bb {
			return ab
		}
		if ab {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	return items
}

func lastActivity(p model.Pane) time.Time {
	if !p.LastActivity.IsZero() {
		return p.LastActivity
	}
	return p.CreatedAt
}

func firstLine(text string) string {
	line := text
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line)
}

func taskStateLabel(state string) string {
	switch state {
	case "completed":
		return "完成"
	case "failed":
		return "失败"
	case "unknown":
		return "结果未确认"
	case "cancelled":
		return "已取消"
	}
	return " " + state
}
