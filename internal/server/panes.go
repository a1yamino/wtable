package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"wtable/internal/agent"
	"wtable/internal/config"
	"wtable/internal/model"
	"wtable/internal/terminal"
)

// setCaps lets a user (or a probe) override what a pane accepts. The controller
// is the only caller; presets provide the defaults.
func (s *Server) setCaps(raw json.RawMessage) (model.Pane, error) {
	var a struct {
		ID        string          `json:"id"`
		Workspace string          `json:"workspace,omitempty"`
		Caps      *model.PaneCaps `json:"caps"`
	}
	if err := decode(raw, &a); err != nil {
		return model.Pane{}, err
	}
	if a.Caps == nil {
		return model.Pane{}, errors.New("缺少 caps")
	}
	s.mu.Lock()
	p, err := s.resolvePaneLocked(a.Workspace, a.ID)
	if err != nil {
		s.mu.Unlock()
		return model.Pane{}, err
	}
	caps := *a.Caps
	if caps.Source == "" {
		caps.Source = "user"
	}
	p.Caps = &caps
	copy := clonePane(*p)
	err = s.saveLocked()
	s.mu.Unlock()
	if err == nil && caps.PushIdle {
		s.wake(copy.ID)
	}
	return copy, err
}

// hasForeground reports whether the workspace already has the human-facing
// foreground. except lets a restart validate against everything but itself.
func (s *Server) hasForeground(workspace, except string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasForegroundLocked(workspace, except)
}

func (s *Server) hasForegroundLocked(workspace, except string) bool {
	for i := range s.state.Panes {
		p := &s.state.Panes[i]
		if p.ID != except && p.Workspace == workspace && p.Kind == model.PaneForeground {
			return true
		}
	}
	return false
}

const acpDisabledError = "ACP 已停用，请使用 PTY"

func (s *Server) spawn(args model.SpawnParams, restartID string) (model.Pane, error) {
	// Reject before resolving roles, layers, accounts, secrets, or writing any
	// state. Restart/copy callers pass the persisted backend through this path.
	if args.Backend == "acp" {
		return model.Pane{}, errors.New(acpDisabledError)
	}
	var role model.RoleTemplate
	var snapshotHash string
	if restartID == "" {
		var err error
		role, err = s.resolveRole(&args)
		if err != nil {
			return model.Pane{}, err
		}
		if args.RoleTemplate != "" {
			snapshotHash = roleHash(role.Instructions)
		}
		if args.Backend == "acp" {
			return model.Pane{}, errors.New(acpDisabledError)
		}
		if err := s.resolvePreset(&args); err != nil {
			return model.Pane{}, err
		}
	}
	if restartID != "" {
		s.mu.Lock()
		p := s.paneLocked(restartID)
		if p != nil {
			args.RoleTemplate = p.RoleTemplate
			args.Kind = p.Kind
			role.Instructions = p.RoleInstructions
			snapshotHash = p.RoleHash
			if args.Workspace == "" {
				args.Workspace = p.Workspace
			}
			if args.Cwd == "" {
				args.Cwd = p.Cwd
			}
			if args.Layer == "" {
				args.Layer = p.Layer
			}
		}
		s.mu.Unlock()
	}
	if err := s.resolveLayer(&args); err != nil {
		return model.Pane{}, err
	}
	if err := s.resolveSpawnLocation(&args); err != nil {
		return model.Pane{}, err
	}
	cwd := args.Cwd
	profileEnv, err := s.accountEnv(args.Account)
	if err != nil {
		return model.Pane{}, err
	}
	endpoint, err := s.accountEndpoint(args.Account)
	if err != nil {
		return model.Pane{}, err
	}
	if !validMetadata(args.Name, 80) || (args.Name != "" && strings.TrimSpace(args.Name) == "") || !validMetadata(args.Role, 256) {
		return model.Pane{}, errors.New("名称最多 80 字节、角色最多 256 字节，均不得包含控制字符")
	}

	if args.Backend == "" {
		args.Backend = "pty"
	}
	if args.Backend == "acp" {
		return model.Pane{}, errors.New(acpDisabledError)
	}
	if args.Backend != "pty" {
		return model.Pane{}, errors.New("backend 必须为 pty")
	}
	// The foreground is the one agent a human talks to. It is always a PTY;
	// ACP has no terminal surface and exists only as a worker backend.
	if args.Kind != "" && args.Kind != model.PaneForeground && args.Kind != model.PaneWorker {
		return model.Pane{}, errors.New("kind 必须为 foreground 或 worker")
	}
	if args.Kind == "" {
		switch {
		case args.Backend == "acp":
			args.Kind = model.PaneWorker
		case !s.hasForeground(args.Workspace, restartID):
			args.Kind = model.PaneForeground
		default:
			args.Kind = model.PaneWorker
		}
	}
	if args.Kind == model.PaneForeground && args.Backend != "pty" {
		return model.Pane{}, errors.New("前台 agent 必须是 PTY；ACP 只能创建为 worker")
	}
	if args.Kind == model.PaneForeground && restartID == "" && s.hasForeground(args.Workspace, "") {
		return model.Pane{}, errors.New("此工作区已有前台 agent；请先移除它，或把新 pane 创建为 worker")
	}
	if len(args.Command) == 0 {
		if args.Backend == "acp" {
			return model.Pane{}, errors.New("ACP 需要明确的启动命令")
		}
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/zsh"
		}
		args.Command = []string{shell}
	}
	if args.DeliveryConfig.BlockedRegex != "" {
		if _, err := regexp.Compile(args.DeliveryConfig.BlockedRegex); err != nil {
			return model.Pane{}, fmt.Errorf("blocked_regex: %w", err)
		}
	}
	if args.DeliveryConfig.SettleMS < 0 || args.DeliveryConfig.VerifyMS < 0 || args.DeliveryConfig.AckTimeoutMS < 0 || args.DeliveryConfig.TailLines < 0 {
		return model.Pane{}, errors.New("delivery_config values must not be negative")
	}
	if args.Cols == 0 {
		args.Cols = 80
	}
	if args.Rows == 0 {
		args.Rows = 24
	}
	args.Cols = max(2, min(240, args.Cols))
	args.Rows = max(1, min(100, args.Rows))
	token, err := config.Secret()
	if err != nil {
		return model.Pane{}, err
	}
	id := restartID
	if id == "" {
		id = newID("p-")
	}
	if args.Name == "" {
		args.Name = id
	}
	runCtx, runCancel := context.WithCancel(s.ctx)
	s.mu.Lock()
	if p := s.paneByNameLocked(args.Workspace, args.Name); p != nil && p.ID != restartID {
		s.mu.Unlock()
		runCancel()
		return model.Pane{}, errors.New("工作区内 pane 名称已存在")
	}
	var sessionID string
	targets := []string{}
	for _, ref := range args.ConnectTo {
		target, err := s.resolvePaneLocked(args.Workspace, ref)
		if err != nil {
			s.mu.Unlock()
			runCancel()
			return model.Pane{}, err
		}
		if target.ID == id {
			s.mu.Unlock()
			runCancel()
			return model.Pane{}, errors.New("connect_to 指向自身")
		}
		if target.Workspace != args.Workspace {
			s.mu.Unlock()
			runCancel()
			return model.Pane{}, errors.New("connect_to 不能跨工作区")
		}
		if !contains(targets, target.ID) {
			targets = append(targets, target.ID)
		}
	}
	candidate := model.Pane{ID: id, Name: args.Name, Kind: args.Kind, Backend: args.Backend, Command: args.Command, Cwd: cwd, Workspace: args.Workspace, Preset: args.Preset, Role: args.Role, RoleTemplate: args.RoleTemplate, RoleInstructions: role.Instructions, RoleHash: snapshotHash, Account: args.Account, Endpoint: endpoint, Layer: args.Layer, InjectBrief: args.InjectBrief, LogPath: s.paths.Log, State: "starting", TurnState: "idle", CreatedAt: time.Now(), DeliveryConfig: args.DeliveryConfig}
	brief, briefErr := s.briefTextLocked(candidate, targets)
	if briefErr != nil {
		s.mu.Unlock()
		runCancel()
		return model.Pane{}, briefErr
	}
	if restartID == "" && args.InjectBrief && len(s.state.Tasks) >= 2000 {
		s.mu.Unlock()
		runCancel()
		return model.Pane{}, errors.New("任务记录已满，无法排队 brief")
	}
	if restartID != "" {
		p := s.paneLocked(restartID)
		if p == nil {
			s.mu.Unlock()
			runCancel()
			return model.Pane{}, errors.New("pane 不存在")
		}
		if p.State == "running" || p.State == "starting" || p.State == "stopping" {
			s.mu.Unlock()
			runCancel()
			return model.Pane{}, errors.New("pane 仍在运行，请先停止")
		}
		sessionID = p.SessionID
		if err = s.writeBrief(&candidate, brief); err != nil {
			s.mu.Unlock()
			runCancel()
			return model.Pane{}, err
		}
		p.BriefPath = candidate.BriefPath
		p.Endpoint = endpoint
		p.Kind = candidate.Kind
		p.ExitSummary = ""
		p.LogPath = s.paths.Log
		p.State = "starting"
		p.TurnState = "idle"
		p.Error = ""
		p.ExitCode = nil
		delete(s.agents, id)
		delete(s.terminals, id)
	} else {
		total, _ := s.workspacePaneCountLocked(args.Workspace)
		if total >= 32 {
			s.mu.Unlock()
			runCancel()
			return model.Pane{}, errors.New("每个工作区最多保留 32 个 pane")
		}
		if len(s.state.Panes) >= 128 {
			s.mu.Unlock()
			runCancel()
			return model.Pane{}, errors.New("pane 总数已达上限 128")
		}
		if candidate.Kind == model.PaneForeground && s.hasForegroundLocked(args.Workspace, "") {
			s.mu.Unlock()
			runCancel()
			return model.Pane{}, errors.New("此工作区已有前台 agent；请先移除它，或把新 pane 创建为 worker")
		}
		s.ensureWorkspaceLocked(args.Workspace, time.Now())
		if err = s.writeBrief(&candidate, brief); err != nil {
			s.mu.Unlock()
			runCancel()
			return model.Pane{}, err
		}
		s.state.Panes = append(s.state.Panes, candidate)
	}
	for _, target := range targets {
		if !s.hasEdgeLocked(id, target) {
			s.state.Edges = append(s.state.Edges, model.Edge{From: id, To: target})
		}
	}
	if s.diagnosticSecrets == nil {
		s.diagnosticSecrets = map[string][]string{}
	}
	s.diagnosticSecrets[id] = []string{token}
	for _, value := range profileEnv {
		if value != "" {
			s.diagnosticSecrets[id] = append(s.diagnosticSecrets[id], value)
		}
	}
	runID := hash(token)
	s.peerTokens[id] = runID
	s.runs[id] = runID
	s.paneCancels[id] = runCancel
	if err = s.saveLocked(); err != nil {
		s.mu.Unlock()
		runCancel()
		return model.Pane{}, err
	}
	s.mu.Unlock()
	env, err := s.childEnv(id, token)
	if err != nil {
		s.failedStart(id, runID, err)
		return model.Pane{}, err
	}
	for key, value := range profileEnv {
		env = append(env, key+"="+value)
	}
	if args.InjectBrief {
		env = append(env, "WTABLE_BRIEF="+candidate.BriefPath)
	}
	if args.Backend == "pty" {
		t, err := terminal.Start(runCtx, terminal.Config{Command: args.Command, Cwd: cwd, Env: env, Cols: args.Cols, Rows: args.Rows, OnChange: func() { s.revision.Add(1) }, OnExit: func(code int, exitErr error) { s.paneExited(id, runID, code, exitErr) }})
		if err != nil {
			s.failedStart(id, runID, err)
			return model.Pane{}, err
		}
		s.mu.Lock()
		if s.runs[id] != runID {
			s.mu.Unlock()
			t.Close()
			return model.Pane{}, errors.New("pane 启动已被替换")
		}
		s.terminals[id] = t
		p := s.paneLocked(id)
		cancelled := runCtx.Err() != nil || p.State == "stopping"
		if p.State == "starting" && !cancelled {
			p.State = "running"
		}
		if p.State == "failed" {
			p.ExitSummary = s.exitSummaryLocked(id)
		}
		err = s.saveLocked()
		if err == nil && restartID == "" && args.InjectBrief && p.State == "running" {
			_, err = s.enqueueBriefLocked(p, brief)
		}
		copy := clonePane(*p)
		s.mu.Unlock()
		if cancelled {
			t.Close()
			return copy, errors.New("pane 启动已取消")
		}
		return copy, err
	}
	a, err := agent.Start(runCtx, agent.Config{Command: args.Command, Cwd: cwd, Env: env, SessionID: sessionID, OnUpdate: func() { s.acceptACP(id, runID) }, OnMessage: func(role, text string) { s.message(id, runID, role, text) }, OnPermission: func(ctx context.Context, p model.Permission) (string, error) { return s.permission(ctx, id, runID, p) }, OnExit: func(err error) { s.paneExited(id, runID, 0, err) }})
	if err != nil {
		s.failedStart(id, runID, err)
		return model.Pane{}, err
	}
	s.mu.Lock()
	if s.runs[id] != runID {
		s.mu.Unlock()
		a.Close()
		return model.Pane{}, errors.New("pane 启动已被替换")
	}
	s.agents[id] = a
	p := s.paneLocked(id)
	p.SessionID = a.SessionID()
	cancelled := runCtx.Err() != nil || p.State == "stopping"
	if p.State == "starting" && !cancelled {
		p.State = "running"
	}
	err = s.saveLocked()
	if err == nil && restartID == "" && args.InjectBrief && p.State == "running" {
		_, err = s.enqueueBriefLocked(p, brief)
	}
	copy := clonePane(*p)
	s.mu.Unlock()
	if cancelled {
		a.Close()
		return copy, errors.New("pane 启动已取消")
	}
	if err == nil {
		s.wake(id)
	}
	return copy, err
}

func (s *Server) childEnv(id, token string) ([]string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	env := make([]string, 0, len(os.Environ())+7)
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "WTABLE_") {
			env = append(env, v)
		}
	}
	s.mu.Lock()
	workspace := ""
	if p := s.paneByIDLocked(id); p != nil {
		workspace = p.Workspace
	}
	s.mu.Unlock()
	env = append(env, "WTABLE_CLI="+exe, "WTABLE_HOME="+s.paths.Home, "WTABLE_PANE_ID="+id, "WTABLE_PEER_TOKEN="+token)
	if workspace != "" {
		env = append(env, "WTABLE_WORKSPACE="+workspace)
	}
	return env, nil
}

func (s *Server) failedStart(id, runID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.runs[id] != runID {
		return
	}
	if p := s.paneLocked(id); p != nil {
		p.State = "failed"
		p.LogPath = s.paths.Log
		p.ExitSummary = s.exitSummaryLocked(id)
		p.Error = s.diagnosticTextLocked(id, err.Error())
	}
	delete(s.peerTokens, id)
	delete(s.runs, id)
	if cancel := s.paneCancels[id]; cancel != nil {
		cancel()
		delete(s.paneCancels, id)
	}
	s.saveLocked()
}

func (s *Server) paneExited(id, runID string, code int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.runs[id] != runID {
		return
	}
	if p := s.paneLocked(id); p != nil {
		p.ExitCode = &code
		p.State = "exited"
		if code != 0 || err != nil {
			p.State = "failed"
			p.ExitSummary = s.exitSummaryLocked(id)
			p.LogPath = s.paths.Log
		}
		if err != nil {
			p.Error = s.diagnosticTextLocked(id, err.Error())
		}
		if p.TurnState == "running" || p.TurnState == "waiting-permission" {
			p.TurnState = "unknown"
		}
	}
	delete(s.peerTokens, id)
	for i := range s.state.Tasks {
		t := &s.state.Tasks[i]
		if t.To == id && activeState(t.State) {
			if t.State == "cancelling" && (taskMode(*t) == "push" || taskMode(*t) == "steer") {
				// Process exit does not replace the human confirmation required by inject cancellation.
				t.Result = "agent 进程已退出；取消仍待人工确认，已发生的操作不会撤销"
			} else {
				t.State = "unknown"
				t.Result = "agent 进程退出，执行结果未确认"
			}
			t.UpdatedAt = time.Now()
			if t.State == "unknown" {
				s.completeReportLocked(t)
			}
		}
	}
	s.saveLocked()
}

func (s *Server) stop(ref, workspace string) error {
	s.mu.Lock()
	p, err := s.resolvePaneLocked(workspace, ref)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	id := p.ID
	t := s.terminals[id]
	a := s.agents[id]
	cancel := s.paneCancels[id]
	if p.State == "starting" || p.State == "running" {
		p.State = "stopping"
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if t != nil {
		return t.Close()
	}
	if a != nil {
		return a.Close()
	}
	return nil
}

func (s *Server) message(id, runID, role, text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.runs[id] != runID {
		return
	}
	p := s.paneLocked(id)
	if p == nil {
		return
	}
	if n := len(p.Messages); n > 0 && p.Messages[n-1].Role == role {
		p.Messages[n-1].Text += text
	} else {
		p.Messages = append(p.Messages, model.Message{Role: role, Text: text, At: time.Now()})
		// Unread is the human's view: a reply that arrives while another pane is
		// focused is news; one that arrives in the focused pane is not.
		if role == "assistant" && s.state.LastFocus != id && p.Unread < 999 {
			p.Unread++
		}
	}
	// Chat is a bounded presentation log; complete task results live separately.
	total := 0
	for i := len(p.Messages) - 1; i >= 0; i-- {
		m := &p.Messages[i]
		if len(m.Text) > 65536 {
			m.Text = string([]rune(m.Text)[max(0, len([]rune(m.Text))-16000):])
		}
		total += len(m.Text)
		if total > 65536 && i < len(p.Messages)-1 {
			p.Messages = p.Messages[i+1:]
			break
		}
	}
	if len(p.Messages) > 200 {
		p.Messages = p.Messages[len(p.Messages)-200:]
	}
	// ACP streams many chunks per turn. Persisting the whole state on every
	// chunk blocks the protocol reader; deliveryLoop flushes chat shortly after
	// it settles, while task state changes still save immediately.
	s.chatDirty = true
}

func (s *Server) permission(ctx context.Context, paneID, runID string, p model.Permission) (string, error) {
	p.PaneID = paneID
	p.ID = paneID + ":" + runID[:8] + ":" + p.ID
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	if p.ExpiresAt.IsZero() {
		p.ExpiresAt = p.CreatedAt.Add(10 * time.Minute)
	}
	a := &approval{permission: p, answer: make(chan string, 1)}
	s.mu.Lock()
	if s.closing || s.runs[paneID] != runID {
		s.mu.Unlock()
		return "", context.Canceled
	}
	s.approvals[p.ID] = a
	if pane := s.paneLocked(paneID); pane != nil {
		pane.TurnState = "waiting-permission"
	}
	s.revision.Add(1)
	s.mu.Unlock()
	answered := false
	defer func() {
		s.mu.Lock()
		delete(s.approvals, p.ID)
		if !answered && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			s.recordDecisionLocked(a, "", "expired", false)
			_ = s.saveLocked()
		}
		if pane := s.paneLocked(paneID); pane != nil && s.runs[paneID] == runID && pane.TurnState == "waiting-permission" {
			pane.TurnState = "running"
		}
		s.revision.Add(1)
		s.mu.Unlock()
	}()
	select {
	case option := <-a.answer:
		answered = true
		return option, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-s.ctx.Done():
		return "", s.ctx.Err()
	}
}

func (s *Server) replyPermission(raw []byte) error {
	var args struct {
		ID       string `json:"id"`
		OptionID string `json:"option_id"`
		Batch    bool   `json:"batch"`
	}
	if err := decode(raw, &args); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.approvals[args.ID]
	if a == nil {
		return errors.New("审批请求已失效")
	}
	valid := args.OptionID == ""
	for _, o := range a.permission.Options {
		if o.ID == args.OptionID {
			valid = true
			if s.storeErr != nil && !strings.HasPrefix(o.Kind, "reject") {
				return errors.New("状态无法持久化时只允许拒绝或取消审批")
			}
		}
	}
	if !valid {
		return fmt.Errorf("审批选项无效：%s", args.OptionID)
	}
	select {
	case a.answer <- args.OptionID:
		delete(s.approvals, args.ID)
		s.recordDecisionLocked(a, args.OptionID, "", args.Batch)
		return s.saveLocked()
	default:
		return errors.New("审批已经响应")
	}
}
