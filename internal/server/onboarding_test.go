package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wtable/internal/config"
	"wtable/internal/model"
)

func onboardingServer(t *testing.T) *Server {
	s := taskTestServer(t)
	home := t.TempDir()
	s.paths = config.Paths{Home: home, Log: filepath.Join(home, "daemon.log")}
	s.runs = map[string]string{}
	s.peerTokens = map[string]string{}
	s.paneCancels = map[string]context.CancelFunc{}
	s.taskCancels = map[string]context.CancelFunc{}
	s.approvals = map[string]*approval{}
	return s
}

func writeProfiles(t *testing.T, s *Server, profiles []accountProfile) {
	t.Helper()
	data, _ := json.Marshal(profiles)
	if err := os.WriteFile(filepath.Join(s.paths.Home, "accounts.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestOnboardingProfilesAndCatalogPrivacy(t *testing.T) {
	s := onboardingServer(t)
	writeProfiles(t, s, []accountProfile{{Name: "work", Env: map[string]string{"WTABLE_PEER_TOKEN": "do-not-expose"}}})
	if _, err := s.accountProfiles(); err == nil || strings.Contains(err.Error(), "do-not-expose") {
		t.Fatalf("reserved override not safely rejected: %v", err)
	}
	writeProfiles(t, s, []accountProfile{{Name: "work", Env: map[string]string{"AGENT_SECRET": "do-not-expose"}}})
	// Keep discovery deterministic; do not execute any installed model clients.
	t.Setenv("PATH", t.TempDir())
	catalog, err := s.catalog()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(catalog)
	if strings.Contains(string(data), "do-not-expose") || strings.Contains(string(data), "AGENT_SECRET") {
		t.Fatal("catalog exposes profile environment")
	}
	if len(catalog.Accounts) != 1 || catalog.Accounts[0] != "work" {
		t.Fatal(catalog.Accounts)
	}
	if len(catalog.Presets) != 5 || catalog.Presets[2].ID != "codex-acp" || catalog.Presets[2].Available || !strings.Contains(catalog.Presets[2].Detail, "单独安装") {
		t.Fatalf("missing adapter must be explained: %+v", catalog.Presets)
	}
}

func TestBriefContractPermissionsAndSize(t *testing.T) {
	s := onboardingServer(t)
	s.state.Panes = []model.Pane{{ID: "allowed", Name: "Allowed"}, {ID: "hidden", Name: "DO_NOT_INCLUDE"}}
	s.state.Edges = []model.Edge{{From: "worker", To: "allowed"}, {From: "hidden", To: "worker"}}
	p := model.Pane{ID: "worker", Name: "Worker", Backend: "pty", Cwd: "/tmp", Role: "Review"}
	text, err := s.briefTextLocked(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"allowed", " peers", " ack <task-id>", " result <task-id>", " next", "cannot recruit"} {
		if !strings.Contains(text, required) {
			t.Fatalf("brief missing %q", required)
		}
	}
	if strings.Contains(text, "DO_NOT_INCLUDE") || len(text) > briefLimit {
		t.Fatal("brief permission/size violation")
	}
	if err := s.writeBrief(&p, text); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p.BriefPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("brief permissions: %v %v", info, err)
	}
	p.Backend = "acp"
	text, err = s.briefTextLocked(p, nil)
	if err != nil || strings.Contains(text, " ack <task-id>") || !strings.Contains(text, "automatically") {
		t.Fatalf("ACP result contract: %s %v", text, err)
	}
	p.Cwd = strings.Repeat("x", briefLimit)
	if _, err := s.briefTextLocked(p, nil); err == nil {
		t.Fatal("oversize brief accepted")
	}
}

func TestSpawnBriefProfileAndDiagnostics(t *testing.T) {
	s := onboardingServer(t)
	secret := "private-profile-value-12345"
	writeProfiles(t, s, []accountProfile{{Name: "work", Env: map[string]string{"AGENT_SECRET": secret}}})
	s.state.Panes = []model.Pane{{ID: "peer", Name: "Peer", Backend: "pty", State: "running"}}
	// A local shell fixture emits its selected profile and brief path, then fails.
	p, err := s.spawn(model.SpawnParams{Name: "fixture", Role: "Review", Account: "work", Cwd: s.paths.Home, Command: []string{"/bin/sh", "-c", "printf '%s\\n' \"$AGENT_SECRET\" \"$WTABLE_BRIEF\"; exit 7"}, InjectBrief: true, ConnectTo: []string{"Peer"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		copy := clonePane(*s.paneLocked(p.ID))
		s.mu.Unlock()
		if copy.ExitCode != nil {
			if *copy.ExitCode != 7 || copy.State != "failed" || copy.LogPath != s.paths.Log {
				t.Fatalf("bad diagnostics: %+v", copy)
			}
			if strings.Contains(copy.ExitSummary, secret) || !strings.Contains(copy.ExitSummary, "[REDACTED]") || !strings.Contains(strings.ReplaceAll(copy.ExitSummary, "\n", ""), copy.BriefPath) {
				t.Fatalf("profile or brief environment missing / summary not redacted: %s", copy.ExitSummary)
			}
			data, _ := os.ReadFile(copy.BriefPath)
			if strings.Contains(string(data), secret) || !strings.Contains(string(data), "peer (Peer)") {
				t.Fatalf("brief credentials/initial peers: %s", data)
			}
			if !s.hasEdgeLocked(p.ID, "peer") {
				t.Fatal("initial edge missing")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture did not exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPaneUpdateRemoveAndControllerBoundary(t *testing.T) {
	s := onboardingServer(t)
	s.state.Panes = []model.Pane{{ID: "worker", Name: "Worker", Backend: "pty", State: "running", Cwd: s.paths.Home}, {ID: "peer", Name: "Peer", State: "running"}}
	s.state.Edges = []model.Edge{{From: "worker", To: "peer"}, {From: "peer", To: "worker"}}
	s.state.Tasks = []model.Task{{ID: "out", From: "worker", To: "peer", State: "queued"}, {ID: "in", From: "peer", To: "worker", State: "queued"}, {ID: "active", From: "peer", To: "worker", State: "accepted"}, {ID: "done", From: "peer", To: "worker", State: "completed", Result: "keep"}}
	s.runs["worker"] = "run"
	s.peerTokens["worker"] = "credentialhash"
	writeProfiles(t, s, []accountProfile{{Name: "work"}})
	if _, err := s.updatePane(model.PaneUpdate{ID: "worker", Name: "Worker", Account: "work"}); err == nil {
		t.Fatal("running account switch allowed")
	}
	p, err := s.updatePane(model.PaneUpdate{ID: "worker", Name: "Renamed", Role: "Review"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.state.Tasks) != 4 || p.BriefTask != "" {
		t.Fatal("metadata edit reinjected brief")
	}
	for _, method := range []string{"preset.list", "pane.spawn", "pane.update", "pane.remove", "pane.brief", "pane.brief.send", "edge.add"} {
		if _, err := s.dispatch(connection{principal: "worker"}, method, rawTask(map[string]string{"id": "peer"})); err == nil {
			t.Fatalf("peer can invoke %s", method)
		}
	}
	if err := s.removePane("worker"); err != nil {
		t.Fatal(err)
	}
	s.paneExited("worker", "run", 1, nil)
	s.message("worker", "run", "agent", "late")
	if s.paneLocked("worker") != nil || len(s.state.Edges) != 0 || s.peerTokens["worker"] != "" {
		t.Fatal("removed pane resurrected / grant retained")
	}
	if s.state.Tasks[0].State != "cancelled" || s.state.Tasks[1].State != "cancelled" || s.state.Tasks[2].State != "unknown" || s.state.Tasks[3].Result != "keep" {
		t.Fatalf("history/cancellation: %+v", s.state.Tasks)
	}
	if _, err := os.Stat(p.BriefPath); err != nil {
		t.Fatal("brief artifact removed", err)
	}
}

func TestSpawnValidationAndBriefQueueRestart(t *testing.T) {
	s := onboardingServer(t)
	args := model.SpawnParams{Name: "worker", Cwd: s.paths.Home, Command: []string{"/bin/cat"}, InjectBrief: true, ConnectTo: []string{"missing"}}
	if _, err := s.spawn(args, ""); err == nil || len(s.state.Panes) != 0 {
		t.Fatal("invalid peers changed state")
	}
	args.ConnectTo = nil
	p, err := s.spawn(args, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.BriefTask == "" || len(s.state.Tasks) != 1 || s.state.Tasks[0].State != "queued" || s.state.Tasks[0].DeliveryMode != "inject" {
		t.Fatalf("brief bypassed queue: %+v %+v", p, s.state.Tasks)
	}
	s.mu.Lock()
	s.state.Tasks[0].State = "unknown"
	s.mu.Unlock()
	if err := s.stop(p.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		state := s.paneLocked(p.ID).State
		s.mu.Unlock()
		if state == "exited" || state == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stop timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
	result, err := s.dispatch(connection{principal: "controller"}, "pane.restart", rawTask(map[string]string{"id": p.ID}))
	if err != nil {
		t.Fatal(err)
	}
	restarted := result.(model.Pane)
	if len(s.state.Tasks) != 1 || restarted.BriefTask != p.BriefTask || restarted.BriefPath != p.BriefPath || !restarted.InjectBrief {
		t.Fatal("restart replayed or lost brief metadata")
	}
	if _, err := s.paneBrief(p.ID, true); err != nil {
		t.Fatal(err)
	}
	if len(s.state.Tasks) != 2 || s.state.Tasks[1].State != "queued" {
		t.Fatal("explicit brief resend not queued")
	}
	if err := s.removePane(p.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticRedactsWrappedCredentials(t *testing.T) {
	if got := redactDiagnostic("before token-123\n456 after", "token-123456"); got != "before [REDACTED] after" {
		t.Fatal(got)
	}
}
