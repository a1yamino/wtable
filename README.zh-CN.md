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

在另一个终端中创建 pane。新会话在后台运行，可用 `Ctrl-a f` 选择打开：

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

## 控制面板与 Agent 接入

按 `Ctrl-a a` 打开面板，Tab / Shift-Tab 切换页面，方向键或滚轮选择条目，Esc 逐级返回。Enter 或 Space 打开选中条目的动作列表，用方向键选择、Enter 执行，不必记忆各页字母键。

| 页面 | 操作 |
|---|---|
| Agents | `a` 添加、`e` 编辑名字/角色/账号、`d` 移除、Enter 跳转、`p` 驻留／打开、`v` 查看最近 20 行、`B` 预览说明、`b` 重发说明、`O` 角色库、`l` 诊断、`R` 重启 |
| Edges | `a` 选择来源和多个目标、`x` 删除、`t` 只看当前 pane 相关的边 |
| Tasks | `s` 多目标派活、Enter 动作列表（详情/重发/取消）、`r` 重发、`x` 取消、`b` 报告收件箱 |
| Layers | `n` 创建 worktree、Enter 将选中目录设为新 pane 的默认目录 |

添加 Agent 时可选择预设、PTY/ACP、目录、Layer、角色、账号档案、是否注入说明，以及新 Agent 可以派活给哪些已有 pane。自定义命令支持引号和转义来组合参数，不展开变量或执行 shell 语法。需要 shell 时请显式填写 `/bin/zsh -lc '命令'`。

内置预设包括 Shell、Codex PTY、Codex ACP、Claude PTY 和 Agy PTY。探测只检查本地二进制，并对已知程序运行限时 `--version`；不会修改登录态。未安装 `codex-acp` 时明确显示不可用，不自动下载。找到二进制不等于已验证登录或协议兼容。

```sh
./bin/wtable presets
./bin/wtable recruit coder --preset codex-pty --role '实现与验证' --brief
./bin/wtable recruit reviewer --preset codex-acp --role '只读复核' --brief --connect coder
./bin/wtable brief coder
./bin/wtable brief send coder
./bin/wtable pane edit --role '负责重试逻辑' coder
```

面板选择非 Shell 预设时默认启用身份说明；CLI 的 `--brief` 显式开启。说明不超过 2048 UTF-8 字节，保存在 `<WTABLE_HOME>/briefs/<pane>.md`，权限为 `0600`。启用时通过 `WTABLE_BRIEF` 暴露路径，并经 P0 闸门排队投递。内容包括身份、目录、角色、当前有权联系的 peer 和回报约定，不包含凭据。PTY 说明要求 `ack/result`；ACP 由协议自动回报，不要求额外调用 CLI。重启不会自动重放已有说明，可显式重发。

ACP pane 底部有常驻输入框，直接输入并按 Enter 即通过原任务队列提交，复用当前 session；Alt-Enter 换行，粘贴插入光标处。权限请求始终优先，Enter 不批准。

编辑运行中的 pane 可以修改名字与角色；账号只能在退出、失败或中断后修改。命令、后端和目录变更需要重新创建。移除前须按 `y` 确认：进程停止、相关排队任务取消、授权边移除，任务/报告/brief 历史保留。非零退出显示 `failed`、退出码、脱敏后的尾部摘要和日志路径；正常退出码 0 保留 `exited`。

### 本地预设与账号档案

可在 `<WTABLE_HOME>/presets.json` 定义额外预设，ID 不得与内置预设重复。自定义程序只检查可执行性，不主动运行它探测版本：

```json
[{"id":"custom-agent","name":"Custom agent","backend":"pty","command":["/absolute/path/to/agent","--flag"]}]
```

账号档案从 `<WTABLE_HOME>/accounts.json` 读取，不自动扫描账号或登录。界面、快照和 brief 只暴露档案名称与端点类型，不返回环境变量值；`WTABLE_*` 保留变量不可覆盖：

```json
[{"name":"work","env":{"CODEX_HOME":"/absolute/path/to/codex-profile"}}]
```

配置变更不会修改已运行进程的环境。Layer 的默认选择仅在当前 TUI 会话内保留。

### 角色模板与凭据引用

在 Agents 页按 `O` 打开角色库，可添加、编辑、删除模板。创建 Agent 时选择角色模板，默认预设、后端和目录可继续调整。模板保存在 `<WTABLE_HOME>/roles/<name>.md`；文件包含 `---` 元数据头（`name`、`preset`、`backend`、`cwd`）与 Markdown 指令正文。

```sh
./bin/wtable role put --preset codex-pty reviewer ./review-instructions.md
./bin/wtable role list
./bin/wtable recruit reviewer --role-template reviewer --brief
./bin/wtable pane edit --role-template reviewer existing-pane
./bin/wtable role delete reviewer
```

启动时保存指令正文与 SHA-256 快照。修改或删除模板不会改变已创建实例的指令；重启也保留快照。普通重命名不会刷新快照；显式选择另一个模板才更新，随后用 `b` 或 `brief send` 发送新说明。自由文本 `--role` 仍可使用。完整 brief 仍限 2 KiB。

账号 `env` 支持原有字符串，以及单一 `literal`、`env_ref`、`keychain_ref` 来源。引用在启动时解析，解析结果不写回配置。macOS 钥匙串通过 `/usr/bin/security` 读取已有项目；工作台不创建或修改钥匙串项。

```json
[{
  "name": "gateway",
  "endpoint": "compatible",
  "env": {
    "ANTHROPIC_BASE_URL": {"literal": "https://gateway.example"},
    "ANTHROPIC_AUTH_TOKEN": {"env_ref": "WORK_GATEWAY_TOKEN"},
    "OTHER_API_KEY": {"keychain_ref": {"service": "work-agent", "account": "me"}}
  }
}]
```

`endpoint` 可选 `official`、`compatible`、`custom`，省略时为 `official`。非官方端点在面板显示兼容性提示，这个标签不代表已验证接口、模型或认证方式。缺失环境变量或钥匙串项会阻止启动并返回不含凭据值的错误。

### 单看、分屏与后台会话

初次接入时单看第一个 pane。`Ctrl-a f` 打开切换器；`Ctrl-a |` 或 `Ctrl-a -` 选择另一个 pane，分别左右或上下分屏。拖动分隔线调整比例，窗口 resize 保留布局树的比例。`Ctrl-a z` 在单看与当前分屏之间切换。

`Ctrl-a T` 新建视图 tab，`Ctrl-a {`／`Ctrl-a }` 切换，`Ctrl-a ,` 重命名，`Ctrl-a W` 关闭当前 tab。关闭 tab 不停止其中的进程。布局、tab 名称与驻留状态仅保留在当前 TUI 会话，detach 后重新接入从单看开始。

Agents 页 `p` 驻留或打开 pane，`v` 查看最近 20 行；Enter 或 Graph 跳转会自动打开目标。后台进程持续运行，PTY 保持最后尺寸；只有当前可见 pane 会取整屏画面并 resize。状态栏显示 tab、焦点位置和后台／待处理数量；`Ctrl-a !` 跳到需要处理的 pane。进入可见视图时清除相关任务和报告的未读标记。

Agents 列表显示输出摘要、最近活动、未读、就绪状态、端点类型及越权次数。创建表单提示同目录／Layer 和同账号运行实例；在选项字段按 `L` 先创建隔离 Layer，再返回表单。`ready` 表示当前投递闸门通过，不代表 agent 已收到或接手任务。

`state.get` 的 `ids` 现在区分三种情况：省略或 `null` 获取全部画面，`[]` 仅取元数据，非空数组只取指定 pane；`frames: false` 始终禁止画面。`pane.peek` 返回文本尾部，不分配整屏 Frame。

## 协作 Graph

按 `Ctrl-a G` 打开实时协作图。箭头由发送方指向接收方；环内节点同层，层内按名称排序。虚线 `you → pane` 表示人类入口，不可选中或删除。

`j/k` 选择节点，Enter 打开所选节点的动作列表，第一项「打开」进入单看；`e` 切换节点与真实授权边模式，`x` 请求删除选中边。只有 `y` 确认删除，并取消该方向排队任务；`n` 或 Esc 取消，Enter 不确认。方向键移动图视口。底部显示所选节点的关联任务和报告 ID、方向、状态和未读标记，报告带 `report` 标记，PgUp/PgDn 滚动列表。未读与对应的 `bell` 只归接收方；节点框显示名字，选中边详情显示 `active` 数量。Esc 返回终端视图。

控制端调用 `state.get` 时可传入 `graph: true`，获取 `graph.nodes` 和 `graph.edges`；同时传入 `frames: false` 可跳过终端画面。该投影不改变授权规则。

## 键盘操作

下列命令先按 `Ctrl-a`，再按对应键：

| 键 | 操作 |
|---|---|
| `?` | 查看帮助 |
| `n` | 新建 shell |
| `a` | 打开 Agents / Edges / Tasks / Layers 控制面板 |
| `c` | 打开自定义 Agent 表单；命令按 argv 执行，不经过 shell |
| `o` / `f` | 循环切换 pane／打开切换器 |
| `\|` / `-` | 选择另一个 pane，左右／上下分屏 |
| `T` / `{` / `}` | 新建／前一个／后一个视图 tab |
| `,` / `W` | 重命名／关闭当前 tab，不停止进程 |
| `!` | 打开下一个需要处理的 pane |
| `s` | 切换网格、左右、上下布局 |
| `z` | 放大当前 pane／恢复布局 |
| `b` | 显示或隐藏任务栏 |
| `[` | 回看模式，使用方向键或 `j/k` 滚动；`y` 复制可见文本，`q` 退出 |
| `t` | 打开多目标派活表单，选择投递、排队和汇报方式 |
| `G` | 打开协作 Graph；`j/k` 选节点，Enter 动作列表 |
| `g` / `r` | 打开加边选择器／授权边列表，无需输入 ID |
| `k` | 打开 Tasks 页：Enter 动作列表（详情/重发/取消）、`r` 重发、`x` 取消、`b` 报告 |
| `x` | 停止当前 pane，输入 `y` 确认 |
| `R` | 原地重启已退出、失败或中断的 pane |
| `d` / `q` | detach，保留后台会话 |
| `Ctrl-a` | 将原始 `Ctrl-a` 发送给当前终端，即连续按两次 |

鼠标左键点击 pane 的标题或内容即可聚焦；按住分隔线拖动可调整对应布局树分支的比例。比例在当前 TUI 中保留，窗口 resize、放大再恢复均保留比例；切换排列方式会重新分配当前布局中的 pane。弹窗打开时不响应鼠标聚焦和拖动。

在 pane 内容区按住左键拖选文字，松开后自动复制到 macOS 剪贴板；支持跨行、反向拖选及中文、emoji。选区固定当前可见画面，按 Esc 或再次点击恢复实时输出。选区限当前 pane 的可见内容，不跨 pane，也不自动滚屏。

滚轮滚动鼠标所在 pane 的历史输出，到底部后回到实时画面。pane 内应用开启鼠标协议时，滚轮会转发给应用；按住 Shift 滚动可强制使用工作台回看。点击和拖选仍由工作台处理，不转发给应用。

输入框支持左右移动、Home/End（或 Ctrl-A/E）定位行首尾、Delete/Backspace 删除、Ctrl-U 删除到行首、Ctrl-W 删除前一个词；粘贴插入光标处。↑↓ 浏览当前输入类型的历史，历史仅在本次 TUI 中保存。任务支持 Alt-Enter 换行。提交失败时保留输入供修正。

`Ctrl-a t` 打开派活表单，默认勾选当前 pane。Tab 切换字段，左右键选择选项，Space 多选目标，Enter 提交。多目标发送若部分失败，只保留未发送成功的目标，继续提交不会重复成功项。`Ctrl-a k` 打开 Tasks 页，Enter 打开动作列表，第一项查看完整任务与结果；详情支持滚轮和翻页，并提供以下操作：

| 详情按键 | 操作 |
|---|---|
| `r` / `a` | 重发 / 改派，创建带 `RetryOf` 的新任务 |
| `v` / `i` | 查看一次性屏幕快照 / 发送原始输入 |
| `m` | 人工确认已送达；仍等待 `ack` 或 `result` |
| `d` / `f` | 输入结果，人工标记完成 / 失败 |
| `c` / `x` | 请求取消 |
| `C` | 确认注入任务已经停止，输入 `y` 才生效 |

Tasks 页按 `b` 打开报告收件箱。报告显示未读和 `undeliverable` 状态；Enter 查看摘要，再按 Enter 查看原任务（仍需原任务读取权限）。原始输入支持 `<Enter>`、`<Esc>`、`<Ctrl-C>` 和方向键标记，不自动追加回车。

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

### 投递与确认

送达方式按目标能力选择：ACP 目标使用 `prompt`，PTY 目标使用 `push`，不再由发送方身份决定。`--delivery pull`（兼容旧名 `claim`）只对声明支持领取的 pane 可用；`inject` 作为 `push` 的兼容别名。无投递字段的排队任务按 `push` 处理。

```sh
./bin/wtable send --delivery push coder '检查当前修改'
./bin/wtable send --delivery pull coder '等待主动领取的任务'
./bin/wtable send --steer coder '再补跑一遍测试'
./bin/wtable send --queue coder '闸门允许后再发送'
```

注入前会检查进程、阻塞文案、输出稳定时间和未结束的前一条注入任务。默认稳定窗口为 400ms，写后验证为 3s，ACK 超时为 90s。直接启动 `codex`、`claude`、`agy` 时使用内置阻塞文案；`gemini`、`opencode`、`grok` 和其他程序或包装脚本需要自行配置，暂不属于已验证的内置适配：

```sh
./bin/wtable recruit coder --blocked-regex '(?i)(Log in|permission required)' \
  --tail-lines 12 --settle 400ms --verify 3s --ack-timeout 90s -- codex
```

闸门拒绝时返回 `blocked:<line>`、`busy:<task>` 或 `not-ready`，不写入；`--queue` 才会保留任务并等待闸门。前一条注入任务处于 `delivered`、`accepted` 或 `cancelling` 时都算 busy；ACK 不解除 busy，ACK 超时也不会结束已接手的任务。支持 bracketed paste 时一次提交完整信封；否则发送包含任务 ID、prompt 和回报约定的单行 JSON。任务文本中的终端控制字符（含 ESC 与 CR）会显示为 Unicode 控制图片，prompt 无法提前结束 bracketed paste 或提交额外行。单行可能受目标终端输入长度限制，完整原文始终可通过 `task TASK_ID` 读取。

`delivered` 只证明写入或协议投递。PTY agent 调用 `ack` 才进入 `accepted`；直接提交结果也可证明已接手。写后只看到回显时记录 `write-unconfirmed`，这不是失败，也不是完成。ACK 超时变为 `unknown`，不自动重投。ACP 首个当前 turn 的更新进入 `accepted`，turn 结束自动完成，不需要 CLI `result`。

启动时注入 `WTABLE_CLI`、`WTABLE_PANE_ID`、`WTABLE_PEER_TOKEN` 和 `WTABLE_HOME`。在 agent pane 内执行：

```sh
# 不要复制或打印凭据。
"$WTABLE_CLI" ack TASK_ID
"$WTABLE_CLI" result TASK_ID '实际结果'
# claim 模式使用 next；一次只能领取一条，保留 running 状态值。
"$WTABLE_CLI" next --timeout 30s
"$WTABLE_CLI" send --delivery inject reviewer '请检查这次修改'
```

结果也可从标准输入提交。`delivered`、`accepted`、`unknown`、`cancelled`、`failed` 和旧 `running` 状态接受迟到结果；`completed` 后拒绝覆盖。相同发送者、去重键及内容返回原 task；同一键对应不同内容时拒绝。

### 报告与后续任务

```sh
./bin/wtable send --on-complete notify --report-to reviewer coder '分析并通知 reviewer'
./bin/wtable send --on-complete handoff --report-to reviewer coder '完成后交给 reviewer 继续'
./bin/wtable reports
./bin/wtable report REPORT_ID
./bin/wtable report seen REPORT_ID
```

`notify` 只写报告收件箱，绝不写入目标终端，也不创建新任务。`handoff` 创建带 `ParentTask` 的新任务，继承父任务的 `From` 和当前 `Origin`，并经过正常投递。授权始终按原 `From` 检查；父任务 `To` 与报告 `From` 记录执行者。没有汇报目标时默认 `none`；指定 `ReportTo` 而未指定模式时默认 `notify`。

汇报目标需要原发送方到目标的授权边，创建和完成时都会检查。撤销授权后报告为 `undeliverable`，摘要仍保存供处理。报告和完成状态一起持久化，同一原任务只生成一份报告。`unknown` 或 `cancelled` 会写入状态通知，绝不触发 handoff；迟到的真实结果更新同一报告并恢复未读，首次最终结果最多触发一次 handoff。报告的 `task_state` 区分未知、取消与最终结果。摘要最多 4KiB，不提供 `ReportMode: full`；完整结果仍只允许原任务收发双方及 controller 读取。

### 人工处理

```sh
./bin/wtable task confirm TASK_ID
./bin/wtable task done TASK_ID '人工确认的实际结果'
./bin/wtable task fail TASK_ID '失败原因'
./bin/wtable task retry --to reviewer TASK_ID
./bin/wtable cancel TASK_ID
# 确认 PTY 工作已停止后再执行，不把 Ctrl-C 当成停止证明。
./bin/wtable cancel --confirmed TASK_ID
```

重发只接受失败、未知、取消或写入未确认的任务，保留原记录，并用 `RetryOf` 关联新任务。已在执行或排队的任务不能重发。重发写入未确认的任务时，原记录转为 `unknown` 并继续接受迟到结果；显式重发仍可能造成重复工作。

排队任务直接取消；ACP 等待协议确认。注入任务先发送中断，进入 `cancelling`，再由人确认 `cancelled`。终端历史和已发生的文件操作不会被撤销。旧 claim 模式的执行中 PTY 任务仍不能可靠取消，需要检查或停止 pane。

`wtable audit` 返回最近 256 条越权拒绝记录，只包含调用者、目标、方法与时间；每个 pane 的累计次数保存在元数据中。记录不包含请求正文或凭据。直接边检查已阻止 agent 向自身派活；目前没有内容来源追踪或 `--no-relay` 模式。

连线限制工作台 API，不限制同一系统用户直接访问文件。worktree 用于隔离工作目录，不是操作系统沙箱。

## Git worktree

```sh
./bin/wtable layer create feature-a --dir /path/to/repo
./bin/wtable layer list
./bin/wtable recruit coder-a --layer feature-a -- codex
```

默认基准为存在的 `origin/main`，否则使用 `HEAD`；也可以用 `--base REF` 指定。新分支为 `wtable/layer/NAME`，目录保存在工作台状态目录下。

在目标仓库目录中执行 `wtable changes` 或 `wtable diff [PATH]` 可查看变更。MVP 不自动提交、合并、删除或归档 worktree。

## 升级、恢复与诊断

当前版本为 `0.4.0`。CLI 会检查 daemon 版本，不会把新投递参数交给旧 daemon 静默忽略。保留旧会话时，请为新版使用独立的 `WTABLE_HOME`；需要沿用原目录时，先结束旧会话并执行 `shutdown`，再启动新版。仅重新 attach 不会升级后台 daemon，`shutdown` 会停止该目录下的全部 pane。

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
go test -race ./internal/terminal ./internal/agent ./internal/server
go vet ./...
go build -o bin/wtable ./cmd/wtable
python3 scripts/smoke.py --binary bin/wtable --output .dev/test-results/smoke.json
python3 scripts/p0_smoke.py --binary bin/wtable --output .dev/test-results/p0.json
python3 scripts/p1_smoke.py --binary bin/wtable --output .dev/test-results/p1.json
python3 scripts/p2_smoke.py --binary bin/wtable --output .dev/test-results/p2.json
python3 scripts/scheduler_smoke.py --binary bin/wtable --output .dev/test-results/scheduler.json
python3 scripts/view_smoke.py --binary bin/wtable --output .dev/test-results/view.json
```

smoke 脚本使用真实 daemon、PTY、SQLite 和临时 Git 仓库，断言授权、任务去重、审批文件效果、worktree 隔离与崩溃恢复。ACP 部分使用本地确定性 fixture，不调用外部模型。测试会清理自建的进程和临时目录，并保留 JSON 验收报告。

外部 provider 的登录、适配器兼容性和恢复能力需要针对实际安装版本验证，不能由 fixture 测试结果推出。

## 当前限制

- 只验证 macOS；不支持远程接入和多 client 旁观。
- 回滚保存最多 10,000 个物理行；改变宽度不会重新排版历史。未完成全面 VT conformance。
- 使用完整 cell 快照轮询，尚未实现 cell 差分与事件重放；原计划中的性能预算尚未验收。
- 支持可见内容的鼠标选区复制、键盘回看和复制可见屏；支持向启用鼠标协议的应用转发滚轮，暂不转发点击和拖动，也不支持 OSC 52 剪贴板协议。
- pane 的布局树、tabs、大小比例、焦点、驻留和 zoom 属于当前 TUI 状态，detach 后从单看开始。
- 最多保留 32 个 pane、2,000 条任务；pane 可从 Agents 页移除，任务历史尚无清理命令。结构化聊天视图有大小上限，完整任务结果单独保存。
- notes、内置编辑器、账号池、计划任务、图片和浏览器门户属于后续范围。

当前实现完成 P0 送达与回传、P1 控制面板和身份说明接入，以及 P2 Graph v1。`docs/`、`.dev/` 和 `bin/` 均由 Git 白名单规则忽略。
