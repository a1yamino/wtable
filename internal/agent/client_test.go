package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"wtable/internal/model"
)

// TestACPFixture is a deterministic subprocess agent. It never calls a provider.
// The test executable provides a separate process with real stdio pipes.
func TestACPFixture(t *testing.T) {
	mode := os.Getenv("WTABLE_ACP_FIXTURE")
	if mode == "" {
		return
	}
	if mode == "process-child" {
		path := os.Getenv("WTABLE_ACP_FIXTURE_CHILD")
		lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			os.Exit(13)
		}
		defer lock.Close()
		if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX) != nil {
			os.Exit(14)
		}
		if os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0600) != nil {
			os.Exit(15)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	if mode == "process-group" {
		child := exec.Command(os.Args[0], "-test.run=^TestACPFixture$")
		child.Env = append(os.Environ(), "WTABLE_ACP_FIXTURE=process-child")
		if child.Start() != nil {
			os.Exit(16)
		}
		go child.Wait()
	}
	enc := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if enc.Encode(v) != nil {
			os.Exit(2)
		}
	}
	respond := func(id json.RawMessage, result any) {
		send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	update := func(kind, text string) {
		send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "fixture-session", "update": map[string]any{"sessionUpdate": kind, "content": map[string]string{"type": "text", "text": text}}}})
	}
	scan := bufio.NewScanner(os.Stdin)
	var promptID json.RawMessage
	permissionID := json.RawMessage(`"permission-A"`)
	if mode == "numeric" {
		permissionID = json.RawMessage(`42`)
	}
	for scan.Scan() {
		var msg message
		if json.Unmarshal(scan.Bytes(), &msg) != nil {
			os.Exit(3)
		}
		switch msg.Method {
		case "initialize":
			var p struct {
				ProtocolVersion    int `json:"protocolVersion"`
				ClientCapabilities struct {
					Terminal bool `json:"terminal"`
					FS       struct {
						Read  bool `json:"readTextFile"`
						Write bool `json:"writeTextFile"`
					} `json:"fs"`
				} `json:"clientCapabilities"`
			}
			if json.Unmarshal(msg.Params, &p) != nil || p.ProtocolVersion != 1 || p.ClientCapabilities.Terminal || p.ClientCapabilities.FS.Read || p.ClientCapabilities.FS.Write {
				os.Exit(4)
			}
			respond(msg.ID, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]bool{"loadSession": mode == "load"}})
		case "session/new", "session/load":
			var p struct {
				Cwd        string `json:"cwd"`
				MCPServers []any  `json:"mcpServers"`
				SessionID  string `json:"sessionId"`
			}
			if json.Unmarshal(msg.Params, &p) != nil || !filepath.IsAbs(p.Cwd) || p.MCPServers == nil {
				os.Exit(5)
			}
			if msg.Method == "session/load" {
				if p.SessionID != "fixture-session" {
					os.Exit(6)
				}
				update("user_message_chunk", "history")
				respond(msg.ID, map[string]any{})
			} else {
				respond(msg.ID, map[string]string{"sessionId": "fixture-session"})
			}
		case "session/prompt":
			promptID = msg.ID
			if marker := os.Getenv("WTABLE_ACP_FIXTURE_MARKER"); marker != "" {
				if os.WriteFile(marker, []byte("prompt received"), 0600) != nil {
					os.Exit(12)
				}
			}
			if mode == "rpc-error" {
				send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32603, "message": "fixture explicit failure"}})
				continue
			}
			if mode == "invalid-response" {
				respond(msg.ID, map[string]any{})
				continue
			}
			if mode == "invalid-stop" {
				respond(msg.ID, map[string]string{"stopReason": "unrecognized-fixture-reason"})
				continue
			}
			if mode == "stream-overflow" {
				update("agent_message_chunk", strings.Repeat("x", maxResult/2))
				update("agent_message_chunk", strings.Repeat("x", maxResult/2))
				update("agent_message_chunk", "x")
				respond(msg.ID, map[string]string{"stopReason": "end_turn"})
				continue
			}
			if mode == "oversized" {
				fmt.Fprintln(os.Stdout, strings.Repeat("x", maxFrame+2))
				continue
			}
			if mode == "disconnect" {
				fmt.Fprintln(os.Stderr, "TOKEN=never-show-this")
				os.Exit(7)
			}
			if strings.HasPrefix(mode, "cancel") {
				if mode == "cancel-completed" {
					update("agent_message_chunk", "finished")
				}
				continue
			}
			update("agent_message_chunk", "hello ")
			update("agent_message_chunk", "世界")
			send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "fixture-session", "update": map[string]any{"sessionUpdate": "tool_call", "toolCallId": "call-1", "title": "Read file", "status": "pending"}}})
			send(map[string]any{"jsonrpc": "2.0", "id": permissionID, "method": "session/request_permission", "params": map[string]any{"sessionId": "fixture-session", "toolCall": map[string]string{"title": "Read file", "toolCallId": "call-1"}, "options": []any{map[string]string{"optionId": "reject", "kind": "reject_once", "name": "Reject"}, map[string]string{"optionId": "allow", "kind": "allow_once", "name": "Allow"}}}})
		case "session/cancel":
			if mode == "cancel-ignored" {
				continue
			}
			if mode == "cancel-disconnect" {
				os.Exit(0)
			}
			if mode == "cancel-completed" {
				respond(promptID, map[string]string{"stopReason": "end_turn"})
				continue
			}
			if mode != "permission-cancel" {
				respond(promptID, map[string]string{"stopReason": "cancelled"})
			}
		case "":
			if string(msg.ID) != string(permissionID) {
				os.Exit(8)
			}
			var p struct {
				Outcome struct {
					Outcome  string `json:"outcome"`
					OptionID string `json:"optionId"`
				} `json:"outcome"`
			}
			if json.Unmarshal(msg.Result, &p) != nil {
				os.Exit(9)
			}
			if mode == "permission-cancel" {
				if p.Outcome.Outcome != "cancelled" {
					os.Exit(10)
				}
				respond(promptID, map[string]string{"stopReason": "cancelled"})
			} else {
				if p.Outcome.Outcome != "selected" || p.Outcome.OptionID != "reject" {
					os.Exit(11)
				}
				respond(promptID, map[string]string{"stopReason": "end_turn"})
			}
		}
	}
	os.Exit(0)
}

func fixtureConfig(t *testing.T, mode string) Config {
	t.Helper()
	return Config{Command: []string{os.Args[0], "-test.run=^TestACPFixture$"}, Cwd: t.TempDir(), Env: []string{"WTABLE_ACP_FIXTURE=" + mode}}
}

func TestStreamAndPermission(t *testing.T) {
	for _, mode := range []string{"string", "numeric"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cfg := fixtureConfig(t, mode)
			var mu sync.Mutex
			var messages []string
			cfg.OnMessage = func(role, text string) { mu.Lock(); messages = append(messages, role+":"+text); mu.Unlock() }
			cfg.OnPermission = func(ctx context.Context, p model.Permission) (string, error) {
				if p.Title != "Read file" || len(p.Options) != 2 || !strings.HasPrefix(p.ID, "acp:") {
					return "", errors.New("invalid permission")
				}
				return "reject", nil
			}
			c, err := Start(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if c.SessionID() != "fixture-session" {
				t.Fatalf("session: %q", c.SessionID())
			}
			text, err := c.Prompt(ctx, "test")
			if err != nil || text != "hello 世界" {
				t.Fatalf("prompt: %q, %v", text, err)
			}
			mu.Lock()
			got := strings.Join(messages, "|")
			mu.Unlock()
			if !strings.Contains(got, "assistant:hello ") || !strings.Contains(got, "tool:Read file [pending]") {
				t.Fatalf("messages: %q", got)
			}
		})
	}
}

func TestDisconnectUnblocks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Start(ctx, fixtureConfig(t, "disconnect"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Prompt(ctx, "test")
	if !IsUncertain(err) || strings.Contains(err.Error(), "never-show-this") {
		t.Fatalf("unsafe or missing exit error: %v", err)
	}
}

func TestOversizedFrameUnblocks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Start(ctx, fixtureConfig(t, "oversized"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Prompt(ctx, "test")
	if !IsUncertain(err) || !strings.Contains(err.Error(), "frame exceeds") {
		t.Fatalf("oversized frame: %v", err)
	}
}

func TestCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Start(ctx, fixtureConfig(t, "cancel"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	turnCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	_, err = c.Prompt(turnCtx, "test")
	if !errors.Is(err, context.Canceled) || IsUncertain(err) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestCancellationFinalOutcome(t *testing.T) {
	for _, mode := range []string{"cancel-completed", "cancel-disconnect", "cancel-ignored"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			c, err := Start(ctx, fixtureConfig(t, mode))
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			turnCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
			defer stop()
			text, err := c.Prompt(turnCtx, "test")
			if mode == "cancel-completed" {
				if err != nil || text != "finished" {
					t.Fatalf("completed during cancellation: %q %v", text, err)
				}
			} else if !IsUncertain(err) {
				t.Fatalf("unconfirmed cancellation must be uncertain: %v", err)
			}
		})
	}
}

func TestPromptErrorClassification(t *testing.T) {
	for _, mode := range []string{"rpc-error", "invalid-response", "invalid-stop"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, err := Start(ctx, fixtureConfig(t, mode))
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_, err = c.Prompt(ctx, "test")
			if err == nil || IsUncertain(err) != (mode != "rpc-error") {
				t.Fatalf("wrong error classification: %v", err)
			}
			if mode != "rpc-error" {
				if _, err = c.Prompt(ctx, "must not reuse"); !IsUncertain(err) {
					t.Fatalf("invalid response connection reused: %v", err)
				}
			}
		})
	}
}

func TestStreamedResultLimitClosesConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Start(ctx, fixtureConfig(t, "stream-overflow"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	text, err := c.Prompt(ctx, "test")
	if !IsUncertain(err) || !strings.Contains(err.Error(), "exceeds 1 MiB") || len(text) != maxResult {
		t.Fatalf("stream limit: bytes=%d err=%v", len(text), err)
	}
	if _, err = c.Prompt(ctx, "must not reuse"); !IsUncertain(err) {
		t.Fatalf("overflow connection reused: %v", err)
	}
}

func TestProcessGroupTermination(t *testing.T) {
	for _, operation := range []string{"close", "context"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			cfg := fixtureConfig(t, "process-group")
			pidFile := filepath.Join(t.TempDir(), "child.pid")
			cfg.Env = append(cfg.Env, "WTABLE_ACP_FIXTURE_CHILD="+pidFile)
			c, err := Start(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			var childPID int
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				data, err := os.ReadFile(pidFile)
				if err == nil {
					childPID, err = strconv.Atoi(string(data))
					if err != nil {
						t.Fatal(err)
					}
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if childPID == 0 {
				t.Fatal("fixture child did not start")
			}
			t.Cleanup(func() { _ = syscall.Kill(childPID, syscall.SIGKILL) })
			lock, err := os.OpenFile(pidFile+".lock", os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			// The child holds this exclusive lock for its entire lifetime. An
			// observer acquiring it proves the child exited and released its FDs;
			// kill(pid,0) alone is misleading while an orphan is still a zombie.
			if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
				t.Fatalf("child did not hold lifetime lock: %v", err)
			}
			if operation == "close" {
				if err = c.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			deadline = time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
				if err == nil {
					_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
					return
				}
				if !errors.Is(err, syscall.EWOULDBLOCK) {
					t.Fatal(err)
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatal("adapter descendant survived client termination")
		})
	}
}

func TestPreCancelledPromptIsNotSent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := fixtureConfig(t, "string")
	marker := filepath.Join(t.TempDir(), "prompt-marker")
	cfg.Env = append(cfg.Env, "WTABLE_ACP_FIXTURE_MARKER="+marker)
	cfg.OnPermission = func(context.Context, model.Permission) (string, error) { return "reject", nil }
	c, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	preCtx, preCancel := context.WithCancel(ctx)
	preCancel()
	if _, err = c.Prompt(preCtx, "must not send"); !errors.Is(err, context.Canceled) || IsUncertain(err) {
		t.Fatalf("pre-cancelled: %v", err)
	}
	if _, err = os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-cancelled prompt was delivered: %v", err)
	}
	text, err := c.Prompt(ctx, "normal prompt")
	if err != nil || text != "hello 世界" {
		t.Fatalf("connection unusable after pre-cancelled prompt: %q %v", text, err)
	}
}

func TestPermissionCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := fixtureConfig(t, "permission-cancel")
	requested := make(chan struct{})
	cfg.OnPermission = func(ctx context.Context, p model.Permission) (string, error) {
		close(requested)
		<-ctx.Done()
		return "", ctx.Err()
	}
	c, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	result := make(chan error, 1)
	go func() { _, err := c.Prompt(ctx, "test"); result <- err }()
	select {
	case <-requested:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := c.Prompt(ctx, "overlapping"); err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("concurrent prompt: %v", err)
	}
	if err := c.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestSessionLoading(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := fixtureConfig(t, "string")
	cfg.SessionID = "fixture-session"
	if c, err := Start(ctx, cfg); err == nil {
		c.Close()
		t.Fatal("load without capability accepted")
	}
	cfg = fixtureConfig(t, "load")
	cfg.SessionID = "fixture-session"
	c, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.SessionID() != cfg.SessionID {
		t.Fatal("loaded session ID changed")
	}
}
