package wingpolicy

import (
	"os"
	"path/filepath"
	"testing"

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
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if egg {
		if err := os.WriteFile(filepath.Join(dir, "egg.yaml"), []byte("fs: []\n"), 0644); err != nil {
			t.Fatal(err)
		}
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
	if CanSeeSession(req, "") {
		t.Fatal("member could see a session with missing ownership metadata")
	}
	if !CanSeeSession(req, "alice") {
		t.Fatal("member could not see their own session")
	}
	if CanSeeSession(req, "bob") {
		t.Fatal("member could see another user's session")
	}
	unknown := ws.TunnelRequest{SenderUserID: "mallory", SenderOrgRole: "outsider"}
	if CanSeeSession(unknown, "alice") {
		t.Fatal("unknown organization role received elevated session visibility")
	}
	owner := ws.TunnelRequest{SenderUserID: "owner", SenderOrgRole: "owner"}
	if !CanSeeSession(owner, "") || !CanSeeSession(owner, "bob") {
		t.Fatal("wing owner lost administrative session visibility")
	}
}

func TestOnlyOwnerAndAdminRolesAreElevated(t *testing.T) {
	for _, role := range []string{"", "member", "outsider", "OWNER", "administrator"} {
		if !IsMemberRole(role) {
			t.Errorf("role %q unexpectedly received elevated behavior", role)
		}
	}
	for _, role := range []string{"owner", "admin"} {
		if IsMemberRole(role) {
			t.Errorf("role %q unexpectedly received member behavior", role)
		}
	}
}

func TestSessionAttachOwnership(t *testing.T) {
	if !CanAttachSession("alice", "member", "alice") {
		t.Fatal("member could not attach to their own session")
	}
	if CanAttachSession("mallory", "member", "alice") {
		t.Fatal("member attached to another member's session")
	}
	if CanAttachSession("mallory", "", "alice") {
		t.Fatal("unknown role attached to another user's session")
	}
	if CanAttachSession("", "member", "") {
		t.Fatal("missing identities were treated as equal owners")
	}
	for _, role := range []string{"owner", "admin"} {
		if !CanAttachSession("operator", role, "alice") {
			t.Fatalf("%s could not attach for session oversight", role)
		}
	}
}

func TestMemberWorkspaceVisibilityFailsClosedWithoutPaths(t *testing.T) {
	member := ws.TunnelRequest{SenderUserID: "alice", SenderOrgRole: "member"}
	owner := ws.TunnelRequest{SenderUserID: "owner", SenderOrgRole: "owner"}
	projects := []ws.WingProject{{Name: "host-project", Path: t.TempDir()}}

	if entries := RequestDirEntries(member, t.TempDir(), nil); len(entries) != 0 {
		t.Fatalf("member without paths could enumerate host directories: %#v", entries)
	}
	if visible := RequestProjects(member, projects, nil); len(visible) != 0 {
		t.Fatalf("member without paths could enumerate host projects: %#v", visible)
	}
	if CanAccessSessionPath(member, projects[0].Path, nil) {
		t.Fatal("member without paths could see a session outside an assigned workspace")
	}
	if visible := RequestProjects(owner, projects, nil); len(visible) != 1 {
		t.Fatal("owner with an empty path list lost personal-wing project visibility")
	}
}

func TestUnknownOrganizationRoleUsesMemberWorkspaceAndAllowlistBoundary(t *testing.T) {
	request := ws.TunnelRequest{SenderUserID: "alice", SenderOrgRole: "outsider"}
	project := ws.WingProject{Name: "host-project", Path: t.TempDir()}
	if entries := RequestDirEntries(request, project.Path, nil); len(entries) != 0 {
		t.Fatalf("unknown role enumerated host directories: %#v", entries)
	}
	if projects := RequestProjects(request, []ws.WingProject{project}, nil); len(projects) != 0 {
		t.Fatalf("unknown role enumerated host projects: %#v", projects)
	}
	allowed := []config.AllowKey{
		{UserID: "alice", Key: "alice-key"},
		{UserID: "bob", Key: "bob-key"},
		{Key: "administrator-key-only"},
	}
	visible := VisibleAllowKeys(request, allowed)
	if len(visible) != 1 || visible[0].UserID != "alice" {
		t.Fatalf("unknown-role allowlist view = %#v, want only caller", visible)
	}
}

func TestScanDir_GitRepos(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root, "alpha", true, false)
	mkProject(t, root, "beta", true, false)
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0755); err != nil {
		t.Fatal(err)
	}

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
	if err := os.MkdirAll(filepath.Join(deep, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	var projects []ws.WingProject
	scanDir(root, 0, 2, &projects)

	if hasName(projects, "project") {
		t.Errorf("project at depth 4 should not appear with maxDepth=2, got %v", projectNames(projects))
	}
}

func TestScanDir_RootIsGitProject(t *testing.T) {
	// Configured path points directly at a git project.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	var projects []ws.WingProject
	scanDir(root, 0, 3, &projects)

	if len(projects) != 1 || projects[0].Path != root {
		t.Fatalf("root git project should be found, got %v", projectNames(projects))
	}
}

func TestScanDir_RootIsEggYamlWithChildren(t *testing.T) {
	// Configured path has egg.yaml but also contains git children.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "egg.yaml"), []byte("fs: []\n"), 0644); err != nil {
		t.Fatal(err)
	}
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
	filtered := FilterProjectsByPaths(projects, []string{"/home/user/repos"})

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
	filtered := FilterProjectsExact(projects, []string{"/opt/wingthing/eng"})
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
		got := IsUnderPaths(tt.path, paths)
		if got != tt.want {
			t.Errorf("isUnderPaths(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestDiscoverProjects_GroupsParentsWithMultipleRepos(t *testing.T) {
	root := t.TempDir()
	container := filepath.Join(root, "repos")
	if err := os.MkdirAll(container, 0755); err != nil {
		t.Fatal(err)
	}
	mkProject(t, container, "a", true, false)
	mkProject(t, container, "b", true, false)
	mkProject(t, container, "c", true, false)

	projects := DiscoverProjects(root, 3)

	// Should have a group entry for "repos" plus individual projects.
	if !hasName(projects, "repos") {
		t.Errorf("repos should appear as group, got %v", projectNames(projects))
	}
	if !hasName(projects, "a") || !hasName(projects, "b") || !hasName(projects, "c") {
		t.Errorf("individual projects should appear, got %v", projectNames(projects))
	}
}

func TestDiscoverWingProjectsExplicitPathsDoNotScanCWD(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(allowed, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	mkProject(t, allowed, "visible", true, false)
	mkProject(t, outside, "private", true, false)

	projects := DiscoverWingProjects([]string{allowed}, outside)
	if !hasName(projects, "visible") {
		t.Fatalf("allowed project missing: %v", projectNames(projects))
	}
	if hasName(projects, "private") {
		t.Fatalf("cwd project escaped explicit path boundary: %v", projectNames(projects))
	}
}

func TestDiscoverWingProjectsWithoutPathsPreservesCWDDiscovery(t *testing.T) {
	cwd := t.TempDir()
	mkProject(t, cwd, "legacy", true, false)
	if projects := DiscoverWingProjects(nil, cwd); !hasName(projects, "legacy") {
		t.Fatalf("legacy cwd project missing: %v", projectNames(projects))
	}
}

func TestRoostBrowserURL(t *testing.T) {
	tests := map[string]string{
		"wss://ws.wingthing.ai":              "https://app.wingthing.ai/",
		"https://wingthing.ai":               "https://app.wingthing.ai/",
		"https://bryan-wingthing.pants.taxi": "https://bryan-wingthing.pants.taxi/app/",
		"ws://localhost:8080":                "http://localhost:8080/app/",
		"https://user:secret@roost.example":  "https://roost.example/app/",
	}
	for input, want := range tests {
		if got := RoostBrowserURL(input); got != want {
			t.Errorf("roostBrowserURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveWingRelayHTTPURLPrecedence(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Dir: dir, RoostURL: "https://config.example/"}
	if err := config.SaveWingConfig(dir, &config.WingConfig{Roost: "wss://wing.example/"}); err != nil {
		t.Fatal(err)
	}

	if got := ResolveWingRelayHTTPURL(cfg, "ws://explicit.example/", true); got != "http://explicit.example" {
		t.Fatalf("explicit roost = %q", got)
	}
	if got := ResolveWingRelayHTTPURL(cfg, "", true); got != "https://wing.example" {
		t.Fatalf("wing.yaml roost = %q", got)
	}
	if err := os.Remove(filepath.Join(dir, "wing.yaml")); err != nil {
		t.Fatal(err)
	}
	if got := ResolveWingRelayHTTPURL(cfg, "", true); got != "http://localhost:8080" {
		t.Fatalf("local roost = %q", got)
	}
	if got := ResolveWingRelayHTTPURL(cfg, "", false); got != "https://config.example" {
		t.Fatalf("config roost = %q", got)
	}
	if got := ResolveWingRelayHTTPURL(nil, "", false); got != "https://ws.wingthing.ai" {
		t.Fatalf("hosted default = %q", got)
	}
}

func TestRelayMetadataURL(t *testing.T) {
	tests := map[string]string{
		"wss://user:secret@roost.example/base/?token=private#fragment": "https://roost.example/base",
		"roost.example/": "https://roost.example",
		"https://%":      "",
	}
	for input, want := range tests {
		if got := RelayMetadataURL(input); got != want {
			t.Errorf("relayMetadataURL(%q) = %q, want %q", input, got, want)
		}
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
			got := ResolveRelayHTTPURL(cfg)
			if got != tt.want {
				t.Errorf("resolveRelayHTTPURL(%q) = %q, want %q", tt.roostURL, got, tt.want)
			}
		})
	}
}

func TestPasskeyPolicyForRoost(t *testing.T) {
	managed := PasskeyPolicyForRoost("wss://ws.wingthing.ai/ws/wing")
	if managed.RPID != "wingthing.ai" || len(managed.Origins) != 1 || managed.Origins[0] != "https://app.wingthing.ai" || !managed.RequireUserVerification {
		t.Fatalf("managed policy = %#v", managed)
	}
	selfHosted := PasskeyPolicyForRoost("https://roost.example.test:8443")
	if selfHosted.RPID != "roost.example.test" || len(selfHosted.Origins) != 1 || selfHosted.Origins[0] != "https://roost.example.test:8443" {
		t.Fatalf("self-hosted policy = %#v", selfHosted)
	}
}

func TestPasskeyPolicyFromRegistration(t *testing.T) {
	policy, ok := PasskeyPolicyFromRegistration(ws.RegisteredMsg{
		PasskeyRPID: "roost.example.test",
		PasskeyOrigins: []string{
			"https://app.roost.example.test",
			"https://roost.example.test",
		},
	})
	if !ok || policy.RPID != "roost.example.test" || len(policy.Origins) != 2 || !policy.RequireUserVerification {
		t.Fatalf("coordinator passkey policy = %#v, ok=%v", policy, ok)
	}
	for _, message := range []ws.RegisteredMsg{
		{PasskeyRPID: "example.test", PasskeyOrigins: []string{"https://attacker.test"}},
		{PasskeyRPID: "example.test", PasskeyOrigins: []string{"http://app.example.test"}},
		{PasskeyRPID: "example.test", PasskeyOrigins: []string{"https://app.example.test/path"}},
		{PasskeyRPID: "", PasskeyOrigins: []string{"https://app.example.test"}},
	} {
		if policy, ok := PasskeyPolicyFromRegistration(message); ok {
			t.Errorf("unsafe coordinator passkey policy accepted: %#v", policy)
		}
	}
}

func TestPasskeyRPURLPrefersPublicBaseURL(t *testing.T) {
	// The embedded roost wing connects over loopback, but browsers reach the
	// roost at WT_BASE_URL; the RP ID must anchor on the browser-facing host.
	embedded := PasskeyPolicyForRoost(PasskeyRPURL("http://localhost:8080", "https://roost.example.test"))
	if embedded.RPID != "roost.example.test" || len(embedded.Origins) != 1 || embedded.Origins[0] != "https://roost.example.test" {
		t.Fatalf("embedded roost policy = %#v", embedded)
	}
	// A standalone wing with no WT_BASE_URL keeps the roost connection host.
	standalone := PasskeyPolicyForRoost(PasskeyRPURL("https://roost.example.test:8443", ""))
	if standalone.RPID != "roost.example.test" {
		t.Fatalf("standalone policy = %#v", standalone)
	}
	// Local HTTPS presents localhost to the browser while the embedded wing
	// deliberately keeps using the separate loopback HTTP listener. Existing
	// localhost development origins remain accepted for backward compatibility.
	localHTTPS := PasskeyPolicyForRoost(PasskeyRPURL("http://127.0.0.1:8080", "https://localhost:8443"))
	if localHTTPS.RPID != "localhost" || len(localHTTPS.Origins) != 3 || localHTTPS.Origins[0] != "https://localhost:8443" {
		t.Fatalf("local HTTPS policy = %#v", localHTTPS)
	}
}

func TestPasskeysForSubjectNeverTrustsAnotherUser(t *testing.T) {
	allowed := []config.AllowKey{
		{UserID: "alice", Key: "alice-key"},
		{UserID: "bob", Key: "bob-key"},
		{Key: "intentional-key-only"},
		{UserID: "alice"},
	}
	got := PasskeysForSubject(allowed, "alice")
	if len(got) != 2 || got[0].Key != "alice-key" || got[1].Key != "intentional-key-only" {
		t.Fatalf("alice keys = %#v", got)
	}
}

func TestVisibleAllowKeysHidesOtherMembersFromOrdinaryUsers(t *testing.T) {
	allowed := []config.AllowKey{
		{UserID: "alice", Email: "alice@example.com", Key: "alice-key"},
		{UserID: "bob", Email: "bob@example.com", Key: "bob-key"},
		{Key: "administrator-key-only"},
	}
	member := VisibleAllowKeys(ws.TunnelRequest{SenderUserID: "alice", SenderOrgRole: "member"}, allowed)
	if len(member) != 1 || member[0].UserID != "alice" {
		t.Fatalf("member-visible allow keys = %#v", member)
	}
	admin := VisibleAllowKeys(ws.TunnelRequest{SenderUserID: "admin", SenderOrgRole: "admin"}, allowed)
	if len(admin) != len(allowed) {
		t.Fatalf("admin-visible allow keys = %#v, want all", admin)
	}
}
