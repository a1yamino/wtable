package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v3"
	"github.com/gdamore/tcell/v3/color"
	"wtable/internal/model"
)

type approvalState struct {
	index     int
	expanded  bool
	snoozed   map[string]bool
	rejectAll bool
	allowAll  bool
}

// activePermission is the card shown under the main pane. Cards never block:
// keys go to the pane unless the human explicitly focused the card.
func (u *ui) activePermission() *model.Permission {
	perms := u.permitsFor(u.canvas.main)
	if len(perms) == 0 {
		return nil
	}
	u.approval.index = max(0, min(u.approval.index, len(perms)-1))
	return &perms[u.approval.index]
}

func (u *ui) cardVisible() bool {
	perm := u.activePermission()
	if perm == nil {
		return false
	}
	return !u.approval.snoozed[u.canvas.main]
}

func (u *ui) approvalHeight() int {
	if !u.cardVisible() {
		return 0
	}
	if u.approval.expanded {
		return 8
	}
	return 4
}

func (u *ui) focusPermission(id string) {
	for _, perm := range u.snapshot.Permissions {
		if perm.ID != id {
			continue
		}
		if u.approval.snoozed == nil {
			u.approval.snoozed = map[string]bool{}
		}
		delete(u.approval.snoozed, perm.PaneID)
		if u.canvas.main != perm.PaneID {
			u.selectMain(perm.PaneID)
		}
		index := 0
		for _, other := range u.permitsFor(perm.PaneID) {
			if other.ID == perm.ID {
				break
			}
			index++
		}
		u.approval.index = index
		u.approval.expanded = false
		u.approval.rejectAll = false
		u.approval.allowAll = false
		u.area = focusApproval
		u.resize()
		return
	}
}

// processNextApproval is Ctrl-a p: pick the oldest blocking request, or move to
// the next one when the card is already focused. It never auto-approves.
func (u *ui) processNextApproval() {
	if len(u.snapshot.Permissions) == 0 {
		u.status = "没有待审批的请求"
		return
	}
	global := u.snapshot.Permissions
	next := 0
	if u.area == focusApproval {
		if perm := u.activePermission(); perm != nil {
			for i, other := range global {
				if other.ID == perm.ID {
					next = (i + 1) % len(global)
					break
				}
			}
		}
	}
	for step := 0; step < len(global); step++ {
		perm := global[(next+step)%len(global)]
		if u.approval.snoozed[perm.PaneID] && u.area != focusApproval {
			continue
		}
		u.focusPermission(perm.ID)
		return
	}
	u.focusPermission(global[next].ID)
}

func (u *ui) shiftPermission(delta int) {
	global := u.snapshot.Permissions
	if len(global) == 0 {
		return
	}
	current := -1
	if perm := u.activePermission(); perm != nil {
		for i, other := range global {
			if other.ID == perm.ID {
				current = i
				break
			}
		}
	}
	next := (current + delta + len(global)) % len(global)
	u.focusPermission(global[next].ID)
}

// allowAllOption returns the shared allow_always option id when every pending
// request in the pane is the same tool and the same option. Anything else must
// be approved one by one.
func allowAllOption(perms []model.Permission) (string, bool) {
	optionID, kind := "", ""
	for _, perm := range perms {
		found := ""
		for _, o := range perm.Options {
			if o.Kind == "allow_always" {
				found = o.ID
				break
			}
		}
		if found == "" {
			return "", false
		}
		if optionID == "" {
			optionID, kind = found, perm.Kind
			continue
		}
		if found != optionID || perm.Kind != kind {
			return "", false
		}
	}
	return optionID, optionID != ""
}

func rejectOption(perm *model.Permission) string {
	fallback := ""
	for _, o := range perm.Options {
		if strings.HasPrefix(o.Kind, "reject") {
			if o.Kind == "reject_once" {
				return o.ID
			}
			if fallback == "" {
				fallback = o.ID
			}
		}
	}
	return fallback
}

func (u *ui) replyPermission(perm *model.Permission, optionID, label string) {
	if !u.call("permission.reply", map[string]string{"id": perm.ID, "option_id": optionID}, nil) {
		return
	}
	u.status = "已" + label + "：" + perm.Title
	u.approval.expanded = false
	u.approval.rejectAll = false
	u.approval.allowAll = false
	if u.approval.snoozed == nil {
		u.approval.snoozed = map[string]bool{}
	}
	delete(u.approval.snoozed, perm.PaneID)
	_, _ = u.refresh()
	if len(u.permitsFor(u.canvas.main)) == 0 {
		u.area = focusMain
	} else {
		u.approval.index = min(u.approval.index, len(u.permitsFor(u.canvas.main))-1)
	}
	u.resize()
}

func (u *ui) approvalKey(e *tcell.EventKey) {
	perm := u.activePermission()
	if perm == nil {
		u.area = focusMain
		return
	}
	if u.approval.allowAll {
		if e.Str() == "y" {
			perms := u.permitsFor(u.canvas.main)
			optionID, ok := allowAllOption(perms)
			if !ok {
				u.approval.allowAll = false
				u.status = "请求不相同，全部允许已取消；请逐个处理"
				return
			}
			count := 0
			for i := range perms {
				if u.call("permission.reply", map[string]any{"id": perms[i].ID, "option_id": optionID, "batch": true}, nil) {
					count++
				}
			}
			u.approval.allowAll = false
			u.status = fmt.Sprintf("已允许 %d 个请求（同一工具、同一 allow_always）", count)
			_, _ = u.refresh()
			u.area = focusMain
			u.resize()
			return
		}
		if e.Key() == tcell.KeyEscape || e.Str() == "n" {
			u.approval.allowAll = false
			u.status = ""
		}
		return
	}
	if u.approval.rejectAll {
		if e.Str() == "y" {
			perms := u.permitsFor(u.canvas.main)
			count := 0
			for i := range perms {
				option := rejectOption(&perms[i])
				if option == "" {
					option = ""
				}
				if u.call("permission.reply", map[string]any{"id": perms[i].ID, "option_id": option, "batch": true}, nil) {
					count++
				}
			}
			u.approval.rejectAll = false
			u.status = fmt.Sprintf("已拒绝 %d 个请求", count)
			_, _ = u.refresh()
			u.area = focusMain
			u.resize()
			return
		}
		if e.Key() == tcell.KeyEscape || e.Str() == "n" {
			u.approval.rejectAll = false
			u.status = ""
		}
		return
	}
	switch e.Key() {
	case tcell.KeyEscape:
		u.area = focusMain
		u.status = "已收起审批卡；请求仍在等待"
		return
	case tcell.KeyEnter:
		u.approval.expanded = !u.approval.expanded
		u.resize()
		return
	}
	if e.Key() == tcell.KeyRune {
		s := e.Str()
		if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
			n := int(s[0] - '1')
			if n < len(perm.Options) {
				o := perm.Options[n]
				label := "允许"
				if strings.HasPrefix(o.Kind, "reject") {
					label = "拒绝"
				}
				u.replyPermission(perm, o.ID, label)
			}
			return
		}
		switch s {
		case "r":
			option := rejectOption(perm)
			label := "拒绝"
			if option == "" {
				label = "取消"
			}
			u.replyPermission(perm, option, label)
		case "d":
			if u.approval.snoozed == nil {
				u.approval.snoozed = map[string]bool{}
			}
			u.approval.snoozed[u.canvas.main] = true
			u.area = focusMain
			u.status = "已稍后；请求仍在等待，Ctrl-a p 可再打开"
			u.resize()
		case "[":
			u.shiftPermission(-1)
		case "]":
			u.shiftPermission(1)
		case "R":
			u.approval.rejectAll = true
			u.status = fmt.Sprintf("拒绝当前 pane 的全部 %d 个请求？输入 y 确认；其他键取消", len(u.permitsFor(u.canvas.main)))
		case "A":
			if _, ok := allowAllOption(u.permitsFor(u.canvas.main)); !ok {
				u.status = "全部允许仅限同一 pane、同一工具、同一 allow_always 选项；这些请求请逐个处理"
				return
			}
			u.approval.allowAll = true
			u.status = fmt.Sprintf("允许当前 pane 的全部 %d 个请求？输入 y 确认；其他键取消", len(u.permitsFor(u.canvas.main)))
		}
	}
}

func approvalSummary(perm *model.Permission) string {
	parts := []string{}
	if perm.Kind != "" {
		parts = append(parts, "工具 "+perm.Kind)
	}
	// Prefer the actual command/input over location metadata; the doc example
	// is "工具 shell · 命令 git push origin main".
	input, body := "", ""
	for _, line := range strings.Split(perm.Preview, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || line == perm.Title || strings.HasPrefix(line, "kind: "):
			continue
		case strings.HasPrefix(line, "input: "):
			if input == "" {
				input = strings.TrimPrefix(line, "input: ")
			}
		case strings.HasPrefix(line, "locations: "):
			continue
		default:
			if body == "" {
				body = line
			}
		}
	}
	if input == "" {
		input = body
	}
	if input == "" {
		input = perm.Title
	}
	parts = append(parts, input)
	if len(perm.Locations) > 0 {
		parts = append(parts, fmt.Sprintf("路径 %d 个", len(perm.Locations)))
	}
	return strings.Join(parts, " · ")
}

func (u *ui) drawApprovalCard(r rect) {
	perm := u.activePermission()
	if perm == nil || !u.cardVisible() || r.w < 2 || r.h < 1 {
		return
	}
	focused := u.area == focusApproval
	base := tcell.StyleDefault.Background(color.NewHexColor(0x3a2b1c)).Foreground(color.NewHexColor(0xffe2bf))
	if focused {
		base = tcell.StyleDefault.Background(color.NewHexColor(0x6b4a22)).Foreground(color.White).Bold(true)
	}
	for y := r.y; y < r.y+r.h; y++ {
		u.line(r.x, y, r.w, "", base)
	}
	perms := u.permitsFor(u.canvas.main)
	pane := u.paneByID(perm.PaneID)
	name := perm.PaneID
	if pane != nil {
		name = pane.Name
	}
	waited := relativeAge(perm.CreatedAt)
	title := fmt.Sprintf("⏳ %s 请求批准 · 已等 %s · %d/%d", name, waited, u.approval.index+1, len(perms))
	if !perm.ExpiresAt.IsZero() {
		left := time.Until(perm.ExpiresAt)
		if left > 0 {
			if left < time.Minute {
				title += fmt.Sprintf(" · %ds 后过期", int(left.Seconds()))
			} else {
				title += fmt.Sprintf(" · %dm 后过期", int(left.Minutes())+1)
			}
		}
	}
	u.line(r.x, r.y, r.w, title, base)
	if r.h > 1 {
		u.line(r.x, r.y+1, r.w, "  "+approvalSummary(perm), base)
	}
	options := []string{}
	for i, o := range perm.Options {
		if i >= 9 {
			break
		}
		options = append(options, fmt.Sprintf("[%d] %s", i+1, o.Name))
	}
	if r.h > 2 {
		u.line(r.x, r.y+2, r.w, "  "+strings.Join(options, "  "), base)
	}
	hint := "1..9 选择 · r 拒绝 · d 稍后 · [ ] 切换 · Enter 详情 · Esc 收起"
	if u.approval.rejectAll {
		hint = "拒绝本页全部请求？y 确认 · 其他键取消"
	} else if u.approval.allowAll {
		hint = "允许本页全部请求？y 确认 · 其他键取消"
	} else if !focused {
		hint = "Ctrl-a p 处理审批；现在输入仍然进入当前 pane"
	}
	if r.h > 3 {
		u.line(r.x, r.y+3, r.w, "  "+hint, base)
	}
	if r.h > 4 {
		line := 4
		for _, text := range strings.Split(perm.Preview, "\n") {
			if line >= r.h {
				break
			}
			u.line(r.x, r.y+line, r.w, "  "+truncateColumns(text, max(1, r.w-4)), base)
			line++
		}
		if perm.ExpiresAt.IsZero() {
			if line < r.h {
				u.line(r.x, r.y+line, r.w, "  Enter 收起详情 · Esc 收起卡片（请求保留）", base)
			}
		}
	}
}
