package server

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"wtable/internal/model"
	"wtable/internal/store"
)

// cleanWorkspacePath accepts only absolute, already-resolved paths. The daemon
// has its own cwd, so silently calling filepath.Abs here would resolve against
// the wrong directory; callers (CLI/TUI) must resolve relative input first.
func cleanWorkspacePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("需要工作区目录")
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("工作区必须是绝对路径")
	}
	path = filepath.Clean(path)
	if !validMetadata(path, 4096) {
		return "", errors.New("工作区路径最多 4096 字节，且不得包含控制字符")
	}
	return path, nil
}

// resolveSpawnLocation decides which workspace a new/restarted pane belongs to
// and validates both the workspace and the process cwd. A pane's cwd may live
// in a layer worktree outside the workspace, so it is never required to be
// contained by it.
func (s *Server) resolveSpawnLocation(args *model.SpawnParams) error {
	if args.Workspace != "" {
		path, err := cleanWorkspacePath(args.Workspace)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("工作区不是目录")
		}
		args.Workspace = path
	} else if args.Layer != "" {
		s.mu.Lock()
		for i := range s.state.Layers {
			if l := &s.state.Layers[i]; l.Path == args.Layer && l.Workspace != "" {
				args.Workspace = l.Workspace
				break
			}
		}
		s.mu.Unlock()
	}
	if args.Cwd == "" && args.Workspace != "" {
		args.Cwd = args.Workspace
	}
	if args.Cwd == "" {
		return errors.New("需要工作目录")
	}
	cwd, err := filepath.Abs(args.Cwd)
	if err != nil {
		return err
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("cwd 不是目录")
	}
	if args.Workspace == "" {
		path, err := cleanWorkspacePath(cwd)
		if err != nil {
			return err
		}
		args.Workspace = path
	}
	args.Cwd = cwd
	return nil
}

func isWithin(path, root string) bool {
	if path == root {
		return true
	}
	if root == string(filepath.Separator) {
		return strings.HasPrefix(path, root)
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

func stateWorkspace(state *store.State, path string) *model.Workspace {
	for i := range state.Workspaces {
		if state.Workspaces[i].Path == path {
			return &state.Workspaces[i]
		}
	}
	return nil
}

func ensureStateWorkspace(state *store.State, path string, opened time.Time) *model.Workspace {
	if ws := stateWorkspace(state, path); ws != nil {
		return ws
	}
	state.Workspaces = append(state.Workspaces, model.Workspace{Path: path, LastOpened: opened})
	return &state.Workspaces[len(state.Workspaces)-1]
}

func (s *Server) workspaceIndexLocked(path string) int {
	for i := range s.state.Workspaces {
		if s.state.Workspaces[i].Path == path {
			return i
		}
	}
	return -1
}

func (s *Server) workspaceLocked(path string) *model.Workspace {
	if i := s.workspaceIndexLocked(path); i >= 0 {
		return &s.state.Workspaces[i]
	}
	return nil
}

func (s *Server) ensureWorkspaceLocked(path string, opened time.Time) *model.Workspace {
	if ws := s.workspaceLocked(path); ws != nil {
		return ws
	}
	s.state.Workspaces = append(s.state.Workspaces, model.Workspace{Path: path, LastOpened: opened})
	return &s.state.Workspaces[len(s.state.Workspaces)-1]
}

// workspaceFocusLocked returns the remembered focus only when it still points
// at a live pane in that workspace.
func (s *Server) workspaceFocusLocked(ws *model.Workspace) string {
	if ws == nil || ws.LastFocus == "" {
		return ""
	}
	p := s.paneByIDLocked(ws.LastFocus)
	if p == nil || p.Workspace != ws.Path {
		return ""
	}
	return p.ID
}

func (s *Server) workspacePaneCountLocked(path string) (total, running int) {
	for i := range s.state.Panes {
		p := &s.state.Panes[i]
		if p.Workspace != path {
			continue
		}
		total++
		if p.State == "running" || p.State == "starting" {
			running++
		}
	}
	return total, running
}

func (s *Server) workspaceInfoLocked(ws *model.Workspace, checkMissing bool) model.WorkspaceInfo {
	info := model.WorkspaceInfo{Path: ws.Path, LastOpened: ws.LastOpened, LastFocus: s.workspaceFocusLocked(ws)}
	info.PaneCount, info.RunningCount = s.workspacePaneCountLocked(ws.Path)
	if checkMissing {
		if st, err := os.Stat(ws.Path); err != nil || !st.IsDir() {
			info.Missing = true
		}
	}
	return info
}

// workspaceInfosLocked lists every registered workspace, most recently opened
// first. Zero timestamps sort last; path breaks ties so the picker is stable.
func (s *Server) workspaceInfosLocked(checkMissing bool) []model.WorkspaceInfo {
	infos := make([]model.WorkspaceInfo, 0, len(s.state.Workspaces))
	for i := range s.state.Workspaces {
		infos = append(infos, s.workspaceInfoLocked(&s.state.Workspaces[i], checkMissing))
	}
	sort.Slice(infos, func(i, j int) bool {
		a, b := infos[i], infos[j]
		aZero, bZero := a.LastOpened.IsZero(), b.LastOpened.IsZero()
		if aZero != bZero {
			return bZero
		}
		if !aZero && !a.LastOpened.Equal(b.LastOpened) {
			return a.LastOpened.After(b.LastOpened)
		}
		return a.Path < b.Path
	})
	return infos
}

func (s *Server) workspaceList() []model.WorkspaceInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workspaceInfosLocked(true)
}

// openWorkspace registers/touches a workspace and restores its remembered
// focus. It deliberately does not mark tasks or reports seen: switching
// workspaces must not consume unread notices.
func (s *Server) openWorkspace(path string) (model.WorkspaceInfo, error) {
	clean, err := cleanWorkspacePath(path)
	if err != nil {
		return model.WorkspaceInfo{}, err
	}
	st, statErr := os.Stat(clean)
	s.mu.Lock()
	defer s.mu.Unlock()
	ws := s.workspaceLocked(clean)
	if ws == nil {
		if statErr != nil || !st.IsDir() {
			return model.WorkspaceInfo{}, errors.New("工作区目录不存在或不是目录")
		}
		ws = s.ensureWorkspaceLocked(clean, time.Now())
	} else if statErr == nil && !st.IsDir() {
		return model.WorkspaceInfo{}, errors.New("工作区路径不是目录")
	}
	ws.LastOpened = time.Now()
	ws.LastFocus = s.workspaceFocusLocked(ws)
	s.state.LastFocus = ws.LastFocus
	if err = s.saveLocked(); err != nil {
		return model.WorkspaceInfo{}, err
	}
	return s.workspaceInfoLocked(ws, true), nil
}

// migrateWorkspaces upgrades state written before workspaces were first-class.
// Layers are backfilled first so panes living in worktrees inherit the owning
// workspace instead of splitting into one workspace per worktree.
func migrateWorkspaces(state *store.State) {
	if state.WorkspacesMigrated {
		return
	}
	for i := range state.Layers {
		l := &state.Layers[i]
		if l.Workspace != "" || l.Repo == "" {
			continue
		}
		if path, err := cleanWorkspacePath(l.Repo); err == nil {
			l.Workspace = path
		}
	}
	for i := range state.Panes {
		p := &state.Panes[i]
		if p.Workspace != "" {
			continue
		}
		ws := ""
		for _, l := range state.Layers {
			if l.Workspace == "" {
				continue
			}
			if p.Layer != "" && (p.Layer == l.Path || p.Layer == l.Name) {
				ws = l.Workspace
				break
			}
		}
		if ws == "" && p.Cwd != "" {
			best := -1
			for _, l := range state.Layers {
				if l.Workspace == "" || l.Path == "" || !isWithin(p.Cwd, l.Path) {
					continue
				}
				if len(l.Path) > best {
					best, ws = len(l.Path), l.Workspace
				}
			}
		}
		if ws == "" && p.Cwd != "" {
			if path, err := cleanWorkspacePath(p.Cwd); err == nil {
				ws = path
			}
		}
		p.Workspace = ws
	}
	latest := map[string]time.Time{}
	for i := range state.Panes {
		p := &state.Panes[i]
		if p.Workspace == "" {
			continue
		}
		if p.CreatedAt.After(latest[p.Workspace]) {
			latest[p.Workspace] = p.CreatedAt
		}
	}
	for i := range state.Panes {
		if p := &state.Panes[i]; p.Workspace != "" {
			ensureStateWorkspace(state, p.Workspace, latest[p.Workspace])
		}
	}
	for i := range state.Layers {
		if l := &state.Layers[i]; l.Workspace != "" {
			ensureStateWorkspace(state, l.Workspace, time.Time{})
		}
	}
	for i := range state.Panes {
		p := &state.Panes[i]
		if p.ID != state.LastFocus || p.Workspace == "" {
			continue
		}
		if ws := stateWorkspace(state, p.Workspace); ws != nil {
			ws.LastFocus = p.ID
		}
	}
	paneWorkspace := map[string]string{}
	for i := range state.Panes {
		paneWorkspace[state.Panes[i].ID] = state.Panes[i].Workspace
	}
	taskWorkspace := map[string]string{}
	for i := range state.Tasks {
		t := &state.Tasks[i]
		if t.Workspace == "" {
			switch {
			case paneWorkspace[t.To] != "":
				t.Workspace = paneWorkspace[t.To]
			case paneWorkspace[t.From] != "":
				t.Workspace = paneWorkspace[t.From]
			}
		}
		taskWorkspace[t.ID] = t.Workspace
	}
	for i := range state.Tasks {
		t := &state.Tasks[i]
		if t.Workspace == "" && t.ParentTask != "" {
			t.Workspace = taskWorkspace[t.ParentTask]
		}
	}
	state.WorkspacesMigrated = true
}
