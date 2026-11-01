package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wtable/internal/model"
	"wtable/internal/store"
)

func TestMigrateWorkspacesBackfillsLayerFirst(t *testing.T) {
	repo := t.TempDir()
	other := t.TempDir()
	worktree := filepath.Join(t.TempDir(), "worktrees", "abc", "feature")
	t1 := time.Now().Add(-2 * time.Hour)
	t2 := time.Now().Add(-time.Hour)
	t3 := time.Now()
	state := store.State{
		Version: 1,
		Layers:  []model.Layer{{Name: "feature", Repo: repo, Path: worktree, Branch: "wtable/layer/feature"}},
		Panes: []model.Pane{
			{ID: "a", Name: "a", Cwd: repo, CreatedAt: t1},
			{ID: "b", Name: "b", Cwd: filepath.Join(worktree, "sub"), Layer: worktree, CreatedAt: t2},
			{ID: "c", Name: "c", Cwd: other, CreatedAt: t3},
		},
		Tasks: []model.Task{
			{ID: "t1", To: "a"},
			{ID: "t2", To: "b"},
			{ID: "t3", ParentTask: "t1"},
		},
		LastFocus: "b",
	}
	migrateWorkspaces(&state)
	if state.Layers[0].Workspace != repo {
		t.Fatalf("layer workspace = %q", state.Layers[0].Workspace)
	}
	if state.Panes[0].Workspace != repo || state.Panes[1].Workspace != repo || state.Panes[2].Workspace != other {
		t.Fatalf("pane workspaces = %q %q %q", state.Panes[0].Workspace, state.Panes[1].Workspace, state.Panes[2].Workspace)
	}
	repoWS := stateWorkspace(&state, repo)
	otherWS := stateWorkspace(&state, other)
	if repoWS == nil || otherWS == nil {
		t.Fatalf("workspace registry = %+v", state.Workspaces)
	}
	if !repoWS.LastOpened.Equal(t2) || !otherWS.LastOpened.Equal(t3) {
		t.Fatalf("last opened = %v %v", repoWS.LastOpened, otherWS.LastOpened)
	}
	if repoWS.LastFocus != "b" {
		t.Fatalf("workspace focus = %q", repoWS.LastFocus)
	}
	for _, id := range []string{"t1", "t2", "t3"} {
		for _, task := range state.Tasks {
			if task.ID == id && task.Workspace != repo {
				t.Fatalf("task %s workspace = %q", id, task.Workspace)
			}
		}
	}
	before := len(state.Workspaces)
	migrateWorkspaces(&state)
	if len(state.Workspaces) != before {
		t.Fatal("migration is not idempotent")
	}
}

func TestSpawnScopesInvariantsPerWorkspace(t *testing.T) {
	s := onboardingServer(t)
	wsA, wsB := t.TempDir(), t.TempDir()
	spawn := func(name, workspace string, kind string, connect ...string) (model.Pane, error) {
		return s.spawn(model.SpawnParams{Name: name, Kind: kind, Backend: "pty", Cwd: workspace, Workspace: workspace, Command: []string{"/bin/cat"}, ConnectTo: connect}, "")
	}
	if _, err := spawn("fg-a", wsA, model.PaneForeground); err != nil {
		t.Fatal(err)
	}
	fgB, err := spawn("fg-b", wsB, model.PaneForeground)
	if err != nil {
		t.Fatalf("second workspace foreground rejected: %v", err)
	}
	if _, err := spawn("fg-a2", wsA, model.PaneForeground); err == nil || !strings.Contains(err.Error(), "前台") {
		t.Fatalf("same-workspace foreground accepted: %v", err)
	}
	if _, err := spawn("dup", wsA, model.PaneWorker); err != nil {
		t.Fatal(err)
	}
	if _, err := spawn("dup", wsB, model.PaneWorker); err != nil {
		t.Fatalf("duplicate name across workspaces rejected: %v", err)
	}
	if _, err := spawn("dup", wsA, model.PaneWorker); err == nil {
		t.Fatal("duplicate name inside a workspace accepted")
	}
	if _, err := spawn("cross", wsA, model.PaneWorker, fgB.ID); err == nil || !strings.Contains(err.Error(), "工作区") {
		t.Fatalf("cross-workspace connect accepted: %v", err)
	}
	s.mu.Lock()
	for i := 0; i < 32; i++ {
		s.state.Panes = append(s.state.Panes, model.Pane{ID: newID("p-"), Name: newID("full-"), Workspace: wsA, Cwd: wsA, State: "exited"})
	}
	s.mu.Unlock()
	if _, err := spawn("overflow", wsA, model.PaneWorker); err == nil || !strings.Contains(err.Error(), "32") {
		t.Fatalf("per-workspace cap not enforced: %v", err)
	}
}

func TestResolvePaneScopedAndExactID(t *testing.T) {
	s := onboardingServer(t)
	wsA, wsB := t.TempDir(), t.TempDir()
	s.state.Panes = []model.Pane{
		{ID: "a", Name: "codex", Workspace: wsA},
		{ID: "b", Name: "codex", Workspace: wsB},
	}
	if p, err := s.resolvePaneLocked(wsA, "codex"); err != nil || p.ID != "a" {
		t.Fatalf("scoped name = %+v, %v", p, err)
	}
	if _, err := s.resolvePaneLocked("", "codex"); err == nil || !strings.Contains(err.Error(), "不唯一") {
		t.Fatalf("ambiguous name = %v", err)
	}
	if p, err := s.resolvePaneLocked(wsB, "b"); err != nil || p.ID != "b" {
		t.Fatalf("scoped exact ID = %+v, %v", p, err)
	}
	if p, err := s.resolvePaneLocked("", "b"); err != nil || p.ID != "b" {
		t.Fatalf("unscoped exact ID = %+v, %v", p, err)
	}
	if _, err := s.resolvePaneLocked(wsA, "b"); err == nil {
		t.Fatal("cross-workspace exact ID accepted")
	}
	if _, err := s.resolvePaneLocked(wsB, "a"); err == nil {
		t.Fatal("workspace mismatch accepted")
	}
}

func TestSnapshotScopesEveryCollection(t *testing.T) {
	s := onboardingServer(t)
	wsA, wsB := t.TempDir(), t.TempDir()
	s.state.Workspaces = []model.Workspace{
		{Path: wsA, LastOpened: time.Now().Add(-time.Hour), LastFocus: "a"},
		{Path: wsB, LastOpened: time.Now(), LastFocus: "b"},
	}
	s.state.Panes = []model.Pane{
		{ID: "a", Name: "A", Workspace: wsA, Cwd: wsA, State: "running"},
		{ID: "b", Name: "B", Workspace: wsB, Cwd: wsB, State: "running"},
	}
	s.state.Edges = []model.Edge{{From: "a", To: "a"}, {From: "b", To: "b"}}
	s.state.Tasks = []model.Task{
		{ID: "ta", From: "controller", To: "a", Workspace: wsA, State: "completed", ReportTo: "a", OnComplete: "notify"},
		{ID: "tb", From: "controller", To: "b", Workspace: wsB, State: "completed", ReportTo: "b", OnComplete: "notify"},
	}
	s.state.Reports = []model.Report{
		{ID: "ra", ParentTask: "ta", From: "a", To: "a", State: "delivered"},
		{ID: "rb", ParentTask: "tb", From: "b", To: "b", State: "delivered"},
	}
	s.state.Layers = []model.Layer{{Name: "la", Workspace: wsA}, {Name: "lb", Workspace: wsB}}
	s.approvals["pa"] = &approval{permission: model.Permission{ID: "pa", PaneID: "a"}}
	s.approvals["pb"] = &approval{permission: model.Permission{ID: "pb", PaneID: "b"}}
	s.state.LastFocus = "b"

	snap, err := s.snapshot(rawTask(map[string]any{"workspace": wsA, "frames": false, "graph": true}))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Panes) != 1 || snap.Panes[0].ID != "a" || snap.Workspace != wsA || snap.LastFocus != "a" {
		t.Fatalf("scoped panes/focus = %+v", snap)
	}
	if len(snap.Tasks) != 1 || snap.Tasks[0].ID != "ta" {
		t.Fatalf("scoped tasks = %+v", snap.Tasks)
	}
	if len(snap.Edges) != 1 || snap.Edges[0].To != "a" {
		t.Fatalf("scoped edges = %+v", snap.Edges)
	}
	if len(snap.Reports) != 1 || snap.Reports[0].ID != "ra" {
		t.Fatalf("scoped reports = %+v", snap.Reports)
	}
	if len(snap.Layers) != 1 || snap.Layers[0].Name != "la" {
		t.Fatalf("scoped layers = %+v", snap.Layers)
	}
	if len(snap.Permissions) != 1 || snap.Permissions[0].ID != "pa" {
		t.Fatalf("scoped permissions = %+v", snap.Permissions)
	}
	if snap.Graph == nil {
		t.Fatal("missing graph")
	}
	hasA, hasB := false, false
	for _, node := range snap.Graph.Nodes {
		hasA = hasA || node.ID == "a"
		hasB = hasB || node.ID == "b"
	}
	if !hasA || hasB {
		t.Fatalf("scoped graph = %+v", snap.Graph)
	}
	if len(snap.Workspaces) != 2 || snap.Workspaces[0].Path != wsB {
		t.Fatalf("workspace listing = %+v", snap.Workspaces)
	}
	all, err := s.snapshot(rawTask(map[string]any{"frames": false}))
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Panes) != 2 || len(all.Tasks) != 2 || len(all.Edges) != 2 || len(all.Reports) != 2 || len(all.Layers) != 2 || len(all.Permissions) != 2 {
		t.Fatalf("unscoped snapshot changed: %+v", all)
	}
}

func TestWorkspaceOpenRecentsFocusAndMissing(t *testing.T) {
	s := onboardingServer(t)
	wsA, wsB := t.TempDir(), t.TempDir()
	s.state.Workspaces = []model.Workspace{
		{Path: wsA, LastOpened: time.Now().Add(-time.Hour), LastFocus: "a"},
		{Path: wsB, LastOpened: time.Now().Add(-2 * time.Hour), LastFocus: "b"},
	}
	s.state.Panes = []model.Pane{
		{ID: "a", Workspace: wsA, Cwd: wsA, State: "exited"},
		{ID: "b", Workspace: wsB, Cwd: wsB, State: "exited"},
	}
	s.state.Tasks = []model.Task{{ID: "done", To: "b", Workspace: wsB, State: "completed", Seen: false}}
	s.state.Reports = []model.Report{{ID: "r", ParentTask: "done", From: "b", To: "b", Seen: false}}
	if _, err := s.openWorkspace(wsB); err != nil {
		t.Fatal(err)
	}
	if s.state.LastFocus != "b" {
		t.Fatalf("global focus = %q", s.state.LastFocus)
	}
	if s.state.Tasks[0].Seen || s.state.Reports[0].Seen {
		t.Fatal("opening a workspace must not acknowledge notices")
	}
	list := s.workspaceList()
	if len(list) != 2 || list[0].Path != wsB || list[1].Path != wsA {
		t.Fatalf("recents = %+v", list)
	}
	if _, err := s.openWorkspace("relative/path"); err == nil {
		t.Fatal("relative workspace accepted")
	}
	if _, err := s.openWorkspace(filepath.Join(wsA, "missing")); err == nil {
		t.Fatal("missing unregistered workspace accepted")
	}
	if err := os.RemoveAll(wsA); err != nil {
		t.Fatal(err)
	}
	info, err := s.openWorkspace(wsA)
	if err != nil || !info.Missing {
		t.Fatalf("registered missing workspace = %+v, %v", info, err)
	}
}

func TestRemovePaneClearsFocusAndKeepsTaskScope(t *testing.T) {
	s := onboardingServer(t)
	ws := t.TempDir()
	s.state.Workspaces = []model.Workspace{{Path: ws, LastFocus: "p"}}
	s.state.Panes = []model.Pane{{ID: "p", Name: "p", Workspace: ws, Cwd: ws, State: "exited"}}
	s.state.LastFocus = "p"
	s.state.Tasks = []model.Task{{ID: "t", To: "p", Workspace: ws, State: "completed"}}
	if err := s.removePane("p", ws); err != nil {
		t.Fatal(err)
	}
	if s.state.LastFocus != "" || s.state.Workspaces[0].LastFocus != "" {
		t.Fatalf("focus survived removal: %q %q", s.state.LastFocus, s.state.Workspaces[0].LastFocus)
	}
	snap, err := s.snapshot(rawTask(map[string]any{"workspace": ws, "frames": false}))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Tasks) != 1 || snap.Tasks[0].ID != "t" {
		t.Fatalf("task history left the workspace: %+v", snap.Tasks)
	}
}
