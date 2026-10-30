// Package agent implements the ACP v1 newline-delimited JSON-RPC stdio transport.
package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"wtable/internal/model"
)

const (
	maxFrame         = 4 << 20
	maxResult        = 1 << 20
	handshakeTimeout = 30 * time.Second
	promptTimeout    = 10 * time.Minute
	writeTimeout     = 5 * time.Second
	cancelGrace      = 3 * time.Second
)

type Config struct {
	Command   []string
	Cwd       string
	Env       []string
	SessionID string
	// OnMessage runs on the protocol reader and must return promptly.
	OnMessage    func(role, text string)
	OnPermission func(context.Context, model.Permission) (string, error)
	OnExit       func(error)
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("ACP error %d: %.512s", e.Code, e.Message) }

// UncertainError means a prompt may have run, but no conclusive ACP response was
// received. Callers must not automatically replay these prompts.
type UncertainError struct{ Err error }

func (e *UncertainError) Error() string { return "ACP prompt outcome unknown: " + e.Err.Error() }
func (e *UncertainError) Unwrap() error { return e.Err }

func IsUncertain(err error) bool {
	var uncertain *UncertainError
	return errors.As(err, &uncertain)
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type turn struct {
	ctx    context.Context
	cancel context.CancelFunc
	text   strings.Builder
}

type outbound struct {
	data []byte
	ack  chan error
	ctx  context.Context
}

type Client struct {
	cfg         Config
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      io.ReadCloser
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	writes      chan outbound
	once        sync.Once
	mu          sync.Mutex
	next        uint64
	pending     map[string]chan message
	permissions map[string]context.CancelFunc
	sessionID   string
	active      *turn
	exitErr     error
	stderrBytes atomic.Int64
}

func Start(ctx context.Context, cfg Config) (*Client, error) {
	if len(cfg.Command) == 0 || cfg.Command[0] == "" {
		return nil, errors.New("ACP command is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cwd := cfg.Cwd
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	cfg.Cwd = cwd
	life, cancel := context.WithCancel(ctx)
	c := &Client{cfg: cfg, ctx: life, cancel: cancel, done: make(chan struct{}), writes: make(chan outbound, 64), pending: make(map[string]chan message), permissions: make(map[string]context.CancelFunc), sessionID: cfg.SessionID}
	c.cmd = exec.CommandContext(life, cfg.Command[0], cfg.Command[1:]...)
	// Ordinary adapter descendants inherit this group. Detached sessions remain
	// outside it and are intentionally not managed by this client.
	c.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.cmd.Cancel = func() error { return c.killProcessGroup() }
	c.cmd.Dir = cwd
	c.cmd.Env = append(os.Environ(), cfg.Env...)
	c.cmd.WaitDelay = time.Second
	c.stdin, err = c.cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		c.stdin.Close()
		cancel()
		return nil, err
	}
	c.stdout = stdout
	// Agent stderr can contain credentials. Count it, never retain or forward it.
	c.cmd.Stderr = byteCounter{&c.stderrBytes}
	if err = c.cmd.Start(); err != nil {
		c.stdin.Close()
		stdout.Close()
		cancel()
		return nil, fmt.Errorf("start ACP process: %w", err)
	}
	go c.writeLoop()
	readDone := make(chan struct{})
	go func() { defer close(readDone); c.readLoop(stdout) }()
	go func() {
		// StdoutPipe must be drained before Wait closes its file descriptor.
		<-readDone
		err := c.cmd.Wait()
		if err != nil {
			err = fmt.Errorf("ACP process exited: %w (stderr: %d bytes suppressed)", err, c.stderrBytes.Load())
		}
		c.finish(err)
	}()
	initCtx, stop := context.WithTimeout(life, handshakeTimeout)
	defer stop()
	var init struct {
		ProtocolVersion   int `json:"protocolVersion"`
		AgentCapabilities struct {
			LoadSession bool `json:"loadSession"`
		} `json:"agentCapabilities"`
	}
	err = c.call(initCtx, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false}, "clientInfo": map[string]string{"name": "wtable", "version": "0.1.0"}}, &init)
	if err == nil && init.ProtocolVersion != 1 {
		err = fmt.Errorf("unsupported ACP protocol version %d", init.ProtocolVersion)
	}
	if err == nil && cfg.SessionID != "" && !init.AgentCapabilities.LoadSession {
		err = errors.New("ACP agent does not support loading existing sessions")
	}
	if err == nil {
		params := map[string]any{"cwd": cwd, "mcpServers": []any{}}
		method := "session/new"
		if cfg.SessionID != "" {
			method = "session/load"
			params["sessionId"] = cfg.SessionID
		}
		var session struct {
			SessionID string `json:"sessionId"`
		}
		err = c.call(initCtx, method, params, &session)
		if err == nil && method == "session/new" && session.SessionID == "" {
			err = errors.New("ACP session/new returned no session ID")
		}
		if err == nil && method == "session/new" {
			c.mu.Lock()
			c.sessionID = session.SessionID
			c.mu.Unlock()
		}
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

type byteCounter struct{ n *atomic.Int64 }

func (w byteCounter) Write(p []byte) (int, error) { w.n.Add(int64(len(p))); return len(p), nil }

func (c *Client) SessionID() string { c.mu.Lock(); defer c.mu.Unlock(); return c.sessionID }

func (c *Client) Prompt(ctx context.Context, text string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ctx, stop := context.WithTimeout(ctx, promptTimeout)
	defer stop()
	turnCtx, cancel := context.WithCancel(ctx)
	t := &turn{ctx: turnCtx, cancel: cancel}
	c.mu.Lock()
	if c.active != nil {
		c.mu.Unlock()
		cancel()
		return "", errors.New("ACP prompt already active")
	}
	c.active = t
	sessionID := c.sessionID
	c.mu.Unlock()
	defer func() { cancel(); c.mu.Lock(); c.active = nil; c.mu.Unlock() }()
	id, reply, err := c.begin(turnCtx, "session/prompt", map[string]any{"sessionId": sessionID, "prompt": []any{map[string]string{"type": "text", "text": text}}})
	if err != nil {
		// A timed-out write may already have reached the peer. Closing prevents
		// a late turn from being mistaken for the next prompt's output.
		c.finish(err)
		return "", &UncertainError{Err: err}
	}
	defer c.forget(id)
	var result message
	select {
	case result = <-reply:
	case <-c.done:
		select {
		case result = <-reply:
		default:
			return c.turnText(t), &UncertainError{Err: c.failure()}
		}
	case <-turnCtx.Done():
		cancelCtx, done := context.WithTimeout(context.Background(), cancelGrace)
		defer done()
		_ = c.notify(cancelCtx, "session/cancel", map[string]string{"sessionId": sessionID})
		select {
		case result = <-reply:
		case <-c.done:
			select {
			case result = <-reply:
			default:
				return c.turnText(t), &UncertainError{Err: c.failure()}
			}
		case <-cancelCtx.Done():
			select {
			case result = <-reply:
			default:
				err := errors.New("ACP agent did not acknowledge prompt cancellation")
				c.finish(err)
				return c.turnText(t), &UncertainError{Err: err}
			}
		}
	}
	if result.Error != nil {
		return c.turnText(t), result.Error
	}
	var outcome struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(result.Result, &outcome); err != nil || !validStopReason(outcome.StopReason) {
		err := errors.New("invalid ACP prompt response")
		c.finish(err)
		return c.turnText(t), &UncertainError{Err: err}
	}
	if outcome.StopReason == "cancelled" {
		return c.turnText(t), context.Canceled
	}
	return c.turnText(t), nil
}

func validStopReason(reason string) bool {
	switch reason {
	case "end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled":
		return true
	}
	return false
}

func (c *Client) turnText(t *turn) string { c.mu.Lock(); defer c.mu.Unlock(); return t.text.String() }

func (c *Client) Cancel(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	t := c.active
	c.mu.Unlock()
	if t != nil {
		t.cancel()
	}
	return nil
}

func (c *Client) Close() error { c.finish(nil); return nil }

func (c *Client) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exitErr != nil {
		return c.exitErr
	}
	return errors.New("ACP connection closed")
}

func (c *Client) finish(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.exitErr = err
		c.mu.Unlock()
		c.cancel()
		_ = c.stdin.Close()
		_ = c.stdout.Close()
		_ = c.killProcessGroup()
		close(c.done)
		if c.cfg.OnExit != nil {
			go c.cfg.OnExit(err)
		}
	})
}

func (c *Client) killProcessGroup() error {
	if c.cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func (c *Client) begin(ctx context.Context, method string, params any) (string, chan message, error) {
	c.mu.Lock()
	c.next++
	id := strconv.FormatUint(c.next, 10)
	reply := make(chan message, 1)
	c.pending[id] = reply
	c.mu.Unlock()
	err := c.send(ctx, map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "method": method, "params": params})
	if err != nil {
		c.forget(id)
		return "", nil, err
	}
	return id, reply, nil
}

func (c *Client) forget(id string) { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	id, reply, err := c.begin(ctx, method, params)
	if err != nil {
		return err
	}
	defer c.forget(id)
	select {
	case msg := <-reply:
		if msg.Error != nil {
			return msg.Error
		}
		if err := json.Unmarshal(msg.Result, out); err != nil {
			return errors.New("invalid ACP response")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		select {
		case msg := <-reply:
			if msg.Error != nil {
				return msg.Error
			}
			if json.Unmarshal(msg.Result, out) != nil {
				return errors.New("invalid ACP response")
			}
			return nil
		default:
			return c.failure()
		}
	}
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	return c.send(ctx, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *Client) send(ctx context.Context, v any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(data) > maxFrame {
		return errors.New("ACP outgoing frame exceeds 4 MiB")
	}
	w := outbound{data: append(data, '\n'), ack: make(chan error, 1), ctx: ctx}
	select {
	case c.writes <- w:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.failure()
	}
	select {
	case err := <-w.ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.failure()
	}
}

func (c *Client) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case w := <-c.writes:
			if err := w.ctx.Err(); err != nil {
				w.ack <- err
				continue
			}
			if f, ok := c.stdin.(*os.File); ok {
				_ = f.SetWriteDeadline(time.Now().Add(writeTimeout))
			}
			_, err := c.stdin.Write(w.data)
			w.ack <- err
			if err != nil {
				c.finish(errors.New("ACP stdin write failed"))
				return
			}
		}
	}
}

func validID(id json.RawMessage) bool {
	if len(id) == 0 {
		return false
	}
	var s string
	if json.Unmarshal(id, &s) == nil {
		return true
	}
	var n json.Number
	return json.Unmarshal(id, &n) == nil && string(id) != "null"
}

func (c *Client) readLoop(r io.Reader) {
	scan := bufio.NewScanner(r)
	scan.Buffer(make([]byte, 64<<10), maxFrame+1)
	for scan.Scan() {
		line := bytes.TrimSpace(scan.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg message
		if len(line) > maxFrame || json.Unmarshal(line, &msg) != nil || msg.JSONRPC != "2.0" {
			c.finish(errors.New("invalid ACP protocol frame"))
			return
		}
		if len(msg.ID) > 0 && !validID(msg.ID) {
			c.finish(errors.New("invalid ACP request ID"))
			return
		}
		if msg.Method != "" {
			if len(msg.ID) > 0 {
				c.handleRequest(msg)
			} else {
				c.handleNotification(msg)
			}
			select {
			case <-c.done:
				return
			default:
			}
			continue
		}
		if len(msg.ID) == 0 || ((len(msg.Result) > 0) == (msg.Error != nil)) {
			c.finish(errors.New("invalid ACP response envelope"))
			return
		}
		c.mu.Lock()
		reply := c.pending[string(msg.ID)]
		c.mu.Unlock()
		if reply != nil {
			select {
			case reply <- msg:
			default:
			}
		}
	}
	if scan.Err() != nil {
		c.finish(errors.New("ACP stdout read failed or frame exceeds 4 MiB"))
	} else {
		c.finish(fmt.Errorf("ACP agent disconnected (stderr: %d bytes suppressed)", c.stderrBytes.Load()))
	}
}

func (c *Client) handleNotification(msg message) {
	if msg.Method == "$/cancel_request" {
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		c.mu.Lock()
		cancel := c.permissions[string(p.RequestID)]
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return
	}
	if msg.Method != "session/update" {
		return
	}
	var p struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Type       string          `json:"sessionUpdate"`
			Content    json.RawMessage `json:"content"`
			Title      string          `json:"title"`
			Status     string          `json:"status"`
			ToolCallID string          `json:"toolCallId"`
		} `json:"update"`
	}
	if json.Unmarshal(msg.Params, &p) != nil {
		return
	}
	c.mu.Lock()
	sessionID := c.sessionID
	c.mu.Unlock()
	if sessionID != "" && p.SessionID != sessionID {
		return
	}
	role, text := "", ""
	switch p.Update.Type {
	case "agent_message_chunk", "user_message_chunk":
		role = "assistant"
		if p.Update.Type == "user_message_chunk" {
			role = "user"
		}
		var content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(p.Update.Content, &content) == nil && content.Type == "text" {
			text = content.Text
		}
	case "tool_call", "tool_call_update":
		role = "tool"
		text = p.Update.Title
		if text == "" {
			text = p.Update.ToolCallID
		}
		if p.Update.Status != "" {
			text += " [" + p.Update.Status + "]"
		}
		var content []struct {
			Type    string `json:"type"`
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(p.Update.Content, &content) == nil {
			for _, v := range content {
				if v.Type == "content" && v.Content.Type == "text" {
					text += "\n" + v.Content.Text
				}
			}
		}
	default:
		return
	}
	if text == "" {
		return
	}
	if role == "assistant" {
		c.mu.Lock()
		if c.active != nil {
			if len(text) > maxResult-c.active.text.Len() {
				c.mu.Unlock()
				c.finish(errors.New("ACP streamed result exceeds 1 MiB"))
				return
			}
			c.active.text.WriteString(text)
		}
		c.mu.Unlock()
	}
	if c.cfg.OnMessage != nil {
		c.cfg.OnMessage(role, text)
	}
}

func (c *Client) handleRequest(msg message) {
	if msg.Method != "session/request_permission" {
		go c.send(c.ctx, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": rpcError{Code: -32601, Message: "Method not implemented"}})
		return
	}
	var p struct {
		SessionID string `json:"sessionId"`
		ToolCall  struct {
			Title      string `json:"title"`
			ToolCallID string `json:"toolCallId"`
		} `json:"toolCall"`
		Options []struct {
			ID   string `json:"optionId"`
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(msg.Params, &p) != nil {
		go c.send(c.ctx, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": rpcError{Code: -32602, Message: "Invalid permission parameters"}})
		return
	}
	c.mu.Lock()
	base := c.ctx
	active := c.active
	if active != nil {
		base = active.ctx
	}
	ctx, cancel := context.WithTimeout(base, promptTimeout)
	c.permissions[string(msg.ID)] = cancel
	sessionID := c.sessionID
	c.mu.Unlock()
	permission := model.Permission{ID: "acp:" + string(msg.ID), Title: p.ToolCall.Title}
	if permission.Title == "" {
		permission.Title = p.ToolCall.ToolCallID
	}
	for _, o := range p.Options {
		permission.Options = append(permission.Options, model.PermissionOption{ID: o.ID, Name: o.Name, Kind: o.Kind})
	}
	go func() {
		defer cancel()
		defer func() { c.mu.Lock(); delete(c.permissions, string(msg.ID)); c.mu.Unlock() }()
		choice := ""
		if p.SessionID == sessionID && active != nil && c.cfg.OnPermission != nil {
			type decision struct {
				id  string
				err error
			}
			selected := make(chan decision, 1)
			go func() { id, err := c.cfg.OnPermission(ctx, permission); selected <- decision{id, err} }()
			select {
			case d := <-selected:
				if d.err == nil {
					choice = d.id
				}
			case <-ctx.Done():
			}
		}
		outcome := map[string]string{"outcome": "cancelled"}
		if ctx.Err() == nil {
			for _, o := range p.Options {
				if choice != "" && choice == o.ID {
					outcome = map[string]string{"outcome": "selected", "optionId": choice}
					break
				}
			}
		}
		_ = c.send(c.ctx, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"outcome": outcome}})
	}()
}
