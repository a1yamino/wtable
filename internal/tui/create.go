package tui

import (
	"fmt"
	"os"
	"sort"

	"github.com/gdamore/tcell/v3"
	"wtable/internal/model"
)

type createView struct {
	mode    string // foreground or worker
	presets []model.Preset
	cursor  int
	reason  string
}

// openCreate is the one-step creation flow. Foreground lists PTY presets only;
// worker may list ACP as well. Defaults are prefilled so Enter is enough.
func (u *ui) openCreate(kind string) {
	if kind == model.PaneForeground {
		if p := u.foregroundPane(); p != nil {
			u.status = "已有前台 " + p.Name + "；先移除它，或改为新建 worker"
			return
		}
	}
	var catalog model.Catalog
	if !u.callWithin(10e9, "preset.list", map[string]string{"kind": kind}, &catalog) {
		return
	}
	presets := append([]model.Preset{}, catalog.Presets...)
	if kind == model.PaneForeground {
		// A foreground is an agent, not a leftover shell; the explicit
		// "create temporary shell" action below is the fallback.
		filtered := presets[:0]
		for _, p := range presets {
			if p.ID != "shell" {
				filtered = append(filtered, p)
			}
		}
		presets = filtered
	}
	sort.SliceStable(presets, func(i, j int) bool {
		if presets[i].Available != presets[j].Available {
			return presets[i].Available
		}
		return false
	})
	c := &createView{mode: kind, presets: presets}
	available := 0
	for _, p := range presets {
		if p.Available {
			available++
		}
	}
	if available == 0 {
		if kind == model.PaneForeground {
			c.reason = "没有可用的 PTY agent：r 重新检测，或 s 创建临时 shell"
		} else {
			c.reason = "没有可用预设：r 重新检测，或 s 创建临时 shell worker"
		}
	}
	u.create = c
}

func (u *ui) createKey(e *tcell.EventKey) {
	c := u.create
	if c == nil {
		return
	}
	if e.Key() == tcell.KeyEscape {
		u.create = nil
		u.status = ""
		return
	}
	available := 0
	for _, p := range c.presets {
		if p.Available {
			available++
		}
	}
	switch e.Key() {
	case tcell.KeyUp:
		c.cursor = max(0, c.cursor-1)
		return
	case tcell.KeyDown:
		if available > 0 {
			c.cursor = min(available-1, c.cursor+1)
		}
		return
	}
	if e.Key() == tcell.KeyRune {
		switch e.Str() {
		case "k":
			c.cursor = max(0, c.cursor-1)
			return
		case "j":
			if available > 0 {
				c.cursor = min(available-1, c.cursor+1)
			}
			return
		case "r":
			u.openCreate(c.mode)
			return
		case "s":
			u.createShell(c.mode)
			return
		}
	}
	if e.Key() != tcell.KeyEnter {
		return
	}
	if available == 0 {
		u.status = c.reason
		return
	}
	index := 0
	for _, p := range c.presets {
		if !p.Available {
			continue
		}
		if index == c.cursor {
			u.spawnPreset(p, c.mode)
			return
		}
		index++
	}
}

func (u *ui) spawnPreset(preset model.Preset, kind string) {
	cwd := u.activeCwd
	if cwd == "" {
		cwd = u.cwd
	}
	if u.activeLayer != "" {
		cwd = ""
	}
	name := u.autoAgentName(preset.ID)
	args := model.SpawnParams{
		Name:        name,
		Kind:        kind,
		Preset:      preset.ID,
		Backend:     preset.Backend,
		Cwd:         cwd,
		Layer:       u.activeLayer,
		InjectBrief: preset.ID != "shell",
		Cols:        80,
		Rows:        24,
	}
	var pane model.Pane
	if !u.callWithin(45e9, "pane.spawn", args, &pane) {
		return
	}
	u.create = nil
	_, _ = u.refresh()
	if kind == model.PaneForeground || u.canvas.main == "" {
		u.selectMain(pane.ID)
		u.status = "已创建前台 " + pane.Name + "；主画布已就绪"
	} else {
		u.status = "已创建 worker " + pane.Name + "；左栏可见，前台不变"
	}
	u.resize()
}

func (u *ui) createShell(kind string) {
	cwd := u.activeCwd
	if cwd == "" {
		cwd = u.cwd
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	args := model.SpawnParams{Name: u.autoAgentName("shell"), Kind: kind, Backend: "pty", Cwd: cwd, Layer: u.activeLayer, Cols: 80, Rows: 24}
	var pane model.Pane
	if !u.callWithin(10e9, "pane.spawn", args, &pane) {
		return
	}
	u.create = nil
	_, _ = u.refresh()
	if kind == model.PaneForeground || u.canvas.main == "" {
		u.selectMain(pane.ID)
	}
	u.status = "已创建临时 shell " + pane.Name
	u.resize()
}

// spawnShell is used by the legacy command prompt; it never becomes the main
// pane unless the canvas is empty.
func (u *ui) spawnShell(command []string) bool {
	if len(command) == 0 {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/zsh"
		}
		command = []string{shell}
	}
	kind := model.PaneWorker
	if u.canvas.main == "" {
		kind = model.PaneForeground
	}
	cwd := u.activeCwd
	if cwd == "" {
		cwd = u.cwd
	}
	if u.activeLayer != "" {
		cwd = ""
	}
	var pane model.Pane
	if !u.call("pane.spawn", model.SpawnParams{Name: u.autoAgentName("shell"), Kind: kind, Backend: "pty", Command: command, Cwd: cwd, Layer: u.activeLayer, Cols: 80, Rows: 24}, &pane) {
		return false
	}
	_, _ = u.refresh()
	if u.canvas.main == "" {
		u.selectMain(pane.ID)
	}
	u.status = "已新建终端 " + pane.Name
	u.resize()
	return true
}

func (u *ui) drawCreate() {
	c := u.create
	if c == nil {
		return
	}
	title := "新建前台 agent"
	if c.mode == model.PaneWorker {
		title = "新建 worker"
	}
	lines := []string{title}
	if c.mode == model.PaneForeground {
		lines = append(lines, "只列出 PTY 预设；ACP 只在 worker 流程出现")
	} else {
		lines = append(lines, "worker 可以是 PTY 或 ACP；不会改变前台")
	}
	lines = append(lines, "")
	index := 0
	available := 0
	for _, p := range c.presets {
		if !p.Available {
			continue
		}
		marker := "  "
		if index == c.cursor {
			marker = "› "
		}
		line := fmt.Sprintf("%s%-20s %s", marker, truncateColumns(p.Name, 20), p.Version)
		lines = append(lines, line)
		index++
		available++
	}
	if available == 0 {
		lines = append(lines, "没有可用的预设")
		lines = append(lines, "  r 重新检测    s 创建 shell（临时）")
	}
	cursor := 0
	for _, p := range c.presets {
		if p.Available {
			continue
		}
		if cursor == 0 {
			lines = append(lines, "", "不可用")
		}
		cursor++
		lines = append(lines, "  "+truncateColumns(p.Name, 20)+" · "+truncateColumns(p.Detail, 48))
	}
	lines = append(lines, "")
	lines = append(lines, "Enter 创建 · ↑↓ 选择 · r 重新检测 · s 临时 shell · Esc 取消")
	lines = append(lines, c.reason)
	u.modal(lines)
}
