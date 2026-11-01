package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v3"
	"github.com/gdamore/tcell/v3/color"

	"wtable/internal/model"
)

type workspaceRow struct {
	path    string
	label   string
	hint    string
	panes   int
	running int
	missing bool
	action  bool
}

type workspacePickerView struct {
	purpose string // startup or switch
	rows    []workspaceRow
	cursor  int
	filter  string
	err     string
}

func shortWorkspace(path string) string {
	if path == "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if path == home {
			return "~"
		}
		if strings.HasPrefix(path, home+string(filepath.Separator)) {
			return "~" + path[len(home):]
		}
	}
	return path
}

// openWorkspacePicker lists recents with the launch directory as the first
// row. The most recently opened workspace is preselected, so a repeat launch is
// one Enter away.
func (u *ui) openWorkspacePicker(purpose string) {
	var infos []model.WorkspaceInfo
	if !u.callWithin(5*time.Second, "workspace.list", struct{}{}, &infos) {
		infos = nil
	}
	cwd := u.cwd
	label := "当前目录"
	if purpose == "switch" {
		label = "当前工作区"
	}
	rows := []workspaceRow{{path: cwd, label: label, hint: shortWorkspace(cwd)}}
	for _, info := range infos {
		if info.Path == cwd {
			rows[0].panes, rows[0].running, rows[0].missing = info.PaneCount, info.RunningCount, info.Missing
		}
	}
	seen := map[string]bool{cwd: true}
	last := cwd
	for _, info := range infos {
		if !info.LastOpened.IsZero() {
			last = info.Path
			break
		}
	}
	for _, info := range infos {
		if seen[info.Path] {
			continue
		}
		seen[info.Path] = true
		rows = append(rows, workspaceRow{
			path:    info.Path,
			label:   filepath.Base(info.Path),
			hint:    shortWorkspace(info.Path),
			panes:   info.PaneCount,
			running: info.RunningCount,
			missing: info.Missing,
		})
	}
	rows = append(rows, workspaceRow{label: "输入其他目录…", action: true})
	cursor := 0
	if purpose == "startup" {
		for i := range rows {
			if rows[i].path == last {
				cursor = i
				break
			}
		}
	}
	u.workspacePicker = &workspacePickerView{purpose: purpose, rows: rows, cursor: cursor}
}

func (u *ui) visibleWorkspaceRows() []workspaceRow {
	p := u.workspacePicker
	if p == nil {
		return nil
	}
	filter := strings.ToLower(strings.TrimSpace(p.filter))
	out := []workspaceRow{}
	action := workspaceRow{}
	if filter == "" {
		out = append(out, p.rows...)
		return out
	}
	for _, row := range p.rows {
		if row.action {
			action = row
			continue
		}
		if strings.Contains(strings.ToLower(row.path+" "+row.label), filter) {
			out = append(out, row)
		}
	}
	if action.action {
		out = append(out, action)
	}
	return out
}

func (u *ui) workspacePickerKey(e *tcell.EventKey) {
	p := u.workspacePicker
	if p == nil {
		return
	}
	rows := u.visibleWorkspaceRows()
	switch e.Key() {
	case tcell.KeyEscape:
		if p.purpose == "startup" {
			u.quit = true
		} else {
			u.workspacePicker = nil
		}
		return
	case tcell.KeyUp:
		p.cursor = max(0, p.cursor-1)
		return
	case tcell.KeyDown:
		if len(rows) > 0 {
			p.cursor = min(len(rows)-1, p.cursor+1)
		}
		return
	case tcell.KeyEnter:
		if len(rows) == 0 {
			return
		}
		row := rows[max(0, min(p.cursor, len(rows)-1))]
		if row.action {
			u.openInput("workspace", "")
			return
		}
		u.applyWorkspace(row.path, p.purpose)
		return
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if p.filter != "" {
			_, size := utf8.DecodeLastRuneInString(p.filter)
			p.filter = p.filter[:len(p.filter)-size]
			p.cursor = 0
		}
		return
	case tcell.KeyCtrlU:
		p.filter = ""
		p.cursor = 0
		return
	case tcell.KeyRune:
		if e.Modifiers()&(tcell.ModCtrl|tcell.ModAlt) != 0 {
			return
		}
		p.filter += e.Str()
		p.cursor = 0
		return
	}
}

// applyWorkspace switches every visible surface to one team. Background
// workspaces keep running; only this client's view changes.
func (u *ui) applyWorkspace(path, purpose string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(u.cwd, path)
	}
	var info model.WorkspaceInfo
	if !u.callWithin(10*time.Second, "workspace.open", map[string]string{"path": filepath.Clean(path)}, &info) {
		if u.workspacePicker != nil {
			u.workspacePicker.err = u.status
		}
		return false
	}
	u.workspace = info.Path
	u.cwd = info.Path
	u.mode = ""
	u.workspacePicker = nil
	u.resetForWorkspace()
	u.snapshot = model.Snapshot{}
	if _, err := u.refresh(); err != nil {
		u.status = "刷新失败：" + err.Error()
		u.resize()
		u.draw()
		return false
	}
	u.syncCanvas()
	if len(u.snapshot.Panes) == 0 {
		u.openCreate(model.PaneForeground)
	} else if purpose == "startup" && u.canvas.main != "" {
		// Keep the old "opening the restored pane acknowledges it" behaviour on
		// startup. A mid-session switch must not consume unseen notices.
		u.markFocusedSeen(u.canvas.main)
	}
	u.resize()
	u.draw()
	return true
}

// resetForWorkspace drops per-pane view state. Seen glyphs and input history
// are intentionally kept: they describe the human, not one team.
func (u *ui) resetForWorkspace() {
	u.focus = 0
	u.prefix = false
	u.help = false
	u.copyMode = false
	u.offsets = map[string]int{}
	u.sizes = map[string][2]int{}
	u.boxes = map[string]rect{}
	u.mode = ""
	u.status = ""
	u.taskView = false
	u.taskCursor = ""
	u.taskDetail = nil
	u.taskScroll = 0
	u.taskRows = nil
	u.reportView = false
	u.reportCursor = 0
	u.reportDetail = nil
	u.taskActionID = ""
	u.taskDelivery = ""
	u.taskScreen = nil
	u.panel = nil
	u.graph = nil
	u.activeCwd = ""
	u.activeLayer = ""
	u.acpInputs = map[string]*lineEdit{}
	u.pasting = false
	u.paste.Reset()
	u.pendingPaste = ""
	u.lastDrawKey = ""
	u.lastDrawSnapshot = model.Snapshot{}
	u.dividers = nil
	u.drag = nil
	u.mouseDown = false
	u.selection = nil
	u.refreshFailed = false
	u.canvas = canvas{ratio: u.canvas.ratio}
	u.area = focusMain
	u.rail = railState{}
	u.approval = approvalState{}
	u.palette = nil
	u.create = nil
	u.queueView = nil
	u.interruptConfirm = false
	u.lastSent = map[string]string{}
	u.knownPermissions = map[string]bool{}
	u.lastFocusSent = ""
	u.utilityLast = ""
}

func (u *ui) drawWorkspacePicker() {
	p := u.workspacePicker
	if p == nil {
		return
	}
	rows := u.visibleWorkspaceRows()
	w, h := u.screen.Size()
	width := min(90, max(40, w-8))
	height := min(len(rows)+7, max(7, h-4))
	x0 := (w - width) / 2
	y0 := (h - height) / 2
	base := tcell.StyleDefault.Background(color.NewHexColor(0x1d2733)).Foreground(color.White)
	dim := base.Foreground(color.NewHexColor(0x8fa3b8))
	sel := tcell.StyleDefault.Background(color.NewHexColor(0x2f4a63)).Foreground(color.White)
	warn := base.Foreground(color.NewHexColor(0xe6b183))
	for row := 0; row < height; row++ {
		u.line(x0, y0+row, width, "", base)
	}
	u.line(x0, y0, width, " 选择工作区 ", base.Bold(true))
	u.line(x0, y0+1, width, " > "+truncateColumns(p.filter+"▏", width-4), base)
	u.line(x0, y0+2, width, strings.Repeat("─", width), dim)
	visible := height - 6
	p.cursor = max(0, min(p.cursor, max(0, len(rows)-1)))
	top := max(0, p.cursor-visible+1)
	top = min(top, max(0, len(rows)-visible))
	if len(rows) == 0 {
		u.line(x0+1, y0+3, width-2, "没有匹配的工作区；Enter 输入路径", dim)
	}
	for i := top; i < min(len(rows), top+visible); i++ {
		row := rows[i]
		style := base
		marker := "   "
		if i == p.cursor {
			marker = " › "
			style = sel
		}
		line := marker + row.label
		if row.path != "" && row.path != row.label {
			line += "  " + row.hint
		}
		if row.panes > 0 {
			line += fmt.Sprintf("  · %d agent", row.panes)
		}
		if row.missing {
			line += "  (目录不存在)"
			if i != p.cursor {
				style = warn
			}
		}
		u.line(x0+1, y0+3+(i-top), width-2, truncateColumns(line, width-4), style)
	}
	if p.err != "" {
		u.line(x0+1, y0+height-4, width-2, truncateColumns("⚠ "+p.err, width-4), warn)
	}
	footer := " ↑↓ 选择 · Enter 进入 · 输入筛选 · Esc 取消 "
	if p.purpose == "startup" {
		footer = " ↑↓ 选择 · Enter 进入 · 输入筛选 · Esc 退出 "
	}
	u.line(x0, y0+height-1, width, footer, dim)
	u.screen.HideCursor()
}

func (u *ui) workspaceLabel() string {
	return shortWorkspace(u.workspace)
}
