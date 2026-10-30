package server

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/rivo/uniseg"
	"wtable/internal/model"
)

func (s *Server) paneMetadataLocked(p model.Pane) model.Pane {
	p = clonePane(p)
	p.Readiness = "unconfirmed"
	p.ReadinessReason = "进程正在启动"
	if p.State != "running" {
		if p.State != "starting" {
			p.Readiness = "failed"
			p.ReadinessReason = "进程未运行"
		}
		return p
	}
	if p.Backend == "acp" {
		p.Readiness = "ready"
		p.ReadinessReason = "ACP 已初始化"
		if p.TurnState == "running" {
			p.Readiness = "busy"
			p.ReadinessReason = "ACP 正在执行任务"
		}
		if p.TurnState == "waiting-permission" {
			p.Readiness = "blocked"
			p.ReadinessReason = "等待权限确认"
		}
		if len(p.Messages) > 0 {
			m := p.Messages[len(p.Messages)-1]
			lines := strings.Split(strings.TrimSpace(s.diagnosticTextLocked(p.ID, m.Text)), "\n")
			p.LastOutput = clipColumns(lines[len(lines)-1], 80)
			p.LastActivity = m.At
		}
		return p
	}
	term := s.terminals[p.ID]
	if term == nil {
		p.Readiness = "failed"
		p.ReadinessReason = "PTY 未运行"
		return p
	}
	cfg := deliveryConfig(&p)
	lines, at := term.Tail(max(1, cfg.TailLines))
	lines = strings.Split(s.diagnosticTextLocked(p.ID, strings.Join(lines, "\n")), "\n")
	p.LastActivity = at
	if len(lines) > 0 {
		p.LastOutput = clipColumns(lines[len(lines)-1], 80)
	}
	alive, _ := term.Readiness()
	if !alive {
		p.Readiness = "failed"
		p.ReadinessReason = "PTY 已停止"
		return p
	}
	if cfg.BlockedRegex != "" {
		re, err := regexp.Compile(cfg.BlockedRegex)
		if err != nil {
			p.Readiness = "blocked"
			p.ReadinessReason = "blocked_regex 无效"
			return p
		}
		for _, line := range lines {
			if re.MatchString(line) {
				p.Readiness = "blocked"
				p.ReadinessReason = "blocked: " + clipColumns(line, 80)
				return p
			}
		}
	}
	for _, task := range s.state.Tasks {
		if task.To != p.ID || taskMode(task) != "inject" {
			continue
		}
		if task.State == "delivered" && task.DeliveryStatus == "write-unconfirmed" {
			p.Readiness = "unconfirmed"
			p.ReadinessReason = "写入未确认，请查看任务详情"
			return p
		}
		if task.State == "delivered" || task.State == "accepted" || task.State == "cancelling" {
			p.Readiness = "busy"
			p.ReadinessReason = "存在未结束的注入任务"
			return p
		}
	}
	if time.Since(at) < time.Duration(cfg.SettleMS)*time.Millisecond {
		p.ReadinessReason = "等待终端输出稳定"
		return p
	}
	p.Readiness = "ready"
	p.ReadinessReason = "投递闸门通过；不代表 agent 已接手任务"
	return p
}

func clipColumns(text string, width int) string {
	g := uniseg.NewGraphemes(text)
	var b strings.Builder
	for g.Next() {
		width -= g.Width()
		if width < 0 {
			break
		}
		b.WriteString(g.Str())
	}
	return b.String()
}

func (s *Server) peek(raw json.RawMessage) (any, error) {
	var args struct {
		ID    string `json:"id"`
		Lines int    `json:"lines"`
	}
	if err := decode(raw, &args); err != nil {
		return nil, err
	}
	if args.Lines == 0 {
		args.Lines = 20
	}
	args.Lines = max(1, min(args.Lines, 100))
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.paneLocked(args.ID)
	if p == nil {
		return nil, errors.New("pane 不存在")
	}
	lines := []string{}
	if term := s.terminals[p.ID]; term != nil {
		lines, _ = term.Tail(args.Lines)
	} else {
		for i := len(p.Messages) - 1; i >= 0 && len(lines) < args.Lines; i-- {
			parts := strings.Split(p.Messages[i].Text, "\n")
			lines = append(parts, lines...)
		}
		if len(lines) > args.Lines {
			lines = lines[len(lines)-args.Lines:]
		}
	}
	lines = strings.Split(s.diagnosticTextLocked(p.ID, strings.Join(lines, "\n")), "\n")
	return struct {
		Lines []string `json:"lines"`
	}{lines}, nil
}
