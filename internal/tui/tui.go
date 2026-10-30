// Package tui provides the local, single-writer terminal workbench.
package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"

	"github.com/gdamore/tcell/v3"

	"wtable/internal/model"
	"wtable/internal/rpc"
)

type rect struct{ x, y, w, h int }
type ui struct {
	ctx                                 context.Context
	client                              *rpc.Client
	screen                              tcell.Screen
	cwd                                 string
	snapshot                            model.Snapshot
	focus                               int
	layout                              int
	zoom, tasks, prefix, help, copyMode bool
	offsets                             map[string]int
	sizes                               map[string][2]int
	boxes                               map[string]rect
	mode, status                        string
	editor                              lineEdit
	taskView                            bool
	taskCursor                          string
	taskDetail                          *model.Task
	taskScroll                          int
	taskRows                            map[int]string
	pasting                             bool
	paste                               strings.Builder
	pendingPaste                        string
	lastPermission                      string
	lastDrawKey                         string
	lastDrawSnapshot                    model.Snapshot
	colCuts, rowCuts                    []float64
	layoutKey                           [2]int
	dividers                            []divider
	drag                                *divider
	mouseDown                           bool
	selection                           *textSelection
}

// Run attaches the control UI. Leaving the UI detaches; daemon panes keep running.
func Run(ctx context.Context, client *rpc.Client, cwd string) error {
	if err := client.Call(ctx, "ui.attach", struct{}{}, nil); err != nil {
		return err
	}
	defer func() {
		detachCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = client.Call(detachCtx, "ui.detach", struct{}{}, nil)
	}()
	screen, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	if err = screen.Init(); err != nil {
		return err
	}
	defer screen.Fini()
	screen.EnablePaste()
	screen.EnableMouse(tcell.MouseDragEvents)
	u := &ui{ctx: ctx, client: client, screen: screen, cwd: cwd, offsets: map[string]int{}, sizes: map[string][2]int{}, boxes: map[string]rect{}}
	if _, err = u.refresh(); err != nil {
		return err
	}
	if len(u.snapshot.Panes) == 0 {
		u.spawn(nil)
		if _, err = u.refresh(); err != nil {
			return err
		}
	}
	u.resize()
	u.draw()
	ticker := time.NewTicker(time.Second / 30)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
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
			u.draw()
		case <-ticker.C:
			changed, err := u.refresh()
			if err != nil {
				return err
			}
			if changed {
				u.resize()
				u.draw()
			}
		}
	}
}

func (u *ui) call(method string, args, out any) bool {
	ctx, cancel := context.WithTimeout(u.ctx, 3*time.Second)
	defer cancel()
	if err := u.client.Call(ctx, method, args, out); err != nil {
		u.status = err.Error()
		return false
	}
	return true
}
func (u *ui) refresh() (bool, error) {
	var next model.Snapshot
	ids := []string{}
	for id := range u.boxes {
		ids = append(ids, id)
	}
	ctx, cancel := context.WithTimeout(u.ctx, 3*time.Second)
	defer cancel()
	if err := u.client.Call(ctx, "state.get", map[string]any{"offsets": u.offsets, "ids": ids}, &next); err != nil {
		return false, err
	}
	changed := !reflect.DeepEqual(u.snapshot, next)
	u.snapshot = next
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
	if len(next.Permissions) > 0 && next.Permissions[0].ID != u.lastPermission {
		u.lastPermission = next.Permissions[0].ID
		// Do not reinterpret buffered paste as a permission decision.
		u.pendingPaste = ""
	}
	return changed, nil
}
func (u *ui) current() *model.Pane {
	if u.focus < 0 || u.focus >= len(u.snapshot.Panes) {
		return nil
	}
	return &u.snapshot.Panes[u.focus]
}
func (u *ui) spawn(command []string) bool {
	if len(command) == 0 {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/zsh"
		}
		command = []string{shell}
	}
	var pane model.Pane
	if u.call("pane.spawn", model.SpawnParams{Backend: "pty", Command: command, Cwd: u.cwd, Cols: 80, Rows: 24}, &pane) {
		u.focus = len(u.snapshot.Panes)
		u.status = "已新建终端 " + pane.Name
		return true
	}
	return false
}
func (u *ui) send(data string) {
	p := u.current()
	if p == nil || p.Backend != "pty" || data == "" {
		return
	}
	u.offsets[p.ID] = 0
	u.call("pane.input", map[string]string{"id": p.ID, "data": data}, nil)
}
func (u *ui) handlePaste(data string) {
	u.selection = nil
	if u.taskView || u.help || u.pendingPaste != "" {
		return
	}
	if len(u.snapshot.Permissions) > 0 {
		u.status = "权限请求期间已忽略粘贴，请逐键选择"
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
	if p.Backend == "acp" {
		u.openInput("task", data)
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
	if len(u.snapshot.Permissions) > 0 {
		p := u.snapshot.Permissions[0]
		option := ""
		if k != tcell.KeyEscape {
			if len(s) != 1 || s[0] < '1' || s[0] > '9' || int(s[0]-'1') >= len(p.Options) {
				return false
			}
			option = p.Options[int(s[0]-'1')].ID
		}
		u.call("permission.reply", map[string]string{"id": p.ID, "option_id": option}, nil)
		_, _ = u.refresh()
		return false
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
	if u.taskView {
		u.taskKey(e)
		return false
	}
	if u.mode != "" {
		u.edit(e)
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
		switch s {
		case "?":
			u.help = true
		case "a":
			u.send("\x01")
		case "n":
			u.spawn(nil)
			_, _ = u.refresh()
			u.resize()
		case "c":
			u.openInput("command", "")
		case "o":
			if len(u.snapshot.Panes) > 0 {
				u.focus = (u.focus + 1) % len(u.snapshot.Panes)
			}
			u.resize()
		case "s":
			u.layout = (u.layout + 1) % 3
			u.resize()
		case "z":
			u.zoom = !u.zoom
			u.resize()
		case "b":
			u.tasks = !u.tasks
			u.resize()
		case "[":
			u.copyMode = true
		case "t":
			u.openInput("task", "")
		case "g":
			u.openInput("edge", "")
		case "r":
			u.openInput("unedge", "")
		case "k":
			u.openTasks()
		case "R":
			u.restartCurrent()
		case "x":
			u.openInput("stop", "")
		case "d", "q":
			return true
		default:
			if k == tcell.KeyCtrlA {
				u.send("\x01")
			}
		}
		return false
	}
	if k == tcell.KeyCtrlA || (s == "a" && e.Modifiers()&tcell.ModCtrl != 0) {
		u.prefix = true
		return false
	}
	p := u.current()
	if p != nil && restartable(p.State) && k == tcell.KeyEnter {
		u.restartCurrent()
		return false
	}
	if p != nil && p.Backend == "acp" {
		if k == tcell.KeyEnter || (k == tcell.KeyRune && s != "") {
			u.openInput("task", s)
		}
		return false
	}
	app := p != nil && p.Frame != nil && p.Frame.AppCursor
	u.send(encodeKey(e, app))
	return false
}
func (u *ui) openInput(mode, initial string) {
	u.mode = mode
	u.editor.Begin(mode, initial)
	u.selection = nil
}
func (u *ui) edit(e *tcell.EventKey) {
	if e.Key() == tcell.KeyEscape {
		u.mode = ""
		u.editor.Set("")
		return
	}
	// Legacy ESC+Return is reported by tcell as Alt+Ctrl+M; CSI-u uses Enter.
	altEnter := e.Modifiers()&tcell.ModAlt != 0 && (e.Key() == tcell.KeyEnter || (e.Key() == tcell.KeyRune && e.Modifiers()&tcell.ModCtrl != 0 && e.Str() == "m"))
	if u.mode == "task" && altEnter {
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
			if !u.spawn([]string{"/bin/zsh", "-lc", input}) {
				return
			}
			_, _ = u.refresh()
			u.resize()
		case "task":
			p := u.current()
			if p == nil || strings.TrimSpace(input) == "" {
				u.status = "请输入任务内容"
				return
			}
			var task model.Task
			if !u.call("task.send", map[string]string{"to": p.ID, "prompt": input}, &task) {
				return
			}
			u.status = "任务已排队 " + shortTask(task.ID) + " → " + p.Name
			if p.Backend == "pty" {
				u.status += " · PTY Agent 需主动领取"
			}
			u.tasks = true
			_, _ = u.refresh()
			u.resize()
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
			if !u.call(method, map[string]string{"from": fields[0], "to": fields[1]}, nil) {
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
	u.copyText(b.String(), "已复制当前可见屏")
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
