package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"wtable/internal/agent"
	"wtable/internal/config"
	"wtable/internal/model"
	"wtable/internal/rpc"
	"wtable/internal/store"
	"wtable/internal/terminal"
)

type approval struct {
	permission model.Permission
	answer     chan string
}

type Server struct {
	mu          sync.Mutex
	inputLanes  sync.Map
	paths       config.Paths
	db          *store.Store
	state       store.State
	token       string
	peerTokens  map[string]string
	runs        map[string]string
	terminals   map[string]*terminal.Terminal
	agents      map[string]*agent.Client
	approvals   map[string]*approval
	ctx         context.Context
	cancel      context.CancelFunc
	revision    atomic.Uint64
	writer      uint64
	storeErr    error
	connections map[net.Conn]struct{}
	paneCancels map[string]context.CancelFunc
	taskCancels map[string]context.CancelFunc
	closing     bool
}

type connection struct {
	id              uint64
	principal, hash string
}

func Run(ctx context.Context, paths config.Paths) error {
	if err := paths.Prepare(); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(paths.Home, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("daemon 已在运行")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	token, err := paths.EnsureToken()
	if err != nil {
		return err
	}
	db, err := store.Open(paths.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	state, err := db.Load()
	if err != nil {
		return err
	}
	for i := range state.Panes {
		p := &state.Panes[i]
		p.Frame = nil
		if p.State == "running" || p.State == "starting" || p.State == "stopping" {
			p.State = "interrupted"
			p.TurnState = "unknown"
			p.Error = "daemon 已重启；请显式重启此 pane"
		}
	}
	for i := range state.Tasks {
		if activeState(state.Tasks[i].State) {
			state.Tasks[i].State = "unknown"
			state.Tasks[i].Result = "执行结果未确认；不会自动重投"
			state.Tasks[i].UpdatedAt = time.Now()
		}
	}
	if err = db.Save(state); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &Server{paths: paths, db: db, state: state, token: token, peerTokens: map[string]string{}, runs: map[string]string{}, terminals: map[string]*terminal.Terminal{}, agents: map[string]*agent.Client{}, approvals: map[string]*approval{}, ctx: ctx, cancel: cancel, connections: map[net.Conn]struct{}{}, paneCancels: map[string]context.CancelFunc{}, taskCancels: map[string]context.CancelFunc{}}
	if err = os.Remove(paths.Socket); err != nil && !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", paths.Socket)
	if err != nil {
		return err
	}
	go s.deliveryLoop()
	defer listener.Close()
	defer os.Remove(paths.Socket)
	if err = os.Chmod(paths.Socket, 0600); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		listener.Close()
		s.mu.Lock()
		for c := range s.connections {
			c.Close()
		}
		s.mu.Unlock()
	}()
	var wg sync.WaitGroup
	var next uint64
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			return err
		}
		next++
		s.mu.Lock()
		s.connections[conn] = struct{}{}
		s.mu.Unlock()
		wg.Add(1)
		go func(id uint64) { defer wg.Done(); s.serve(conn, id) }(next)
	}
	wg.Wait()
	s.mu.Lock()
	s.closing = true
	for i := range s.state.Panes {
		p := &s.state.Panes[i]
		if p.State == "running" || p.State == "starting" || p.State == "stopping" {
			p.State = "interrupted"
			p.TurnState = "unknown"
		}
	}
	for i := range s.state.Tasks {
		t := &s.state.Tasks[i]
		if activeState(t.State) {
			t.State = "unknown"
			t.Result = "daemon 已停止，执行结果未确认"
			t.UpdatedAt = time.Now()
		}
	}
	terms := make([]*terminal.Terminal, 0, len(s.terminals))
	for _, t := range s.terminals {
		terms = append(terms, t)
	}
	agents := make([]*agent.Client, 0, len(s.agents))
	for _, a := range s.agents {
		agents = append(agents, a)
	}
	s.mu.Unlock()
	for _, a := range agents {
		a.Close()
	}
	for _, t := range terms {
		t.Close()
	}
	s.mu.Lock()
	err = s.saveLocked()
	s.mu.Unlock()
	return err
}

func (s *Server) serve(conn net.Conn, id uint64) {
	defer func() {
		conn.Close()
		s.mu.Lock()
		delete(s.connections, conn)
		if s.writer == id {
			s.writer = 0
		}
		s.mu.Unlock()
	}()
	reader := bufio.NewScanner(conn)
	reader.Buffer(make([]byte, 4096), rpc.MaxFrame)
	c := connection{id: id}
	for reader.Scan() {
		var req rpc.Request
		if err := json.Unmarshal(reader.Bytes(), &req); err != nil {
			s.respond(conn, req.ID, nil, errors.New("无效 JSON 请求"))
			return
		}
		if req.JSONRPC != "2.0" || req.ID == 0 {
			s.respond(conn, req.ID, nil, errors.New("需要 JSON-RPC 2.0 和非零请求 ID"))
			return
		}
		if c.principal == "" {
			var a struct {
				Token string `json:"token"`
			}
			if req.Method != "auth" || json.Unmarshal(req.Params, &a) != nil {
				s.respond(conn, req.ID, nil, errors.New("需要先鉴权"))
				return
			}
			s.mu.Lock()
			if subtle.ConstantTimeCompare([]byte(a.Token), []byte(s.token)) == 1 {
				c.principal = "controller"
			} else {
				c.hash = hash(a.Token)
				for pid, h := range s.peerTokens {
					if subtle.ConstantTimeCompare([]byte(h), []byte(c.hash)) == 1 {
						c.principal = pid
						break
					}
				}
			}
			s.mu.Unlock()
			if c.principal == "" {
				s.respond(conn, req.ID, nil, errors.New("鉴权失败"))
				return
			}
			if !s.respond(conn, req.ID, map[string]any{"version": 1, "principal": c.principal}, nil) {
				return
			}
			continue
		}
		s.mu.Lock()
		valid := c.principal == "controller" || s.peerTokens[c.principal] == c.hash
		storeErr := s.storeErr
		s.mu.Unlock()
		if !valid {
			s.respond(conn, req.ID, nil, errors.New("pane 凭证已失效"))
			return
		}
		if storeErr != nil && !contains([]string{"diag.info", "state.get", "pane.stop", "shutdown", "task.cancel", "permission.reply"}, req.Method) {
			s.respond(conn, req.ID, nil, fmt.Errorf("状态无法持久化，已停止新操作：%w", storeErr))
			continue
		}
		result, err := s.dispatch(c, req.Method, req.Params)
		if !s.respond(conn, req.ID, result, err) {
			return
		}
	}
}

func (s *Server) respond(conn net.Conn, id uint64, result any, err error) bool {
	res := rpc.Response{JSONRPC: "2.0", ID: id}
	if err != nil {
		res.Error = &rpc.Error{Code: -32000, Message: err.Error()}
	} else {
		res.Result, err = json.Marshal(result)
		if err != nil {
			res.Error = &rpc.Error{Code: -32603, Message: err.Error()}
		}
	}
	data, err := json.Marshal(res)
	if err != nil {
		return false
	}
	if len(data) >= rpc.MaxFrame {
		data, _ = json.Marshal(rpc.Response{JSONRPC: "2.0", ID: id, Error: &rpc.Error{Code: -32000, Message: "响应过大，请缩小终端尺寸或只读取指定 pane"}})
	}
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err = conn.Write(append(data, '\n'))
	return err == nil
}

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	return json.Unmarshal(raw, v)
}
func hash(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func newID(prefix string) string {
	secret, err := config.Secret()
	if err != nil {
		panic(err)
	}
	return prefix + secret[:12]
}

func (s *Server) saveLocked() error {
	s.revision.Add(1)
	err := s.db.Save(s.state)
	if err != nil {
		s.storeErr = err
		log.Printf("state persistence failed: %v", err)
	}
	return err
}

func clonePane(p model.Pane) model.Pane {
	p.Command = append([]string{}, p.Command...)
	p.Messages = append([]model.Message{}, p.Messages...)
	return p
}

func (s *Server) paneLocked(ref string) *model.Pane {
	for i := range s.state.Panes {
		p := &s.state.Panes[i]
		if p.ID == ref || p.Name == ref {
			return p
		}
	}
	return nil
}
func (s *Server) taskLocked(id string) *model.Task {
	for i := range s.state.Tasks {
		if s.state.Tasks[i].ID == id {
			return &s.state.Tasks[i]
		}
	}
	return nil
}
func (s *Server) hasEdgeLocked(from, to string) bool {
	for _, e := range s.state.Edges {
		if e.From == from && e.To == to {
			return true
		}
	}
	return false
}

func (s *Server) snapshot(raw json.RawMessage) (model.Snapshot, error) {
	var args struct {
		Offsets map[string]int `json:"offsets"`
		IDs     []string       `json:"ids"`
		Frames  *bool          `json:"frames"`
	}
	if err := decode(raw, &args); err != nil {
		return model.Snapshot{}, err
	}
	s.mu.Lock()
	result := model.Snapshot{Revision: s.revision.Load(), Edges: append([]model.Edge{}, s.state.Edges...), Reports: append([]model.Report{}, s.state.Reports...), Layers: append([]model.Layer{}, s.state.Layers...)}
	terms := map[string]*terminal.Terminal{}
	for _, p := range s.state.Panes {
		result.Panes = append(result.Panes, clonePane(p))
		if t := s.terminals[p.ID]; t != nil {
			terms[p.ID] = t
		}
	}
	for _, t := range s.state.Tasks {
		t.Prompt = short(t.Prompt, 300)
		t.Result = short(t.Result, 500)
		result.Tasks = append(result.Tasks, t)
	}
	for _, a := range s.approvals {
		result.Permissions = append(result.Permissions, a.permission)
	}
	s.mu.Unlock()
	if args.Frames == nil || *args.Frames {
		for i := range result.Panes {
			p := &result.Panes[i]
			if len(args.IDs) > 0 && !contains(args.IDs, p.ID) {
				continue
			}
			if t := terms[p.ID]; t != nil {
				f := t.Snapshot(max(0, min(10000, args.Offsets[p.ID])))
				p.Frame = &f
			}
		}
	}
	return result, nil
}

func short(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
func contains(values []string, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}

func (s *Server) dispatch(c connection, method string, raw json.RawMessage) (any, error) {
	controller := c.principal == "controller"
	if !controller && !contains([]string{"diag.info", "peer.list", "task.send", "task.next", "task.result", "task.ack", "task.get", "task.list", "task.seen", "report.list", "report.get", "report.seen"}, method) {
		return nil, errors.New("此操作需要人类控制端权限")
	}
	switch method {
	case "diag.info":
		if !controller {
			return map[string]string{"version": config.Version}, nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return map[string]any{"pid": os.Getpid(), "home": s.paths.Home, "panes": len(s.state.Panes), "tasks": len(s.state.Tasks), "version": config.Version, "store_error": fmt.Sprint(s.storeErr)}, nil
	case "ui.attach":
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.writer != 0 && s.writer != c.id {
			return nil, errors.New("已有可写 TUI；请先在另一窗口 detach")
		}
		s.writer = c.id
		return true, nil
	case "ui.detach":
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.writer == c.id {
			s.writer = 0
		}
		return true, nil
	case "state.get":
		return s.snapshot(raw)
	case "pane.spawn":
		var p model.SpawnParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return s.spawn(p, "")
	case "pane.restart":
		var a struct {
			ID string `json:"id"`
		}
		if err := decode(raw, &a); err != nil {
			return nil, err
		}
		s.mu.Lock()
		p := s.paneLocked(a.ID)
		if p == nil {
			s.mu.Unlock()
			return nil, errors.New("pane 不存在")
		}
		copy := clonePane(*p)
		s.mu.Unlock()
		return s.spawn(model.SpawnParams{Name: copy.Name, Backend: copy.Backend, Command: copy.Command, Cwd: copy.Cwd, DeliveryConfig: copy.DeliveryConfig}, copy.ID)
	case "pane.stop":
		var a struct {
			ID string `json:"id"`
		}
		if err := decode(raw, &a); err != nil {
			return nil, err
		}
		return true, s.stop(a.ID)
	case "pane.input":
		var a struct {
			ID   string `json:"id"`
			Data string `json:"data"`
		}
		if err := decode(raw, &a); err != nil {
			return nil, err
		}
		if len(a.Data) > 1<<20 {
			return nil, errors.New("输入超过 1 MiB")
		}
		s.mu.Lock()
		t := s.terminals[a.ID]
		writer := s.writer
		s.mu.Unlock()
		if writer != 0 && writer != c.id {
			return nil, errors.New("输入由当前 TUI 持有")
		}
		if t == nil {
			return nil, errors.New("pane 没有运行中的 PTY")
		}
		lane := s.inputLane(a.ID)
		lane.Lock()
		defer lane.Unlock()
		return true, t.Input([]byte(a.Data))
	case "pane.resize":
		var a struct {
			ID   string `json:"id"`
			Cols int    `json:"cols"`
			Rows int    `json:"rows"`
		}
		if err := decode(raw, &a); err != nil {
			return nil, err
		}
		s.mu.Lock()
		t := s.terminals[a.ID]
		writer := s.writer
		s.mu.Unlock()
		if writer != 0 && writer != c.id {
			return nil, errors.New("尺寸由当前 TUI 持有")
		}
		if a.Cols < 2 || a.Cols > 240 || a.Rows < 1 || a.Rows > 100 {
			return nil, errors.New("pane 尺寸范围为 2–240 列、1–100 行")
		}
		if t == nil {
			return true, nil
		}
		return true, t.Resize(a.Cols, a.Rows)
	case "pane.screen":
		var a struct {
			ID     string `json:"id"`
			Offset int    `json:"offset"`
		}
		if err := decode(raw, &a); err != nil {
			return nil, err
		}
		s.mu.Lock()
		p := s.paneLocked(a.ID)
		var t *terminal.Terminal
		if p != nil {
			t = s.terminals[p.ID]
		}
		s.mu.Unlock()
		if t == nil {
			return nil, errors.New("没有可读取的终端画面")
		}
		return t.Snapshot(max(0, min(10000, a.Offset))), nil
	case "edge.add", "edge.remove":
		return s.edge(method, raw)
	case "peer.list":
		return s.peers(c.principal), nil
	case "task.send":
		return s.sendTask(c.principal, raw)
	case "task.next":
		return s.nextTask(c.principal, raw)
	case "task.result":
		return s.taskResult(c.principal, raw)
	case "task.ack", "task.confirm", "task.seen":
		return s.taskMark(c.principal, method, raw)
	case "task.done":
		return s.finishTask(c.principal, raw, true)
	case "task.retry":
		return s.retryTask(raw)
	case "report.list", "report.get", "report.seen":
		return s.reports(c.principal, method, raw)
	case "task.get", "task.list":
		return s.getTasks(c.principal, method, raw)
	case "task.cancel":
		return s.cancelTask(raw)
	case "permission.reply":
		return true, s.replyPermission(raw)
	case "layer.create":
		return s.createLayer(raw)
	case "git.changes", "git.diff":
		return s.gitRead(method, raw)
	case "shutdown":
		go func() { time.Sleep(50 * time.Millisecond); s.cancel() }()
		return true, nil
	default:
		return nil, fmt.Errorf("未知方法：%s", method)
	}
}
