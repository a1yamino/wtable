package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v3"
	"wtable/internal/config"
	"wtable/internal/model"
	"wtable/internal/rpc"
)

type panelOption struct{ value, label string }
type panelField struct {
	key, label string
	edit       lineEdit
	options    []panelOption
	choice     int
	multi      bool
	checked    map[string]bool
}

func textField(key, label, value string) panelField {
	f := panelField{key: key, label: label}
	f.edit.Begin(key, value)
	return f
}
func optionField(key, label, value string, options []panelOption) panelField {
	f := panelField{key: key, label: label, options: options}
	for i, o := range options {
		if o.value == value {
			f.choice = i
			break
		}
	}
	return f
}
func multiField(key, label string, options []panelOption) panelField {
	f := optionField(key, label, "", options)
	f.multi = true
	f.checked = map[string]bool{}
	return f
}
func (f *panelField) value() string {
	if len(f.options) > 0 {
		return f.options[f.choice].value
	}
	return f.edit.Text()
}
func (f *panelField) values() []string {
	out := []string{}
	for _, o := range f.options {
		if f.checked[o.value] {
			out = append(out, o.value)
		}
	}
	return out
}

type controlPanel struct {
	tab, selected, field, scroll                               int
	form, targetID, confirmKind, confirmID, preview, afterLoad string
	fields                                                     []panelField
	catalog                                                    model.Catalog
	busy, related                                              bool
	rows                                                       map[int]int
	roleView                                                   bool
	roleChanged                                                bool
	layerReturn                                                []panelField
	presetCursor                                               int
	fastCreate                                                 bool
	focusSide                                                  bool
	sideRows                                                   map[int]int
	fieldRows                                                  map[int]int
	confirmCancelRow                                           int
}
type panelResult struct {
	owner  *controlPanel
	method string
	data   json.RawMessage
	err    error
}

var panelTabs = []string{"Agents", "Edges", "Tasks", "Layers"}

func (u *ui) openPanel(tab int) {
	u.selection = nil
	u.copyMode = false
	u.taskView = false
	u.prefix = false
	if p := u.current(); p != nil {
		u.offsets[p.ID] = 0
	}
	u.panel = &controlPanel{tab: tab, rows: map[int]int{}}
	_, _ = u.refresh()
	u.panelAsync("preset.list", nil)
}
func (u *ui) panelAsync(method string, args any) {
	p := u.panel
	if p == nil || p.busy {
		return
	}
	p.busy = true
	if u.panelResults == nil {
		u.panelResults = make(chan panelResult, 1)
	}
	ctx := u.ctx
	results := u.panelResults
	go func() {
		result := panelResult{owner: p, method: method}
		paths, err := config.Resolve()
		if err == nil {
			var token string
			token, err = paths.ClientToken()
			if err == nil {
				callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
				defer cancel()
				var client *rpc.Client
				client, err = rpc.Dial(callCtx, paths.Socket, token)
				if err == nil {
					err = client.Call(callCtx, method, args, &result.data)
					client.Close()
				}
			}
		}
		result.err = err
		select {
		case results <- result:
		case <-ctx.Done():
		}
	}()
}
func (u *ui) panelFinished(r panelResult) {
	r.owner.busy = false
	if r.err != nil {
		u.status = r.err.Error()
		return
	}
	switch r.method {
	case "preset.list":
		if err := json.Unmarshal(r.data, &r.owner.catalog); err != nil {
			u.status = err.Error()
			return
		}
		if u.panel == r.owner {
			switch r.owner.afterLoad {
			case "agent", "custom":
				u.startAgentForm(false)
				if r.owner.afterLoad == "custom" {
					f := r.owner.get("preset")
					f.choice = len(f.options) - 1
				}
			case "edge":
				u.startEdgeForm()
			case "send":
				u.startSendForm()
			}
			r.owner.afterLoad = ""
		}
		return
	case "pane.spawn", "pane.update":
		var pane model.Pane
		if err := json.Unmarshal(r.data, &pane); err != nil {
			u.status = err.Error()
			return
		}
		_, _ = u.refresh()
		u.resize()
		for i, p := range u.snapshot.Panes {
			if p.ID == pane.ID {
				u.focus = i
				u.openPane(pane.ID, true)
				r.owner.selected = i
				break
			}
		}
		r.owner.form = ""
		if r.owner.fastCreate {
			r.owner.fastCreate = false
			u.panel = nil
			u.status = "已创建 " + pane.Name + "（Alt+数字 直达，Ctrl-a a 可再调）"
		} else {
			u.status = "已保存 Agent：" + pane.Name
		}
	case "role.put", "role.delete":
		r.owner.form = ""
		r.owner.confirmKind = ""
		u.panelAsync("preset.list", nil)
	case "pane.remove":
		r.owner.confirmKind = ""
		_, _ = u.refresh()
		u.resize()
		u.status = "已移除 pane；任务与报告历史保留"
	case "layer.create":
		var layer model.Layer
		if err := json.Unmarshal(r.data, &layer); err != nil {
			u.status = err.Error()
			return
		}
		u.activeLayer = layer.Path
		u.activeCwd = layer.Path
		r.owner.form = ""
		_, _ = u.refresh()
		if r.owner.layerReturn != nil {
			r.owner.fields = r.owner.layerReturn
			r.owner.layerReturn = nil
			r.owner.form = "agent"
			r.owner.field = 0
			r.owner.get("layer").options = u.layerOptions()
			for i, o := range r.owner.get("layer").options {
				if o.value == layer.Path {
					r.owner.get("layer").choice = i
				}
			}
			r.owner.get("cwd").edit.Set("")
		}
		u.status = "新 pane 默认使用 Layer：" + layer.Name
	}
}
func (p *controlPanel) get(key string) *panelField {
	for i := range p.fields {
		if p.fields[i].key == key {
			return &p.fields[i]
		}
	}
	return nil
}
func (u *ui) paneOptions(includeAll bool) []panelOption {
	out := []panelOption{}
	for _, p := range u.snapshot.Panes {
		if includeAll || p.State == "running" {
			out = append(out, panelOption{p.ID, p.Name + " · " + p.State})
		}
	}
	return out
}
func (u *ui) accountOptions() []panelOption {
	out := []panelOption{{"", "继承当前环境"}}
	for _, name := range u.panel.catalog.Accounts {
		out = append(out, panelOption{name, name})
	}
	return out
}
func (u *ui) layerOptions() []panelOption {
	out := []panelOption{{"", "目录（不选 Layer）"}}
	for _, l := range u.snapshot.Layers {
		out = append(out, panelOption{l.Path, l.Name + " · " + l.Repo})
	}
	return out
}
func (u *ui) startAgentForm(edit bool) {
	p := u.panel
	p.roleChanged = false
	p.field = 0
	p.preview = ""
	if edit {
		if p.selected >= len(u.snapshot.Panes) {
			return
		}
		pane := u.snapshot.Panes[p.selected]
		p.targetID = pane.ID
		p.form = "edit"
		accounts := u.accountOptions()
		found := false
		for _, a := range accounts {
			if a.value == pane.Account {
				found = true
				break
			}
		}
		if !found {
			accounts = append(accounts, panelOption{pane.Account, pane.Account + " · 原账号档案已缺失"})
		}
		p.fields = []panelField{textField("name", "名字", pane.Name), textField("role", "角色", pane.Role), optionField("account", "账号档案（运行中不可切换）", pane.Account, accounts), optionField("role_template", "角色模板（重新快照）", pane.RoleTemplate, u.roleOptions())}
		return
	}
	presets := []panelOption{}
	for _, preset := range p.catalog.Presets {
		label := preset.Name
		if !preset.Available {
			label += " · 不可用"
		}
		presets = append(presets, panelOption{preset.ID, label})
	}
	presets = append(presets, panelOption{"", "自定义命令"})
	p.form = "agent"
	p.targetID = ""
	cwd := u.activeCwd
	if cwd == "" {
		cwd = u.cwd
	}
	p.fields = []panelField{
		textField("name", "名字", ""),
		optionField("preset", "预设", "shell", presets),
		optionField("backend", "后端（自定义命令）", "pty", []panelOption{{"pty", "PTY 终端"}, {"acp", "ACP 会话"}}),
		textField("command", "自定义命令（支持引号，不展开变量）", ""),
		textField("cwd", "目录", cwd),
		optionField("layer", "Layer", u.activeLayer, u.layerOptions()),
		textField("role", "角色", ""),
		optionField("account", "账号档案", "", u.accountOptions()),
		optionField("brief", "注入身份说明", "false", []panelOption{{"false", "关闭"}, {"true", "开启（≤2KB，可审阅）"}}),
		multiField("connect", "新 Agent → 选中目标", u.paneOptions(true)),
		optionField("role_template", "角色模板", "", u.roleOptions()),
	}
}
func (u *ui) startEdgeForm() {
	p := u.panel
	opts := u.paneOptions(true)
	if len(opts) < 2 {
		u.status = "至少需要两个 pane 才能建立连线"
		return
	}
	from := ""
	if pane := u.current(); pane != nil {
		from = pane.ID
	}
	p.form = "edge"
	p.field = 0
	p.fields = []panelField{optionField("from", "来源", from, opts), multiField("targets", "目标（Space 多选）", opts)}
}
func (u *ui) startSendForm() {
	p := u.panel
	opts := u.paneOptions(false)
	if len(opts) == 0 {
		u.status = "没有运行中的 Agent"
		return
	}
	p.form = "send"
	p.field = 0
	targets := multiField("targets", "目标（Space 多选）", opts)
	if pane := u.current(); pane != nil && pane.State == "running" {
		targets.checked[pane.ID] = true
	}
	reportOpts := append([]panelOption{{"", "未选择"}}, opts...)
	p.fields = []panelField{targets, textField("prompt", "任务内容", ""), optionField("delivery", "PTY 投递方式", "", []panelOption{{"", "默认"}, {"inject", "inject"}, {"claim", "claim"}}), optionField("queue", "等待就绪 / 忙时排队", "false", []panelOption{{"false", "否"}, {"true", "是"}}), optionField("complete", "完成后", "none", []panelOption{{"none", "不汇报"}, {"notify", "通知（不执行）"}, {"handoff", "派发后续任务"}}), optionField("report", "汇报给", "", reportOpts)}
}
func (u *ui) startLayerForm() {
	p := u.panel
	p.form = "layer"
	p.field = 0
	p.fields = []panelField{textField("name", "Layer 名称", ""), textField("cwd", "仓库目录", u.cwd), textField("base", "基准（空值为 origin/main 或 HEAD）", "")}
}
func (u *ui) panelEdges() []model.Edge {
	out := []model.Edge{}
	for _, e := range u.snapshot.Edges {
		if u.panel.related {
			p := u.current()
			if p == nil || (e.From != p.ID && e.To != p.ID) {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}
func (u *ui) panelCount() int {
	switch u.panel.tab {
	case 0:
		return len(u.snapshot.Panes)
	case 1:
		return len(u.panelEdges())
	case 2:
		return len(u.orderedTasks())
	case 3:
		return len(u.snapshot.Layers) + 1
	}
	return 0
}
func (u *ui) panelKey(e *tcell.EventKey) {
	p := u.panel
	if p.busy {
		return
	}
	if p.preview != "" {
		switch e.Key() {
		case tcell.KeyEscape:
			p.preview = ""
			p.scroll = 0
		case tcell.KeyUp:
			p.scroll = max(0, p.scroll-1)
		case tcell.KeyDown:
			p.scroll++
		case tcell.KeyPgUp:
			p.scroll = max(0, p.scroll-8)
		case tcell.KeyPgDn:
			p.scroll += 8
		}
		return
	}
	if p.confirmKind != "" {
		if e.Key() == tcell.KeyEscape || e.Str() == "n" {
			p.confirmKind = ""
			return
		}
		if e.Str() != "y" {
			return
		}
		if p.confirmKind == "pane" {
			u.panelAsync("pane.remove", map[string]string{"id": p.confirmID})
		} else if p.confirmKind == "role" {
			u.panelAsync("role.delete", map[string]string{"name": p.confirmID})
		} else {
			var edge model.Edge
			if err := json.Unmarshal([]byte(p.confirmID), &edge); err != nil {
				u.status = err.Error()
				return
			}
			if u.call("edge.remove", edge, nil) {
				p.confirmKind = ""
				_, _ = u.refresh()
				u.status = "已删除授权边，该方向排队任务已取消"
			}
		}
		return
	}
	if p.form == "preset" {
		u.presetFormKey(e)
		return
	}
	if p.form != "" {
		u.panelFormKey(e)
		return
	}
	if p.roleView {
		u.rolePanelKey(e)
		return
	}
	if e.Key() == tcell.KeyEscape {
		u.panel = nil
		return
	}
	if e.Key() == tcell.KeyTab || e.Key() == tcell.KeyBacktab {
		step := 1
		if e.Key() == tcell.KeyBacktab {
			step = -1
		}
		p.tab = (p.tab + step + len(panelTabs)) % len(panelTabs)
		p.selected = 0
		return
	}
	switch e.Key() {
	case tcell.KeyLeft:
		p.focusSide = true
		return
	case tcell.KeyRight:
		p.focusSide = false
		return
	}
	if p.focusSide {
		switch e.Key() {
		case tcell.KeyUp:
			p.tab = (p.tab + len(panelTabs) - 1) % len(panelTabs)
			p.selected = 0
			return
		case tcell.KeyDown:
			p.tab = (p.tab + 1) % len(panelTabs)
			p.selected = 0
			return
		case tcell.KeyEnter:
			p.focusSide = false
			return
		case tcell.KeyEscape:
			u.panel = nil
			return
		default:
			// 其他按键直接作用于内容区，并把焦点交回去，避免"按键没反应"。
			p.focusSide = false
		}
	}
	switch e.Key() {
	case tcell.KeyUp:
		p.selected = max(0, p.selected-1)
		return
	case tcell.KeyDown:
		p.selected = min(max(0, u.panelCount()-1), p.selected+1)
		return
	case tcell.KeyHome:
		p.selected = 0
		return
	case tcell.KeyEnd:
		p.selected = max(0, u.panelCount()-1)
		return
	}
	p.selected = min(p.selected, max(0, u.panelCount()-1))
	// Enter and Space open the action list for the selection; nothing has to be
	// memorised beyond "pick it, then pick what to do".
	if p.form == "" && p.confirmKind == "" && p.preview == "" && !p.roleView {
		if e.Key() == tcell.KeyEnter || e.Str() == " " {
			u.openActions()
			return
		}
	}
	switch p.tab {
	case 0:
		if e.Str() == "O" {
			p.roleView = true
			p.selected = 0
			return
		}
		if e.Str() == "a" {
			u.startPresetPicker()
			return
		}
		if len(u.snapshot.Panes) == 0 {
			return
		}
		pane := u.snapshot.Panes[p.selected]
		switch {
		case e.Key() == tcell.KeyEnter:
			u.panel = nil
			u.openPane(pane.ID, true)
		case e.Str() == "p":
			u.togglePark(pane.ID)
		case e.Str() == "O":
			p.roleView = true
			p.selected = 0
		case e.Str() == "L":
			u.startLayerForm()
		case e.Str() == "e":
			u.startAgentForm(true)
		case e.Str() == "d":
			p.confirmKind = "pane"
			p.confirmID = pane.ID
		case e.Str() == "v":
			u.peekPane(pane)
		case e.Str() == "B":
			var brief struct{ Path, Text, TaskID string }
			// JSON task_id is unused by preview; path/text are enough to audit the instructions.
			if u.call("pane.brief", map[string]string{"id": pane.ID}, &brief) {
				p.preview = brief.Path + "\n\n" + brief.Text
				p.scroll = 0
			}
		case e.Str() == "b":
			var task model.Task
			if u.call("pane.brief.send", map[string]string{"id": pane.ID}, &task) {
				u.status = "身份说明已排队：" + shortTask(task.ID)
				_, _ = u.refresh()
			}
		case e.Str() == "R":
			u.focus = p.selected
			u.restartCurrent()
		case e.Str() == "l":
			p.preview = "日志：" + pane.LogPath + "\n\n" + pane.Error + "\n" + pane.ExitSummary
			p.scroll = 0
		}
	case 1:
		switch e.Str() {
		case "a":
			u.startEdgeForm()
		case "t":
			p.related = !p.related
			p.selected = 0
		case "x":
			edges := u.panelEdges()
			if len(edges) > 0 {
				b, _ := json.Marshal(edges[p.selected])
				p.confirmKind = "edge"
				p.confirmID = string(b)
			}
		}
	case 2:
		if e.Str() == "s" {
			u.startSendForm()
			return
		}
		if e.Str() == "b" {
			u.openTasks()
			u.reportView = true
			return
		}
		tasks := u.orderedTasks()
		if len(tasks) == 0 {
			return
		}
		t := tasks[p.selected]
		switch {
		case e.Key() == tcell.KeyEnter:
			u.readTask(t.ID)
			u.taskView = true
		case e.Str() == "r":
			u.readTask(t.ID)
			u.taskView = true
			u.taskAction("r")
		case e.Str() == "x" || e.Str() == "c":
			u.taskCursor = t.ID
			u.taskDetail = nil
			u.cancelPickedTask()
		}
	case 3:
		if e.Str() == "n" {
			u.startLayerForm()
			return
		}
		if e.Key() == tcell.KeyEnter {
			if p.selected == 0 {
				u.activeLayer = ""
				u.activeCwd = u.cwd
				u.status = "新 pane 默认使用工作区目录"
			} else {
				l := u.snapshot.Layers[p.selected-1]
				u.activeLayer = l.Path
				u.activeCwd = l.Path
				u.status = "新 pane 默认使用 Layer：" + l.Name
			}
		}
	}
}
func (u *ui) panelFormKey(e *tcell.EventKey) {
	p := u.panel
	if p.form == "agent" && e.Str() == "L" && len(p.fields[p.field].options) > 0 {
		p.layerReturn = p.fields
		u.startLayerForm()
		return
	}
	if e.Key() == tcell.KeyEscape {
		p.form = ""
		if p.layerReturn != nil {
			p.fields = p.layerReturn
			p.layerReturn = nil
			p.form = "agent"
		}
		return
	}
	if e.Key() == tcell.KeyTab || e.Key() == tcell.KeyBacktab {
		step := 1
		if e.Key() == tcell.KeyBacktab {
			step = -1
		}
		p.field = (p.field + step + len(p.fields)) % len(p.fields)
		return
	}
	f := &p.fields[p.field]
	// ↑↓ move between fields (options already use ←→ for their value), so the
	// whole form is reachable without Tab.
	switch e.Key() {
	case tcell.KeyDown:
		p.field = (p.field + 1) % len(p.fields)
		return
	case tcell.KeyUp:
		p.field = (p.field + len(p.fields) - 1) % len(p.fields)
		return
	}
	if e.Key() == tcell.KeyEnter {
		multiline := f.key == "prompt" || f.key == "instructions"
		if multiline && e.Modifiers()&tcell.ModAlt != 0 {
			f.edit.Insert("\n")
			return
		}
		if e.Modifiers()&tcell.ModCtrl != 0 || e.Modifiers()&tcell.ModAlt != 0 {
			u.submitPanelForm()
			return
		}
		// Enter advances; only the last field submits, so a half-filled form
		// cannot be sent by accident.
		if p.field < len(p.fields)-1 {
			p.field++
			return
		}
		u.submitPanelForm()
		return
	}
	if (f.key == "prompt" || f.key == "instructions") && e.Key() == tcell.KeyRune && e.Str() == "m" && e.Modifiers()&(tcell.ModAlt|tcell.ModCtrl) == tcell.ModAlt|tcell.ModCtrl {
		f.edit.Insert("\n")
		return
	}
	if len(f.options) > 0 {
		switch e.Key() {
		case tcell.KeyLeft, tcell.KeyUp:
			f.choice = (f.choice + len(f.options) - 1) % len(f.options)
		case tcell.KeyRight, tcell.KeyDown:
			f.choice = (f.choice + 1) % len(f.options)
		case tcell.KeyHome:
			f.choice = 0
		case tcell.KeyEnd:
			f.choice = len(f.options) - 1
		case tcell.KeyRune:
			if e.Str() == " " {
				if f.multi {
					f.checked[f.value()] = !f.checked[f.value()]
				} else {
					f.choice = (f.choice + 1) % len(f.options)
				}
			}
		}
		if f.key == "preset" {
			for _, preset := range p.catalog.Presets {
				if preset.ID == f.value() {
					p.get("backend").choice = 0
					if preset.Backend == "acp" {
						p.get("backend").choice = 1
					}
					p.get("brief").choice = 0
					if preset.ID != "shell" {
						p.get("brief").choice = 1
					}
					break
				}
			}
		}
		if f.key == "role_template" {
			p.roleChanged = true
			u.applyRoleDefaults()
		}
	} else {
		f.edit.Handle(e)
	}
}
func (u *ui) submitPanelForm() {
	p := u.panel
	value := func(key string) string {
		if f := p.get(key); f != nil {
			return f.value()
		}
		return ""
	}
	switch p.form {
	case "agent":
		args := model.SpawnParams{Name: strings.TrimSpace(value("name")), Preset: value("preset"), Backend: value("backend"), Cwd: value("cwd"), Layer: value("layer"), Role: value("role"), RoleTemplate: value("role_template"), Account: value("account"), InjectBrief: value("brief") == "true", ConnectTo: p.get("connect").values(), Cols: 80, Rows: 24}
		if args.Name == "" {
			u.status = "请填写 Agent 名字"
			return
		}
		if args.Preset == "" {
			command, err := parseArgv(value("command"))
			if err != nil {
				u.status = err.Error()
				return
			}
			args.Command = command
		} else {
			for _, preset := range p.catalog.Presets {
				if preset.ID == args.Preset {
					if !preset.Available {
						u.status = preset.Name + " 不可用：" + preset.Detail
						return
					}
					args.Backend = preset.Backend
					break
				}
			}
		}
		if args.Layer != "" {
			args.Cwd = ""
		}
		u.panelAsync("pane.spawn", args)
	case "edit":
		roleTemplate := value("role_template")
		var selectedRole *string
		if p.roleChanged {
			selectedRole = &roleTemplate
		}
		u.panelAsync("pane.update", model.PaneUpdate{ID: p.targetID, Name: strings.TrimSpace(value("name")), Role: value("role"), RoleTemplate: selectedRole, Account: value("account")})
	case "role":
		u.panelAsync("role.put", model.RoleTemplate{Name: strings.TrimSpace(value("name")), Instructions: value("instructions"), Preset: value("preset"), Backend: value("backend"), Cwd: value("cwd")})
	case "layer":
		u.panelAsync("layer.create", map[string]string{"name": strings.TrimSpace(value("name")), "cwd": value("cwd"), "base": value("base")})
	case "edge":
		from := value("from")
		targets := p.get("targets")
		if len(targets.values()) == 0 {
			u.status = "请用 Space 选择目标"
			return
		}
		for _, to := range targets.values() {
			if from == to {
				u.status = "来源不能连接自己"
				return
			}
		}
		count := 0
		for _, to := range targets.values() {
			if !u.call("edge.add", model.Edge{From: from, To: to}, nil) {
				return
			}
			delete(targets.checked, to)
			count++
		}
		p.form = ""
		_, _ = u.refresh()
		u.status = fmt.Sprintf("已添加 %d 条单向授权边", count)
	case "send":
		targets := p.get("targets")
		if len(targets.values()) == 0 || strings.TrimSpace(value("prompt")) == "" {
			u.status = "请选择目标并填写任务"
			return
		}
		if value("complete") != "none" && value("report") == "" {
			u.status = "请选择汇报目标"
			return
		}
		count := 0
		for _, to := range targets.values() {
			mode := value("delivery")
			for _, pane := range u.snapshot.Panes {
				if pane.ID == to && pane.Backend == "acp" {
					mode = "prompt"
				}
			}
			args := model.SendParams{To: to, Prompt: value("prompt"), DeliveryMode: mode, Queue: value("queue") == "true", OnComplete: value("complete")}
			if args.OnComplete != "none" {
				args.ReportTo = value("report")
			}
			var task model.Task
			if !u.call("task.send", args, &task) {
				u.status = fmt.Sprintf("已发送 %d 个；未完成目标保留：%s", count, u.status)
				return
			}
			delete(targets.checked, to)
			count++
		}
		p.form = ""
		u.tasks = true
		_, _ = u.refresh()
		u.resize()
		u.status = fmt.Sprintf("已发送 %d 条任务", count)
	}
}

// A small argv tokenizer: quotes and escapes group arguments; nothing is expanded or executed.
func parseArgv(input string) ([]string, error) {
	args := []string{}
	var b strings.Builder
	var quote rune
	escaped, started := false, false
	for _, r := range input {
		if escaped {
			b.WriteRune(r)
			escaped = false
			started = true
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			started = true
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			started = true
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' {
			if started {
				args = append(args, b.String())
				b.Reset()
				started = false
			}
			continue
		}
		b.WriteRune(r)
		started = true
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("命令引号或转义未闭合")
	}
	if started {
		args = append(args, b.String())
	}
	if len(args) == 0 || args[0] == "" {
		return nil, fmt.Errorf("请输入可执行命令")
	}
	return args, nil
}

// startPresetPicker is the fast path: pick what to run, press Enter, done.
// Everything else (name, dir, layer, account, role, edges) has a sane default
// and can still be changed in the advanced form behind e / Tab.
func (u *ui) startPresetPicker() {
	p := u.panel
	p.form = "preset"
	p.field = 0
	p.presetCursor = 0
	p.preview = ""
	p.presetCursor = 0
}

func (u *ui) presetEntries() []model.Preset {
	p := u.panel
	entries := []model.Preset{}
	for _, preset := range p.catalog.Presets {
		entries = append(entries, preset)
	}
	return entries
}

// presetOrder lists preset indices as displayed: usable ones first, then the
// ones that need installing, so the cursor and the screen never disagree.
func (u *ui) presetOrder() []int {
	entries := u.presetEntries()
	order := []int{}
	for i, preset := range entries {
		if preset.Available {
			order = append(order, i)
		}
	}
	for i, preset := range entries {
		if !preset.Available {
			order = append(order, i)
		}
	}
	return order
}

func (u *ui) presetFormKey(e *tcell.EventKey) {
	p := u.panel
	entries := u.presetEntries()
	order := u.presetOrder()
	count := len(entries) + 1 // +1: 自定义命令
	switch e.Key() {
	case tcell.KeyEscape:
		p.form = ""
		return
	case tcell.KeyUp:
		p.presetCursor = max(0, p.presetCursor-1)
		return
	case tcell.KeyDown:
		p.presetCursor = min(count-1, p.presetCursor+1)
		return
	case tcell.KeyHome:
		p.presetCursor = 0
		return
	case tcell.KeyEnd:
		p.presetCursor = count - 1
		return
	}
	switch e.Str() {
	case "k":
		p.presetCursor = max(0, p.presetCursor-1)
		return
	case "j":
		p.presetCursor = min(count-1, p.presetCursor+1)
		return
	case "e":
		u.advancedAgentForm()
		return
	case "c":
		u.advancedAgentForm()
		p.get("preset").choice = len(p.get("preset").options) - 1
		return
	}
	if e.Key() == tcell.KeyTab || e.Key() == tcell.KeyEnter {
		if p.presetCursor >= len(order) {
			u.advancedAgentForm()
			p.get("preset").choice = len(p.get("preset").options) - 1
			return
		}
		preset := entries[order[p.presetCursor]]
		if !preset.Available {
			u.status = preset.Name + " 不可用：" + preset.Detail
			return
		}
		u.createFromPreset(preset)
	}
}

// advancedAgentForm switches from the picker to the full form, keeping the
// highlighted preset so nobody has to pick it twice.
func (u *ui) advancedAgentForm() {
	cursor := u.panel.presetCursor
	u.startAgentForm(false)
	if f := u.panel.get("preset"); f != nil {
		if cursor >= 0 && cursor < len(f.options) {
			f.choice = cursor
		}
	}
	u.applyPresetDefaults()
}

// applyPresetDefaults keeps backend / brief in step with the chosen preset.
func (u *ui) applyPresetDefaults() {
	p := u.panel
	f := p.get("preset")
	if f == nil {
		return
	}
	id := f.value()
	for _, preset := range p.catalog.Presets {
		if preset.ID != id {
			continue
		}
		if b := p.get("backend"); b != nil {
			b.choice = 0
			if preset.Backend == "acp" {
				b.choice = 1
			}
		}
		if b := p.get("brief"); b != nil {
			b.choice = 0
			if preset.ID != "shell" {
				b.choice = 1
			}
		}
		break
	}
}

// autoAgentName derives a readable, unique name from the preset id.
func (u *ui) autoAgentName(presetID string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(presetID, "-pty"), "-acp")
	if base == "" {
		base = "agent"
	}
	taken := map[string]bool{}
	for _, pane := range u.snapshot.Panes {
		taken[pane.Name] = true
	}
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s#%d", base, i)
		if !taken[candidate] {
			return candidate
		}
	}
}

// createFromPreset applies defaults and spawns immediately.
func (u *ui) createFromPreset(preset model.Preset) {
	cwd := u.activeCwd
	if cwd == "" {
		cwd = u.cwd
	}
	connect := []string{}
	if current := u.current(); current != nil {
		connect = append(connect, current.ID)
	}
	args := model.SpawnParams{
		Name:        u.autoAgentName(preset.ID),
		Preset:      preset.ID,
		Backend:     preset.Backend,
		Cwd:         cwd,
		Layer:       u.activeLayer,
		InjectBrief: preset.ID != "shell",
		ConnectTo:   connect,
		Cols:        80,
		Rows:        24,
	}
	u.panel.form = ""
	u.panel.fastCreate = true
	u.panelAsync("pane.spawn", args)
}
