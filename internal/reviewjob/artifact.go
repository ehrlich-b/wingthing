// Package reviewjob contains the isolated artifact format used by review jobs.
package reviewjob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	maxArtifactBytes = 256 << 10
	maxFiles         = 32
)

type FileChange struct {
	Path       string `json:"path"`
	Content    []byte `json:"content,omitempty"`
	Delete     bool   `json:"delete,omitempty"`
	Executable bool   `json:"executable,omitempty"`
}
type Candidate struct {
	BaseCommit string       `json:"base_commit"`
	Patch      string       `json:"patch"`
	SHA256     string       `json:"sha256"`
	Files      []FileChange `json:"files"`
}

// Capture records tracked changes and new nonignored or explicitly allowed files.
func Capture(ctx context.Context, cwd, base string, allowedPaths []string) (Candidate, error) {
	root, err := pinnedRoot(ctx, cwd, base)
	if err != nil {
		return Candidate{}, err
	}
	allowed, err := allowedSet(allowedPaths)
	if err != nil {
		return Candidate{}, err
	}
	files, err := worktreeChanges(ctx, root, base, allowedPaths)
	if err != nil {
		return Candidate{}, err
	}
	if len(files) > maxFiles {
		return Candidate{}, fmt.Errorf("artifact has more than %d files", maxFiles)
	}
	total := 0
	for _, f := range files {
		if !allowed[f.Path] {
			return Candidate{}, fmt.Errorf("changed path %q is not allowed", f.Path)
		}
		if !f.Delete {
			total += len(f.Content)
			if total > maxArtifactBytes {
				return Candidate{}, errors.New("artifact content exceeds limit")
			}
		}
	}
	patch, err := makePatch(ctx, root, base, files)
	if err != nil {
		return Candidate{}, err
	}
	if len(patch) > maxArtifactBytes {
		return Candidate{}, errors.New("artifact patch exceeds limit")
	}
	sum := sha256.Sum256([]byte(patch))
	return Candidate{base, patch, hex.EncodeToString(sum[:]), files}, nil
}

// Apply verifies the entire candidate before replacing any file.
func Apply(ctx context.Context, cwd string, c Candidate, allowedPaths []string) error {
	root, err := pinnedRoot(ctx, cwd, c.BaseCommit)
	if err != nil {
		return err
	}
	if err = clean(ctx, root, c.BaseCommit); err != nil {
		return err
	}
	if err = validateCandidate(ctx, root, c, allowedPaths); err != nil {
		return err
	}
	for _, f := range c.Files {
		full := filepath.Join(root, filepath.FromSlash(f.Path))
		if f.Delete {
			if err = os.Remove(full); err != nil {
				return err
			}
			continue
		}
		if err = os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			return err
		}
		mode := os.FileMode(0644)
		if f.Executable {
			mode = 0755
		}
		if err = os.WriteFile(full, f.Content, mode); err != nil {
			return err
		}
		if err = os.Chmod(full, mode); err != nil {
			return err
		}
	}
	return nil
}

func worktreeChanges(ctx context.Context, root, base string, includePaths []string) ([]FileChange, error) {
	old, err := treeFiles(ctx, root, base)
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	for p, b := range old {
		if b.symlink {
			return nil, fmt.Errorf("symlink %q is not allowed", p)
		}
		paths[p] = true
		if err := safePath(root, p, false); err != nil {
			return nil, err
		}
		info, e := os.Lstat(filepath.Join(root, filepath.FromSlash(p)))
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%q is not a regular file", p)
		}
		same, e := sameBlob(ctx, root, b.oid, filepath.Join(root, filepath.FromSlash(p)))
		if e != nil {
			return nil, e
		}
		if same && executable(info.Mode()) == b.executable {
			delete(paths, p)
		}
	}
	// ls-files only identifies untracked names; contents are read by Go. fsmonitor is disabled in git().
	o, e := git(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if e != nil {
		return nil, e
	}
	for _, p := range nulPaths(o) {
		paths[p] = true
	}
	for _, p := range includePaths {
		if _, tracked := old[p]; !tracked {
			if _, err := os.Lstat(filepath.Join(root, p)); err == nil {
				paths[p] = true
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
	}
	ordered := make([]string, 0, len(paths))
	if len(paths) > maxFiles {
		return nil, fmt.Errorf("artifact has more than %d files", maxFiles)
	}
	for p := range paths {
		ordered = append(ordered, p)
	}
	sort.Strings(ordered)
	r := make([]FileChange, 0, len(ordered))
	for _, p := range ordered {
		if err := validPath(p); err != nil {
			return nil, err
		}
		b, exists := old[p]
		if err := safePath(root, p, false); err != nil {
			return nil, err
		}
		full := filepath.Join(root, filepath.FromSlash(p))
		info, e := os.Lstat(full)
		if errors.Is(e, os.ErrNotExist) {
			if exists {
				r = append(r, FileChange{Path: p, Delete: true})
			}
			continue
		}
		if e != nil {
			return nil, e
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%q is not a regular file", p)
		}
		data, e := readBounded(full, maxArtifactBytes)
		if e != nil {
			return nil, e
		}
		if exists && executable(info.Mode()) == b.executable {
			same, e := sameBlob(ctx, root, b.oid, full)
			if e != nil {
				return nil, e
			}
			if same {
				continue
			}
		}
		r = append(r, FileChange{Path: p, Content: data, Executable: executable(info.Mode())})
	}
	return r, nil
}
func validateCandidate(ctx context.Context, root string, c Candidate, allowedPaths []string) error {
	if !commit(c.BaseCommit) {
		return errors.New("base commit must be exactly 40 hexadecimal characters")
	}
	if len(c.Files) > maxFiles || len(c.Patch) > maxArtifactBytes {
		return errors.New("artifact exceeds limits")
	}
	allowed, err := allowedSet(allowedPaths)
	if err != nil {
		return err
	}
	old, err := treeFiles(ctx, root, c.BaseCommit)
	if err != nil {
		return err
	}
	total := 0
	last := ""
	regular := make(map[string]bool, len(c.Files))
	for _, f := range c.Files {
		if err := validPath(f.Path); err != nil {
			return err
		}
		if !allowed[f.Path] {
			return fmt.Errorf("path %q is not allowed", f.Path)
		}
		if f.Path <= last {
			return errors.New("files must be unique and sorted")
		}
		last = f.Path
		if f.Delete && (len(f.Content) != 0 || f.Executable) {
			return fmt.Errorf("delete %q has content or mode", f.Path)
		}
		if !f.Delete {
			regular[f.Path] = true
			total += len(f.Content)
			if total > maxArtifactBytes {
				return errors.New("artifact content exceeds limit")
			}
		}
		if err := safePath(root, f.Path, !f.Delete); err != nil {
			return err
		}
		b, exists := old[f.Path]
		if exists && b.symlink {
			return fmt.Errorf("symlink %q is not allowed", f.Path)
		}
		if f.Delete && !exists {
			return fmt.Errorf("delete %q does not exist in base", f.Path)
		}
	}
	for p := range regular {
		for parent := filepath.ToSlash(filepath.Dir(p)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if regular[parent] {
				return fmt.Errorf("file %q conflicts with descendant %q", parent, p)
			}
		}
	}
	want, err := makePatch(ctx, root, c.BaseCommit, c.Files)
	if err != nil {
		return err
	}
	if c.Patch != want {
		return errors.New("patch does not match file changes")
	}
	sum := sha256.Sum256([]byte(c.Patch))
	if !strings.EqualFold(c.SHA256, hex.EncodeToString(sum[:])) {
		return errors.New("patch sha256 does not match")
	}
	return nil
}

// makePatch uses a clean, deterministic fixture repository: Git emits proper
// a/path and b/path headers and /dev/null sides without rewriting content bytes.
func makePatch(ctx context.Context, root, base string, files []FileChange) (string, error) {
	tmp, err := os.MkdirTemp("", "wt-review-patch-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if _, err := git(ctx, tmp, "init", "-q", "--template="); err != nil {
		return "", err
	}
	old, err := treeFiles(ctx, root, base)
	if err != nil {
		return "", err
	}
	for _, f := range files {
		if b, ok := old[f.Path]; ok {
			if b.symlink {
				return "", fmt.Errorf("symlink %q is not allowed", f.Path)
			}
			data, err := blob(ctx, root, b.oid)
			if err != nil {
				return "", err
			}
			if err := indexFixture(ctx, tmp, f.Path, data, b.executable); err != nil {
				return "", err
			}
		}
	}
	tree, err := git(ctx, tmp, "write-tree")
	if err != nil {
		return "", err
	}
	for _, f := range files {
		if f.Delete {
			if _, err := git(ctx, tmp, "update-index", "--force-remove", "--", f.Path); err != nil {
				return "", err
			}
		} else if err := indexFixture(ctx, tmp, f.Path, f.Content, f.Executable); err != nil {
			return "", err
		}
	}
	out, err := git(ctx, tmp, "diff", "--cached", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", strings.TrimSpace(string(tree)), "--")
	return string(out), err
}

func indexFixture(ctx context.Context, root, path string, data []byte, executable bool) error {
	cmd := gitCmd(ctx, root, "hash-object", "-w", "--no-filters", "--stdin")
	cmd.Stdin = bytes.NewReader(data)
	oid, err := cmd.Output()
	if err != nil {
		return err
	}
	mode := "100644"
	if executable {
		mode = "100755"
	}
	_, err = git(ctx, root, "update-index", "--add", "--cacheinfo", mode, strings.TrimSpace(string(oid)), path)
	return err
}

type treeFile struct {
	oid                 string
	executable, symlink bool
}

func treeFiles(ctx context.Context, root, base string) (map[string]treeFile, error) {
	o, err := git(ctx, root, "ls-tree", "-r", "-z", base)
	if err != nil {
		return nil, err
	}
	r := map[string]treeFile{}
	for _, rec := range bytes.Split(o, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		p := bytes.SplitN(rec, []byte{'\t'}, 2)
		if len(p) != 2 {
			return nil, errors.New("malformed tree output")
		}
		f := bytes.Fields(p[0])
		if len(f) != 3 {
			return nil, errors.New("malformed tree output")
		}
		mode := string(f[0])
		if mode != "100644" && mode != "100755" && mode != "120000" {
			continue
		}
		r[string(p[1])] = treeFile{string(f[2]), mode == "100755", mode == "120000"}
	}
	return r, nil
}
func clean(ctx context.Context, root, base string) error {
	c, e := worktreeChanges(ctx, root, base, nil)
	if e != nil {
		return e
	}
	if len(c) > 0 {
		return errors.New("worktree is not clean")
	}
	return nil
}
func readBounded(n string, limit int) ([]byte, error) {
	f, e := os.Open(n)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if e != nil {
		return nil, e
	}
	if len(b) > limit {
		return nil, errors.New("artifact content exceeds limit")
	}
	return b, nil
}
func blob(ctx context.Context, root, oid string) ([]byte, error) {
	size, err := git(ctx, root, "cat-file", "-s", oid)
	if err != nil {
		return nil, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(size)), 10, 64)
	if err != nil || n < 0 || n > maxArtifactBytes {
		return nil, errors.New("changed baseline content exceeds limit")
	}
	return git(ctx, root, "cat-file", "blob", oid)
}

// sameBlob compares the immutable object and worktree file in bounded chunks.
// In particular, an unchanged large baseline file does not consume candidate
// content budget or require retaining its bytes in memory.
func sameBlob(ctx context.Context, root, oid, name string) (bool, error) {
	cmd := gitCmd(ctx, root, "cat-file", "blob", oid)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	f, err := os.Open(name)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := cmd.Start(); err != nil {
		return false, err
	}
	left := sha256.New()
	if _, err := io.Copy(left, out); err != nil {
		_ = cmd.Wait()
		return false, err
	}
	if err := cmd.Wait(); err != nil {
		return false, err
	}
	right := sha256.New()
	if _, err := io.Copy(right, f); err != nil {
		return false, err
	}
	return bytes.Equal(left.Sum(nil), right.Sum(nil)), nil
}
func executable(m os.FileMode) bool { return m&0111 != 0 }
func nulPaths(b []byte) []string {
	var r []string
	for _, p := range bytes.Split(b, []byte{0}) {
		if len(p) > 0 {
			r = append(r, string(p))
		}
	}
	return r
}
func commit(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
func validPath(p string) error {
	if p == "" || filepath.IsAbs(p) || strings.Contains(p, "\\") || strings.HasPrefix(p, ":") || strings.ContainsAny(p, "\x00\r\n") {
		return fmt.Errorf("invalid path %q", p)
	}
	for _, x := range strings.Split(p, "/") {
		if x == "" || x == "." || x == ".." || x == ".git" || x == "egg.yaml" {
			return fmt.Errorf("invalid path %q", p)
		}
	}
	return nil
}
func allowedSet(ps []string) (map[string]bool, error) {
	r := map[string]bool{}
	for _, p := range ps {
		if e := validPath(p); e != nil {
			return nil, fmt.Errorf("invalid allowed path: %w", e)
		}
		if r[p] {
			return nil, fmt.Errorf("duplicate allowed path %q", p)
		}
		r[p] = true
	}
	return r, nil
}
func safePath(root, p string, final bool) error {
	cur := root
	xs := strings.Split(p, "/")
	for i, x := range xs {
		cur = filepath.Join(cur, x)
		info, e := os.Lstat(cur)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink component for %q", p)
		}
		if i < len(xs)-1 && !info.IsDir() {
			return fmt.Errorf("non-directory ancestor for %q", p)
		}
		if i == len(xs)-1 && final && !info.Mode().IsRegular() {
			return fmt.Errorf("final path %q is not regular", p)
		}
	}
	return nil
}
func pinnedRoot(ctx context.Context, cwd, base string) (string, error) {
	if !commit(base) {
		return "", errors.New("base commit must be exactly 40 hexadecimal characters")
	}
	abs, e := filepath.Abs(cwd)
	if e != nil {
		return "", e
	}
	abs, e = filepath.EvalSymlinks(abs)
	if e != nil {
		return "", e
	}
	o, e := git(ctx, abs, "rev-parse", "--show-toplevel")
	if e != nil {
		return "", errors.New("cwd must be a repository root")
	}
	root, e := filepath.EvalSymlinks(strings.TrimSpace(string(o)))
	if e != nil {
		return "", e
	}
	if abs != root {
		return "", errors.New("cwd must be repository root")
	}
	head, e := git(ctx, root, "rev-parse", "HEAD")
	if e != nil {
		return "", e
	}
	if !strings.EqualFold(strings.TrimSpace(string(head)), base) {
		return "", errors.New("HEAD does not match base commit")
	}
	return root, nil
}
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return gitCmd(ctx, dir, args...).Output()
}
func gitCmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	pre := []string{"-c", "core.autocrlf=false", "-c", "core.attributesfile=/dev/null", "-c", "diff.external=false", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}
	cmd := exec.CommandContext(ctx, "git", append(pre, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z", "HOME=" + filepath.Join(os.TempDir(), "wt-empty-home"), "XDG_CONFIG_HOME=" + filepath.Join(os.TempDir(), "wt-empty-config"), "PATH=" + os.Getenv("PATH")}
	return cmd
}
