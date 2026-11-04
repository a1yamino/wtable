# wtable

[English](README.md) | 简体中文

macOS 上的终端多 Agent 工作台。使用 Go、自有 pane 和真实 PTY，不依赖 tmux 或 zellij。

当前为 MVP：可以通过 PTY 同时运行终端 CLI，用显式授权边投递任务，并通过 Git worktree 隔离工作目录。

> **当前状态：仅支持 PTY。** ACP 适配代码仍保留在仓库中，供后续恢复使用，但 ACP pane 的创建和重启入口目前停用。已有 ACP pane 及其历史记录仍可查看，旧 pane 也可移除；所有新工作均使用 PTY。

## 启动

需要 Go 1.27.3 或以上版本。Git 功能需要本机 Git；agent 使用各自已安装的程序和登录配置。

安装一次后，可以从任意目录运行：

```sh
make install                 # 构建并安装到 ~/.local/bin/wtable
# make install PREFIX=/usr/local
# 或：go install ./cmd/wtable
wtable
```

`make install` 默认安装到 `~/.local/bin`，请确认该目录在 `PATH` 中。在源码目录里也可以直接运行 `./bin/wtable`，无需安装。

首次运行会启动本地 daemon，并先询问打开哪个工作区（目录）。当前目录排在第一行，最近打开的工作区随后，光标默认落在上次选择；按 `Enter` 进入后，如果该工作区还没有 agent，再打开 PTY 前台选择器。按 `Ctrl-a`，再按 `d`，退出界面；pane 继续运行。再次运行 `wtable` 即可接回。

默认状态目录为 `~/Library/Application Support/wtable`。可以设置 `WTABLE_HOME` 使用独立目录，例如：

```sh
export WTABLE_HOME="$PWD/.dev/local"
wtable
```

`WTABLE_HOME` 应使用较短的绝对路径，以满足 macOS Unix socket 的长度限制。同一个状态目录只运行一个 daemon，以及一个可写界面（TUI 或桌面应用）。

## 原生桌面

桌面应用使用 MyGo 的 Go 原生控件，以及连接同一 daemon 的 Ghostty 终端。界面支持工作区和 Agent 管理、带辅助终端的 Focus 视图，以及包含定向授权和拓扑模板的空间 Board。

```sh
make desktop-dev            # 启动原生应用的开发模式
make desktop                # 构建并临时签名 macOS 应用，不生成 DMG
open desktop-mygo/build/darwin-arm64/wtable.app
```

也可以使用明确的原生命令 `make desktop-mygo` 和 `make desktop-mygo-dev`。产物目录取决于构建机器的架构，Intel Mac 使用 `darwin-amd64`。应用以 `CGO_ENABLED=0` 构建，无需 Node.js 或前端构建。关闭窗口只会断开界面，daemon 中的 Agent 继续运行。同一 `WTABLE_HOME` 下，打开桌面应用前需要先退出已连接的 TUI。详细说明见[桌面构建与验证](desktop-mygo/README.md)。

Wails v3 客户端单独保留在 `pluto/feat/wails3` 分支。需要对比时使用该分支的工作区；同时打开两个客户端时，分别设置独立的 `WTABLE_HOME`。两版的布局和模板各自保存。

## 工作区

工作区就是你选择的绝对目录。每个工作区拥有自己的一套团队：前台 agent、worker、授权边、layer、任务和收件箱。切换工作区时，后台团队继续运行。

- TUI 内按 `Ctrl-a w` 切换工作区。
- `wtable --workspace PATH` 或 `WTABLE_WORKSPACE=PATH` 跳过启动选择器。
- `wtable new` / `wtable recruit` 默认加入当前目录的工作区；`--workspace PATH` 可覆盖。
- pane 名称只在工作区内唯一，不再全局唯一。跨工作区同名时请使用 pane ID 或指定 `--workspace`。
- 工作区不会自动提升到 Git 仓库根：你打开哪个目录，就是哪个目录。

## 终端与 Agent

在另一个终端中创建 pane。新会话在后台运行并出现在左栏；按 `Ctrl-a a` 选择打开：

```sh
wtable new shell --dir "$PWD"
wtable recruit coder -- codex
wtable recruit reviewer -- claude
wtable list
```

`--` 后的命令原样执行，不经过工作台的 shell 字符串解析。普通 CLI 使用 PTY 后端，显示它真实的终端界面。工作台不负责这些程序的安装和登录。

所有新 pane 均使用 PTY 后端。任务通过终端投递，并遵循下文说明的 `ack`/`result` 契约。工作台不会根据终端静默、输出格式或 provider 回合结束推断任务完成。

## 控制面板与 Agent 接入

左栏常驻。`Ctrl-a a` 聚焦左栏；`Enter` 打开选中的 agent 或收件箱项，`Space` 打开动作列表，`n` 新建，`p` 放入/移出工具槽，`x` 移除，`/` 过滤。完整的管理面板（Agents / Edges / Tasks / Layers）改为 `Ctrl-a Space` →「管理面板」。Tab / Shift-Tab 切换页面，方向键或滚轮选择条目，Esc 逐级返回；Enter 或 Space 打开动作列表，不必记忆各页字母键。

| 页面 | 操作 |
|---|---|
| Agents | `a` 添加、`e` 编辑名字/角色/账号、`d` 移除、Enter 操作列表（首项为打开）、`p` 放入/移出工具槽、`v` 查看最近 20 行、`B` 预览说明、`b` 重发说明、`O` 角色库、`l` 诊断、`R` 重启 |
| Edges | `a` 选择来源和多个目标、`x` 删除、`t` 只看当前 pane 相关的边 |
| Tasks | `s` 多目标派活、Enter 动作列表（详情/重发/取消）、`r` 重发、`x` 取消、`b` 报告收件箱 |
| Layers | `n` 创建 worktree、Enter 将选中目录设为新 pane 的默认目录 |

添加 Agent 时可选择预设、目录、Layer、角色、账号档案、是否注入说明，以及新 Agent 可以派活给哪些已有 pane。自定义命令支持引号和转义来组合参数，不展开变量或执行 shell 语法。需要 shell 时请显式填写 `/bin/zsh -lc '命令'`。

内置预设包括 Shell、Codex PTY、Claude PTY 和 Agy PTY；`wtable presets` 与创建面板只列出 PTY 预设。探测只检查本地二进制，并对已知程序运行限时 `--version`；不会修改登录态。找到二进制不等于已验证登录或与当前 CLI 版本兼容。

```sh
wtable presets
wtable recruit coder --preset codex-pty --role '实现与验证' --brief
wtable recruit reviewer --preset claude-pty --role '只读复核' --brief --connect coder
wtable brief coder
wtable brief send coder
wtable pane edit --role '负责重试逻辑' coder
```

面板选择非 Shell 预设时默认启用身份说明；CLI 的 `--brief` 显式开启。说明不超过 2048 UTF-8 字节，保存在 `<WTABLE_HOME>/briefs/<pane>.md`，权限为 `0600`。启用时通过 `WTABLE_BRIEF` 暴露路径，并经 P0 闸门排队投递。内容包括身份、目录、角色、当前有权联系的 peer 和回报约定，不包含凭据。brief 是常驻说明，不是委派任务：不占用忙碌闸门，不出现在任务列表或收件箱，也不需要 `ack/result`。之后收到的正式任务仍按 PTY 的 `ack/result` 契约回报。重启不会自动重放已有说明，可显式重发。

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
wtable role put --preset codex-pty reviewer ./review-instructions.md
wtable role list
wtable recruit reviewer --role-template reviewer --brief
wtable pane edit --role-template reviewer existing-pane
wtable role delete reviewer
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

### 左栏、主画布、工具槽与收件箱

工作台只有一个主画布、一个可选工具槽和常驻左栏。左栏按固定顺序分三组：「需要你」（退出、投递受阻、未读回复、人类任务结果）、「前台」（最多一个 PTY 前台 agent）、「Workers」。从左栏选中任意 agent 只会替换主画布，不再分屏。`Ctrl-a s` 打开右侧工具槽（默认 shell，cwd 跟随主画布），再按一次关闭；关闭只把它移出画布，进程仍在左栏运行，下次 `Ctrl-a s` 复用同一个 pane。没有 tab、驻留、zoom、布局树或分屏。

前台 agent 是人主要对话的那一个，只能是 PTY。API 会拒绝第二个前台。首次启动打开一次 Enter 即可完成的 PTY 预设选择器；没有可用 PTY 时提供「重新检测」和「创建临时 shell」两个出路。重新接入时服务端恢复上次聚焦的 pane，若已退出则选下一个运行中的 pane；工具槽不自动恢复。

左栏每行只显示一个主信号：退出 `✕` → 未读 `◉` → 排队 `⏸` → 运行/空闲。主画布标题使用固定槽位，只显示非零项（`▌ coder · ◐ running · 12s · ⏸2 ◉3`）。只有真正打开某个 pane 才清除它的未读。后台 PTY 保持最后尺寸，只有主画布和工具槽请求画面与 resize。列表显示输出摘要、最近活动、未读、就绪状态、端点类型及越权次数。创建表单提示同目录／Layer 和同账号运行实例；在选项字段按 `L` 先创建隔离 Layer，再返回表单。`ready` 表示当前投递闸门通过，不代表 agent 已收到或接手任务。

`wtable inbox` 输出同样的收件箱。

`state.get` 的 `ids` 现在区分三种情况：省略或 `null` 获取全部画面，`[]` 仅取元数据，非空数组只取指定 pane；`frames: false` 始终禁止画面。`pane.peek` 返回文本尾部，不分配整屏 Frame。

## 协作 Graph

按 `Ctrl-a G` 打开实时协作图。箭头由发送方指向接收方；环内节点同层，层内按名称排序。虚线 `you → pane` 表示人类入口，不可选中或删除。

`j/k` 选择节点，Enter 打开所选节点的动作列表，第一项「打开」进入单看；`e` 切换节点与真实授权边模式，`x` 请求删除选中边。只有 `y` 确认删除，并取消该方向排队任务；`n` 或 Esc 取消，Enter 不确认。方向键移动图视口。底部显示所选节点的关联任务和报告 ID、方向、状态和未读标记，报告带 `report` 标记，PgUp/PgDn 滚动列表。未读与对应的 `bell` 只归接收方；节点框显示名字，选中边详情显示 `active` 数量。Esc 返回终端视图。

控制端调用 `state.get` 时可传入 `graph: true`，获取 `graph.nodes` 和 `graph.edges`；同时传入 `frames: false` 可跳过终端画面。该投影不改变授权规则。

## 键盘操作

工作台只使用 `Ctrl-a` 一个领导键，不使用任何 Alt 组合。连按两次 `Ctrl-a` 会把字面 `Ctrl-a` 发给终端。

| 键 | 操作 |
|---|---|
| `Ctrl-a j` / `Ctrl-a k`，或 `Ctrl-a ↓/↑` | 下一个／上一个 agent（左栏顺序） |
| `Ctrl-a 1..9` | 跳到左栏第 N 个 agent |
| `Ctrl-a h` / `Ctrl-a l`，或 `Ctrl-a ←/→` | 主画布／工具槽切换焦点 |
| `Ctrl-a a` | 聚焦左栏（`Esc` 返回；窄终端展开抽屉） |
| `Ctrl-a s` | 打开／关闭工具槽 |
| `Ctrl-a t` | 任务输入条：排队、插话（目标声明且忙时才可用）、中断后发送 |
| `Ctrl-a Space` | 命令面板 |
| `Ctrl-a ?` | 当前上下文帮助（3 行，含状态图例） |
| `Ctrl-a q`（或 `d`） | detach，后台继续运行 |
| `Ctrl-a Ctrl-a` | 发送字面 `Ctrl-a` |

左栏内：`↑↓` 选择、`Enter` 打开、`Space` 动作、`n` 新建、`e` 编辑、`p` 工具槽、`x` 移除（输入 `y` 确认）、`/` 过滤、`Esc` 返回。

输入条：`Enter` 发送/排队、`Ctrl-J` 换行、`Esc` 清空、`Ctrl-a Enter` 首次只提示、再次确认才中断并发送。低频动作（协作图、任务列表、队列、管理面板、工具槽方向、重启/停止、离开）都在 `Ctrl-a Space` 命令面板；`Ctrl-a G`（协作图）与 `Ctrl-a b`（任务）保留为兼容别名。

点击左栏行打开该 agent；点击 pane 或任务输入条即可聚焦。拖动主画布与工具槽之间的唯一分隔线调整比例。弹窗打开时不响应鼠标聚焦和拖动。

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

已退出的 PTY pane 显示重启入口：聚焦后按 Enter、点击入口行，或按 `Ctrl-a R` 原地重启。重启保留 pane ID、名称与授权边，并重新执行原启动命令；PTY 的聊天历史恢复取决于原命令是否包含对应 Agent 的 resume 参数。旧 ACP 记录不能重启。

普通按键直接送到 PTY。多行粘贴在应用没有启用 bracketed paste 时需要确认。复制使用 macOS `pbcopy`。

## 协作任务

人类控制端可以向任意运行中的 pane 发送任务。pane 内的 agent 只能向显式连线的目标发送任务：

```sh
wtable connect coder reviewer
wtable send --key review-1 reviewer '检查当前修改'
wtable tasks
wtable task TASK_ID
wtable wait --timeout 5m TASK_ID
```

连线是单向的；双向协作需要分别授权。`disconnect` 会取消这条边上尚未派发的任务，已执行的操作不会撤销。

### 投递与确认

PTY 目标默认使用 `push`。`--delivery pull`（兼容旧名 `claim`）只对声明支持领取的 pane 可用；`inject` 作为 `push` 的兼容别名。无投递字段的排队任务按 `push` 处理。

```sh
wtable send --delivery push coder '检查当前修改'
wtable send --delivery pull coder '等待主动领取的任务'
wtable send --steer coder '再补跑一遍测试'
wtable send --queue coder '闸门允许后再发送'
```

Steer 只在该目标正在执行任务且声明了 `push_steer` 时接受。发给空闲目标会直接报错而不是排队。若当前工作在投递前结束，已排队的 steer 会降级为普通 `push`，避免被插进之后一个无关任务。

注入前会检查进程、阻塞文案、输出稳定时间和未结束的前一条注入任务。默认稳定窗口为 400ms，写后验证为 3s，ACK 超时为 90s。直接启动 `codex`、`claude`、`agy` 时使用内置阻塞文案；`gemini`、`opencode`、`grok` 和其他程序或包装脚本需要自行配置，暂不属于已验证的内置适配：

```sh
wtable recruit coder --blocked-regex '(?i)(Log in|permission required)' \
  --tail-lines 12 --settle 400ms --verify 3s --ack-timeout 90s -- codex
```

闸门拒绝时返回 `blocked:<line>`、`busy:<task>` 或 `not-ready`，不写入；有授权连线的 agent 发送方只会看到 `blocked` 类别，命中的终端行（已脱敏并截断）仅对控制端可见。`--queue` 才会保留任务并等待闸门。前一条注入任务处于 `delivered`、`accepted` 或 `cancelling` 时都算 busy；ACK 不解除 busy，ACK 超时也不会结束已接手的任务。支持 bracketed paste 时一次提交完整信封；否则发送包含任务 ID、prompt 和回报约定的单行 JSON。任务文本中的终端控制字符（含 ESC 与 CR）会显示为 Unicode 控制图片，prompt 无法提前结束 bracketed paste 或提交额外行。单行可能受目标终端输入长度限制，完整原文始终可通过 `task TASK_ID` 读取。

`delivered` 只证明已写入终端。PTY agent 调用 `ack` 才进入 `accepted`；直接提交结果也可证明已接手。写后只看到回显时记录 `write-unconfirmed`，这不是失败，也不是完成。ACK 超时变为 `unknown`，不自动重投。PTY 工作不会在回合结束时自动完成：接收方必须提交 `result`，或由人工记录结果。

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
wtable send --on-complete notify --report-to reviewer coder '分析并通知 reviewer'
wtable send --on-complete handoff --report-to reviewer coder '完成后交给 reviewer 继续'
wtable reports
wtable report REPORT_ID
wtable report seen REPORT_ID
```

`notify` 只写报告收件箱，绝不写入目标终端，也不创建新任务。`handoff` 创建带 `ParentTask` 的新任务，继承父任务的 `From` 和当前 `Origin`，并经过正常投递。授权始终按原 `From` 检查；父任务 `To` 与报告 `From` 记录执行者。没有汇报目标时默认 `none`；指定 `ReportTo` 而未指定模式时默认 `notify`。

汇报目标需要原发送方到目标的授权边，创建和完成时都会检查。撤销授权后报告为 `undeliverable`，摘要仍保存供处理。报告和完成状态一起持久化，同一原任务只生成一份报告。`unknown` 或 `cancelled` 会写入状态通知，绝不触发 handoff；迟到的真实结果更新同一报告并恢复未读，首次最终结果最多触发一次 handoff。报告的 `task_state` 区分未知、取消与最终结果。摘要最多 4KiB，不提供 `ReportMode: full`；完整结果仍只允许原任务收发双方及 controller 读取。

### 人工处理

```sh
wtable task confirm TASK_ID
wtable task done TASK_ID '人工确认的实际结果'
wtable task fail TASK_ID '失败原因'
wtable task retry --to reviewer TASK_ID
wtable cancel TASK_ID
# 确认 PTY 工作已停止后再执行，不把 Ctrl-C 当成停止证明。
# 执行中的 pull 领取也用它释放 pane 队列。
wtable cancel --confirmed TASK_ID
```

重发只接受失败、未知、取消或写入未确认的任务，保留原记录，并用 `RetryOf` 关联新任务。已在执行或排队的任务不能重发。重发写入未确认的任务时，原记录转为 `unknown` 并继续接受迟到结果；显式重发仍可能造成重复工作。

排队任务直接取消。注入任务先发送中断，进入 `cancelling`，再由人确认 `cancelled`。终端历史和已发生的文件操作不会被撤销。执行中的 `pull` 领取无法由 workbench 可靠停止；人工确认工作已停止后，用 `cancel --confirmed` 将其标记为 `cancelled` 并释放该 pane 的队列。

`wtable audit` 返回最近 256 条越权拒绝记录，只包含调用者、目标、方法与时间；每个 pane 的累计次数保存在元数据中。记录不包含请求正文或凭据。直接边检查已阻止 agent 向自身派活；目前没有内容来源追踪或 `--no-relay` 模式。

连线限制工作台 API，不限制同一系统用户直接访问文件。worktree 用于隔离工作目录，不是操作系统沙箱。

## Git worktree

```sh
wtable layer create feature-a --dir /path/to/repo
wtable layer list
wtable recruit coder-a --layer feature-a -- codex
```

默认基准为存在的 `origin/main`，否则使用 `HEAD`；也可以用 `--base REF` 指定。新分支为 `wtable/layer/NAME`，目录保存在工作台状态目录下。

在目标仓库目录中执行 `wtable changes` 或 `wtable diff [PATH]` 可查看变更。MVP 不自动提交、合并、删除或归档 worktree。

## 升级、恢复与诊断

当前版本为 `0.5.0`。CLI 会检查 daemon 版本，不会把新投递参数交给旧 daemon 静默忽略。保留旧会话时，请为新版使用独立的 `WTABLE_HOME`；需要沿用原目录时，先结束旧会话并执行 `shutdown`，再启动新版。仅重新 attach 不会升级后台 daemon，`shutdown` 会停止该目录下的全部 pane。

```sh
wtable diag
wtable stop coder
wtable restart coder
wtable shutdown
```

- TUI detach 后，daemon、PTY 和 agent 保持运行。
- daemon 重启后恢复 pane 配置、任务、结果和 worktree 记录；不会复活原 PTY 进程，也不会自动重放未知任务。
- `restart` 是显式操作，仅适用于 PTY pane。它重新执行原启动命令；只有命令包含 provider 的 resume 参数时才恢复聊天历史。旧 ACP 记录可查看或移除，但不能重启。
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
python3 scripts/first_run_smoke.py --binary bin/wtable --output .dev/test-results/first-run.json
python3 scripts/workspace_smoke.py --binary bin/wtable --output .dev/test-results/workspace.json
```

验证脚本使用隔离状态和本地确定性 fixture，不调用外部模型。其中保留的 ACP fixture 仅用于测试适配器实现，不代表 ACP 是受支持的用户工作流；当前用户侧任务完成仍遵循 PTY 的 `ack`/`result` 契约。测试会清理自建的进程和临时目录，并保留 JSON 验收报告。

外部 provider 的登录、CLI 兼容性和 PTY 会话恢复能力需要针对实际安装版本验证，不能由 fixture 测试结果推出。

## 本地开发 fixture

`make tui` 在 `.dev/devstack` 打开隔离 TUI，只列本机真实 agent；`make tui-fixtures` 额外提供确定性的 PTY fixture。`make dev` 只启动 daemon 不进入界面；`make dev-clean` 清掉 fixture pane，`make dev-down` 回收整个开发栈。两者都不读写默认状态目录。`scripts/dev.sh add fg|pty|auto|shell [NAME]` 添加 PTY fixture pane，`kill NAME` 移除一个，`clean` 清掉所有 `dev-*` pane。`auto` fixture 会通过 `ack`/`result` 显式确认并完成注入任务，无需模型即可观察任务与报告界面。

## 当前限制

- 只验证 macOS；不支持远程接入和多 client 旁观。
- 回滚保存最多 10,000 个物理行；改变宽度不会重新排版历史。未完成全面 VT conformance。
- 使用完整 cell 快照轮询，尚未实现 cell 差分与事件重放；原计划中的性能预算尚未验收。
- 支持可见内容的鼠标选区复制、键盘回看和复制可见屏；支持向启用鼠标协议的应用转发滚轮，暂不转发点击和拖动，也不支持 OSC 52 剪贴板协议。
- 主画布选择由服务端 `last_focus` 记录，attach 时恢复；工具槽及其比例不持久化。左栏顺序为稳定的创建顺序。
- 每个工作区最多保留 32 个 pane（全局 128 个）、2,000 条任务，prompt 与 result 合计上限 64 MiB；pane 可从 Agents 页移除，任务历史尚无清理命令。保存的消息历史有大小上限，完整任务结果单独保存。
- notes、内置编辑器、账号池、计划任务、图片和浏览器门户属于后续范围。

当前实现完成 P0 送达与回传、P1 控制面板和身份说明接入，以及 P2 Graph v1。`docs/`、`.dev/` 和 `bin/` 均由 Git 白名单规则忽略。
