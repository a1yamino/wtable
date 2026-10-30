package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"wtable/internal/config"
	"wtable/internal/model"
	"wtable/internal/rpc"
	"wtable/internal/server"
	"wtable/internal/tui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "wtable:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		fmt.Print(help)
		return nil
	}
	if len(args) > 0 && args[0] == "version" {
		fmt.Println("wtable 0.1.0")
		return nil
	}
	paths, err := config.Resolve()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if len(args) > 0 && args[0] == "daemon" {
		return server.Run(ctx, paths)
	}
	if os.Getenv("WTABLE_PEER_TOKEN") == "" {
		if len(args) > 0 && args[0] == "shutdown" {
			if _, err = os.Stat(paths.Socket); os.IsNotExist(err) {
				return errors.New("daemon 未运行")
			}
		}
		if err = ensureDaemon(ctx, paths); err != nil {
			return err
		}
	}
	token, err := paths.ClientToken()
	if err != nil {
		return err
	}
	c, err := rpc.Dial(ctx, paths.Socket, token)
	if err != nil {
		return err
	}
	defer c.Close()
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "attach" {
		if len(args) > 1 {
			return errors.New("MVP 只支持本地 attach")
		}
		return tui.Run(ctx, c, cwd)
	}
	call := func(method string, args, result any) error { return c.Call(ctx, method, args, result) }
	jsonMode := false
	// Free-form prompts and child command arguments must remain byte-for-byte.
	if args[0] != "new" && args[0] != "recruit" && args[0] != "send" && args[0] != "ask" && args[0] != "next" && args[0] != "result" && args[0] != "wait" && args[0] != "input" {
		clean := make([]string, 0, len(args))
		for _, arg := range args {
			if arg == "--json" {
				jsonMode = true
			} else {
				clean = append(clean, arg)
			}
		}
		args = clean
	}
	if len(args) == 0 {
		return errors.New("缺少命令")
	}
	switch args[0] {
	case "diag":
		var out any
		if err = call("diag.info", nil, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "list", "panes":
		var state model.Snapshot
		if err = call("state.get", map[string]bool{"frames": false}, &state); err != nil {
			return err
		}
		if jsonMode {
			return printJSON(state.Panes)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\t名称\t后端\t进程\t轮次\t目录")
		for _, p := range state.Panes {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", p.ID, p.Name, p.Backend, p.State, p.TurnState, p.Cwd)
		}
		return w.Flush()
	case "peers":
		var panes []model.Pane
		if err = call("peer.list", nil, &panes); err != nil {
			return err
		}
		return printJSON(panes)
	case "new", "recruit":
		fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
		fs.BoolVar(&jsonMode, "json", false, "输出 JSON")
		name := fs.String("name", "", "pane 名称")
		dir := fs.String("dir", cwd, "工作目录")
		backend := fs.String("backend", "pty", "pty 或 acp")
		layer := fs.String("layer", "", "已有 layer 名称")
		rest := args[1:]
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			*name = rest[0]
			rest = rest[1:]
		}
		if err = fs.Parse(rest); err != nil {
			return err
		}
		abs, err := filepath.Abs(*dir)
		if err != nil {
			return err
		}
		if *layer != "" {
			var state model.Snapshot
			if err = call("state.get", map[string]bool{"frames": false}, &state); err != nil {
				return err
			}
			found := false
			for _, l := range state.Layers {
				if l.Name == *layer {
					if found {
						return errors.New("多个仓库有同名 layer，请用 --dir 明确路径")
					}
					abs = l.Path
					found = true
				}
			}
			if !found {
				return errors.New("layer 不存在")
			}
		}
		command := fs.Args()
		if len(command) == 0 && args[0] == "recruit" {
			return errors.New("recruit 需要 -- 后的 agent 启动命令")
		}
		var p model.Pane
		spawnCtx, stop := context.WithTimeout(ctx, 45*time.Second)
		defer stop()
		err = c.Call(spawnCtx, "pane.spawn", model.SpawnParams{Name: *name, Backend: *backend, Cwd: abs, Command: command, Cols: 80, Rows: 24}, &p)
		if err != nil {
			return err
		}
		if jsonMode {
			return printJSON(p)
		}
		fmt.Printf("%s  %s  %s\n", p.ID, p.Name, p.Backend)
		return nil
	case "stop", "restart":
		if len(args) != 2 {
			return fmt.Errorf("用法：wtable %s <pane>", args[0])
		}
		var out any
		if err = call("pane."+args[0], map[string]string{"id": args[1]}, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "connect", "disconnect":
		if len(args) != 3 {
			return fmt.Errorf("用法：wtable %s <from> <to>", args[0])
		}
		method := "edge.add"
		if args[0] == "disconnect" {
			method = "edge.remove"
		}
		var out any
		if err = call(method, model.Edge{From: args[1], To: args[2]}, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "send", "ask":
		fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
		fs.BoolVar(&jsonMode, "json", false, "输出 JSON")
		key := fs.String("key", "", "任务去重键")
		timeout := fs.Duration("timeout", 10*time.Minute, "等待时限")
		if err = fs.Parse(args[1:]); err != nil {
			return err
		}
		rest := fs.Args()
		if len(rest) < 2 {
			return fmt.Errorf("用法：wtable %s [--key KEY] <pane> <prompt>", args[0])
		}
		var t model.Task
		if err = call("task.send", map[string]string{"to": rest[0], "prompt": strings.Join(rest[1:], " "), "dedupe_key": *key}, &t); err != nil {
			return err
		}
		if args[0] == "ask" {
			t, err = waitTask(ctx, c, t.ID, *timeout)
			if err != nil {
				return err
			}
			if jsonMode {
				return printJSON(t)
			}
			fmt.Println(t.Result)
			if t.State != "completed" {
				return fmt.Errorf("任务 %s：%s", t.ID, t.State)
			}
			return nil
		}
		if jsonMode {
			return printJSON(t)
		}
		fmt.Println(t.ID)
		return nil
	case "next":
		fs := flag.NewFlagSet("next", flag.ContinueOnError)
		fs.BoolVar(&jsonMode, "json", false, "输出 JSON")
		timeout := fs.Duration("timeout", 0, "等待新任务的时限")
		if err = fs.Parse(args[1:]); err != nil {
			return err
		}
		deadline := time.Now().Add(*timeout)
		for {
			var t *model.Task
			if err = call("task.next", nil, &t); err != nil {
				return err
			}
			if t != nil {
				return printJSON(t)
			}
			if time.Now().After(deadline) {
				fmt.Println("null")
				return nil
			}
			if err = pause(ctx, 150*time.Millisecond); err != nil {
				return err
			}
		}
	case "result":
		fs := flag.NewFlagSet("result", flag.ContinueOnError)
		fs.BoolVar(&jsonMode, "json", false, "输出 JSON")
		failed := fs.Bool("failed", false, "标记失败")
		if err = fs.Parse(args[1:]); err != nil {
			return err
		}
		rest := fs.Args()
		if len(rest) < 2 {
			return errors.New("用法：wtable result [--failed] <task-id> <text|->")
		}
		text := strings.Join(rest[1:], " ")
		if text == "-" {
			b, err := io.ReadAll(io.LimitReader(os.Stdin, (1<<20)+1))
			if err != nil {
				return err
			}
			text = string(b)
		}
		var out any
		if err = call("task.result", map[string]any{"id": rest[0], "result": text, "failed": *failed}, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "wait":
		fs := flag.NewFlagSet("wait", flag.ContinueOnError)
		fs.BoolVar(&jsonMode, "json", false, "输出 JSON")
		timeout := fs.Duration("timeout", 10*time.Minute, "等待时限")
		if err = fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) != 1 {
			return errors.New("用法：wtable wait [--timeout 10m] <task-id>")
		}
		t, err := waitTask(ctx, c, fs.Args()[0], *timeout)
		if err != nil {
			return err
		}
		if jsonMode {
			return printJSON(t)
		}
		fmt.Println(t.Result)
		if t.State != "completed" {
			return fmt.Errorf("任务状态：%s", t.State)
		}
		return nil
	case "task":
		if len(args) != 2 {
			return errors.New("用法：wtable task <task-id>")
		}
		var t model.Task
		if err = call("task.get", map[string]string{"id": args[1]}, &t); err != nil {
			return err
		}
		return printJSON(t)
	case "tasks":
		var tasks []model.Task
		if err = call("task.list", nil, &tasks); err != nil {
			return err
		}
		if jsonMode {
			return printJSON(tasks)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\t来源\t目标\t状态\t内容")
		for _, t := range tasks {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.From, t.To, t.State, strings.ReplaceAll(short(t.Prompt, 70), "\n", " "))
		}
		return w.Flush()
	case "cancel":
		if len(args) != 2 {
			return errors.New("用法：wtable cancel <task-id>")
		}
		var out any
		if err = call("task.cancel", map[string]string{"id": args[1]}, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "permissions":
		var state model.Snapshot
		if err = call("state.get", map[string]bool{"frames": false}, &state); err != nil {
			return err
		}
		return printJSON(state.Permissions)
	case "approve":
		if len(args) != 3 {
			return errors.New("用法：wtable approve <permission-id> <option-id>（空 option-id 表示取消）")
		}
		return call("permission.reply", map[string]string{"id": args[1], "option_id": args[2]}, nil)
	case "screen":
		if len(args) != 2 {
			return errors.New("用法：wtable screen <pane>")
		}
		var f model.Frame
		if err = call("pane.screen", map[string]string{"id": args[1]}, &f); err != nil {
			return err
		}
		if jsonMode {
			return printJSON(f)
		}
		for _, row := range f.Cells {
			var line strings.Builder
			for _, cell := range row {
				if cell.Width > 0 {
					line.WriteString(cell.Text)
				}
			}
			fmt.Println(strings.TrimRight(line.String(), " "))
		}
		return nil
	case "input":
		if len(args) < 3 {
			return errors.New("用法：wtable input <pane-id> <text>；通过 shell 的 $'…\\n' 发送换行")
		}
		return call("pane.input", map[string]string{"id": args[1], "data": strings.Join(args[2:], " ")}, nil)
	case "layer":
		if len(args) < 2 {
			return errors.New("用法：wtable layer list|create")
		}
		if args[1] == "list" {
			var state model.Snapshot
			if err = call("state.get", map[string]bool{"frames": false}, &state); err != nil {
				return err
			}
			return printJSON(state.Layers)
		}
		if args[1] != "create" || len(args) < 3 {
			return errors.New("用法：wtable layer create <name> [--dir PATH] [--base REF]")
		}
		fs := flag.NewFlagSet("layer create", flag.ContinueOnError)
		dir := fs.String("dir", cwd, "仓库目录")
		base := fs.String("base", "", "基准引用")
		if err = fs.Parse(args[3:]); err != nil {
			return err
		}
		abs, err := filepath.Abs(*dir)
		if err != nil {
			return err
		}
		var out model.Layer
		if err = call("layer.create", map[string]string{"name": args[2], "cwd": abs, "base": *base}, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "changes", "diff":
		var out string
		method := "git.changes"
		a := map[string]string{"cwd": cwd}
		if args[0] == "diff" {
			method = "git.diff"
			if len(args) > 1 {
				a["path"] = args[1]
			}
		}
		if err = call(method, a, &out); err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	case "shutdown":
		return call("shutdown", nil, nil)
	default:
		return fmt.Errorf("未知命令 %q；运行 wtable help 查看用法", args[0])
	}
}

func ensureDaemon(ctx context.Context, p config.Paths) error {
	probe, err := net.DialTimeout("unix", p.Socket, 200*time.Millisecond)
	if err == nil {
		probe.Close()
		return nil
	}
	if err = p.Prepare(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(p.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(exe, "daemon")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	for i := 0; i < 50; i++ {
		conn, err := net.DialTimeout("unix", p.Socket, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		if err = pause(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
	return fmt.Errorf("daemon 启动失败，请查看 %s", p.Log)
}

func waitTask(ctx context.Context, c *rpc.Client, id string, timeout time.Duration) (model.Task, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		var t model.Task
		if err := c.Call(ctx, "task.get", map[string]string{"id": id}, &t); err != nil {
			return t, err
		}
		if t.State != "queued" && t.State != "running" && t.State != "cancelling" {
			return t, nil
		}
		if err := pause(ctx, 150*time.Millisecond); err != nil {
			return t, fmt.Errorf("等待已结束，任务仍可用 wtable task %s 查询：%w", id, err)
		}
	}
}

func pause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
func short(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

const help = `wtable — macOS 终端 Agent 工作台（MVP）

启动与终端：
  wtable                                  打开 TUI，自动启动本地 daemon
  wtable attach                           重新接入；Ctrl-a d 退出界面
  wtable new [name] [--dir PATH] -- CMD...  新建真实终端
  wtable recruit NAME [--backend pty|acp] [--dir PATH] [--layer NAME] -- CMD...
  wtable list [--json]                     查看 pane
  wtable screen PANE                      读取终端画面
  wtable stop PANE | restart PANE          停止或显式恢复
  wtable input PANE TEXT                   向终端发送原始输入（不自动加换行）

任务与授权：
  wtable connect FROM TO                   授予单向任务投递权限
  wtable disconnect FROM TO                移除权限并取消尚未投递的任务
  wtable peers                            查看当前身份可访问的接收者
  wtable send [--key KEY] PANE PROMPT       投递任务，打印 task ID
  wtable ask [--timeout 10m] PANE PROMPT    投递并等待（ACP 自动执行）
  wtable next [--timeout 30s]               pane 内领取任务，输出 JSON
  wtable result [--failed] TASK TEXT       接收 pane 提交结果；TEXT 为 - 则读 stdin
  wtable wait [--timeout 10m] TASK          等待已有任务
  wtable tasks | task TASK | cancel TASK   列表、详情、取消
  wtable permissions                      查看待审批操作及选项 ID
  wtable approve REQUEST OPTION           明确选择权限选项

Git 与运维：
  wtable layer create NAME [--dir PATH] [--base REF]
  wtable layer list                       列出隔离的 worktree
  wtable changes | diff [PATH]            当前仓库变更与只读 diff
  wtable diag | version | shutdown        诊断、版本、停止 daemon 和所有 pane

选项放在位置参数前（layer create 的选项放在名称后）；CMD 放在 -- 后。
设置 WTABLE_HOME 可使用独立状态目录。默认使用 macOS Application Support/wtable。
PTY 中的 agent 需主动调用 next/result；普通终端输出不会伪装成结构化回答。
Ctrl-a ? 查看 TUI 快捷键。首版不支持远程、历史 reflow、鼠标选区或账号池。
`
