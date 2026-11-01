package tui

import (
	"fmt"

	"github.com/gdamore/tcell/v3"
	"wtable/internal/model"
)

func (u *ui) roleOptions() []panelOption {
	options := []panelOption{{"", "自由角色（不选模板）"}}
	for _, r := range u.panel.catalog.Roles {
		options = append(options, panelOption{r.Name, r.Name})
	}
	return options
}
func (u *ui) startRoleForm(existing *model.RoleTemplate) {
	p := u.panel
	r := model.RoleTemplate{}
	if existing != nil {
		r = *existing
	}
	p.form = "role"
	p.field = 0
	p.fields = []panelField{textField("name", "模板名称", r.Name), textField("instructions", "角色指令（Alt-Enter 换行）", r.Instructions), textField("preset", "默认 preset（可空）", r.Preset), optionField("backend", "默认后端", r.Backend, []panelOption{{"", "跟随 preset"}, {"pty", "PTY"}, {"acp", "ACP"}}), textField("cwd", "约定目录（可空）", r.Cwd)}
}
func (u *ui) rolePanelKey(e *tcell.EventKey) {
	p := u.panel
	n := len(p.catalog.Roles)
	if e.Key() == tcell.KeyEscape {
		p.roleView = false
		p.selected = 0
		return
	}
	if e.Str() == "a" {
		u.startRoleForm(nil)
		return
	}
	if n == 0 {
		return
	}
	p.selected = min(p.selected, n-1)
	switch {
	case e.Key() == tcell.KeyDown || e.Str() == "j":
		p.selected = (p.selected + 1) % n
	case e.Key() == tcell.KeyUp || e.Str() == "k":
		p.selected = (p.selected + n - 1) % n
	case e.Key() == tcell.KeyHome:
		p.selected = 0
	case e.Key() == tcell.KeyEnd:
		p.selected = n - 1
	case e.Str() == "e" || e.Key() == tcell.KeyEnter:
		r := p.catalog.Roles[p.selected]
		u.startRoleForm(&r)
	case e.Str() == "d":
		p.confirmKind = "role"
		p.confirmID = p.catalog.Roles[p.selected].Name
	}
}
func (u *ui) drawRoles() {
	p := u.panel
	_, h := u.screen.Size()
	capacity := max(1, h-7)
	lines := []string{"角色模板库 · a 添加 · e / Enter 编辑 · d 删除 · Esc 返回", "创建 agent 时会保存当时的角色指令；之后的修改只对新建的 agent 生效"}
	p.selected = min(p.selected, max(0, len(p.catalog.Roles)-1))
	start := max(0, p.selected-capacity+1)
	for i := start; i < min(len(p.catalog.Roles), start+capacity); i++ {
		r := p.catalog.Roles[i]
		mark := "  "
		if i == p.selected {
			mark = "› "
		}
		lines = append(lines, fmt.Sprintf("%s%s · %s · %s", mark, r.Name, r.Preset, truncateColumns(r.Instructions, 45)))
	}
	if len(p.catalog.Roles) == 0 {
		lines = append(lines, "暂无模板；a 添加")
	}
	u.modal(lines)
}
func (u *ui) applyRoleDefaults() {
	p := u.panel
	if p.form != "agent" {
		return
	}
	field := p.get("role_template")
	if field == nil {
		return
	}
	for _, r := range p.catalog.Roles {
		if r.Name != field.value() {
			continue
		}
		p.get("brief").choice = 1
		if r.Preset != "" {
			for i, o := range p.get("preset").options {
				if o.value == r.Preset {
					p.get("preset").choice = i
				}
			}
		}
		if r.Backend != "" {
			for i, o := range p.get("backend").options {
				if o.value == r.Backend {
					p.get("backend").choice = i
				}
			}
		}
		if r.Cwd != "" {
			p.get("cwd").edit.Set(r.Cwd)
			p.get("layer").choice = 0
		}
		if p.get("role").value() == "" {
			p.get("role").edit.Set(r.Name)
		}
		u.status = "已应用角色默认值；指令将在创建时快照"
		return
	}
}
