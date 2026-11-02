package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
	// Keep discovery deterministic and prove hidden ACP adapters are not probed.
	path := t.TempDir()
	probeMarker := filepath.Join(s.paths.Home, "acp-probed")
	if err := os.WriteFile(filepath.Join(path, "codex-acp"), []byte("#!/bin/sh\n/usr/bin/touch "+probeMarker+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", path)
	custom := `[{"id":"legacy-acp","name":"Legacy ACP","backend":"acp","command":["codex-acp"]}]`
	if err := os.WriteFile(filepath.Join(s.paths.Home, "presets.json"), []byte(custom), 0600); err != nil {
		t.Fatal(err)
	}
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
	if len(catalog.Presets) != 4 {
		t.Fatalf("PTY-only catalog exposed extra presets: %+v", catalog.Presets)
	}
	for _, kind := range []string{model.PaneForeground, model.PaneWorker} {
		byKind, err := s.catalog(rawTask(map[string]string{"kind": kind}))
		if err != nil {
			t.Fatal(err)
		}
		for _, preset := range byKind.Presets {
			if preset.Backend != "pty" {
				t.Fatalf("%s catalog exposed ACP preset: %+v", kind, preset)
			}
		}
	}
	if _, err := os.Stat(probeMarker); !os.IsNotExist(err) {
		t.Fatalf("hidden codex-acp was probed: %v", err)
	}
}

func TestACPLaunchEntrypointsFailWithoutSideEffects(t *testing.T) {
	s := onboardingServer(t)
	marker := filepath.Join(s.paths.Home, "acp-started")
	command := []string{"/usr/bin/touch", marker}
	custom := `[{"id":"legacy-acp","name":"Legacy ACP","backend":"acp","command":["/usr/bin/touch","` + marker + `"]}]`
	if err := os.WriteFile(filepath.Join(s.paths.Home, "presets.json"), []byte(custom), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.paths.Home, "roles"), 0700); err != nil {
		t.Fatal(err)
	}
	legacyRole := "---\nname: \"legacy\"\nbackend: \"acp\"\n---\nLegacy role\n"
	if err := os.WriteFile(filepath.Join(s.paths.Home, "roles", "legacy.md"), []byte(legacyRole), 0600); err != nil {
		t.Fatal(err)
	}
	s.state.Panes = []model.Pane{{ID: "legacy-pane", Name: "legacy-pane", Kind: model.PaneWorker, Backend: "acp", Command: command, Cwd: s.paths.Home, Workspace: s.paths.Home, State: "exited", CreatedAt: time.Now().Round(0)}}
	s.mu.Lock()
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	before, err := s.db.Load()
	if err != nil {
		t.Fatal(err)
	}
	assertRejected := func(name string, err error) {
		t.Helper()
		if err == nil || err.Error() != acpDisabledError {
			t.Fatalf("%s error = %v, want %q", name, err, acpDisabledError)
		}
		after, loadErr := s.db.Load()
		if loadErr != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(s.state, before) {
			t.Fatalf("%s changed state: memory=%+v durable=%+v loadErr=%v", name, s.state, after, loadErr)
		}
		if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
			t.Fatalf("%s launched ACP command: %v", name, statErr)
		}
	}

	for _, test := range []struct {
		name string
		args model.SpawnParams
	}{
		{name: "direct backend", args: model.SpawnParams{Name: "direct", Kind: model.PaneWorker, Backend: "acp", Command: command, Cwd: s.paths.Home}},
		{name: "builtin preset", args: model.SpawnParams{Name: "builtin", Preset: "codex-acp", Cwd: s.paths.Home}},
		{name: "configured preset", args: model.SpawnParams{Name: "configured", Preset: "legacy-acp", Cwd: s.paths.Home}},
		{name: "legacy role default", args: model.SpawnParams{Name: "role", RoleTemplate: "legacy", Command: command, Cwd: s.paths.Home}},
	} {
		_, err := s.dispatch(connection{principal: "controller"}, "pane.spawn", rawTask(test.args))
		assertRejected(test.name, err)
	}
	_, err = s.dispatch(connection{principal: "controller"}, "pane.restart", rawTask(map[string]string{"id": "legacy-pane"}))
	assertRejected("existing pane restart", err)
}

func TestRolePutRejectsACPDefaults(t *testing.T) {
	s := onboardingServer(t)
	for _, role := range []model.RoleTemplate{
		{Name: "backend-acp", Backend: "acp", Instructions: "legacy"},
		{Name: "preset-acp", Preset: "codex-acp", Instructions: "legacy"},
	} {
		if _, err := s.putRole(role); err == nil || err.Error() != acpDisabledError {
			t.Fatalf("putRole(%s) error = %v", role.Name, err)
		}
		if _, err := os.Stat(filepath.Join(s.paths.Home, "roles", role.Name+".md")); !os.IsNotExist(err) {
			t.Fatalf("rejected role %s was written: %v", role.Name, err)
		}
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
	s.state.Panes = []model.Pane{{ID: "peer", Name: "Peer", Backend: "pty", State: "running", Workspace: s.paths.Home, Cwd: s.paths.Home}}
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
	for _, method := range []string{"preset.list", "pane.spawn", "pane.update", "pane.remove", "pane.brief", "pane.brief.send", "role.list", "role.put", "role.delete", "edge.add"} {
		if _, err := s.dispatch(connection{principal: "worker"}, method, rawTask(map[string]string{"id": "peer"})); err == nil {
			t.Fatalf("peer can invoke %s", method)
		}
	}
	if err := s.removePane("worker", ""); err != nil {
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
	if p.BriefTask == "" || len(s.state.Tasks) != 1 || s.state.Tasks[0].State != "queued" || s.state.Tasks[0].DeliveryMode != "push" {
		t.Fatalf("brief bypassed queue: %+v %+v", p, s.state.Tasks)
	}
	s.mu.Lock()
	s.state.Tasks[0].State = "unknown"
	s.mu.Unlock()
	if err := s.stop(p.ID, ""); err != nil {
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
	if _, err := s.paneBrief(p.ID, "", true); err != nil {
		t.Fatal(err)
	}
	if len(s.state.Tasks) != 2 || s.state.Tasks[1].State != "queued" {
		t.Fatal("explicit brief resend not queued")
	}
	if err := s.removePane(p.ID, ""); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticRedactsWrappedCredentials(t *testing.T) {
	if got := redactDiagnostic("before token-123\n456 after", "token-123456"); got != "before [REDACTED] after" {
		t.Fatal(got)
	}
}

func TestAccountCredentialReferences(t *testing.T) {
	s := onboardingServer(t)
	t.Setenv("WTABLE_TEST_PROFILE_SECRET", "reference-value-do-not-expose")
	path := filepath.Join(s.paths.Home, "accounts.json")
	data := `[{"name":"gateway","endpoint":"compatible","env":{"LITERAL_OLD":"old-value","LITERAL_NEW":{"literal":"new-value"},"API_TOKEN":{"env_ref":"WTABLE_TEST_PROFILE_SECRET"}}}]`
	// References to Worktable's own reserved variables are not permitted.
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.accountEnv("gateway"); err == nil {
		t.Fatal("reserved source reference accepted")
	}
	t.Setenv("AGENT_TEST_PROFILE_SECRET", "reference-value-do-not-expose")
	data = strings.ReplaceAll(data, "WTABLE_TEST_PROFILE_SECRET", "AGENT_TEST_PROFILE_SECRET")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	env, err := s.accountEnv("gateway")
	if err != nil || env["API_TOKEN"] != "reference-value-do-not-expose" || env["LITERAL_OLD"] != "old-value" || env["LITERAL_NEW"] != "new-value" {
		t.Fatalf("reference resolution failed: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	catalog, err := s.catalog()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(catalog)
	for _, private := range []string{"reference-value-do-not-expose", "AGENT_TEST_PROFILE_SECRET", "API_TOKEN", "old-value", "new-value"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("catalog leaks credential data %q", private)
		}
	}
	if len(catalog.AccountProfiles) != 1 || catalog.AccountProfiles[0].Endpoint != "compatible" {
		t.Fatalf("missing endpoint metadata: %+v", catalog.AccountProfiles)
	}
	t.Setenv("AGENT_TEST_PROFILE_SECRET", "")
	if _, err := s.accountEnv("gateway"); err == nil || !strings.Contains(err.Error(), "env_ref") || strings.Contains(err.Error(), "AGENT_TEST_PROFILE_SECRET") {
		t.Fatalf("missing reference not safely explained: %v", err)
	}
	data = `[{"name":"keychain","env":{"API_TOKEN":{"keychain_ref":{"service":"wtable-test-missing-service-857328510","account":"missing"}}}}]`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.accountEnv("keychain"); err == nil || !strings.Contains(err.Error(), "keychain_ref") || strings.Contains(err.Error(), "857328510") {
		t.Fatalf("keychain failure not safely explained: %v", err)
	}
	for _, value := range []string{`null`, `{"env_ref":"A","literal":"leak"}`, `{"unknown":"leak"}`} {
		data = `[{"name":"invalid","env":{"TOKEN":` + value + `}}]`
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.accountEnv("invalid"); err == nil || strings.Contains(err.Error(), "leak") {
			t.Fatalf("invalid credential source accepted or exposed: %s %v", value, err)
		}
	}
}

func TestRoleSnapshotSurvivesTemplateUpdatesAndBriefReads(t *testing.T) {
	s := onboardingServer(t)
	role := model.RoleTemplate{Name: "reviewer", Instructions: "Review correctness and report concrete evidence.", Preset: "shell", Backend: "pty", Cwd: s.paths.Home}
	if _, err := s.putRole(role); err != nil {
		t.Fatal(err)
	}
	roles, err := s.roles()
	if err != nil || len(roles) != 1 || roles[0] != role {
		t.Fatalf("role round trip: %+v %v", roles, err)
	}
	defaults := model.SpawnParams{RoleTemplate: role.Name}
	if _, err := s.resolveRole(&defaults); err != nil || defaults.Preset != "shell" || defaults.Backend != "pty" || defaults.Cwd != s.paths.Home {
		t.Fatalf("role defaults not applied: %+v %v", defaults, err)
	}
	explicit := model.SpawnParams{RoleTemplate: role.Name, Preset: "codex-acp"}
	if _, err := s.resolveRole(&explicit); err != nil || explicit.Backend != "" {
		t.Fatalf("role backend overrode explicit preset: %+v %v", explicit, err)
	}
	p, err := s.spawn(model.SpawnParams{Name: "review", RoleTemplate: role.Name, Command: []string{"/bin/cat"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.removePane(p.ID, "")
	if p.RoleTemplate != role.Name || p.Role != role.Name || p.RoleInstructions != role.Instructions || p.RoleHash != roleHash(role.Instructions) {
		t.Fatalf("missing role snapshot: %+v", p)
	}
	originalHash := p.RoleHash
	role.Instructions = "CHANGED_TEMPLATE_MUST_NOT_REACH_RUNNING_PANE"
	if _, err := s.putRole(role); err != nil {
		t.Fatal(err)
	}
	if _, err := s.paneBrief(p.ID, "", false); err != nil {
		t.Fatal(err)
	}
	// Saving a metadata edit with the same selected template must not refresh it.
	if _, err := s.updatePane(model.PaneUpdate{ID: p.ID, Name: p.Name, Role: p.Role, RoleTemplate: &p.RoleTemplate}); err != nil {
		t.Fatal(err)
	}
	if err := s.deleteRole(role.Name); err != nil {
		t.Fatal(err)
	}
	// Legacy edit clients omit role_template when renaming a pane. Omission
	// must preserve its startup snapshot even after the template is deleted.
	renamed, err := s.updatePane(model.PaneUpdate{ID: p.ID, Name: "renamed-review", Role: p.Role})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "renamed-review" || renamed.RoleTemplate != p.RoleTemplate || renamed.RoleInstructions != p.RoleInstructions || renamed.RoleHash != originalHash {
		t.Fatalf("omitted role_template erased startup snapshot: %+v", renamed)
	}
	if _, err := s.paneBrief(p.ID, "", false); err != nil {
		t.Fatal(err)
	}
	if err := s.stop(p.ID, ""); err != nil {
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
			t.Fatal("role fixture stop timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := s.dispatch(connection{principal: "controller"}, "pane.restart", rawTask(map[string]string{"id": p.ID})); err != nil {
		t.Fatalf("deleted role prevented restart: %v", err)
	}
	s.mu.Lock()
	stored := clonePane(*s.paneLocked(p.ID))
	s.mu.Unlock()
	text, err := os.ReadFile(stored.BriefPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RoleHash != originalHash || stored.RoleInstructions != p.RoleInstructions || strings.Contains(string(text), "CHANGED_TEMPLATE") || !strings.Contains(string(text), p.RoleInstructions) {
		t.Fatalf("brief/template operation mutated startup snapshot: %s", text)
	}
	freeRole := ""
	cleared, err := s.updatePane(model.PaneUpdate{ID: p.ID, Name: stored.Name, Role: "Free text", RoleTemplate: &freeRole})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Role != "Free text" || cleared.RoleTemplate != "" || cleared.RoleInstructions != "" || cleared.RoleHash != "" {
		t.Fatalf("explicit empty role_template did not select free text: %+v", cleared)
	}
	if _, err := s.putRole(model.RoleTemplate{Name: "../escape"}); err == nil {
		t.Fatal("role path traversal accepted")
	}
}

func TestBriefIsInstructionsNotDelegatedWork(t *testing.T) {
	s := onboardingServer(t)
	s.state.Panes = []model.Pane{{ID: "worker", Name: "Worker", Kind: model.PaneWorker, Backend: "pty", State: "running", Cwd: "/tmp"}}
	s.mu.Lock()
	brief, err := s.enqueueBriefLocked(&s.state.Panes[0], "# Worktable brief\nInstructions")
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if brief.Kind != model.TaskKindBrief || brief.OnComplete != "none" || brief.ReportTo != "" {
		t.Fatalf("brief carries delegation semantics: %+v", brief)
	}
	listed, err := s.getTasks("controller", "task.list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if tasks := listed.([]model.Task); len(tasks) != 0 {
		t.Fatalf("brief leaked into task.list: %+v", tasks)
	}
	if detail, err := s.getTasks("controller", "task.get", rawTask(map[string]string{"id": brief.ID})); err != nil {
		t.Fatalf("task.get must still return a brief by ID: %v", err)
	} else if detail.(model.Task).Kind != model.TaskKindBrief {
		t.Fatalf("task.get lost the brief kind: %+v", detail)
	}
	envelope := taskEnvelope(brief)
	if strings.Contains(envelope, "\"$WTABLE_CLI\" ack") || strings.Contains(envelope, "\"$WTABLE_CLI\" result") || !strings.Contains(envelope, "standing instructions") {
		t.Fatalf("brief envelope asks for a receipt: %s", envelope)
	}

	s.mu.Lock()
	s.state.Tasks[0].State = "delivered"
	s.mu.Unlock()
	if s.paneBusyLocked("worker") {
		t.Fatal("a delivered brief keeps the pane busy")
	}
	s.mu.Lock()
	s.state.Tasks[0].Kind = model.TaskKindDelegation
	s.mu.Unlock()
	if !s.paneBusyLocked("worker") {
		t.Fatal("delivered work must still keep the pane busy")
	}

	s.mu.Lock()
	s.state.Tasks[0].Kind = model.TaskKindBrief
	s.state.Tasks[0].State = "completed"
	s.state.Tasks[0].Seen = false
	s.state.Panes = append(s.state.Panes, model.Pane{ID: "sender", Name: "Sender", Backend: "pty", State: "running"})
	s.state.Edges = append(s.state.Edges, model.Edge{From: "sender", To: "worker"})
	if _, err := s.createTaskLocked("sender", model.SendParams{To: "worker", Prompt: "work", Kind: model.TaskKindBrief}, ""); err == nil {
		t.Fatal("a peer was allowed to label work as a brief")
	}
	snapshot := model.Snapshot{Panes: append([]model.Pane{}, s.state.Panes...), Tasks: []model.Task{s.state.Tasks[0]}}
	s.mu.Unlock()

	if inbox := deriveInbox(snapshot); len(inbox) != 0 {
		t.Fatalf("completed brief reached the inbox: %+v", inbox)
	}
	graph := snapshotGraph(snapshot)
	if graph.Nodes[1].Unread != 0 {
		t.Fatalf("completed brief marked unread: %+v", graph.Nodes[1])
	}
	for _, edge := range graph.Edges {
		if edge.From == "you" && edge.To == "worker" && edge.Active != 0 {
			t.Fatalf("completed brief counted as active work: %+v", edge)
		}
	}
}
