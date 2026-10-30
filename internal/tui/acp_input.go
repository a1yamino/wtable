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
func (u *ui) acpKey(e *tcell.EventKey, p *model.Pane) {
	input := u.acpInput(p.ID)
	altEnter := e.Modifiers()&tcell.ModAlt != 0 && (e.Key() == tcell.KeyEnter || (e.Key() == tcell.KeyRune && e.Str() == "m" && e.Modifiers()&tcell.ModCtrl != 0))
	if altEnter {
		input.Insert("\n")
		return
	}
	if e.Key() == tcell.KeyEnter {
		if strings.TrimSpace(input.Text()) == "" {
			return
		}
		var task model.Task
		if u.call("task.send", model.SendParams{To: p.ID, Prompt: input.Text(), DeliveryMode: "prompt"}, &task) {
			input.Remember("acp")
			input.Begin("acp", "")
			u.status = "已发送 → " + p.Name + "：" + task.State
		}
		return
	}
	if e.Key() == tcell.KeyEscape {
		input.Set("")
		return
	}
	input.Handle(e)
}
func (u *ui) drawACP(p model.Pane, r rect, focused bool) {
	if r.h < 4 {
		u.drawMessages(p, r)
		return
	}
	content := r
	content.h -= 2
	u.drawMessages(p, content)
	text := u.acpInput(p.ID).Text()
	if focused {
		text = u.acpInput(p.ID).Display()
	}
	text = strings.ReplaceAll(text, "\n", " ↵ ")
	parts := wrap(text, max(1, r.w-2))
	visible := parts[len(parts)-1]
	if focused {
		for _, part := range parts {
			if strings.Contains(part, "▏") {
				visible = part
				break
			}
		}
	}
	u.line(r.x, r.y+r.h-2, r.w, "› "+visible, focusStyle)
	u.line(r.x, r.y+r.h-1, r.w, "ACP 输入 · Enter 发送 · Alt-Enter 换行", barStyle)
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
	return fmt.Sprint(p.tab, p.selected, p.field, p.scroll, p.form, p.targetID, p.confirmKind, p.confirmID, p.preview, p.busy, p.related, p.catalog, p.fields, p.roleView, p.layerReturn)
}
