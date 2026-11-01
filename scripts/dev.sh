#!/usr/bin/env bash
# Isolated development stack for wtable: one state directory, deterministic
# fixtures, and fast pane reuse without touching the real WTABLE_HOME.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HOME_DIR="${WTABLE_DEV_HOME:-$ROOT/.dev/devstack}"
BIN="${WTABLE_DEV_BIN:-$ROOT/bin/wtable}"
FIXTURES="$ROOT/scripts/fixtures"
SOCKET="$HOME_DIR/daemon.sock"
MARKER="$HOME_DIR/.devstack"
RUN_DIR="$HOME_DIR/run"

die() { echo "dev: $*" >&2; exit 1; }

usage() {
  cat <<EOF
用法：scripts/dev.sh COMMAND [ARGS]

隔离状态目录（默认 $ROOT/.dev/devstack，可用 WTABLE_DEV_HOME 覆盖）：

  up [--attach] [--real]  启动 daemon；--attach 直接进入 TUI
                         --real 不注册 dev-* fixture 预设，只留真实 agent
  tui [--real]         启动并直接进入 TUI（等价于 up --attach）
  down [--keep]        shutdown 并删除该状态目录；--keep 只停服务、保留日志
  reset [--real]       等价于 down 后 up；加 --attach 可直接进 TUI
  attach               在隔离状态目录中打开 TUI
  status               列出 pane
  logs                 跟踪 daemon 日志
  path                 打印隔离状态目录

测试进程（fixture，不调用真实模型）：

  add fg   [NAME] [--brief] [recruit 参数...]   新建 PTY 前台
  add pty  [NAME] [--brief] [recruit 参数...]   新建 PTY worker（回显输入）
  add auto [NAME] [--brief] [recruit 参数...]   新建自动 ack/result 的 PTY worker
  add acp  [NAME] [--brief] [recruit 参数...]   新建 ACP worker（回合结束自动完成）
  add shell [NAME]                              新建 shell worker
  kill NAME_OR_ID      移除一个 pane（进程随之停止）
  clean                移除所有 dev-* 名称或 dev-* 预设的 pane，保留 daemon

真实 agent（使用本机已安装、已登录的 CLI）：

  recruit NAME [OPTIONS] [-- CMD...]   在隔离状态目录里执行 wtable recruit
  cli ARGS...                          在隔离状态目录里执行任意 wtable 命令

示例：
  scripts/dev.sh tui --real        # 干净 TUI：首启选 Codex/Claude/Agy
  scripts/dev.sh reset --real --attach
  scripts/dev.sh up --attach
  scripts/dev.sh add fg --brief
  scripts/dev.sh add acp worker1 --brief
  scripts/dev.sh kill worker1
  scripts/dev.sh clean
  scripts/dev.sh down
  scripts/dev.sh recruit coder --preset codex-pty --brief --dir /tmp/wtable-scratch
  scripts/dev.sh cli tasks
EOF
}

ensure_bin() {
  if [[ ! -x "$BIN" ]]; then
    echo "dev: building $BIN"
    (cd "$ROOT" && go build -o "$BIN" ./cmd/wtable)
  fi
}

build_bin() {
  if [[ "${WTABLE_DEV_SKIP_BUILD:-0}" == 1 ]]; then
    ensure_bin
  else
    (cd "$ROOT" && go build -o "$BIN" ./cmd/wtable)
  fi
}

wt() { env WTABLE_HOME="$HOME_DIR" "$BIN" "$@"; }
running() { [[ -S "$SOCKET" ]]; }
socket_live() {
  python3 - "$SOCKET" <<'PY'
import socket, sys
client = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
try:
    client.settimeout(0.2)
    client.connect(sys.argv[1])
except OSError:
    sys.exit(1)
PY
}

write_presets() {
  local python="$1" mode="${2:-fixtures}"
  python3 - "$HOME_DIR/presets.json" "$FIXTURES" "$RUN_DIR" "$python" "$mode" <<'PY'
import json, sys
path, fixtures, run, python, mode = sys.argv[1:6]
data = [
    {"id": "dev-pty", "name": "Dev echo (PTY)", "backend": "pty",
     "command": [python, "-u", fixtures + "/pty_echo.py", "--root", run, "--mode", "echo"]},
    {"id": "dev-pty-auto", "name": "Dev auto (PTY)", "backend": "pty",
     "command": [python, "-u", fixtures + "/pty_echo.py", "--root", run, "--mode", "auto"]},
    {"id": "dev-acp", "name": "Dev ACP worker", "backend": "acp",
     "command": [python, "-u", fixtures + "/acp_agent.py", "--root", run]},
]
if mode == "real":
    # Built-in real presets (codex-pty, claude-pty, agy-pty) need no entry.
    data = []
# Keep presets the user added for real agents; only dev-* entries are managed.
try:
    with open(path) as stream:
        existing = json.load(stream)
except (OSError, json.JSONDecodeError):
    existing = []
kept = [p for p in existing if not str(p.get("id", "")).startswith("dev-")]
with open(path, "w") as stream:
    json.dump(kept + data, stream, indent=2)
    stream.write("\n")
PY
}

cmd_up() {
  local attach=0 mode=fixtures
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --attach) attach=1 ;;
      --real) mode=real ;;
      --fixtures) mode=fixtures ;;
      *) die "up: 未知参数 $1" ;;
    esac
    shift
  done
  command -v python3 >/dev/null || die "需要 python3"
  build_bin
  mkdir -p "$HOME_DIR" "$RUN_DIR"
  printf 'devstack\n' >"$MARKER"
  write_presets "$(command -v python3)" "$mode"
  if ! wt list >/dev/null 2>&1; then
    # A stale socket from a killed daemon blocks ensureDaemon's probe. Only
    # remove it when nothing is listening.
    if ! socket_live; then
      rm -f "$SOCKET"
    fi
    # The CLI starts the daemon detached and logs to <home>/daemon.log.
    wt list >/dev/null 2>&1 || die "daemon 启动失败，请查看 $HOME_DIR/daemon.log"
  fi
  echo "dev stack ready"
  echo "  home:   $HOME_DIR"
  echo "  attach: WTABLE_HOME='$HOME_DIR' '$BIN'"
  echo "  add:    '$ROOT/scripts/dev.sh' add fg --brief"
  echo "  clean:  '$ROOT/scripts/dev.sh' clean"
  echo "  down:   '$ROOT/scripts/dev.sh' down"
  if (( attach )); then
    exec env WTABLE_HOME="$HOME_DIR" "$BIN"
  fi
}

cmd_add() {
  local kind="${1:-fg}"
  shift || true
  local name=""
  if [[ $# -gt 0 && "$1" != -* ]]; then
    name="$1"
    shift
  fi
  if [[ -z "$name" ]]; then
    name="dev-$kind-$(date +%H%M%S)"
  fi
  ensure_bin
  if ! running; then
    cmd_up >/dev/null
    running || die "daemon 未运行"
  fi
  case "$kind" in
    fg)    wt recruit "$name" --preset dev-pty --kind foreground "$@" ;;
    pty)   wt recruit "$name" --preset dev-pty --kind worker "$@" ;;
    auto)  wt recruit "$name" --preset dev-pty-auto --kind worker "$@" ;;
    acp)   wt recruit "$name" --preset dev-acp --kind worker "$@" ;;
    shell) wt new "$name" --kind worker "$@" ;;
    *)     die "add: 未知类型 $kind（fg/pty/auto/acp/shell）" ;;
  esac
}

cmd_kill() {
  [[ $# -eq 1 ]] || die "用法：scripts/dev.sh kill NAME_OR_ID"
  running || die "daemon 未运行"
  wt remove "$1" >/dev/null
  echo "removed $1"
}

cmd_clean() {
  running || { echo "daemon 未运行"; return 0; }
  local ids
  ids="$(wt list --json | python3 -c '
import json, sys
for pane in json.load(sys.stdin):
    if pane.get("name", "").startswith("dev-") or pane.get("preset", "").startswith("dev-"):
        print(pane["id"])
')"
  if [[ -z "$ids" ]]; then
    echo "没有 dev-* pane"
    return 0
  fi
  while IFS= read -r id; do
    [[ -n "$id" ]] || continue
    wt remove "$id" >/dev/null
    echo "removed $id"
  done <<<"$ids"
}

cmd_down() {
  local keep=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --keep) keep=1 ;;
      *) die "down: 未知参数 $1" ;;
    esac
    shift
  done
  if running; then
    wt shutdown >/dev/null 2>&1 || true
    for _ in $(seq 1 50); do
      running || break
      sleep 0.1
    done
  fi
  kill_fixtures
  if [[ -f "$MARKER" ]]; then
    if (( keep )); then
      echo "dev stack stopped; kept $HOME_DIR"
    else
      rm -rf "$HOME_DIR"
      echo "dev stack removed: $HOME_DIR"
    fi
  else
    echo "dev stack stopped; $HOME_DIR is not a dev state dir, kept it"
  fi
}

kill_fixtures() {
  local file="$RUN_DIR/fixture-pids.jsonl"
  [[ -f "$file" ]] || return 0
  python3 - "$file" <<'PY'
import json, os, signal, subprocess, sys
seen = set()
for line in open(sys.argv[1]):
    try:
        pid = int(json.loads(line))
    except (ValueError, json.JSONDecodeError):
        continue
    if pid in seen:
        continue
    seen.add(pid)
    try:
        cmd = subprocess.check_output(["ps", "-ww", "-p", str(pid), "-o", "command="],
                                      text=True, stderr=subprocess.DEVNULL).strip()
    except subprocess.CalledProcessError:
        continue
    if "acp_agent.py" not in cmd and "pty_echo.py" not in cmd:
        continue
    try:
        os.kill(pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
PY
}

cmd_recruit() {
  ensure_bin
  running || cmd_up >/dev/null
  wt recruit "$@"
}

cmd_cli() {
  ensure_bin
  running || cmd_up >/dev/null
  wt "$@"
}

cmd_status() {
  running || { echo "daemon 未运行（$HOME_DIR）"; return 0; }
  wt list
}

cmd_tui() {
  cmd_up --attach "$@"
}

cmd_attach() {
  ensure_bin
  running || cmd_up >/dev/null
  exec env WTABLE_HOME="$HOME_DIR" "$BIN"
}

cmd_logs() {
  [[ -f "$HOME_DIR/daemon.log" ]] || die "没有日志：$HOME_DIR/daemon.log"
  tail -n 80 -f "$HOME_DIR/daemon.log"
}

command="${1:-up}"
shift || true
case "$command" in
  up)      cmd_up "$@" ;;
  tui)     cmd_tui "$@" ;;
  down)    cmd_down "$@" ;;
  reset)   cmd_down; cmd_up "$@" ;;
  add)     cmd_add "$@" ;;
  kill|rm) cmd_kill "$@" ;;
  clean)   cmd_clean ;;
  recruit) cmd_recruit "$@" ;;
  cli|wt)  cmd_cli "$@" ;;
  status|ls) cmd_status ;;
  attach)  cmd_attach ;;
  logs)    cmd_logs ;;
  path)    echo "$HOME_DIR" ;;
  help|-h|--help) usage ;;
  *)       usage; exit 1 ;;
esac
