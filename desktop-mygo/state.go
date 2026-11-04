package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"wtable/internal/model"
)

// Native desktop preferences have their own format. The daemon remains the
// source of truth for panes, workspaces, and authorization.
type DesktopState struct {
	Workspaces map[string]*WorkspaceUI `json:"workspaces"`
	Templates  []TopologyTemplate      `json:"templates"`
}

type WorkspaceUI struct {
	Mode        string                `json:"mode"`
	MainID      string                `json:"main_id"`
	FocusID     string                `json:"focus_id"`
	DockID      string                `json:"dock_id"`
	DockWidth   float32               `json:"dock_width"`
	SelectedID  string                `json:"selected_id"`
	Layouts     map[string]NodeLayout `json:"layouts"`
	Viewport    BoardViewport         `json:"viewport"`
	ShowMinimap bool                  `json:"show_minimap"`
}

type NodeLayout struct {
	X, Y          float32
	Width, Height float32
	Expanded      bool
}

type BoardViewport struct{ X, Y, Scale float32 }

type TemplateAgent struct {
	Key, Name, Preset, Role, Account, Cwd string
	Command                               []string
	Layout                                NodeLayout
}

type TopologyTemplate struct {
	ID, Name, Description, Main string
	Agents                      []TemplateAgent
	Links                       []model.Edge
}

// Check the complete replacement before stopping any live agents. A malformed
// draft must not destroy a workspace just to discover a missing directory.
func validateTemplate(template *TopologyTemplate, catalog model.Catalog, workspace string) error {
	if len(template.Agents) == 0 {
		return fmt.Errorf("模板至少需要一个 Agent")
	}
	keys, names := make(map[string]bool), make(map[string]bool)
	for i := range template.Agents {
		agent := &template.Agents[i]
		if agent.Key == "" || keys[agent.Key] {
			return fmt.Errorf("模板中有重复或缺失的 Agent 标识")
		}
		keys[agent.Key] = true
		if !utf8.ValidString(agent.Name) || !utf8.ValidString(agent.Role) ||
			strings.TrimSpace(agent.Name) == "" || len(agent.Name) > 80 || len(agent.Role) > 256 ||
			strings.ContainsFunc(agent.Name+agent.Role, unicode.IsControl) {
			return fmt.Errorf("Agent 名称须为 1–80 字节，角色说明最多 256 字节，不能含控制字符")
		}
		if names[agent.Name] {
			return fmt.Errorf("模板中的 Agent 名称 %q 重复", agent.Name)
		}
		names[agent.Name] = true
		if agent.Account != "" && !slices.Contains(catalog.Accounts, agent.Account) {
			return fmt.Errorf("Agent %s 的账号配置 %q 不存在", agent.Name, agent.Account)
		}
		if agent.Cwd == "" {
			agent.Cwd = workspace
		}
		if !filepath.IsAbs(agent.Cwd) {
			return fmt.Errorf("Agent %s 的工作目录必须为绝对路径", agent.Name)
		}
		info, err := os.Stat(agent.Cwd)
		if err != nil {
			return fmt.Errorf("Agent %s 的工作目录无效：%w", agent.Name, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("Agent %s 的工作目录不是文件夹", agent.Name)
		}
		if agent.Preset != "" {
			available := slices.ContainsFunc(catalog.Presets, func(p model.Preset) bool {
				return p.ID == agent.Preset && p.Available && p.Backend == "pty"
			})
			if !available {
				return fmt.Errorf("Agent %s 的底座 %q 不可用", agent.Name, agent.Preset)
			}
		} else if len(agent.Command) == 0 {
			return fmt.Errorf("Agent %s 缺少底座或启动命令", agent.Name)
		}
	}
	if template.Main != "" && !keys[template.Main] {
		return fmt.Errorf("模板的主 Agent 不存在")
	}
	for _, edge := range template.Links {
		if !keys[edge.From] || !keys[edge.To] || edge.From == edge.To {
			return fmt.Errorf("模板授权必须指向两个不同且存在的 Agent")
		}
	}
	return nil
}

func defaultWorkspaceUI() *WorkspaceUI {
	return &WorkspaceUI{
		Mode: "focus", DockWidth: 360, Layouts: make(map[string]NodeLayout),
		Viewport: BoardViewport{X: 32, Y: 32, Scale: 1}, ShowMinimap: true,
	}
}

func loadDesktopState(path string) (DesktopState, error) {
	state := DesktopState{Workspaces: make(map[string]*WorkspaceUI)}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("读取桌面布局：%w", err)
	}
	if state.Workspaces == nil {
		state.Workspaces = make(map[string]*WorkspaceUI)
	}
	return state, nil
}

func writeDesktopState(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".desktop-*.json")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func paneStatus(pane model.Pane) string {
	if pane.State == "failed" || pane.State == "interrupted" {
		return "异常"
	}
	if pane.Readiness == "blocked" || pane.TurnState == "waiting-permission" || pane.TurnState == "waiting" {
		return "等待处理"
	}
	if pane.TurnState == "running" || pane.Readiness == "busy" || pane.State == "starting" || pane.State == "stopping" {
		return "工作中"
	}
	if pane.State == "exited" {
		return "已退出"
	}
	return "空闲"
}

func backendLabel(pane model.Pane) string {
	preset := strings.ToLower(pane.Preset)
	for _, name := range []string{"codex", "claude", "agy"} {
		if strings.Contains(preset, name) {
			return map[string]string{"codex": "Codex", "claude": "Claude", "agy": "Agy"}[name]
		}
	}
	if pane.Backend == "pty" && len(pane.Command) > 0 {
		switch filepath.Base(pane.Command[len(pane.Command)-1]) {
		case "zsh", "bash", "fish", "sh":
			return "Shell"
		}
	}
	return strings.ToUpper(pane.Backend)
}
