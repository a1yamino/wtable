# wtable

[English](README.md) | 简体中文

macOS 上的终端多 Agent 工作台。使用 Go、自有 pane 和真实 PTY，不依赖 tmux 或 zellij。

当前为 MVP：可以同时运行终端 CLI，通过 ACP 接入结构化会话，用显式授权边投递任务，并通过 Git worktree 隔离工作目录。

## 启动

需要 Go 1.27 或以上版本。Git 功能需要本机 Git；agent 使用各自已安装的程序和登录配置。

```sh
go build -o bin/wtable ./cmd/wtable
./bin/wtable
```

首次运行会启动本地 daemon 并创建一个 shell pane。按 `Ctrl-a`，再按 `d`，退出界面；pane 继续运行。再次运行 `./bin/wtable` 即可接回。

默认状态目录为 `~/Library/Application Support/wtable`。可以设置 `WTABLE_HOME` 使用独立目录，例如：

```sh
export WTABLE_HOME="$PWD/.dev/local"
./bin/wtable
```

`WTABLE_HOME` 应使用较短的绝对路径，以满足 macOS Unix socket 的长度限制。同一个状态目录只运行一个 daemon、一个可写 TUI。

## 终端与 Agent

在另一个终端中创建 pane，已经打开的 TUI 会自动显示它：

```sh
./bin/wtable new shell --dir "$PWD"
./bin/wtable recruit coder -- codex
./bin/wtable recruit reviewer -- claude
./bin/wtable list
```

`--` 后的命令原样执行，不经过工作台的 shell 字符串解析。普通 CLI 使用 PTY 后端，显示它真实的终端界面。工作台不负责这些程序的安装和登录。

对于已安装的 ACP v1 适配器，使用：

```sh
./bin/wtable recruit assistant --backend acp -- /absolute/path/to/acp-agent
./bin/wtable send assistant '分析当前项目并给出实现建议'
./bin/wtable tasks
```

ACP 任务会自动执行，输出、工具步骤和权限请求显示在 TUI 中。只有明确选择权限选项才会响应；按 Enter 不会自动批准。没有打开 TUI 时，可运行 `permissions` 查看请求，再用 `approve REQUEST OPTION` 选择返回的选项 ID。

PTY 和 ACP 是不同后端。当前不提供同一 provider 会话在两者之间切换，也不从终端静默或输出格式推断任务完成。

## 键盘操作

下列命令先按 `Ctrl-a`，再按对应键：

| 键 | 操作 |
|---|---|
| `?` | 查看帮助 |
| `n` | 新建 shell |
| `c` | 输入 shell 命令并创建 pane |
| `o` | 切换聚焦 pane |
| `s` | 切换网格、左右、上下布局 |
| `z` | 放大当前 pane／恢复布局 |
| `b` | 显示或隐藏任务栏 |
| `[` | 回看模式，使用方向键或 `j/k` 滚动；`y` 复制可见文本，`q` 退出 |
| `t` | 向当前 pane 提交任务；ACP 自动执行，PTY 需要主动领取 |
| `g` / `r` | 新增／删除单向授权边，输入来源与目标 pane |
| `k` | 打开任务列表：↑↓ 选择、Enter 查看详情、`c` 请求取消、Esc 返回 |
| `x` | 停止当前 pane，输入 `y` 确认 |
| `R` | 原地重启已退出、失败或中断的 pane |
| `d` / `q` | detach，保留后台会话 |
| `a` | 将原始 `Ctrl-a` 发送给当前终端 |

鼠标左键点击 pane 的标题或内容即可聚焦；按住分隔线拖动可调整大小，网格布局按整行、整列调整。调整比例在当前 TUI 中保留，切换布局或增减 pane 时恢复均分；放大再恢复不会丢失比例。弹窗打开时不响应鼠标聚焦和拖动。

在 pane 内容区按住左键拖选文字，松开后自动复制到 macOS 剪贴板；支持跨行、反向拖选及中文、emoji。选区固定当前可见画面，按 Esc 或再次点击恢复实时输出。选区限当前 pane 的可见内容，不跨 pane，也不自动滚屏。

滚轮滚动鼠标所在 pane 的历史输出，到底部后回到实时画面。pane 内应用开启鼠标协议时，滚轮会转发给应用；按住 Shift 滚动可强制使用工作台回看。点击和拖选仍由工作台处理，不转发给应用。

输入框支持左右移动、Home/End（或 Ctrl-A/E）定位行首尾、Delete/Backspace 删除、Ctrl-U 删除到行首、Ctrl-W 删除前一个词；粘贴插入光标处。↑↓ 浏览当前输入类型的历史，历史仅在本次 TUI 中保存。任务支持 Alt-Enter 换行。提交失败时保留输入供修正。

`Ctrl-a t` 向当前 pane 发送任务，成功后显示目标名称和排队反馈，并展开任务栏；PTY 任务仍须由 Agent 主动领取。`Ctrl-a k` 打开任务列表，也可点击列表中的任务进入详情。详情展示完整 prompt 和 result，支持方向键、翻页键或滚轮滚动。`c` 取消排队任务或请求取消运行中的 ACP 任务；运行中的 PTY 任务不能可靠取消，会显示原因。

已退出的 pane 显示重启入口：聚焦后按 Enter、点击入口行，或按 `Ctrl-a R` 原地重启。重启保留 pane ID、名称与授权边，并重新执行原启动命令；PTY 的聊天历史恢复取决于原命令是否包含对应 Agent 的 resume 参数。

普通按键直接送到 PTY。多行粘贴在应用没有启用 bracketed paste 时需要确认。复制使用 macOS `pbcopy`。

## 协作任务

人类控制端可以向任意运行中的 pane 发送任务。pane 内的 agent 只能向显式连线的目标发送任务：

```sh
./bin/wtable connect coder reviewer
./bin/wtable send --key review-1 reviewer '检查当前修改'
./bin/wtable tasks
./bin/wtable task TASK_ID
./bin/wtable wait --timeout 5m TASK_ID
```

连线是单向的；双向协作需要分别授权。`disconnect` 会取消这条边上尚未派发的任务，已执行的操作不会撤销。

PTY 中的 agent 需要明确使用工作台命令接收和回报任务。启动时会注入 `WTABLE_CLI`、`WTABLE_PANE_ID`、`WTABLE_PEER_TOKEN` 和 `WTABLE_HOME`。给 CLI agent 的协作指令可以包含：

```sh
# 在工作台创建的 pane 内执行，不要手工复制或打印凭证。
"$WTABLE_CLI" peers
"$WTABLE_CLI" next --timeout 30s
"$WTABLE_CLI" result TASK_ID '已完成检查，结果如下……'
"$WTABLE_CLI" send reviewer '请检查这次修改'
```

`next` 返回任务 JSON；没有任务时返回 `null`。一次只领取一个任务。结果可以通过标准输入提交：`printf '%s' "$result" | "$WTABLE_CLI" result TASK_ID -`。

同一发送者、同一 `--key` 和同一任务内容返回原 task；相同 key 对应不同内容时拒绝。已派发但无法确认结果的任务标为 `unknown`，不会自动重投。`cancelling` 表示已请求取消但仍等待 agent 确认。

连线限制工作台 API，不限制同一系统用户直接访问文件。worktree 用于隔离工作目录，不是操作系统沙箱。

## Git worktree

```sh
./bin/wtable layer create feature-a --dir /path/to/repo
./bin/wtable layer list
./bin/wtable recruit coder-a --layer feature-a -- codex
```

默认基准为存在的 `origin/main`，否则使用 `HEAD`；也可以用 `--base REF` 指定。新分支为 `wtable/layer/NAME`，目录保存在工作台状态目录下。

在目标仓库目录中执行 `wtable changes` 或 `wtable diff [PATH]` 可查看变更。MVP 不自动提交、合并、删除或归档 worktree。

## 恢复与诊断

```sh
./bin/wtable diag
./bin/wtable stop coder
./bin/wtable restart coder
./bin/wtable shutdown
```

- TUI detach 后，daemon、PTY 和 agent 保持运行。
- daemon 重启后恢复 pane 配置、任务、结果和 worktree 记录；不会复活原 PTY 进程，也不会自动重放未知任务。
- `restart` 是显式操作。ACP 有已保存的 session ID 时，仅在适配器支持 `loadSession` 时尝试恢复；不支持时明确报错，可以用新名称创建新会话。
- `shutdown` 停止这个状态目录下的 daemon 和所有 pane。日志位于状态目录的 `daemon.log`。

## 验证

```sh
go test -race ./internal/terminal ./internal/agent
go vet ./...
go build -o bin/wtable ./cmd/wtable
python3 scripts/smoke.py --binary bin/wtable --output .dev/test-results/smoke.json
```

smoke 脚本使用真实 daemon、PTY、SQLite 和临时 Git 仓库，断言授权、任务去重、审批文件效果、worktree 隔离与崩溃恢复。ACP 部分使用本地确定性 fixture，不调用外部模型。测试会清理自建的进程和临时目录，并保留 JSON 验收报告。

外部 provider 的登录、适配器兼容性和恢复能力需要针对实际安装版本验证，不能由 fixture 测试结果推出。

## 当前限制

- 只验证 macOS；不支持远程接入和多 client 旁观。
- 回滚保存最多 10,000 个物理行；改变宽度不会重新排版历史。未完成全面 VT conformance。
- 使用完整 cell 快照轮询，尚未实现 cell 差分与事件重放；原计划中的性能预算尚未验收。
- 支持可见内容的鼠标选区复制、键盘回看和复制可见屏；支持向启用鼠标协议的应用转发滚轮，暂不转发点击和拖动，也不支持 OSC 52 剪贴板协议。
- pane 的排列、大小比例、焦点和 zoom 属于当前 TUI 状态，detach 后会按已有 pane 重新布局。
- 最多保留 32 个 pane、2,000 条任务；结构化聊天视图有大小上限，完整任务结果单独保存。没有内置清理命令。
- notes、角色管理、内置编辑器、账号池、计划任务、图片和浏览器门户属于后续范围。

原始计划与本轮范围说明在 `.dev/plan.md`；`.dev/` 和 `bin/` 均由 Git 忽略。
