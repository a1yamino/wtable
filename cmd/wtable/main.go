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
		fmt.Println("wtable " + config.Version)
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
	checkVersion := true
	if len(args) > 0 {
		switch args[0] {
		case "diag", "list", "panes", "screen", "stop", "shutdown":
			checkVersion = false
		}
	}
	if checkVersion {
		var info struct {
			Version string `json:"version"`
		}
		if err = c.Call(ctx, "diag.info", nil, &info); err != nil {
			return fmt.Errorf("daemon 未提供兼容的版本确认（CLI %s）；请使用新的 WTABLE_HOME，或先结束旧会话再运行 wtable shutdown：%w", config.Version, err)
		}
		if info.Version != config.Version {
			return fmt.Errorf("CLI 版本 %s 与 daemon 版本 %q 不一致；请使用新的 WTABLE_HOME，或先结束旧会话再运行 wtable shutdown，然后重新启动", config.Version, info.Version)
		}
	}
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
	if args[0] != "new" && args[0] != "recruit" && args[0] != "send" && args[0] != "ask" && args[0] != "next" && args[0] != "result" && args[0] != "wait" && args[0] != "input" && args[0] != "task" && args[0] != "cancel" && args[0] != "pane" {
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
	case "presets":
		if len(args) != 1 {
			return errors.New("用法：wtable presets [--json]")
		}
		var catalog model.Catalog
		if err = call("preset.list", nil, &catalog); err != nil {
			return err
		}
		if jsonMode {
			return printJSON(catalog)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\t名称\t后端\t可用\t版本\t说明")
		for _, p := range catalog.Presets {
			fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%s\t%s\n", p.ID, p.Name, p.Backend, p.Available, p.Version, p.Detail)
		}
		if err = w.Flush(); err != nil {
			return err
		}
		if len(catalog.Accounts) > 0 {
			fmt.Println("账号：" + strings.Join(catalog.Accounts, "、"))
		}
		return nil
	case "role", "roles":
		return roleCommand(args[1:], call)
	case "audit":
		var out []model.DeniedEvent
		if err = call("audit.list", nil, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "pane":
		return paneCommand(args[1:], call)
	case "remove":
		if len(args) != 2 {
			return errors.New("用法：wtable remove PANE")
		}
		var out any
		if err = call("pane.remove", map[string]string{"id": args[1]}, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "brief":
		method, target := "pane.brief", ""
		if len(args) == 2 {
			target = args[1]
		} else if len(args) == 3 && args[1] == "send" {
			method, target = "pane.brief.send", args[2]
		} else {
			return errors.New("用法：wtable brief PANE | wtable brief send PANE")
		}
		var out any
		if err = call(method, map[string]string{"id": target}, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "new", "recruit":
		fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
		fs.BoolVar(&jsonMode, "json", false, "输出 JSON")
		name := fs.String("name", "", "pane 名称")
		dir := fs.String("dir", cwd, "工作目录")
		backend := fs.String("backend", "", "pty 或 acp（默认使用预设后端，否则 pty）")
		preset := fs.String("preset", "", "启动预设 ID")
		role := fs.String("role", "", "角色说明")
		roleTemplate := fs.String("role-template", "", "角色模板名称（创建时快照）")
		account := fs.String("account", "", "已配置的账号名称")
		brief := fs.Bool("brief", false, "通过任务通道注入接入说明")
		var connectTo []string
		fs.Func("connect", "授权新 pane 向目标投递；可重复或用逗号分隔", func(value string) error {
			for _, target := range strings.Split(value, ",") {
				target = strings.TrimSpace(target)
				if target == "" {
					return errors.New("--connect 的目标不能为空")
				}
				connectTo = append(connectTo, target)
			}
			return nil
		})
		layer := fs.String("layer", "", "已有 layer 名称或路径（重名时使用路径）")
		blockedRegex := fs.String("blocked-regex", "", "阻塞界面的正则表达式")
		tailLines := fs.Int("tail-lines", 0, "检测屏幕末尾行数（0 使用默认 12）")
		settle := fs.Duration("settle", 0, "投递前输出稳定时间（0 使用默认 400ms）")
		verify := fs.Duration("verify", 0, "写入后验证时间（0 使用默认 3s）")
		ackTimeout := fs.Duration("ack-timeout", 0, "等待接手证据的时限（0 使用默认 90s）")
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
		if *roleTemplate != "" && *layer == "" {
			dirSet := false
			fs.Visit(func(f *flag.Flag) { dirSet = dirSet || f.Name == "dir" })
			if !dirSet {
				var roles []model.RoleTemplate
				if err = call("role.list", nil, &roles); err != nil {
					return err
				}
				for _, template := range roles {
					if template.Name == *roleTemplate && template.Cwd != "" {
						abs = ""
						break
					}
				}
			}
		}
		if *layer != "" {
			dirSet := false
			fs.Visit(func(f *flag.Flag) { dirSet = dirSet || f.Name == "dir" })
			if !dirSet {
				abs = "" // The server resolves the layer and chooses its directory.
			}
		}
		command := fs.Args()
		if len(command) == 0 && *preset == "" && *roleTemplate == "" && args[0] == "recruit" {
			return errors.New("recruit 需要 --preset ID 或 -- 后的 agent 启动命令")
		}
		var p model.Pane
		spawnCtx, stop := context.WithTimeout(ctx, 45*time.Second)
		defer stop()
		err = c.Call(spawnCtx, "pane.spawn", model.SpawnParams{Name: *name, Backend: *backend, Cwd: abs, Command: command, Cols: 80, Rows: 24,
			Preset: *preset, Role: *role, RoleTemplate: *roleTemplate, Account: *account, Layer: *layer, InjectBrief: *brief, ConnectTo: connectTo, DeliveryConfig: model.DeliveryConfig{
				BlockedRegex: *blockedRegex, TailLines: *tailLines, SettleMS: int(settle.Milliseconds()),
				VerifyMS: int(verify.Milliseconds()), AckTimeoutMS: int(ackTimeout.Milliseconds()),
			}}, &p)
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
		delivery := fs.String("delivery", "", "push、steer、prompt 或 pull（省略时按目标能力选择）")
		queue := fs.Bool("queue", false, "目标忙时显式排队")
		steer := fs.Bool("steer", false, "作为当前 turn 的追加要求插入（需目标声明支持）")
		onComplete := fs.String("on-complete", "", "none、notify 或 handoff")
		reportTo := fs.String("report-to", "", "结果回传目标 pane")
		parentTask := fs.String("parent", "", "关联的父任务 ID")
		timeout := fs.Duration("timeout", 10*time.Minute, "等待时限")
		if err = fs.Parse(args[1:]); err != nil {
			return err
		}
		rest := fs.Args()
		if len(rest) < 2 {
			return fmt.Errorf("用法：wtable %s [--key KEY] <pane> <prompt>", args[0])
		}
		mode := *delivery
		if *steer {
			mode = "steer"
		}
		var t model.Task
		if err = call("task.send", model.SendParams{
			To: rest[0], Prompt: strings.Join(rest[1:], " "), DedupeKey: *key,
			DeliveryMode: mode, Queue: *queue, OnComplete: *onComplete, ReportTo: *reportTo, ParentTask: *parentTask,
		}, &t); err != nil {
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
		timeout := fs.Duration("timeout", 30*time.Second, "阻塞等待新任务的时限；0 表示立即返回")
		if err = fs.Parse(args[1:]); err != nil {
			return err
		}
		var t *model.Task
		if err = call("task.next", map[string]any{"timeout_ms": timeout.Milliseconds()}, &t); err != nil {
			return err
		}
		if t == nil {
			if jsonMode {
				fmt.Println("null")
				return nil
			}
			fmt.Println("null")
			return nil
		}
		if jsonMode {
			return printJSON(t)
		}
		fmt.Println(t.ID)
		fmt.Println(t.Prompt)
		return nil
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
		var t model.Task
		err = call("task.wait", map[string]any{"id": fs.Args()[0], "timeout_ms": timeout.Milliseconds()}, &t)
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
	case "ack":
		if len(args) != 2 {
			return errors.New("用法：wtable ack <task-id>")
		}
		var out any
		if err = call("task.ack", map[string]string{"id": args[1]}, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "task":
		return taskCommand(args[1:], call)
	case "reports":
		if len(args) != 1 {
			return errors.New("用法：wtable reports [--json]")
		}
		var reports []model.Report
		if err = call("report.list", nil, &reports); err != nil {
			return err
		}
		if jsonMode {
			return printJSON(reports)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\t父任务\t来源\t目标\t状态\t模式\t已读\t摘要")
		for _, r := range reports {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%t\t%s\n", r.ID, r.ParentTask, r.From, r.To, r.State, r.Mode, r.Seen, strings.ReplaceAll(short(r.Summary, 70), "\n", " "))
		}
		return w.Flush()
	case "report":
		method, id := "report.get", ""
		if len(args) == 2 {
			id = args[1]
		} else if len(args) == 3 && args[1] == "seen" {
			method, id = "report.seen", args[2]
		} else {
			return errors.New("用法：wtable report <report-id> | report seen <report-id>")
		}
		var out any
		if err = call(method, map[string]string{"id": id}, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "tasks":
		var tasks []model.Task
		if err = call("task.list", nil, &tasks); err != nil {
			return err
		}
		if jsonMode {
			return printJSON(tasks)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\t来源\t目标\t状态\t送达\t送达错误\t内容")
		for _, t := range tasks {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.From, t.To, t.State, t.DeliveryStatus, strings.ReplaceAll(t.DeliveryError, "\n", " "), strings.ReplaceAll(short(t.Prompt, 70), "\n", " "))
		}
		return w.Flush()
	case "cancel":
		fs := flag.NewFlagSet("cancel", flag.ContinueOnError)
		fs.BoolVar(&jsonMode, "json", false, "输出 JSON")
		confirmed := fs.Bool("confirmed", false, "人工确认：已请求中断且 PTY 内工作确已停止")
		if err = fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) != 1 {
			return errors.New("用法：wtable cancel [--confirmed] <task-id>；--confirmed 仅在人工确认 PTY 工作已停止后使用")
		}
		var out any
		if err = call("task.cancel", map[string]any{"id": fs.Args()[0], "confirmed": *confirmed}, &out); err != nil {
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

// paneCommand preserves omitted metadata fields when editing a pane.
func paneCommand(args []string, call func(string, any, any) error) error {
	if len(args) > 0 && args[0] == "caps" {
		return paneCaps(args[1:], call)
	}
	if len(args) == 0 || args[0] != "edit" {
		return errors.New("用法：wtable pane edit [...] 或 wtable pane caps PANE key=value...")
	}
	fs := flag.NewFlagSet("pane edit", flag.ContinueOnError)
	name := fs.String("name", "", "pane 名称")
	role := fs.String("role", "", "角色说明（空字符串清除）")
	roleTemplate := fs.String("role-template", "", "角色模板（空字符串切换为自由角色）")
	account := fs.String("account", "", "已配置的账号名称（空字符串清除）")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 || fs.NFlag() == 0 {
		return errors.New("用法：wtable pane edit [--name NAME] [--role TEXT] [--account NAME] PANE；至少指定一个选项")
	}
	var state model.Snapshot
	if err := call("state.get", map[string]bool{"frames": false}, &state); err != nil {
		return err
	}
	target := fs.Arg(0)
	var current *model.Pane
	for i := range state.Panes {
		if state.Panes[i].ID == target {
			current = &state.Panes[i]
			break
		}
	}
	if current == nil {
		for i := range state.Panes {
			if state.Panes[i].Name == target {
				if current != nil {
					return errors.New("pane 名称不唯一，请使用 pane ID")
				}
				current = &state.Panes[i]
			}
		}
	}
	if current == nil {
		return fmt.Errorf("pane 不存在：%s", target)
	}
	update := model.PaneUpdate{ID: current.ID, Name: current.Name, Role: current.Role, Account: current.Account}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "name":
			update.Name = *name
		case "role":
			update.Role = *role
		case "role-template":
			update.RoleTemplate = roleTemplate
		case "account":
			update.Account = *account
		}
	})
	var out model.Pane
	if err := call("pane.update", update, &out); err != nil {
		return err
	}
	return printJSON(out)
}

// taskCommand parses options only before task IDs, preserving free-form result text.
func taskCommand(args []string, call func(string, any, any) error) error {
	usage := errors.New("用法：wtable task TASK | task done|fail [--json] TASK TEXT|- | task retry [--to PANE] [--queue] TASK | task confirm|seen|result TASK")
	fs := flag.NewFlagSet("task", flag.ContinueOnError)
	jsonMode := fs.Bool("json", false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	args = fs.Args()
	if len(args) == 0 {
		return usage
	}
	subcommand := args[0]
	switch subcommand {
	case "done", "fail":
		fs := flag.NewFlagSet("task "+subcommand, flag.ContinueOnError)
		fs.BoolVar(jsonMode, "json", *jsonMode, "输出 JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		rest := fs.Args()
		if len(rest) < 2 {
			return usage
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
		if err := call("task.done", map[string]any{"id": rest[0], "result": text, "failed": subcommand == "fail"}, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "retry":
		fs := flag.NewFlagSet("task retry", flag.ContinueOnError)
		fs.BoolVar(jsonMode, "json", *jsonMode, "输出 JSON")
		to := fs.String("to", "", "改派给目标 pane")
		queue := fs.Bool("queue", false, "目标忙时显式排队")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) != 1 {
			return usage
		}
		var t model.Task
		if err := call("task.retry", map[string]any{"id": fs.Args()[0], "to": *to, "queue": *queue}, &t); err != nil {
			return err
		}
		if *jsonMode {
			return printJSON(t)
		}
		fmt.Println(t.ID)
		return nil
	}
	// Read-only/detail commands have no free-form text; retain trailing --json support.
	clean := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--json" {
			*jsonMode = true
		} else {
			clean = append(clean, arg)
		}
	}
	args = clean
	method, id, resultOnly := "task.get", "", false
	switch subcommand {
	case "confirm", "seen", "result":
		if len(args) != 2 {
			return usage
		}
		id = args[1]
		if subcommand == "result" {
			resultOnly = true
		} else {
			method = "task." + subcommand
		}
	default:
		if len(args) != 1 {
			return usage
		}
		id = args[0]
	}
	var t model.Task
	if err := call(method, map[string]string{"id": id}, &t); err != nil {
		return err
	}
	if resultOnly && !*jsonMode {
		fmt.Print(t.Result)
		return nil
	}
	return printJSON(t)
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
		if t.State != "queued" && t.State != "running" && t.State != "delivered" && t.State != "accepted" && t.State != "cancelling" {
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

const help = `wtable 0.4.0 — macOS 终端 Agent 工作台

启动与终端：
  wtable                                  打开 TUI，自动启动本地 daemon
  wtable attach                           重新接入；Ctrl-a d 退出界面
  wtable new [name] [--dir PATH] -- CMD...  新建真实终端
  wtable recruit [NAME] [OPTIONS] [-- CMD...]
    --preset ID --backend pty|acp --dir PATH --layer NAME|PATH
    --role TEXT --account NAME --brief --connect TARGET
    --connect 可重复或用逗号分隔；方向为新 pane → TARGET
    layer 重名时使用路径；与 --dir 同时使用时目录必须位于该层内
    recruit 需要预设或启动命令；new 省略两者时打开默认 shell
  wtable presets [--json]                  探测本机预设与账号名称
  wtable pane edit [--name NAME] [--role TEXT] [--account NAME] PANE
  wtable remove PANE                       移除 pane 并取消相关排队任务
  wtable brief PANE                        查看接入说明（JSON）
  wtable brief send PANE                   通过任务通道发送接入说明
  wtable list [--json]                     查看 pane
  wtable screen PANE                      读取终端画面
  wtable stop PANE | restart PANE          停止或显式恢复
  wtable input PANE TEXT                   向终端发送原始输入（不自动加换行）

任务与授权：
  wtable connect FROM TO                   授予单向任务投递权限
  wtable disconnect FROM TO                移除权限并取消尚未投递的任务
  wtable peers                            查看当前身份可访问的接收者
  wtable send [OPTIONS] PANE PROMPT        投递任务，打印 task ID
  wtable ask [OPTIONS] PANE PROMPT         投递并等待（--timeout 默认 10m）
    --key KEY --delivery push|steer|prompt|pull --queue --steer
    --on-complete none|notify|handoff --report-to PANE --parent TASK
  wtable next [--timeout 30s]               pane 内领取任务，输出 JSON
  wtable ack TASK                         接收 pane 确认已接手
  wtable result [--failed] TASK TEXT|-     接收 pane 提交结果；- 读取 stdin
  wtable wait [--timeout 10m] TASK          等待已有任务
  wtable tasks | task TASK                 任务列表（含送达状态）、详情
  wtable task done|fail TASK TEXT|-        人工标记完成或失败
  wtable task retry [--to PANE] [--queue] TASK  新建重试任务，保留原记录
  wtable task confirm TASK | task seen TASK   人工确认送达、标记已读
  wtable task result TASK                  读取完整结果
  wtable cancel [--confirmed] TASK         请求取消；PTY 中断后需人工确认已停止
    --confirmed 仅表示人工已确认 PTY 工作停止，不自动确认
  wtable reports | report ID | report seen ID  报告收件箱、详情、已读
  wtable role list | put [OPTIONS] NAME FILE | delete NAME   角色模板
  wtable audit                            最近越权拒绝记录（最多 256 条）
  wtable permissions                      查看待审批操作及选项 ID
  wtable approve REQUEST OPTION           明确选择权限选项

Git 与运维：
  wtable layer create NAME [--dir PATH] [--base REF]
  wtable layer list                       列出隔离的 worktree
  wtable changes | diff [PATH]            当前仓库变更与只读 diff
  wtable diag | version | shutdown        诊断、版本、停止 daemon 和所有 pane

选项放在位置参数前（layer create 的选项放在名称后）；CMD 放在 -- 后。
设置 WTABLE_HOME 可使用独立状态目录。默认使用 macOS Application Support/wtable。
new/recruit 均接受预设、角色、账号、brief 与连线选项；--brief 默认关闭。
new/recruit 可配置 --blocked-regex REGEX --tail-lines N --settle 400ms --verify 3s --ack-timeout 90s。
省略送达方式时按目标能力选择：ACP 使用 prompt，PTY 使用 push；pull 仅用于已声明支持领取的目标。
PTY push 收到任务后调用 ack/result；pull 使用 next/result；ACP turn 结束自动完成。
notify 只生成报告；handoff 才派新任务。unknown 表示缺少接手证据，不自动重试。
Ctrl-a ? 查看 TUI 快捷键。不支持远程、历史 reflow 或鼠标选区。账号仅使用已有本地配置，不自动安装或登录。
`

// paneCaps sets one or more capability flags, e.g.
// wtable pane caps p-1 inject_steer=true pull=false
func paneCaps(args []string, call func(string, any, any) error) error {
	if len(args) < 2 {
		return errors.New("用法：wtable pane caps PANE receive_tasks=true push=true steer=true prompt=false pull=true")
	}
	var state model.Snapshot
	if err := call("state.get", map[string]bool{"frames": false}, &state); err != nil {
		return err
	}
	var current *model.Pane
	for i := range state.Panes {
		if state.Panes[i].ID == args[0] || state.Panes[i].Name == args[0] {
			current = &state.Panes[i]
			break
		}
	}
	if current == nil {
		return fmt.Errorf("pane 不存在：%s", args[0])
	}
	caps := current.EffectiveCaps()
	aliases := map[string]*bool{
		"receive_tasks":  &caps.ReceiveTasks,
		"return_results": &caps.ReturnResults,
		"push":           &caps.PushIdle,
		"push_idle":      &caps.PushIdle,
		"steer":          &caps.PushSteer,
		"inject_steer":   &caps.PushSteer,
		"prompt":         &caps.Prompt,
		"pull":           &caps.Pull,
	}
	for _, item := range args[1:] {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			return fmt.Errorf("需要 key=value：%s", item)
		}
		target, found := aliases[key]
		if !found {
			return fmt.Errorf("未知的 caps 键：%s", key)
		}
		*target = value == "1" || value == "true" || value == "yes" || value == "on"
	}
	caps.Source = "user"
	var out model.Pane
	if err := call("pane.caps.set", map[string]any{"id": current.ID, "caps": caps}, &out); err != nil {
		return err
	}
	return printJSON(out.Caps)
}
