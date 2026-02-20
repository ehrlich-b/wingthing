package main

import (
	"os"
	"path/filepath"
	"testing"

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
