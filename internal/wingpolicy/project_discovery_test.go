package wingpolicy

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func discoveryGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func discoveryRepository(t *testing.T, base string) string {
	t.Helper()
	repo := filepath.Join(base, "primary")
	discoveryGit(t, base, "init", repo)
	discoveryGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "--allow-empty", "-m", "Initialize fixture")
	return repo
}

func requireProjectPaths(t *testing.T, projects []ws.WingProject, paths ...string) {
	t.Helper()
	counts := make(map[string]int)
	for _, project := range projects {
		counts[project.Path]++
	}
	if len(projects) != len(paths) {
		t.Errorf("discovered paths = %v, want exactly %v", counts, paths)
	}
	for _, path := range paths {
		if counts[path] != 1 {
			t.Errorf("project %s appeared %d times, want once", path, counts[path])
		}
	}
}

func TestDiscoverWingProjectsLinkedWorktrees(t *testing.T) {
	base := testsocket.Dir(t)
	primary := discoveryRepository(t, base)
	root := filepath.Join(base, "worktrees")
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for _, worktree := range []string{first, second} {
		discoveryGit(t, primary, "worktree", "add", "--detach", worktree, "HEAD")
		if info, err := os.Stat(filepath.Join(worktree, ".git")); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("expected real worktree .git file at %s: %v", worktree, err)
		}
	}
	dev := mkProject(t, first, "dev", false, true)
	// The configured container must include .git files, without advertising
	// the primary repository outside it (even when that repository is cwd).
	requireProjectPaths(t, DiscoverWingProjects([]string{root}, primary), first, second, dev)
	// A worktree can also be configured directly; overlapping roots must not
	// advertise it twice.
	requireProjectPaths(t, DiscoverWingProjects([]string{root, first}, primary), first, second, dev)
	requireProjectPaths(t, DiscoverWingProjects([]string{first}, primary), first, dev)
}

func TestDiscoverWingProjectsConfiguredGitRootRoleChildren(t *testing.T) {
	for _, linked := range []bool{false, true} {
		name := "repository"
		if linked {
			name = "worktree"
		}
		t.Run(name, func(t *testing.T) {
			base := testsocket.Dir(t)
			primary := discoveryRepository(t, base)
			root := primary
			if linked {
				root = filepath.Join(base, "linked")
				discoveryGit(t, primary, "worktree", "add", "--detach", root, "HEAD")
			}
			dev := mkProject(t, root, "dev", false, true)
			qa := mkProject(t, root, "qa", false, true)
			mkProject(t, root, ".hidden-role", false, true)
			mkProject(t, dev, "nested-role", false, true)
			mkProject(t, root, "nested-repo", true, false)
			mkProject(t, root, "ordinary", false, false)
			// A directory named egg.yaml is not a role configuration file.
			if err := os.MkdirAll(filepath.Join(root, "invalid-role", "egg.yaml"), 0755); err != nil {
				t.Fatal(err)
			}
			outside := mkProject(t, base, "denied", true, true)
			projects := DiscoverWingProjects([]string{root, root}, outside)
			requireProjectPaths(t, projects, root, dev, qa)

			// Discovery must preserve the request's exact-path ACL: another
			// role and the parent repository remain absent for this member.
			member := ws.TunnelRequest{SenderUserID: "dev-user", SenderOrgRole: "member"}
			requireProjectPaths(t, RequestProjects(member, projects, []string{dev}), dev)
			requireProjectPaths(t, RequestProjects(member, projects, nil))
		})
	}
}
