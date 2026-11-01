// Package tui provides the local, single-writer terminal workbench.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"time"

	"github.com/gdamore/tcell/v3"

	"wtable/internal/config"
	"wtable/internal/model"
	"wtable/internal/rpc"
)

type rect struct{ x, y, w, h int }

type ui struct {
	actions                *actionMenu
	ctx                    context.Context
	client                 *rpc.Client
	screen                 tcell.Screen
	cwd                    string
	workspace              string
	snapshot               model.Snapshot
	focus                  int
	prefix, help, copyMode bool
	offsets                map[string]int
	sizes                  map[string][2]int
	boxes                  map[string]rect
	mode, status           string
	editor                 lineEdit
	taskView               bool
	taskCursor             string
	taskDetail             *model.Task
	taskScroll             int
	taskRows               map[int]string
	reportView             bool
	reportCursor           int
	reportDetail           *model.Report
	taskActionID           string
	taskDelivery           string
	taskScreen             *model.Frame
	panel                  *controlPanel
	graph                  *graphView
	panelResults           chan panelResult
	activeCwd, activeLayer string
	acpInputs              map[string]*lineEdit
	pasting                bool
	paste                  strings.Builder
	pendingPaste           string
	lastDrawKey            string
	lastDrawSnapshot       model.Snapshot
	dividers               []divider
	drag                   *divider
	mouseDown              bool
	selection              *textSelection
	refreshFailed          bool
	lastReconnect          time.Time

	// Interaction redesign: rail + one main canvas + one utility slot.
	canvas           canvas
	area             focusArea
	rail             railState
	approval         approvalState
	palette          *paletteView
	create           *createView
	queueView        *queueView
	workspacePicker  *workspacePickerView
	interruptConfirm bool
	lastSent         map[string]string
	seenGlyphs       map[string]bool
	knownPermissions map[string]bool
	lastFocusSent    string
	allowChat        bool
	utilityLast      string
	quit             bool

	railRect     rect
	railHits     map[int]railRow
	mainRect     rect
	utilRect     rect
	approvalRect rect
	composerRect rect
}

// Run attaches the control UI. Leaving the UI detaches; daemon panes keep running.
// preferredWorkspace, when non-empty, skips the startup picker.
func Run(ctx context.Context, client *rpc.Client, cwd, preferredWorkspace string) error {
	if err := client.Call(ctx, "ui.attach", struct{}{}, nil); err != nil {
		return err
	}
	// Agent output, task results and report summaries are untrusted. tcell
	// sanitises control characters from Put/PutStr when this option is set;
	// leaving it off lets a trailing ESC/CR reach the outer terminal.
	screen, err := tcell.NewScreen(tcell.OptSanitizeContent(true))
	if err != nil {
		return err
	}
	if err = screen.Init(); err != nil {
		return err
	}
	defer screen.Fini()
	screen.EnablePaste()
	screen.EnableMouse(tcell.MouseDragEvents)
	u := &ui{ctx: ctx, client: client, screen: screen, cwd: cwd, offsets: map[string]int{}, sizes: map[string][2]int{}, boxes: map[string]rect{}, knownPermissions: map[string]bool{}, lastSent: map[string]string{}}
	defer func() {
		if u.client == nil {
			return
		}
		detachCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = u.client.Call(detachCtx, "ui.detach", struct{}{}, nil)
	}()
	if preferredWorkspace != "" {
		if !u.applyWorkspace(preferredWorkspace, "startup") {
			msg := u.status
			if msg == "" {
				msg = "无法打开工作区 " + preferredWorkspace
			}
			return errors.New(msg)
		}
	} else {
		u.openWorkspacePicker("startup")
		u.resize()
		u.draw()
	}
	ticker := time.NewTicker(time.Second / 30)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-u.panelResults:
			u.panelFinished(result)
			u.draw()
		case ev, ok := <-screen.EventQ():
			if !ok {
				return nil
			}
			switch e := ev.(type) {
			case *tcell.EventResize:
				u.drag = nil
				u.resize()
			case *tcell.EventMouse:
				u.mouse(e)
			case *tcell.EventPaste:
				if e.Start() {
					u.pasting = true
					u.paste.Reset()
				}
				if e.End() {
					u.pasting = false
					u.handlePaste(u.paste.String())
				}
			case *tcell.EventKey:
				if !e.Pressed() {
					continue
				}
				if u.pasting {
					u.paste.WriteString(encodeKey(e, false))
					continue
				}
				u.drag = nil
				if u.key(e) {
					return nil
				}
			}
			// Overlays change which panes are visible, so recompute the canvas
			// rects after every event instead of waiting for a snapshot change.
			u.resize()
			u.draw()
		case <-ticker.C:
			changed, err := u.refresh()
			if err != nil {
				u.refreshFailed = true
				u.status = "刷新失败：" + err.Error()
				// A server error leaves the stream synchronized: retry on the
				// same connection. A transport error poisons bufio.Scanner, so
				// the only recovery is a fresh connection.
				if !isServerResponseError(err) {
					if time.Since(u.lastReconnect) < 2*time.Second {
						u.draw()
						continue
					}
					u.lastReconnect = time.Now()
					if rerr := u.reconnect(); rerr != nil {
						u.status = "刷新失败：" + err.Error() + "；重连失败：" + rerr.Error()
						u.draw()
						continue
					}
					changed, err = u.refresh()
					if err != nil {
						u.status = "刷新失败：" + err.Error()
						u.draw()
						continue
					}
				} else {
					u.draw()
					continue
				}
			}
			if u.refreshFailed {
				u.refreshFailed = false
				u.status = ""
			}
			if changed {
				u.resize()
				u.draw()
			}
		}
	}
}

// A server response (even an error) leaves the JSON-RPC stream synchronized.
// Anything else (deadline, EOF, decode failure) latches the scanner's error,
// so retrying the same connection cannot recover.
func isServerResponseError(err error) bool {
	var rpcErr *rpc.Error
	return errors.As(err, &rpcErr)
}

// reconnect replaces the long-lived control connection. The old socket is
// closed first; ui.attach is retried briefly because the daemon may not have
// processed that close and released the single-writer slot yet.
func (u *ui) reconnect() error {
	if u.client != nil {
		_ = u.client.Close()
	}
	paths, err := config.Resolve()
	if err != nil {
		return err
	}
	token, err := paths.ClientToken()
	if err != nil {
		return err
	}
	dialCtx, cancel := context.WithTimeout(u.ctx, 2*time.Second)
	defer cancel()
	next, err := rpc.Dial(dialCtx, paths.Socket, token)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		attachCtx, attachCancel := context.WithTimeout(u.ctx, time.Second)
		err = next.Call(attachCtx, "ui.attach", struct{}{}, nil)
		attachCancel()
		if err == nil {
			u.client = next
			return nil
		}
		if time.Now().After(deadline) || !strings.Contains(err.Error(), "已有可写 TUI") {
			_ = next.Close()
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (u *ui) call(method string, args, out any) bool {
	return u.callWithin(3*time.Second, method, args, out)
}

func (u *ui) callWithin(timeout time.Duration, method string, args, out any) bool {
	ctx, cancel := context.WithTimeout(u.ctx, timeout)
	defer cancel()
	if err := u.client.Call(ctx, method, args, out); err != nil {
		u.status = err.Error()
		return false
	}
	return true
}

func (u *ui) refresh() (bool, error) {
	if u.workspace == "" {
		return false, nil
	}
	ids := make([]string, 0, len(u.boxes))
	for id := range u.boxes {
		ids = append(ids, id)
	}
	var next model.Snapshot
	ctx, cancel := context.WithTimeout(u.ctx, 3*time.Second)
	defer cancel()
	// Frames are requested only for the panes actually drawn. Background panes
	// keep their last size and stay cheap to poll.
	params := map[string]any{"offsets": u.offsets, "ids": ids, "graph": true, "frames": u.graph == nil && u.panel == nil && !u.taskView, "workspace": u.workspace}
	if err := u.client.Call(ctx, "state.get", params, &next); err != nil {
		return false, err
	}
	changed := !reflect.DeepEqual(u.snapshot, next)
	u.snapshot = next
	u.syncCanvas()
	u.detectNewPermissions()
	if u.taskDetail != nil {
		for _, t := range next.Tasks {
			if t.ID == u.taskDetail.ID && !t.UpdatedAt.Equal(u.taskDetail.UpdatedAt) {
				var detail model.Task
				if u.call("task.get", map[string]string{"id": t.ID}, &detail) {
					u.taskDetail = &detail
				}
				break
			}
		}
	}
	if u.focus >= len(next.Panes) {
		u.focus = max(0, len(next.Panes)-1)
	}
	return changed, nil
}

// detectNewPermissions announces arrivals without taking focus or clearing a
// draft. The request stays where it belongs: the rail, the title and its card.
func (u *ui) detectNewPermissions() {
	present := map[string]bool{}
	for _, p := range u.snapshot.Permissions {
		present[p.ID] = true
		if !u.knownPermissions[p.ID] {
			name := p.PaneID
			if pane := u.paneByID(p.PaneID); pane != nil {
				name = pane.Name
			}
			u.legendOnce(glyphApproval)
			u.status = "⏳ " + name + " 请求批准 · Ctrl-a p 处理"
		}
	}
	u.knownPermissions = present
}

func (u *ui) send(data string) {
	p := u.current()
	if p == nil || p.Backend != "pty" || data == "" {
		return
	}
	if _, visible := u.boxes[p.ID]; !visible {
		return
	}
	u.offsets[p.ID] = 0
	u.call("pane.input", map[string]string{"id": p.ID, "data": data}, nil)
}

func (u *ui) handlePaste(data string) {
	u.selection = nil
	if u.graph != nil || u.palette != nil || u.create != nil || u.queueView != nil || u.workspacePicker != nil || u.area == focusRail || u.area == focusApproval {
		return
	}
	if u.panel != nil && !u.taskView && u.mode == "" {
		if u.panel.form != "" && !u.panel.busy {
			f := &u.panel.fields[u.panel.field]
			if len(f.options) == 0 && !f.multi {
				f.edit.Insert(data)
			}
		}
		return
	}
	if (u.taskView && u.mode == "") || u.help || u.pendingPaste != "" {
		return
	}
	if u.mode != "" {
		u.editor.Insert(data)
		return
	}
	p := u.current()
	if p == nil {
		return
	}
	if _, visible := u.boxes[p.ID]; !visible {
		return
	}
	if p.Backend == "acp" {
		u.acpInput(p.ID).Insert(data)
		return
	}
	if p.Frame != nil && p.Frame.BracketedPaste {
		u.send("\x1b[200~" + data + "\x1b[201~")
		return
	}
	if strings.ContainsAny(data, "\r\n") {
		u.pendingPaste = data
		return
	}
	u.send(data)
}

func (u *ui) key(e *tcell.EventKey) bool {
	k, s := e.Key(), e.Str()
	if u.selection != nil {
		u.selection = nil
		u.status = ""
		if k == tcell.KeyEscape {
			return false
		}
	}
	if u.pendingPaste != "" {
		if s == "y" {
			u.send(u.pendingPaste)
			u.pendingPaste = ""
		} else if k == tcell.KeyEscape || s == "n" {
			u.pendingPaste = ""
		}
		return false
	}
	if u.help {
		u.help = false
		return false
	}
	if u.mode != "" {
		u.edit(e)
		return false
	}
	if u.workspacePicker != nil {
		u.workspacePickerKey(e)
		return u.quit
	}
	if u.actions != nil {
		u.actionKey(e)
		return false
	}
	if u.palette != nil {
		u.paletteKey(e)
		return u.quit
	}
	if u.create != nil {
		u.createKey(e)
		return false
	}
	if u.queueView != nil {
		u.queueKey(e)
		return false
	}
	if u.graph != nil {
		u.graphKey(e)
		return false
	}
	if u.taskView {
		u.taskKey(e)
		return false
	}
	if u.panel != nil {
		u.panelKey(e)
		return false
	}
	if u.copyMode {
		p := u.current()
		if p == nil {
			u.copyMode = false
			return false
		}
		switch {
		case k == tcell.KeyEscape || s == "q":
			u.copyMode = false
			u.offsets[p.ID] = 0
		case k == tcell.KeyUp || s == "k":
			u.offsets[p.ID]++
		case k == tcell.KeyPgUp:
			u.offsets[p.ID] += max(1, u.boxes[p.ID].h-1)
		case k == tcell.KeyDown || s == "j":
			u.offsets[p.ID] = max(0, u.offsets[p.ID]-1)
		case k == tcell.KeyPgDn:
			u.offsets[p.ID] = max(0, u.offsets[p.ID]-max(1, u.boxes[p.ID].h-1))
		case s == "y":
			u.copyVisible()
		}
		if p.Frame != nil {
			u.offsets[p.ID] = min(u.offsets[p.ID], p.Frame.HistoryLines)
		}
		_, _ = u.refresh()
		return false
	}
	if u.prefix {
		u.prefix = false
		return u.prefixKey(e)
	}
	// Ctrl-a Enter is two-stage: the first press only explains, the second
	// confirms. Esc cancels the confirmation but keeps the draft.
	if u.interruptConfirm {
		switch k {
		case tcell.KeyEnter:
			u.interruptCurrent()
			return false
		case tcell.KeyEscape:
			u.interruptConfirm = false
			u.status = "已取消中断"
			return false
		default:
			u.interruptConfirm = false
		}
	}
	if k == tcell.KeyCtrlA || (s == "a" && e.Modifiers()&tcell.ModCtrl != 0) {
		u.prefix = true
		return false
	}
	if u.area == focusRail {
		u.railKey(e)
		return false
	}
	if u.area == focusApproval {
		u.approvalKey(e)
		return false
	}
	p := u.current()
	if p == nil {
		return false
	}
	if _, visible := u.boxes[p.ID]; !visible {
		return false
	}
	if p.Backend == "acp" {
		u.composerKey(e, p)
		return false
	}
	if restartable(p.State) && k == tcell.KeyEnter {
		u.restartCurrent()
		return false
	}
	app := p.Frame != nil && p.Frame.AppCursor
	u.send(encodeKey(e, app))
	return false
}

// prefixKey handles the one leader key. Everything here is either a daily
// action or opens the command palette; there are no hidden modes.
func (u *ui) prefixKey(e *tcell.EventKey) bool {
	k, s := e.Key(), e.Str()
	// Legacy/CSI-u may report the second Ctrl-a as a rune with ModCtrl.
	if k == tcell.KeyCtrlA || (s == "a" && e.Modifiers()&tcell.ModCtrl != 0) {
		u.send("\x01")
		return false
	}
	switch k {
	case tcell.KeyUp:
		u.nextPane(-1)
		return false
	case tcell.KeyDown:
		u.nextPane(1)
		return false
	case tcell.KeyLeft:
		u.focusMainArea()
		return false
	case tcell.KeyRight:
		u.focusUtilityArea()
		return false
	case tcell.KeyEnter:
		u.interruptCurrent()
		return false
	}
	if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
		u.jumpToNth(int(s[0] - '0'))
		return false
	}
	switch s {
	case "j":
		u.nextPane(1)
	case "k":
		u.nextPane(-1)
	case "h", "l":
		if s == "h" {
			u.focusMainArea()
		} else {
			u.focusUtilityArea()
		}
	case "a":
		u.focusRail()
	case "p":
		u.processNextApproval()
	case "s":
		u.toggleUtility()
	case "t":
		u.openTaskComposer()
	case " ":
		u.openPalette()
	case "?":
		u.help = true
	case "G":
		u.openGraph()
	case "n":
		u.openCreate(model.PaneWorker)
	case "b":
		u.openPanel(2)
	case "w":
		u.openWorkspacePicker("switch")
	case "q", "d":
		return true
	default:
		u.status = "未知的 Ctrl-a 组合；Ctrl-a Space 打开命令面板"
	}
	return false
}

// focusMainArea / focusUtilityArea switch keyboard focus without changing the
// canvas. Missing utility is explained, never guessed.
func (u *ui) focusMainArea() {
	u.area = focusMain
	if u.canvas.main != "" {
		u.markFocusedSeen(u.canvas.main)
	}
}

func (u *ui) focusUtilityArea() {
	if u.canvas.utility == "" {
		u.status = "没有工具槽；Ctrl-a s 打开一个"
		return
	}
	u.area = focusUtility
	u.markFocusedSeen(u.canvas.utility)
}

func (u *ui) openInput(mode, initial string) {
	u.mode = mode
	u.editor.Begin(mode, initial)
	if mode == "task" {
		u.taskDelivery = ""
	}
	u.selection = nil
}

func (u *ui) edit(e *tcell.EventKey) {
	if u.mode == "task" {
		if e.Key() == tcell.KeyTab || e.Key() == tcell.KeyBacktab {
			u.cycleTaskMode(e.Key() == tcell.KeyBacktab)
			return
		}
		if u.interruptConfirm && e.Key() != tcell.KeyEnter {
			u.interruptConfirm = false
		}
	}
	if e.Key() == tcell.KeyEscape {
		u.mode = ""
		u.editor.Set("")
		u.interruptConfirm = false
		return
	}
	// Legacy ESC+Return is reported by tcell as Alt+Ctrl+M; CSI-u uses Enter.
	altEnter := e.Modifiers()&tcell.ModAlt != 0 && (e.Key() == tcell.KeyEnter || (e.Key() == tcell.KeyRune && e.Modifiers()&tcell.ModCtrl != 0 && e.Str() == "m"))
	ctrlJ := e.Key() == tcell.KeyCtrlJ || (e.Key() == tcell.KeyRune && e.Str() == "j" && e.Modifiers()&tcell.ModCtrl != 0)
	if u.mode == "task" && (altEnter || ctrlJ) {
		u.editor.Insert("\n")
		return
	}
	if e.Key() == tcell.KeyEnter {
		input, mode := u.editor.Text(), u.mode
		switch mode {
		case "command":
			if strings.TrimSpace(input) == "" {
				return
			}
			u.spawnShell([]string{"/bin/zsh", "-lc", input})
		case "workspace":
			if strings.TrimSpace(input) == "" {
				u.status = "请输入工作区目录"
				return
			}
			purpose := "switch"
			if u.workspacePicker != nil {
				purpose = u.workspacePicker.purpose
			}
			if !u.applyWorkspace(input, purpose) {
				return
			}
		case "task":
			u.submitTaskComposer(input)
			return
		case "queue-edit":
			if strings.TrimSpace(input) == "" || u.taskActionID == "" {
				u.status = "请输入任务内容"
				return
			}
			var t model.Task
			if u.call("task.edit", map[string]string{"id": u.taskActionID, "prompt": input}, &t) {
				u.status = "已更新排队任务 " + shortTask(t.ID)
				_, _ = u.refresh()
			}
		case "rail-filter":
			u.rail.filter = strings.TrimSpace(input)
			u.railSelectFirst()
		case "task-done", "task-fail", "task-redirect", "task-confirm-stop", "task-raw":
			if !u.submitTaskAction(mode, input) {
				return
			}
		case "edge", "unedge":
			fields := strings.Fields(input)
			if len(fields) != 2 {
				u.status = "请输入来源与目标 pane 名称"
				return
			}
			method := "edge.add"
			if mode == "unedge" {
				method = "edge.remove"
			}
			if !u.call(method, map[string]string{"from": fields[0], "to": fields[1], "workspace": u.workspace}, nil) {
				return
			}
		case "stop":
			if input != "y" {
				u.status = "输入 y 确认，或 Esc 取消"
				return
			}
			if p := u.current(); p != nil {
				if !u.call("pane.stop", map[string]string{"id": p.ID}, nil) {
					return
				}
			}
		}
		u.editor.Remember(mode)
		u.mode = ""
		u.editor.Set("")
		return
	}
	u.editor.Handle(e)
}

func (u *ui) copyVisible() {
	p := u.current()
	if p == nil {
		return
	}
	var b strings.Builder
	if p.Frame != nil {
		for _, row := range p.Frame.Cells {
			for _, c := range row {
				if c.Width > 0 {
					b.WriteString(c.Text)
				}
			}
			b.WriteByte('\n')
		}
	} else {
		for _, m := range p.Messages {
			fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Text)
		}
	}
	u.copyText(b.String(), "已复制当前可见内容")
}

func (u *ui) copyText(text, success string) {
	ctx, cancel := context.WithTimeout(u.ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/pbcopy")
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		u.status = "复制失败：" + err.Error()
	} else {
		u.status = success
	}
}
