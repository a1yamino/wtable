package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"wtable/internal/model"
)

const briefLimit = 2048

// Configuration is explicit and local. Never infer profiles from agent credentials.
func readOnboardingConfig(path string, value any) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return fmt.Errorf("%s 超过 1 MiB", filepath.Base(path))
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("%s JSON 无效：%w", filepath.Base(path), err)
	}
	return nil
}

func validMetadata(value string, limit int) bool {
	return utf8.ValidString(value) && len(value) <= limit && !strings.ContainsFunc(value, unicode.IsControl)
}

func builtinPresets() []model.Preset {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	return []model.Preset{
		{ID: "shell", Name: "Shell · 终端", Backend: "pty", Command: []string{shell}},
		{ID: "codex-pty", Name: "Codex · 终端(PTY)", Backend: "pty", Command: []string{"codex"}},
		{ID: "codex-acp", Name: "Codex · ACP 会话", Backend: "acp", Command: []string{"codex-acp"}},
		{ID: "claude-pty", Name: "Claude · 终端(PTY)", Backend: "pty", Command: []string{"claude"}},
		{ID: "agy-pty", Name: "Agy · 终端(PTY)", Backend: "pty", Command: []string{"agy"}},
	}
}

func (s *Server) configuredPresets() ([]model.Preset, error) {
	presets := builtinPresets()
	custom := []model.Preset{}
	if err := readOnboardingConfig(filepath.Join(s.paths.Home, "presets.json"), &custom); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, p := range presets {
		seen[p.ID] = true
	}
	for _, p := range custom {
		if strings.TrimSpace(p.ID) == "" || !validMetadata(p.ID, 80) || strings.TrimSpace(p.Name) == "" || !validMetadata(p.Name, 80) || seen[p.ID] || (p.Backend != "pty" && p.Backend != "acp") || len(p.Command) == 0 || p.Command[0] == "" {
			return nil, errors.New("presets.json：预设 ID 须唯一，ID/名称不超过 80 字节，backend 为 pty/acp，command 为非空 argv")
		}
		for _, arg := range p.Command {
			if strings.ContainsRune(arg, 0) {
				return nil, errors.New("presets.json：command 含 NUL")
			}
		}
		seen[p.ID] = true
		p.Available = false
		p.Version = ""
		p.Detail = ""
		presets = append(presets, p)
	}
	return presets, nil
}

func (s *Server) catalog(input ...json.RawMessage) (model.Catalog, error) {
	var args struct {
		Kind string `json:"kind"`
	}
	var raw json.RawMessage
	if len(input) > 0 {
		raw = input[0]
	}
	if len(raw) > 0 {
		_ = decode(raw, &args)
	}
	if args.Kind != "" && args.Kind != model.PaneForeground && args.Kind != model.PaneWorker {
		return model.Catalog{}, errors.New("kind 必须为 foreground 或 worker")
	}
	presets, err := s.configuredPresets()
	if err != nil {
		return model.Catalog{}, err
	}
	if args.Kind == model.PaneForeground {
		// Foreground panes are PTY-only; ACP is a worker backend.
		filtered := presets[:0]
		for _, p := range presets {
			if p.Backend == "pty" {
				filtered = append(filtered, p)
			}
		}
		presets = filtered
	}
	profiles, err := s.accountProfiles()
	if err != nil {
		return model.Catalog{}, err
	}
	// Only known binaries receive --version. Custom wrappers may have side effects.
	s.catalogMu.Lock()
	if time.Since(s.catalogAt) > time.Minute || s.catalogVersions == nil {
		s.catalogVersions = map[string]string{}
		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, name := range []string{"codex", "codex-acp", "claude", "agy"} {
			path, e := exec.LookPath(name)
			if e != nil {
				continue
			}
			wg.Add(1)
			go func(name, path string) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(s.ctx, time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, path, "--version")
				cmd.WaitDelay = 100 * time.Millisecond
				output := &boundedOutput{limit: 4096}
				cmd.Stdout = output
				cmd.Stderr = output
				version := "version 探测失败/超时（仅确认二进制存在）"
				if cmd.Run() == nil {
					version = short(strings.TrimSpace(output.String()), 200)
				}
				mu.Lock()
				s.catalogVersions[name] = version
				mu.Unlock()
			}(name, path)
		}
		wg.Wait()
		s.catalogAt = time.Now()
	}
	for i := range presets {
		p := &presets[i]
		_, e := exec.LookPath(p.Command[0])
		p.Available = e == nil
		if !p.Available {
			p.Detail = "未找到可执行文件：" + p.Command[0]
			if p.ID == "codex-acp" {
				p.Detail += "；需要单独安装 ACP 适配器，不会自动安装"
			}
		} else {
			p.Detail = "二进制已找到；登录态与协议兼容性须在启动后确认"
		}
		if len(p.Command) == 1 {
			p.Version = s.catalogVersions[p.Command[0]]
		}
	}
	s.catalogMu.Unlock()
	roles, err := s.roles()
	if err != nil {
		return model.Catalog{}, err
	}
	accounts := []string{}
	accountInfos := []model.AccountInfo{}
	for _, p := range profiles {
		accounts = append(accounts, p.Name)
		accountInfos = append(accountInfos, model.AccountInfo{Name: p.Name, Endpoint: p.Endpoint})
	}
	sort.Strings(accounts)
	sort.Slice(accountInfos, func(i, j int) bool { return accountInfos[i].Name < accountInfos[j].Name })
	return model.Catalog{Presets: presets, Accounts: accounts, Roles: roles, AccountProfiles: accountInfos}, nil
}

func (s *Server) resolvePreset(args *model.SpawnParams) error {
	if args.Preset == "" {
		return nil
	}
	presets, err := s.configuredPresets()
	if err != nil {
		return err
	}
	for _, p := range presets {
		if p.ID != args.Preset {
			continue
		}
		if args.Backend != "" && args.Backend != p.Backend {
			return errors.New("backend 与预设不匹配")
		}
		if len(args.Command) > 0 {
			return errors.New("preset 与自定义 command 不能同时指定")
		}
		if _, err := exec.LookPath(p.Command[0]); err != nil {
			return fmt.Errorf("预设 %s 不可用：未找到 %s", p.ID, p.Command[0])
		}
		args.Backend = p.Backend
		args.Command = append([]string{}, p.Command...)
		return nil
	}
	return errors.New("预设不存在")
}

func (s *Server) resolveLayer(args *model.SpawnParams) error {
	if args.Layer == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var found *model.Layer
	for i := range s.state.Layers {
		l := &s.state.Layers[i]
		if l.Path == args.Layer {
			found = l
			break
		}
		if l.Name == args.Layer {
			if found != nil {
				return errors.New("layer 名称不唯一，请使用完整路径")
			}
			found = l
		}
	}
	if found == nil {
		return errors.New("layer 不存在")
	}
	if args.Cwd == "" {
		args.Cwd = found.Path
	}
	cwd, err := filepath.Abs(args.Cwd)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(found.Path, cwd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("cwd 必须位于选定 layer 内")
	}
	args.Layer = found.Path
	return nil
}

func (s *Server) briefTextLocked(p model.Pane, extra []string) (string, error) {
	peers := []string{}
	for _, other := range s.state.Panes {
		if other.ID != p.ID && (s.hasEdgeLocked(p.ID, other.ID) || contains(extra, other.ID)) {
			peers = append(peers, other.ID+" ("+other.Name+")")
		}
	}
	sort.Strings(peers)
	text := fmt.Sprintf("# Worktable brief\nName: %s\nPane: %s\nRole: %s\nCwd: %s\nLayer: %s\nPermitted outgoing peers: %s\n", p.Name, p.ID, p.Role, p.Cwd, p.Layer, strings.Join(peers, ", "))
	text += "\n## Collaboration contract\n"
	text += "This brief is your standing instructions, not a delegated task. Do not ack or result it.\n"
	text += "Check current peers before sending: \"$WTABLE_CLI\" peers. Grants can change.\n"
	if p.Backend == "acp" {
		text += "Delegated work arrives through ACP session/prompt; turn completion reports the result automatically. Do not call CLI ack, next or result for ACP tasks.\n"
	} else {
		text += "Delegated work arrives later as an injected task envelope. Read task_id in the envelope; accept with \"$WTABLE_CLI\" ack <task-id>; finish with \"$WTABLE_CLI\" result <task-id> <result>. ACK is not completion.\nFor claim tasks only: \"$WTABLE_CLI\" next claims work; finish with the same result command. Never use next to claim injected tasks.\n"
	}
	text += "Send only to permitted peers. For PTY peers use \"$WTABLE_CLI\" send --queue --delivery inject <peer-id> <text>; for ACP peers use --delivery prompt instead. You cannot recruit/spawn agents or add/remove edges. Never print tokens or credentials.\n"
	if p.RoleTemplate != "" {
		text += "\n## Role instructions (startup snapshot)\n" + p.RoleInstructions + "\n"
	}
	if len(text) > briefLimit {
		return "", errors.New("brief 超过 2048 UTF-8 字节；请缩短名称、角色、目录或连线列表")
	}
	return text, nil
}

func (s *Server) writeBrief(p *model.Pane, text string) error {
	p.BriefPath = filepath.Join(s.paths.Home, "briefs", p.ID+".md")
	if err := os.MkdirAll(filepath.Dir(p.BriefPath), 0700); err != nil {
		return err
	}
	// Temp + rename avoids exposing a partially updated brief to a running process.
	f, err := os.CreateTemp(filepath.Dir(p.BriefPath), ".brief-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p.BriefPath)
}

func (s *Server) enqueueBriefLocked(p *model.Pane, text string) (model.Task, error) {
	task, err := s.createTaskLocked("controller", model.SendParams{To: p.ID, Prompt: text, Kind: model.TaskKindBrief, Queue: true}, "")
	if err != nil {
		return task, err
	}
	p.BriefTask = task.ID
	return task, s.saveLocked()
}

func (s *Server) paneBrief(ref string, send bool) (any, error) {
	s.mu.Lock()
	p := s.paneLocked(ref)
	if p == nil {
		s.mu.Unlock()
		return nil, errors.New("pane 不存在")
	}
	text, err := s.briefTextLocked(*p, nil)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if err = s.writeBrief(p, text); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if !send {
		result := map[string]string{"path": p.BriefPath, "text": text, "task_id": p.BriefTask}
		err = s.saveLocked()
		s.mu.Unlock()
		return result, err
	}
	task, err := s.enqueueBriefLocked(p, text)
	s.mu.Unlock()
	if err == nil && task.DeliveryMode == "prompt" {
		s.wake(task.To)
	}
	return task, err
}

func (s *Server) updatePane(args model.PaneUpdate) (model.Pane, error) {
	if strings.TrimSpace(args.Name) == "" || !validMetadata(args.Name, 80) || !validMetadata(args.Role, 256) {
		return model.Pane{}, errors.New("名称需为 1–80 字节，角色最多 256 字节，均不得含控制字符")
	}
	if _, err := s.accountEnv(args.Account); err != nil {
		return model.Pane{}, err
	}
	endpoint, err := s.accountEndpoint(args.Account)
	if err != nil {
		return model.Pane{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.paneLocked(args.ID)
	if p == nil {
		return model.Pane{}, errors.New("pane 不存在")
	}
	if other := s.paneLocked(args.Name); other != nil && other.ID != p.ID {
		return model.Pane{}, errors.New("pane 名称已存在")
	}
	if p.Account != args.Account && !contains([]string{"exited", "failed", "interrupted"}, p.State) {
		return model.Pane{}, errors.New("账号只能在 exited/failed/interrupted 时修改；运行中环境不会改变")
	}
	copy := clonePane(*p)
	copy.Name = args.Name
	copy.Role = args.Role
	if args.RoleTemplate != nil && *args.RoleTemplate != p.RoleTemplate {
		copy.RoleTemplate = *args.RoleTemplate
		copy.RoleInstructions, copy.RoleHash = "", ""
		if *args.RoleTemplate != "" {
			role, err := s.role(*args.RoleTemplate)
			if err != nil {
				return model.Pane{}, err
			}
			copy.RoleInstructions, copy.RoleHash = role.Instructions, roleHash(role.Instructions)
			if copy.Role == "" {
				copy.Role = role.Name
			}
		}
	}
	copy.Account = args.Account
	copy.Endpoint = endpoint
	text, err := s.briefTextLocked(copy, nil)
	if err != nil {
		return model.Pane{}, err
	}
	if err = s.writeBrief(&copy, text); err != nil {
		return model.Pane{}, err
	}
	*p = copy
	return copy, s.saveLocked()
}

func (s *Server) removePane(ref string) error {
	s.mu.Lock()
	p := s.paneLocked(ref)
	if p == nil {
		s.mu.Unlock()
		return errors.New("pane 不存在")
	}
	id := p.ID
	s.mu.Unlock()
	lane := s.inputLane(id)
	lane.Lock()
	defer lane.Unlock()
	s.mu.Lock()
	if s.paneLocked(id) == nil {
		s.mu.Unlock()
		return errors.New("pane 不存在")
	}
	term, agent, cancel := s.terminals[id], s.agents[id], s.paneCancels[id]
	delete(s.runs, id)
	delete(s.peerTokens, id)
	delete(s.paneCancels, id)
	delete(s.terminals, id)
	delete(s.agents, id)
	delete(s.diagnosticSecrets, id)
	for i, p := range s.state.Panes {
		if p.ID == id {
			s.state.Panes = append(s.state.Panes[:i], s.state.Panes[i+1:]...)
			break
		}
	}
	edges := s.state.Edges[:0]
	for _, e := range s.state.Edges {
		if e.From != id && e.To != id {
			edges = append(edges, e)
		}
	}
	s.state.Edges = edges
	for i := range s.state.Tasks {
		t := &s.state.Tasks[i]
		changed := false
		if t.State == "queued" && (t.From == id || t.To == id) {
			changed = true
			t.State = "cancelled"
			t.Result = "pane 已移除，排队任务已取消"
			t.UpdatedAt = time.Now()
		} else if t.To == id && activeState(t.State) {
			changed = true
			t.State = "unknown"
			t.Result = "pane 已移除，执行结果未确认"
			t.UpdatedAt = time.Now()
		}
		if changed {
			s.completeReportLocked(t)
		}
		if t.To == id {
			if cancel := s.taskCancels[t.ID]; cancel != nil {
				cancel()
				delete(s.taskCancels, t.ID)
			}
		}
	}
	for key, a := range s.approvals {
		if a.permission.PaneID == id {
			delete(s.approvals, key)
		}
	}
	err := s.saveLocked()
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if term != nil {
		err = errors.Join(err, term.Close())
	}
	if agent != nil {
		err = errors.Join(err, agent.Close())
	}
	return err
}

func (s *Server) diagnosticTextLocked(id, text string) string {
	for _, secret := range s.diagnosticSecrets[id] {
		if secret != "" {
			text = redactDiagnostic(text, secret)
		}
	}
	return text
}

func (s *Server) exitSummaryLocked(id string) string {
	if term := s.terminals[id]; term != nil {
		lines := frameLines(term.Snapshot(0))
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		text := s.diagnosticTextLocked(id, strings.Join(lines, "\n"))
		lines = strings.Split(text, "\n")
		return strings.Join(lines[max(0, len(lines)-20):], "\n")
	}
	return ""
}

// A terminal may wrap a credential across physical rows. Match across line
// breaks so the diagnostic excerpt cannot turn wrapping into a secret leak.
func redactDiagnostic(text, secret string) string {
	cleanSecret := strings.NewReplacer("\r", "", "\n", "").Replace(secret)
	if cleanSecret == "" {
		return text
	}
	var clean strings.Builder
	positions := make([]int, 0, len(text))
	for i := 0; i < len(text); i++ {
		if text[i] != '\r' && text[i] != '\n' {
			clean.WriteByte(text[i])
			positions = append(positions, i)
		}
	}
	normalized := clean.String()
	var out strings.Builder
	cursor, original := 0, 0
	for cursor < len(normalized) {
		match := strings.Index(normalized[cursor:], cleanSecret)
		if match < 0 {
			break
		}
		start := cursor + match
		end := start + len(cleanSecret)
		out.WriteString(text[original:positions[start]])
		out.WriteString("[REDACTED]")
		original = positions[end-1] + 1
		cursor = end
	}
	out.WriteString(text[original:])
	return out.String()
}
