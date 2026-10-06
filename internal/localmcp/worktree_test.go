package localmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/worktree"
)

func worktreeMCPFixture(t *testing.T) (*Server, string) {
	t.Helper()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(workspace, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"init"},
		{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "Initial commit"},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo}, argv...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", argv, output, err)
		}
	}
	t.Setenv("WINGTHING_WORKTREE_ROOT", "")
	return &Server{
		Version: "dev", Cfg: &config.Config{Dir: filepath.Join(workspace, "state")},
		Logs: &bytes.Buffer{}, Principal: "coordinator",
		Grants:       GrantSet([]string{"worktree.read", "worktree.write", "terminal.start"}),
		allowedPaths: []string{workspace}, enforcePathBounds: true,
	}, repo
}

func callWorktreeTool(t *testing.T, s *Server, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, isError, protocolErr := s.callTool(context.Background(), name, encoded)
	if protocolErr != nil {
		t.Fatalf("%s protocol error: %v", name, protocolErr)
	}
	return result, isError
}

func TestWorktreeMCPCallToolHappyPath(t *testing.T) {
	s, repo := worktreeMCPFixture(t)
	s.MaxSpawnsPerHour = 1
	created, failed := callWorktreeTool(t, s, "worktree_create", map[string]any{"repo": repo, "name": "child"})
	if failed || created["branch"] != "wt/child" || created["repo"] != repo || created["checkout_required"] != true {
		t.Fatalf("create = %#v, failed = %v", created, failed)
	}
	cwd, ok := created["cwd"].(string)
	if !ok || cwd != created["path"] {
		t.Fatalf("missing launch cwd: %#v", created)
	}
	if got, err := s.resolveWorkingDirectory(cwd); err != nil || got != cwd {
		t.Fatalf("agent launch path rejected: %q, %v", got, err)
	}
	// Creation used the only admission slot. agent_start must accept the cwd and
	// reach admission without launching an egg or touching a provider login.
	launch, failed := callWorktreeTool(t, s, "agent_start", map[string]any{"cwd": cwd, "agent": "claude"})
	if !failed || !strings.Contains(launch["error"].(string), "max_spawns_per_hour=1") {
		t.Fatalf("agent_start did not reach admission: %#v", launch)
	}
	listed, failed := callWorktreeTool(t, s, "worktree_list", map[string]any{"repo": repo})
	entries, ok := listed["worktrees"].([]worktree.Worktree)
	if failed || !ok || len(entries) != 1 || entries[0].Path != cwd {
		t.Fatalf("list = %#v, failed = %v", listed, failed)
	}
	removed, failed := callWorktreeTool(t, s, "worktree_remove", map[string]any{"repo": repo, "name": "child"})
	if failed || removed["removed"] != true {
		t.Fatalf("remove = %#v, failed = %v", removed, failed)
	}
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Fatalf("checkout not removed: %v", err)
	}
	audit, err := os.ReadFile(filepath.Join(s.Cfg.Dir, "mcp-audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"worktree_create", "worktree_list", "worktree_remove"} {
		if !bytes.Contains(audit, []byte(`"tool":"`+name+`"`)) {
			t.Errorf("audit missing %s: %s", name, audit)
		}
	}
	if bytes.Contains(audit, []byte(repo)) {
		t.Fatalf("audit recorded full repository arguments: %s", audit)
	}
}

func TestWorktreeMCPGrantsAndAdmission(t *testing.T) {
	s, repo := worktreeMCPFixture(t)
	s.Grants = GrantSet([]string{"worktree.read"})
	for _, name := range []string{"worktree_create", "worktree_remove"} {
		result, failed := callWorktreeTool(t, s, name, map[string]any{"repo": repo, "name": "denied"})
		if !failed || !strings.Contains(result["error"].(string), `lacks grant "worktree.write"`) {
			t.Fatalf("%s grant refusal = %#v", name, result)
		}
	}
	if result, failed := callWorktreeTool(t, s, "worktree_list", map[string]any{"repo": repo}); failed {
		t.Fatalf("read grant rejected: %#v", result)
	}
	s.Grants = GrantSet([]string{"worktree.write"})
	if result, failed := callWorktreeTool(t, s, "worktree_list", map[string]any{"repo": repo}); !failed {
		t.Fatalf("missing read grant accepted: %#v", result)
	}
	s.MaxSpawnsPerHour = 1
	s.admission = NewMCPAdmissionState()
	if result, failed := callWorktreeTool(t, s, "worktree_create", map[string]any{"repo": repo, "name": "first"}); failed {
		t.Fatalf("first admission rejected: %#v", result)
	}
	result, failed := callWorktreeTool(t, s, "worktree_create", map[string]any{"repo": repo, "name": "second"})
	if !failed || !strings.Contains(result["error"].(string), "max_spawns_per_hour=1") {
		t.Fatalf("admission bounds bypassed: %#v", result)
	}
	if result, failed := callWorktreeTool(t, s, "worktree_remove", map[string]any{"repo": repo, "name": "first"}); failed {
		t.Fatalf("cleanup blocked by spawn admission: %#v", result)
	}
}

func TestWorktreeMCPPathBoundsAndForce(t *testing.T) {
	s, repo := worktreeMCPFixture(t)
	created, failed := callWorktreeTool(t, s, "worktree_create", map[string]any{"repo": repo, "name": "dirty"})
	if failed {
		t.Fatal(created)
	}
	if err := os.WriteFile(filepath.Join(created["cwd"].(string), "untracked"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	result, failed := callWorktreeTool(t, s, "worktree_remove", map[string]any{"repo": repo, "name": "dirty"})
	if !failed || !strings.Contains(result["error"].(string), "uncommitted changes") {
		t.Fatalf("dirty removal accepted: %#v", result)
	}
	if result, failed := callWorktreeTool(t, s, "worktree_remove", map[string]any{"repo": repo, "name": "dirty", "force": true}); failed {
		t.Fatalf("forced removal rejected: %#v", result)
	}
	for _, tool := range []string{"worktree_create", "worktree_list", "worktree_remove"} {
		args := map[string]any{"repo": t.TempDir()}
		if tool != "worktree_list" {
			args["name"] = "outside"
		}
		result, failed := callWorktreeTool(t, s, tool, args)
		if !failed || !strings.Contains(result["error"].(string), "outside this user's roost paths") {
			t.Fatalf("%s outside repo accepted: %#v", tool, result)
		}
	}
	s.allowedPaths = []string{repo}
	if result, failed := callWorktreeTool(t, s, "worktree_create", map[string]any{"repo": repo, "name": "sibling"}); !failed {
		t.Fatalf("unadmitted destination accepted: %#v", result)
	}
	t.Setenv("WINGTHING_WORKTREE_ROOT", filepath.Join(repo, "checkouts"))
	if result, failed := callWorktreeTool(t, s, "worktree_create", map[string]any{"repo": repo, "name": "inside"}); failed {
		t.Fatalf("configured admitted root rejected: %#v", result)
	}
	s.allowedPaths = nil
	if result, failed := callWorktreeTool(t, s, "worktree_list", map[string]any{"repo": repo}); !failed {
		t.Fatalf("empty enforced path policy accepted: %#v", result)
	}
}

func TestWorktreeMCPStrictArguments(t *testing.T) {
	s, repo := worktreeMCPFixture(t)
	for _, tool := range []string{"worktree_create", "worktree_list", "worktree_remove"} {
		if result, failed := callWorktreeTool(t, s, tool, map[string]any{"repo": repo, "unexpected": true}); !failed {
			t.Fatalf("%s accepted unknown field: %#v", tool, result)
		}
	}
}

func TestWorktreeMCPIgnoredRemoval(t *testing.T) {
	s, repo := worktreeMCPFixture(t)
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte(".env\n"), 0600); err != nil {
		t.Fatal(err)
	}
	created, failed := callWorktreeTool(t, s, "worktree_create", map[string]any{"repo": repo, "name": "ignored"})
	if failed {
		t.Fatal(created)
	}
	path := filepath.Join(created["cwd"].(string), ".env")
	if err := os.WriteFile(path, []byte("preserve\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, failed := callWorktreeTool(t, s, "worktree_remove", map[string]any{"repo": repo, "name": "ignored"})
	if !failed || !strings.Contains(result["error"].(string), "uncommitted changes") {
		t.Fatalf("ignored file removal accepted: %#v", result)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "preserve\n" {
		t.Fatalf("refusal lost ignored file: %q, %v", content, err)
	}
	if result, failed := callWorktreeTool(t, s, "worktree_remove", map[string]any{"repo": repo, "name": "ignored", "force": true}); failed {
		t.Fatalf("forced ignored removal rejected: %#v", result)
	}
}
