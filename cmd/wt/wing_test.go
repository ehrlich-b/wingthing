package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// helper: create a dir with optional .git subdir and/or egg.yaml file.
func mkProject(t *testing.T, base, name string, git, egg bool) string {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if git {
		os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	}
	if egg {
		os.WriteFile(filepath.Join(dir, "egg.yaml"), []byte("fs: []\n"), 0644)
	}
	return dir
}

func projectNames(ps []ws.WingProject) []string {
	var names []string
	for _, p := range ps {
		names = append(names, p.Name)
	}
	return names
}

func hasName(ps []ws.WingProject, name string) bool {
	for _, p := range ps {
		if p.Name == name {
			return true
		}
	}
	return false
}

func TestMemberSessionVisibilityFailsClosed(t *testing.T) {
	req := ws.TunnelRequest{SenderUserID: "alice", SenderOrgRole: "member"}
	if canSeeSession(req, "") {
		t.Fatal("member could see a session with missing ownership metadata")
	}
	if !canSeeSession(req, "alice") {
		t.Fatal("member could not see their own session")
	}
	if canSeeSession(req, "bob") {
		t.Fatal("member could see another user's session")
	}
	owner := ws.TunnelRequest{SenderUserID: "owner", SenderOrgRole: "owner"}
	if !canSeeSession(owner, "") || !canSeeSession(owner, "bob") {
		t.Fatal("wing owner lost administrative session visibility")
	}
}

func TestLoadWingConfigForStartFailsClosedWithoutBreakingLegacyAbsence(t *testing.T) {
	t.Run("missing file remains the compatible zero-value policy", func(t *testing.T) {
		cfg, err := loadWingConfigForStart(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if cfg == nil || cfg.DirectMCP != nil {
			t.Fatalf("default wing config = %#v", cfg)
		}
	})

	for name, body := range map[string]string{
		"malformed direct policy": "direct_mcp:\n  allow_grants: [terminal.read]\n  deny_grants: [terminal.stop]\n",
		"unknown direct grant":    "direct_mcp:\n  allow_grants: [host.root]\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "wing.yaml"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadWingConfigForStart(dir); err == nil || !strings.Contains(err.Error(), "load wing.yaml") {
				t.Fatalf("start config error = %v", err)
			}
		})
	}
}

func TestHostedRelayPolicyAuditIsContentFreeAndPrivate(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Dir: dir}
	if err := appendHostedRelayPolicyAudit(cfg, ws.TypePTYStart); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "policy-audit.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]string
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record["event"] != "hosted_relay_denied" || record["operation"] != ws.TypePTYStart || record["policy"] != config.HostedRelayDeny {
		t.Fatalf("audit record = %#v", record)
	}
	if len(record) != 5 {
		t.Fatalf("audit record contains unexpected fields: %#v", record)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("policy audit mode = %o, want 600", got)
	}
}

func TestSessionAttachOwnership(t *testing.T) {
	if !canAttachSession("alice", "member", "alice") {
		t.Fatal("member could not attach to their own session")
	}
	if canAttachSession("mallory", "member", "alice") {
		t.Fatal("member attached to another member's session")
	}
	if canAttachSession("mallory", "", "alice") {
		t.Fatal("unknown role attached to another user's session")
	}
	if canAttachSession("", "member", "") {
		t.Fatal("missing identities were treated as equal owners")
	}
	for _, role := range []string{"owner", "admin"} {
		if !canAttachSession("operator", role, "alice") {
			t.Fatalf("%s could not attach for session oversight", role)
		}
	}
}

func TestPendingReattachAuthsAreViewerScoped(t *testing.T) {
	pending := newPendingReattachAuths()
	t.Cleanup(pending.close)

	challengeA := []byte("challenge-a")
	pending.put(ws.PTYAttach{Type: ws.TypePTYAttach, ViewerID: "viewer-a", UserID: "alice"}, challengeA, "alice:key-a", time.Hour)
	pending.put(ws.PTYAttach{Type: ws.TypePTYAttach, ViewerID: "viewer-b", UserID: "alice"}, []byte("challenge-b"), "alice:key-b", time.Hour)
	challengeA[0] = 'X'

	if _, ok := pending.take(""); ok {
		t.Fatal("response without a viewer ID consumed a pending viewer attach")
	}
	got, ok := pending.take("viewer-b")
	if !ok {
		t.Fatal("viewer-b could not resume its pending attach")
	}
	if got.attach.ViewerID != "viewer-b" || got.subject != "alice:key-b" || string(got.challenge) != "challenge-b" {
		t.Fatalf("viewer-b resumed the wrong challenge: %#v", got)
	}
	got, ok = pending.take("viewer-a")
	if !ok || string(got.challenge) != "challenge-a" {
		t.Fatalf("viewer-a challenge was lost or aliased: %#v, %v", got, ok)
	}
}

func TestPendingReattachAuthsExpireIndependently(t *testing.T) {
	pending := newPendingReattachAuths()
	t.Cleanup(pending.close)
	now := time.Now()
	pending.byViewer["expired"] = pendingReattachAuth{
		attach:    ws.PTYAttach{ViewerID: "expired"},
		expiresAt: now.Add(-time.Second),
	}
	pending.byViewer["live"] = pendingReattachAuth{
		attach:    ws.PTYAttach{ViewerID: "live"},
		expiresAt: now.Add(time.Hour),
	}
	pending.resetTimer()

	expired := pending.expire(now)
	if len(expired) != 1 || expired[0].attach.ViewerID != "expired" {
		t.Fatalf("expired attaches = %#v, want only expired", expired)
	}
	if _, ok := pending.take("live"); !ok {
		t.Fatal("expiring one viewer removed another viewer's pending attach")
	}
}

func TestMemberWorkspaceVisibilityFailsClosedWithoutPaths(t *testing.T) {
	member := ws.TunnelRequest{SenderUserID: "alice", SenderOrgRole: "member"}
	owner := ws.TunnelRequest{SenderUserID: "owner", SenderOrgRole: "owner"}
	projects := []ws.WingProject{{Name: "host-project", Path: t.TempDir()}}

	if entries := requestDirEntries(member, t.TempDir(), nil); len(entries) != 0 {
		t.Fatalf("member without paths could enumerate host directories: %#v", entries)
	}
	if visible := requestProjects(member, projects, nil); len(visible) != 0 {
		t.Fatalf("member without paths could enumerate host projects: %#v", visible)
	}
	if canAccessSessionPath(member, projects[0].Path, nil) {
		t.Fatal("member without paths could see a session outside an assigned workspace")
	}
	if visible := requestProjects(owner, projects, nil); len(visible) != 1 {
		t.Fatal("owner with an empty path list lost personal-wing project visibility")
	}
}

func TestWingStatusRoundTrip(t *testing.T) {
	// writeWingStatus/readWingStatus use wingStatusPath() which depends on config.Load().
	// We test the JSON struct directly for unit isolation.
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "wing.status")

	s := wingStatus{State: "connected", Error: "", TS: "2026-02-21T00:00:00Z"}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	var got wingStatus
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "connected" {
		t.Errorf("state = %q, want connected", got.State)
	}
}

func TestWingStatusAuthFailed(t *testing.T) {
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "wing.status")

	s := wingStatus{State: "auth_failed", Error: "relay rejected authentication (401)", TS: "2026-02-21T00:00:00Z"}
	data, _ := json.Marshal(s)
	os.WriteFile(statusPath, data, 0644)

	raw, _ := os.ReadFile(statusPath)
	var got wingStatus
	json.Unmarshal(raw, &got)
	if got.State != "auth_failed" {
		t.Errorf("state = %q, want auth_failed", got.State)
	}
	if got.Error == "" {
		t.Error("expected non-empty error")
	}
}

func TestScanDir_GitRepos(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root, "alpha", true, false)
	mkProject(t, root, "beta", true, false)
	os.MkdirAll(filepath.Join(root, "empty"), 0755)

	var projects []ws.WingProject
	scanDir(root, 0, 3, &projects)

	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d: %v", len(projects), projectNames(projects))
	}
	if !hasName(projects, "alpha") || !hasName(projects, "beta") {
		t.Fatalf("expected alpha and beta, got %v", projectNames(projects))
	}
}

func TestScanDir_EggYamlCountsAsProject(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root, "myapp", false, true) // egg.yaml, no .git

	var projects []ws.WingProject
	scanDir(root, 0, 3, &projects)

	if !hasName(projects, "myapp") {
		t.Fatalf("egg.yaml dir should appear as project, got %v", projectNames(projects))
	}
}

func TestScanDir_EggYamlParentDoesNotSwallowGitChildren(t *testing.T) {
	// repos/ has egg.yaml (shared config), repos/wingthing/ has .git.
	// Both should appear.
	root := t.TempDir()
	repos := mkProject(t, root, "repos", false, true)
	mkProject(t, repos, "wingthing", true, false)
	mkProject(t, repos, "blog", true, false)

	var projects []ws.WingProject
	scanDir(root, 0, 3, &projects)

	if !hasName(projects, "repos") {
		t.Errorf("repos (egg.yaml) should appear, got %v", projectNames(projects))
	}
	if !hasName(projects, "wingthing") {
		t.Errorf("wingthing (.git) should appear, got %v", projectNames(projects))
	}
	if !hasName(projects, "blog") {
		t.Errorf("blog (.git) should appear, got %v", projectNames(projects))
	}
}

func TestScanDir_GitRepoWithEggYamlSubProjects(t *testing.T) {
	// ai-playground/ has .git, ai-playground/dev/ has egg.yaml.
	// Both should appear.
	root := t.TempDir()
	aip := mkProject(t, root, "ai-playground", true, false)
	mkProject(t, aip, "dev", false, true)
	mkProject(t, aip, "qa", false, true)

	var projects []ws.WingProject
	scanDir(root, 0, 3, &projects)

	if !hasName(projects, "ai-playground") {
		t.Errorf("ai-playground (.git) should appear, got %v", projectNames(projects))
	}
	if !hasName(projects, "dev") {
		t.Errorf("dev (egg.yaml under git repo) should appear, got %v", projectNames(projects))
	}
	if !hasName(projects, "qa") {
		t.Errorf("qa (egg.yaml under git repo) should appear, got %v", projectNames(projects))
	}
}

func TestScanDir_HiddenDirsSkipped(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root, ".hidden", true, false)
	mkProject(t, root, "visible", true, false)

	var projects []ws.WingProject
	scanDir(root, 0, 3, &projects)

	if hasName(projects, ".hidden") {
		t.Errorf(".hidden should be skipped, got %v", projectNames(projects))
	}
	if !hasName(projects, "visible") {
		t.Errorf("visible should appear, got %v", projectNames(projects))
	}
}

func TestScanDir_DepthLimit(t *testing.T) {
	root := t.TempDir()
	// Create a project 4 levels deep — should not be found with maxDepth=2.
	deep := filepath.Join(root, "a", "b", "c", "project")
	os.MkdirAll(deep, 0755)
	os.MkdirAll(filepath.Join(deep, ".git"), 0755)

	var projects []ws.WingProject
	scanDir(root, 0, 2, &projects)

	if hasName(projects, "project") {
		t.Errorf("project at depth 4 should not appear with maxDepth=2, got %v", projectNames(projects))
	}
}

func TestScanDir_RootIsGitProject(t *testing.T) {
	// Configured path points directly at a git project.
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git"), 0755)

	var projects []ws.WingProject
	scanDir(root, 0, 3, &projects)

	if len(projects) != 1 || projects[0].Path != root {
		t.Fatalf("root git project should be found, got %v", projectNames(projects))
	}
}

func TestScanDir_RootIsEggYamlWithChildren(t *testing.T) {
	// Configured path has egg.yaml but also contains git children.
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "egg.yaml"), []byte("fs: []\n"), 0644)
	mkProject(t, root, "child", true, false)

	var projects []ws.WingProject
	scanDir(root, 0, 3, &projects)

	if len(projects) != 2 {
		t.Fatalf("expected root + child, got %d: %v", len(projects), projectNames(projects))
	}
}

func TestFilterProjectsByPaths(t *testing.T) {
	projects := []ws.WingProject{
		{Name: "allowed", Path: "/home/user/repos/allowed"},
		{Name: "denied", Path: "/home/user/secret/denied"},
		{Name: "also-ok", Path: "/home/user/repos/also-ok"},
	}
	filtered := filterProjectsByPaths(projects, []string{"/home/user/repos"})

	if len(filtered) != 2 {
		t.Fatalf("expected 2, got %d: %v", len(filtered), projectNames(filtered))
	}
	if hasName(filtered, "denied") {
		t.Errorf("denied project should be filtered out")
	}
}

func TestFilterProjectsExact(t *testing.T) {
	projects := []ws.WingProject{
		{Name: "eng", Path: "/opt/wingthing/eng"},
		{Name: "stu", Path: "/opt/wingthing/eng/stu"},
		{Name: "support", Path: "/opt/wingthing/support"},
	}
	filtered := filterProjectsExact(projects, []string{"/opt/wingthing/eng"})
	if len(filtered) != 1 {
		t.Fatalf("expected 1, got %d: %v", len(filtered), projectNames(filtered))
	}
	if filtered[0].Name != "eng" {
		t.Errorf("expected eng, got %s", filtered[0].Name)
	}
}

func TestIsUnderPaths(t *testing.T) {
	paths := []string{"/home/user/repos", "/home/user/work"}

	tests := []struct {
		path string
		want bool
	}{
		{"/home/user/repos/wingthing", true},
		{"/home/user/repos", true},
		{"/home/user/work/project", true},
		{"/home/user/secret", false},
		{"/home/user/reposX", false}, // prefix trick
	}
	for _, tt := range tests {
		got := isUnderPaths(tt.path, paths)
		if got != tt.want {
			t.Errorf("isUnderPaths(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestDiscoverProjects_GroupsParentsWithMultipleRepos(t *testing.T) {
	root := t.TempDir()
	container := filepath.Join(root, "repos")
	os.MkdirAll(container, 0755)
	mkProject(t, container, "a", true, false)
	mkProject(t, container, "b", true, false)
	mkProject(t, container, "c", true, false)

	projects := discoverProjects(root, 3)

	// Should have a group entry for "repos" plus individual projects.
	if !hasName(projects, "repos") {
		t.Errorf("repos should appear as group, got %v", projectNames(projects))
	}
	if !hasName(projects, "a") || !hasName(projects, "b") || !hasName(projects, "c") {
		t.Errorf("individual projects should appear, got %v", projectNames(projects))
	}
}

func TestFormatUserIdentity(t *testing.T) {
	tests := []struct {
		name string
		info auth.UserInfo
		want string
	}{
		{
			"full info",
			auth.UserInfo{DisplayName: "Phil Heckel", Email: "phil@test.com", Provider: "github"},
			"Phil Heckel (phil@test.com) via github",
		},
		{
			"email only",
			auth.UserInfo{Email: "phil@test.com", Provider: "google"},
			"phil@test.com via google",
		},
		{
			"name only",
			auth.UserInfo{DisplayName: "Phil Heckel"},
			"Phil Heckel",
		},
		{
			"name and provider",
			auth.UserInfo{DisplayName: "Phil Heckel", Provider: "github"},
			"Phil Heckel via github",
		},
		{
			"user_id fallback",
			auth.UserInfo{UserID: "abc123"},
			"abc123",
		},
		{
			"empty",
			auth.UserInfo{},
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatUserIdentity(&tt.info)
			if got != tt.want {
				t.Errorf("formatUserIdentity(%+v) = %q, want %q", tt.info, got, tt.want)
			}
		})
	}
}

func TestSetupAPIKeyHelper_RemovesKeyFromEnv(t *testing.T) {
	home := t.TempDir()
	envMap := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test123", "OTHER": "keep"}
	setupAPIKeyHelper("claude", envMap, home)
	if _, ok := envMap["ANTHROPIC_API_KEY"]; ok {
		t.Error("ANTHROPIC_API_KEY should be removed from envMap")
	}
	if envMap["OTHER"] != "keep" {
		t.Error("other env vars should be preserved")
	}
}

func TestSetupAPIKeyHelper_WritesKeyFile(t *testing.T) {
	home := t.TempDir()
	envMap := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-secret"}
	setupAPIKeyHelper("claude", envMap, home)
	keyFile := filepath.Join(home, ".anthropic_key")
	data, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("key file not written: %v", err)
	}
	if string(data) != "sk-ant-secret" {
		t.Errorf("key file = %q, want sk-ant-secret", string(data))
	}
	info, _ := os.Stat(keyFile)
	if info.Mode().Perm() != 0400 {
		t.Errorf("key file perm = %o, want 0400", info.Mode().Perm())
	}
}

func TestSetupAPIKeyHelper_SetsApiKeyHelperInSettings(t *testing.T) {
	home := t.TempDir()
	envMap := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"}
	setupAPIKeyHelper("claude", envMap, home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings not written: %v", err)
	}
	var settings map[string]any
	json.Unmarshal(data, &settings)
	want := "cat " + filepath.Join(home, ".anthropic_key")
	if got := settings["apiKeyHelper"]; got != want {
		t.Errorf("apiKeyHelper = %q, want %q", got, want)
	}
}

func TestSetupAPIKeyHelper_PreservesExistingSettings(t *testing.T) {
	home := t.TempDir()
	settingsDir := filepath.Join(home, ".claude")
	os.MkdirAll(settingsDir, 0700)
	existing := map[string]any{"theme": "dark", "permissions": map[string]any{"allow": true}}
	data, _ := json.Marshal(existing)
	os.WriteFile(filepath.Join(settingsDir, "settings.json"), data, 0644)
	envMap := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"}
	setupAPIKeyHelper("claude", envMap, home)
	raw, _ := os.ReadFile(filepath.Join(settingsDir, "settings.json"))
	var settings map[string]any
	json.Unmarshal(raw, &settings)
	if settings["theme"] != "dark" {
		t.Errorf("existing theme setting clobbered, got %v", settings["theme"])
	}
	if settings["apiKeyHelper"] == nil {
		t.Error("apiKeyHelper not set")
	}
}

func TestSetupAPIKeyHelper_StablePath_NoSessionRace(t *testing.T) {
	// Two "sessions" calling setupAPIKeyHelper should write to the same file,
	// not per-session paths. This is the v0.128.0 bug fix.
	home := t.TempDir()
	env1 := map[string]string{"ANTHROPIC_API_KEY": "key-session-1"}
	env2 := map[string]string{"ANTHROPIC_API_KEY": "key-session-2"}
	setupAPIKeyHelper("claude", env1, home)
	setupAPIKeyHelper("claude", env2, home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	raw, _ := os.ReadFile(settingsPath)
	var settings map[string]any
	json.Unmarshal(raw, &settings)
	helper := settings["apiKeyHelper"].(string)
	// Both sessions should point to the same stable path (no session ID in path)
	wantPath := filepath.Join(home, ".anthropic_key")
	if helper != "cat "+wantPath {
		t.Errorf("apiKeyHelper = %q, want stable path %q", helper, "cat "+wantPath)
	}
	// The key file should contain the last writer's key (both are valid,
	// the point is the PATH is stable, not per-session)
	data, _ := os.ReadFile(wantPath)
	if string(data) != "key-session-2" {
		t.Errorf("key file = %q, want key-session-2", string(data))
	}
}

func TestSetupAPIKeyHelper_OverwritesOldReadOnlyKeyFile(t *testing.T) {
	home := t.TempDir()
	keyFile := filepath.Join(home, ".anthropic_key")
	os.WriteFile(keyFile, []byte("old-key"), 0400)
	envMap := map[string]string{"ANTHROPIC_API_KEY": "new-key"}
	setupAPIKeyHelper("claude", envMap, home)
	data, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("key file gone after overwrite: %v", err)
	}
	if string(data) != "new-key" {
		t.Errorf("key file = %q, want new-key", string(data))
	}
}

func TestSetupAPIKeyHelper_NonClaudeAgent_Noop(t *testing.T) {
	home := t.TempDir()
	envMap := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"}
	setupAPIKeyHelper("codex", envMap, home)
	if _, ok := envMap["ANTHROPIC_API_KEY"]; !ok {
		t.Error("non-claude agent should not remove ANTHROPIC_API_KEY")
	}
	keyFile := filepath.Join(home, ".anthropic_key")
	if _, err := os.Stat(keyFile); err == nil {
		t.Error("key file should not be created for non-claude agent")
	}
}

func TestSetupAPIKeyHelper_NoKey_Noop(t *testing.T) {
	home := t.TempDir()
	envMap := map[string]string{"OTHER": "val"}
	setupAPIKeyHelper("claude", envMap, home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); err == nil {
		t.Error("settings should not be created when no API key present")
	}
}

func TestResolveRelayHTTPURL(t *testing.T) {
	tests := []struct {
		name     string
		roostURL string
		want     string
	}{
		{"wss scheme", "wss://ws.wingthing.ai", "https://ws.wingthing.ai"},
		{"ws scheme", "ws://localhost:8080", "http://localhost:8080"},
		{"https scheme", "https://relay.example.com", "https://relay.example.com"},
		{"http scheme", "http://localhost:8080/", "http://localhost:8080"},
		{"trailing slash stripped", "https://relay.example.com/", "https://relay.example.com"},
		{"bare hostname gets https", "relay.example.com", "https://relay.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{RoostURL: tt.roostURL}
			got := resolveRelayHTTPURL(cfg)
			if got != tt.want {
				t.Errorf("resolveRelayHTTPURL(%q) = %q, want %q", tt.roostURL, got, tt.want)
			}
		})
	}
}

func TestPasskeyPolicyForRoost(t *testing.T) {
	managed := passkeyPolicyForRoost("wss://ws.wingthing.ai/ws/wing")
	if managed.RPID != "wingthing.ai" || len(managed.Origins) != 1 || managed.Origins[0] != "https://app.wingthing.ai" || !managed.RequireUserVerification {
		t.Fatalf("managed policy = %#v", managed)
	}
	selfHosted := passkeyPolicyForRoost("https://roost.example.test:8443")
	if selfHosted.RPID != "roost.example.test" || len(selfHosted.Origins) != 1 || selfHosted.Origins[0] != "https://roost.example.test:8443" {
		t.Fatalf("self-hosted policy = %#v", selfHosted)
	}
}

func TestPasskeyRPURLPrefersPublicBaseURL(t *testing.T) {
	// The embedded roost wing connects over loopback, but browsers reach the
	// roost at WT_BASE_URL; the RP ID must anchor on the browser-facing host.
	embedded := passkeyPolicyForRoost(passkeyRPURL("http://localhost:8080", "https://roost.example.test"))
	if embedded.RPID != "roost.example.test" || len(embedded.Origins) != 1 || embedded.Origins[0] != "https://roost.example.test" {
		t.Fatalf("embedded roost policy = %#v", embedded)
	}
	// A standalone wing with no WT_BASE_URL keeps the roost connection host.
	standalone := passkeyPolicyForRoost(passkeyRPURL("https://roost.example.test:8443", ""))
	if standalone.RPID != "roost.example.test" {
		t.Fatalf("standalone policy = %#v", standalone)
	}
}

func TestPasskeysForSubjectNeverTrustsAnotherUser(t *testing.T) {
	allowed := []config.AllowKey{
		{UserID: "alice", Key: "alice-key"},
		{UserID: "bob", Key: "bob-key"},
		{Key: "intentional-key-only"},
		{UserID: "alice"},
	}
	got := passkeysForSubject(allowed, "alice")
	if len(got) != 2 || got[0].Key != "alice-key" || got[1].Key != "intentional-key-only" {
		t.Fatalf("alice keys = %#v", got)
	}
}

func TestParsePreviewFile(t *testing.T) {
	tests := []struct {
		name                            string
		data                            string
		mode, url, content, file, mtype string
	}{
		{name: "empty", data: "", mode: ""},
		{name: "whitespace only", data: "  \n\t\n", mode: ""},
		{
			name: "url mode", data: "url:https://example.com/app/",
			mode: "url", url: "https://example.com/app/",
		},
		{
			name: "bare markdown defaults to preview.md",
			data: "# Report\n\n| a | b |\n",
			mode: "markdown", content: "# Report\n\n| a | b |\n",
			file: "preview.md", mtype: "text/markdown",
		},
		{
			name: "file header carries name and mime",
			data: "file:report.csv\npartner,status\nAcme,OK\n",
			mode: "markdown", content: "partner,status\nAcme,OK\n",
			file: "report.csv", mtype: "text/csv",
		},
		{
			name: "file header preserves leading whitespace in content",
			data: "file:main.go\n\tif x {\n\t\treturn\n\t}\n",
			mode: "markdown", content: "\tif x {\n\t\treturn\n\t}\n",
			file: "main.go", mtype: "text/x-go",
		},
		{
			name: "file header after blank lines",
			data: "\n\nfile:notes.txt\nbody\n",
			mode: "markdown", content: "body\n",
			file: "notes.txt", mtype: "text/plain",
		},
		{
			name: "file header with no content",
			data: "file:empty.json",
			mode: "markdown", content: "",
			file: "empty.json", mtype: "application/json",
		},
		{
			name: "path traversal stripped to base name",
			data: "file:../../etc/passwd\nroot\n",
			mode: "markdown", content: "root\n",
			file: "passwd", mtype: "application/octet-stream",
		},
		{
			name: "unusable file name falls back to markdown",
			data: "file:   \nstill content\n",
			mode: "markdown", content: "file:   \nstill content\n",
			file: "preview.md", mtype: "text/markdown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePreviewFile([]byte(tt.data))
			if got["mode"] != tt.mode {
				t.Errorf("mode = %q, want %q", got["mode"], tt.mode)
			}
			if got["url"] != tt.url {
				t.Errorf("url = %q, want %q", got["url"], tt.url)
			}
			if got["content"] != tt.content {
				t.Errorf("content = %q, want %q", got["content"], tt.content)
			}
			if got["filename"] != tt.file {
				t.Errorf("filename = %q, want %q", got["filename"], tt.file)
			}
			if tt.mtype != "" && !strings.HasPrefix(got["mime"], tt.mtype) {
				t.Errorf("mime = %q, want prefix %q", got["mime"], tt.mtype)
			}
		})
	}
}

func TestPreviewMIME(t *testing.T) {
	tests := []struct{ name, want string }{
		{"a.md", "text/markdown"},
		{"a.csv", "text/csv"},
		{"a.json", "application/json"},
		{"a.png", "image/png"},
		{"a.pdf", "application/pdf"},
		{"a.go", "text/x-go"},
		{"a.zig", "text/x-zig"},
		{"a.yaml", "application/yaml"},
		{"a.wasm", "application/wasm"},
		{"Makefile", "application/octet-stream"},
		{"a.qqqzzz", "application/octet-stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := previewMIME(tt.name); !strings.HasPrefix(got, tt.want) {
				t.Errorf("previewMIME(%q) = %q, want prefix %q", tt.name, got, tt.want)
			}
		})
	}
}
