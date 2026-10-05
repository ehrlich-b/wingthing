package wingpolicy

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// ClonePathList deep-copies a PathList so a failed save can roll the live ACL
// back to exactly its prior state.
func ClonePathList(paths config.PathList) config.PathList {
	out := make(config.PathList, len(paths))
	for i, e := range paths {
		out[i] = e
		out[i].Members = append([]string(nil), e.Members...)
	}
	return out
}

// ResolvePathStrings resolves ~/ prefixes and makes paths absolute.
// Returns empty if input is empty (no path restrictions).
func ResolvePathStrings(paths []string, home string) []string {
	var out []string
	for _, p := range paths {
		if strings.HasPrefix(p, "~/") {
			p = filepath.Join(home, p[2:])
		} else if p == "~" {
			p = home
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		out = append(out, p)
	}
	return out
}

// PathsForRequest returns resolved paths filtered by the request sender's ACLs.
func PathsForRequest(pathList config.PathList, email, orgRole, home string) []string {
	return ResolvePathStrings(pathList.PathsForUser(email, orgRole), home)
}

// FilterProjectsByPaths returns only projects whose paths are under one of the resolved paths.
func FilterProjectsByPaths(projects []ws.WingProject, resolvedPaths []string) []ws.WingProject {
	var out []ws.WingProject
	for _, p := range projects {
		if IsUnderPaths(p.Path, resolvedPaths) {
			out = append(out, p)
		}
	}
	return out
}

// DiscoverWingProjects returns the project metadata a wing may advertise.
// Explicit path configuration is a disclosure boundary: never supplement it
// with projects found beneath the process cwd.
func DiscoverWingProjects(resolvedPaths []string, cwd string) []ws.WingProject {
	scanPaths := resolvedPaths
	maxDepth := 3
	if len(scanPaths) == 0 {
		if cwd == "" {
			return nil
		}
		scanPaths = []string{cwd}
		maxDepth = 2
	}

	seen := make(map[string]bool)
	var projects []ws.WingProject
	for _, scanPath := range scanPaths {
		for _, project := range DiscoverProjects(scanPath, maxDepth) {
			if seen[project.Path] {
				continue
			}
			seen[project.Path] = true
			projects = append(projects, project)
		}
	}
	return projects
}

// IsUnderPaths returns true if path is equal to or under one of the resolved paths.
func IsUnderPaths(path string, resolvedPaths []string) bool {
	cleaned := filepath.Clean(path)
	for _, rp := range resolvedPaths {
		if cleaned == rp || strings.HasPrefix(cleaned, rp+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// FilterProjectsExact returns only projects whose paths exactly match one of the resolved paths.
func FilterProjectsExact(projects []ws.WingProject, resolvedPaths []string) []ws.WingProject {
	var out []ws.WingProject
	for _, p := range projects {
		if IsExactPath(p.Path, resolvedPaths) {
			out = append(out, p)
		}
	}
	return out
}

// IsExactPath returns true if path exactly matches one of the configured paths.
func IsExactPath(path string, paths []string) bool {
	cleaned := filepath.Clean(path)
	for _, p := range paths {
		if cleaned == p {
			return true
		}
	}
	return false
}

// IsMemberRole grants elevated behavior only to the two coordinator roles the
// wing understands. Empty, legacy, and unexpected values stay least-privilege.
func IsMemberRole(orgRole string) bool {
	return orgRole != "owner" && orgRole != "admin"
}

// DiscoverProjects scans dir for git repositories up to maxDepth levels deep.
// Returns group directories (sorted by project count) followed by individual repos (sorted by mtime).
func DiscoverProjects(dir string, maxDepth int) []ws.WingProject {
	var repos []ws.WingProject
	scanDir(dir, 0, maxDepth, &repos)

	// Count repos per parent directory
	parentCount := make(map[string]int)
	for _, r := range repos {
		parent := filepath.Dir(r.Path)
		if parent != dir { // skip the root scan dir itself
			parentCount[parent]++
		}
	}

	// Build group entries for parents with 2+ repos
	var groups []ws.WingProject
	seen := make(map[string]bool)
	for parent, count := range parentCount {
		if count >= 2 && !seen[parent] {
			seen[parent] = true
			groups = append(groups, ws.WingProject{
				Name:    filepath.Base(parent),
				Path:    parent,
				ModTime: int64(count), // abuse ModTime to carry count for sorting
			})
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].ModTime > groups[j].ModTime // most projects first
	})
	// Reset ModTime to actual value
	for i := range groups {
		groups[i].ModTime = projectModTime(groups[i].Path)
	}

	// Sort individual repos by mtime
	sort.Slice(repos, func(i, j int) bool {
		return repos[i].ModTime > repos[j].ModTime
	})

	return append(groups, repos...)
}

func projectModTime(dir string) int64 {
	info, err := os.Stat(dir)
	if err != nil {
		return 0
	}
	return info.ModTime().Unix()
}

func scanDir(dir string, depth, maxDepth int, projects *[]ws.WingProject) {
	if depth > maxDepth {
		return
	}

	// At depth 0, check if the configured path itself is a project.
	// This handles paths that point directly at project dirs (e.g.
	// paths: [~/repos/myproject]). At depth > 0, the parent's child
	// scan already added this dir if it had .git or egg.yaml.
	if depth == 0 {
		hasGit := false
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			hasGit = true
		}
		hasEgg := false
		if _, err := os.Stat(filepath.Join(dir, "egg.yaml")); err == nil {
			hasEgg = true
		}
		if hasGit || hasEgg {
			*projects = append(*projects, ws.WingProject{
				Name:    filepath.Base(dir),
				Path:    dir,
				ModTime: projectModTime(dir),
			})
			if hasGit {
				return
			}
			// egg.yaml only: also scan children for git repos
		}
	}

	// Not a project itself — scan children.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		gitDir := filepath.Join(full, ".git")
		eggFile := filepath.Join(full, "egg.yaml")
		hasGit := false
		hasEgg := false
		if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
			hasGit = true
		}
		if info, err := os.Stat(eggFile); err == nil && !info.IsDir() {
			hasEgg = true
		}
		if hasGit || hasEgg {
			*projects = append(*projects, ws.WingProject{
				Name:    e.Name(),
				Path:    full,
				ModTime: projectModTime(full),
			})
		}
		if hasGit {
			// Git repo found. Also check immediate children for egg.yaml
			// sub-projects (e.g. ai-playground/.git + ai-playground/dev/egg.yaml).
			if subs, err := os.ReadDir(full); err == nil {
				for _, sub := range subs {
					if !sub.IsDir() || strings.HasPrefix(sub.Name(), ".") {
						continue
					}
					subFull := filepath.Join(full, sub.Name())
					if info, err := os.Stat(filepath.Join(subFull, "egg.yaml")); err == nil && !info.IsDir() {
						*projects = append(*projects, ws.WingProject{
							Name:    sub.Name(),
							Path:    subFull,
							ModTime: projectModTime(subFull),
						})
					}
				}
			}
			continue
		}
		// No .git — keep scanning (egg.yaml dirs can contain git repos).
		scanDir(full, depth+1, maxDepth, projects)
	}
}

// GetDirEntries returns directory entries for the given path, suitable for cwd selection.
// When resolvedPaths is set, acts as a strict whitelist: only the configured paths are
// returned, no filesystem browsing. This prevents users from navigating into subdirectories
// and writing their own egg.yaml (sandbox escape).
func GetDirEntries(path string, resolvedPaths []string) []ws.DirEntry {
	// Strict whitelist mode: only return configured paths, no browsing.
	if len(resolvedPaths) > 0 {
		var results []ws.DirEntry
		for _, rp := range resolvedPaths {
			results = append(results, ws.DirEntry{
				Name:  filepath.Base(rp),
				IsDir: true,
				Path:  rp,
			})
		}
		return results
	}

	if path == "" {
		home, _ := os.UserHomeDir()
		path = home
	}
	if strings.HasPrefix(path, "~") {
		home, _ := os.UserHomeDir()
		path = home + path[1:]
	}

	// Try path as a directory first; if it doesn't exist, treat the last
	// component as a prefix filter on the parent (tab-completion behavior).
	prefix := ""
	entries, err := os.ReadDir(path)
	if err != nil {
		prefix = strings.ToLower(filepath.Base(path))
		path = filepath.Dir(path)
		entries, err = os.ReadDir(path)
		if err != nil {
			return nil
		}
	}

	var results []ws.DirEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue // dirs only -- this is for cwd selection
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue // skip hidden dirs
		}
		if prefix != "" && !strings.HasPrefix(strings.ToLower(e.Name()), prefix) {
			continue
		}
		full := filepath.Join(path, e.Name())
		results = append(results, ws.DirEntry{
			Name:  e.Name(),
			IsDir: true,
			Path:  full,
		})
	}
	return results
}
