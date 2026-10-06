package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testRepo(t *testing.T) (Manager, string) {
	t.Helper()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(workspace, "repo with spaces")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := git(repo, "init"); err != nil {
		t.Fatal(err)
	}
	commit(t, repo, "Initial commit")
	return Manager{AllowedPaths: []string{workspace}}, repo
}

func commit(t *testing.T, repo, message string) {
	t.Helper()
	if _, err := git(repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", message); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeCreateListRemove(t *testing.T) {
	m, repo := testRepo(t)
	created, err := m.Create(repo, "task_1-A", "")
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(filepath.Dir(repo), ".wingthing-worktrees", filepath.Base(repo), "task_1-A")
	if created.Path != wantPath || created.Repo != repo || created.Branch != "wt/task_1-A" {
		t.Fatalf("created = %#v", created)
	}
	head, err := git(repo, "rev-parse", "HEAD")
	if err != nil || created.Head != strings.TrimSpace(head) {
		t.Fatalf("base HEAD = %q, created = %#v, err = %v", head, created, err)
	}
	entries, err := m.List(repo)
	if err != nil || len(entries) != 1 || entries[0] != *created {
		t.Fatalf("list = %#v, err = %v", entries, err)
	}
	if err := m.Remove(repo, created.Name, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(created.Path); !os.IsNotExist(err) {
		t.Fatalf("removed checkout still exists: %v", err)
	}
	if _, err := git(repo, "rev-parse", "--verify", "refs/heads/"+created.Branch); err != nil {
		t.Fatalf("removal deleted branch: %v", err)
	}
	entries, err = m.List(repo)
	if err != nil || len(entries) != 0 {
		t.Fatalf("list after removal = %#v, err = %v", entries, err)
	}
	if _, err := m.Create(repo, created.Name, ""); err == nil {
		t.Fatal("creation overwrote retained branch")
	}
}

func TestWorktreeExplicitBaseAndRoot(t *testing.T) {
	m, repo := testRepo(t)
	base, err := git(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	commit(t, repo, "Second commit")
	m.Root = filepath.Join(repo, "checkouts")
	created, err := m.Create(repo, "older", "HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	if created.Head != strings.TrimSpace(base) || created.Path != filepath.Join(m.Root, filepath.Base(repo), "older") {
		t.Fatalf("explicit base/root = %#v", created)
	}
	if _, err := m.Create(repo, "bad-base", "--help"); err == nil {
		t.Fatal("option-like base accepted")
	}
	if _, err := git(repo, "rev-parse", "--verify", "refs/heads/wt/bad-base"); err == nil {
		t.Fatal("invalid base left a branch")
	}
}

func TestWorktreeDirtyRemoval(t *testing.T) {
	for _, kind := range []string{"untracked", "staged", "modified"} {
		t.Run(kind, func(t *testing.T) {
			m, repo := testRepo(t)
			if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := git(repo, "add", "tracked"); err != nil {
				t.Fatal(err)
			}
			commit(t, repo, "Track file")
			created, err := m.Create(repo, "dirty", "")
			if err != nil {
				t.Fatal(err)
			}
			file := "untracked"
			if kind == "modified" {
				file = "tracked"
			}
			if err := os.WriteFile(filepath.Join(created.Path, file), []byte("change"), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "staged" {
				if _, err := git(created.Path, "add", file); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.Remove(repo, "dirty", false); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
				t.Fatalf("dirty removal error = %v", err)
			}
			if _, err := os.Stat(filepath.Join(created.Path, file)); err != nil {
				t.Fatalf("refusal lost changes: %v", err)
			}
			if err := m.Remove(repo, "dirty", true); err != nil {
				t.Fatal(err)
			}
			if _, err := git(repo, "rev-parse", "--verify", "refs/heads/wt/dirty"); err != nil {
				t.Fatalf("forced removal deleted branch: %v", err)
			}
		})
	}
}

func TestWorktreePathBounds(t *testing.T) {
	m, repo := testRepo(t)
	outside := Manager{AllowedPaths: []string{t.TempDir()}}
	for _, operation := range []func() error{
		func() error { _, err := outside.Create(repo, "outside", ""); return err },
		func() error { _, err := outside.List(repo); return err },
		func() error { return outside.Remove(repo, "outside", true) },
		func() error { _, err := (Manager{}).Create(repo, "unbounded", ""); return err },
	} {
		if err := operation(); err == nil || !strings.Contains(err.Error(), "outside allowed workspace paths") {
			t.Fatalf("outside repo error = %v", err)
		}
	}
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := (Manager{AllowedPaths: []string{sub}}).Create(sub, "parent", ""); err == nil {
		t.Fatal("parent repository outside allowed path accepted")
	}
	narrow := Manager{AllowedPaths: []string{repo}}
	if _, err := narrow.Create(repo, "sibling", ""); err == nil {
		t.Fatal("sibling destination outside allowed paths accepted")
	}
	narrow.Root = filepath.Join(repo, "checkouts")
	if _, err := narrow.Create(repo, "inside", ""); err != nil {
		t.Fatalf("root inside allowed repo rejected: %v", err)
	}
	m.Root = filepath.Join(t.TempDir(), "outside")
	if _, err := m.Create(repo, "outside-root", ""); err == nil {
		t.Fatal("outside root accepted")
	}
}

func TestWorktreeSymlinkBounds(t *testing.T) {
	m, repo := testRepo(t)
	outside := t.TempDir()
	link := filepath.Join(filepath.Dir(repo), "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(link, "escape", ""); err == nil {
		t.Fatal("symlink outside allowed repo accepted")
	}
	m.Root = filepath.Join(link, "not-yet-created")
	if _, err := m.Create(repo, "escape", ""); err == nil {
		t.Fatal("symlink ancestor outside allowed root accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "not-yet-created")); !os.IsNotExist(err) {
		t.Fatalf("outside path was created: %v", err)
	}
}

func TestWorktreeNameValidation(t *testing.T) {
	for _, name := range []string{"task", "Task_12-a", "-", "_"} {
		if err := ValidateName(name); err != nil {
			t.Errorf("valid name %q: %v", name, err)
		}
	}
	for _, name := range []string{"", ".", "..", "a/b", "a\\b", "a b", "a.b", "$(touch x)", "é", "a\n", "a\x00"} {
		if err := ValidateName(name); err == nil {
			t.Errorf("invalid name %q accepted", name)
		}
		if _, err := (Manager{}).Create("missing", name, ""); err == nil {
			t.Errorf("invalid create name %q accepted", name)
		}
		if err := (Manager{}).Remove("missing", name, true); err == nil {
			t.Errorf("invalid remove name %q accepted", name)
		}
	}
}

func TestWorktreeConcurrentCreates(t *testing.T) {
	m, repo := testRepo(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			<-start
			_, err := m.Create(repo, "parallel", "")
			results <- err
		})
	}
	close(start)
	workers.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("losing create did not fail cleanly: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("successful creates = %d, want 1", wins)
	}
	entries, err := m.List(repo)
	if err != nil || len(entries) != 1 {
		t.Fatalf("concurrent list = %#v, err = %v", entries, err)
	}
}

func TestWorktreeRemoveOnlyRegisteredCheckouts(t *testing.T) {
	m, repo := testRepo(t)
	dir, err := m.directory(repo)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "unregistered")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(repo, "unregistered", true); err == nil {
		t.Fatal("unregistered directory removed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("unregistered directory lost: %v", err)
	}
}

func TestWorktreeGitEnvironmentDoesNotChangeRepository(t *testing.T) {
	m, repo := testRepo(t)
	_, other := testRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	created, err := m.Create(repo, "bound", "")
	if err != nil {
		t.Fatal(err)
	}
	if created.Repo != repo {
		t.Fatalf("ambient Git repository override accepted: %#v", created)
	}
	if _, err := git(other, "rev-parse", "--verify", "refs/heads/wt/bound"); err == nil {
		t.Fatal("creation wrote a branch to the ambient repository")
	}
}

func TestWorktreeCommonDirectoryBounds(t *testing.T) {
	m, repo := testRepo(t)
	outside := filepath.Join(t.TempDir(), "gitdir")
	if err := os.Rename(filepath.Join(repo, ".git"), outside); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: "+outside+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(repo, "outside-common", ""); err == nil || !strings.Contains(err.Error(), "outside allowed workspace paths") {
		t.Fatalf("outside Git metadata accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "wingthing-worktree.lock")); !os.IsNotExist(err) {
		t.Fatalf("outside Git metadata mutated: %v", err)
	}
}
