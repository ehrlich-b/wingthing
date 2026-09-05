package reviewjob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureApplyRoundTrip(t *testing.T) {
	root, base := fixture(t, map[string]string{"keep.txt": "old\n", "gone.txt": "gone\n", "bin": "old"})
	write(t, root, "keep.txt", "new\n", 0644)
	write(t, root, "added.txt", "added\n", 0755)
	write(t, root, "bin", string([]byte{0, 1, 2, 0}), 0755)
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	allowed := []string{"keep.txt", "gone.txt", "bin", "added.txt"}
	c, err := Capture(context.Background(), root, base, allowed)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Files) != 4 || c.SHA256 == "" || !strings.Contains(c.Patch, "diff --git") {
		t.Fatalf("bad candidate: %#v", c)
	}
	if !strings.Contains(c.Patch, "added.txt") {
		t.Fatal("addition is missing from patch")
	}
	target, targetBase := fixture(t, map[string]string{"keep.txt": "old\n", "gone.txt": "gone\n", "bin": "old"})
	if targetBase != base {
		t.Fatal("fixtures should have equivalent commits")
	}
	// The serialized patch itself must apply to a fresh pinned worktree, not
	// merely reproduce through this package's own Apply implementation.
	applyPatch(t, target, c.Patch, "--check")
	applyPatch(t, target, c.Patch)
	assertCandidateTree(t, target, c)
	gitApplied, err := Capture(context.Background(), target, base, allowed)
	if err != nil || gitApplied.Patch != c.Patch {
		t.Fatalf("git apply did not reproduce artifact: %v", err)
	}
	target, targetBase = fixture(t, map[string]string{"keep.txt": "old\n", "gone.txt": "gone\n", "bin": "old"})
	if targetBase != base {
		t.Fatal("fixture pin changed")
	}
	if err := Apply(context.Background(), target, c, allowed); err != nil {
		t.Fatal(err)
	}
	assertCandidateTree(t, target, c)
	got, err := Capture(context.Background(), target, base, allowed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Patch != c.Patch || got.SHA256 != c.SHA256 || len(got.Files) != len(c.Files) {
		t.Fatalf("round trip mismatch\nwant %#v\ngot %#v", c, got)
	}
}

func TestCaptureAllowsUntouchedProtectedAndLargeBaselineFiles(t *testing.T) {
	root, base := fixture(t, map[string]string{
		"egg.yaml": "ordinary baseline configuration\n",
		"large":    strings.Repeat("x", maxArtifactBytes+1),
		"a":        "old",
	})
	write(t, root, "a", "new", 0644)
	c, err := Capture(context.Background(), root, base, []string{"a"})
	if err != nil || len(c.Files) != 1 || c.Files[0].Path != "a" {
		t.Fatalf("unchanged protected/large baseline blocked capture: %#v %v", c, err)
	}
}

func TestCaptureExplicitIgnoredAdditionIsInExactPatch(t *testing.T) {
	files := map[string]string{".gitignore": "generated.dat\n", "tracked": "base\n"}
	root, base := fixture(t, files)
	write(t, root, "generated.dat", "required candidate bytes\n", 0644)
	c, err := Capture(context.Background(), root, base, []string{"generated.dat"})
	if err != nil || len(c.Files) != 1 || !strings.Contains(c.Patch, "required candidate bytes") {
		t.Fatalf("ignored allowed addition omitted: %+v %v", c, err)
	}
	target, _ := fixture(t, files)
	applyPatch(t, target, c.Patch)
	assertCandidateTree(t, target, c)
}

func TestCaptureExecutableOnlyTransitionAndAddedContentTamper(t *testing.T) {
	root, base := fixture(t, map[string]string{"a": "same", "old": "old"})
	if err := os.Chmod(filepath.Join(root, "a"), 0755); err != nil {
		t.Fatal(err)
	}
	write(t, root, "added", "actual addition", 0755)
	c, err := Capture(context.Background(), root, base, []string{"a", "added"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Patch, "old mode 100644\nnew mode 100755") {
		t.Fatalf("executable-only transition omitted: %s", c.Patch)
	}
	target, _ := fixture(t, map[string]string{"a": "same", "old": "old"})
	applyPatch(t, target, c.Patch, "--check")
	applyPatch(t, target, c.Patch)
	assertCandidateTree(t, target, c)
	bad := c
	bad.Files = append([]FileChange(nil), c.Files...)
	for i := range bad.Files {
		if bad.Files[i].Path == "added" {
			bad.Files[i].Content = []byte("tampered addition")
		}
	}
	target, _ = fixture(t, map[string]string{"a": "same", "old": "old"})
	if err := Apply(context.Background(), target, bad, []string{"a", "added"}); err == nil {
		t.Fatal("tampered added content accepted")
	}
	if _, err := os.Stat(filepath.Join(target, "added")); !os.IsNotExist(err) {
		t.Fatalf("invalid apply created addition: %v", err)
	}
}

func TestCaptureLiteralPrefixesAndUnsafePaths(t *testing.T) {
	root, base := fixture(t, map[string]string{"a": "old"})
	write(t, root, "a", "literal a/old/ and b/new/ survive\n", 0644)
	c, err := Capture(context.Background(), root, base, []string{"a"})
	if err != nil || !strings.Contains(c.Patch, "literal a/old/ and b/new/") {
		t.Fatalf("literal content corrupted: %v %q", err, c.Patch)
	}
	target, _ := fixture(t, map[string]string{"a": "old"})
	applyPatch(t, target, c.Patch, "--check")
	// Nested metadata, pathspec magic, and a final symlink are all rejected
	// before a target write occurs.
	for _, p := range []string{"x/.git/a", "x/egg.yaml/a", ":(glob)*", "a\n"} {
		bad := c
		bad.Files = append([]FileChange(nil), c.Files...)
		bad.Files[0].Path = p
		if Apply(context.Background(), target, bad, []string{"a", p}) == nil {
			t.Fatalf("unsafe path accepted: %q", p)
		}
	}
	if err := os.Remove(filepath.Join(target, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/tmp", filepath.Join(target, "a")); err != nil {
		t.Fatal(err)
	}
	if Apply(context.Background(), target, c, []string{"a"}) == nil {
		t.Fatal("final symlink accepted")
	}
	if err := os.Remove(filepath.Join(target, "a")); err != nil {
		t.Fatal(err)
	}
	escape := t.TempDir()
	if err := os.Symlink(escape, filepath.Join(target, "dir")); err != nil {
		t.Fatal(err)
	}
	parent := c
	parent.Files = append([]FileChange(nil), c.Files...)
	parent.Files[0].Path = "dir/a"
	if Apply(context.Background(), target, parent, []string{"dir/a"}) == nil {
		t.Fatal("parent symlink accepted")
	}
	if _, err := os.Lstat(filepath.Join(escape, "a")); err == nil {
		t.Fatal("parent symlink apply escaped target")
	}
}

func TestCaptureIgnoresIndexBitsAndDoesNotRunFiltersOrFsmonitor(t *testing.T) {
	root, base := fixture(t, map[string]string{
		"a":              "old",
		".gitignore":     "a\n",
		".gitattributes": "a filter=sentinel\n",
	})
	// These would leave a visible marker if Capture invoked either repository
	// extension. The tracked file is also marked assume-unchanged, which must
	// not hide its actual bytes from the direct tree comparison.
	run(t, root, "update-index", "--assume-unchanged", "a")
	run(t, root, "config", "filter.sentinel.clean", "sh -c 'touch filter-ran; cat'")
	run(t, root, "config", "core.fsmonitor", "sh -c 'touch fsmonitor-ran'")
	write(t, root, "a", "new", 0644)
	c, err := Capture(context.Background(), root, base, []string{"a"})
	if err != nil || len(c.Files) != 1 || string(c.Files[0].Content) != "new" {
		t.Fatalf("assume-unchanged change missed: %#v %v", c, err)
	}
	for _, marker := range []string{"filter-ran", "fsmonitor-ran"} {
		if _, err := os.Stat(filepath.Join(root, marker)); !os.IsNotExist(err) {
			t.Fatalf("repository extension ran: %s (%v)", marker, err)
		}
	}
}

func TestRejectsScopeSymlinkAndBadApplyWithoutSideEffects(t *testing.T) {
	root, base := fixture(t, map[string]string{"ok": "old"})
	write(t, root, "outside", "no", 0644)
	if _, err := Capture(context.Background(), root, base, []string{"ok"}); err == nil {
		t.Fatal("scope drift accepted")
	}
	if err := os.Remove(filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ok", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(context.Background(), root, base, []string{"ok", "link"}); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Remove(filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "ok", "new", 0644)
	c, err := Capture(context.Background(), root, base, []string{"ok"})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := fixture(t, map[string]string{"ok": "old"})
	c.Patch += "tamper"
	if err := Apply(context.Background(), target, c, []string{"ok"}); err == nil {
		t.Fatal("tampered patch accepted")
	}
	b, err := os.ReadFile(filepath.Join(target, "ok"))
	if err != nil || string(b) != "old" {
		t.Fatalf("invalid apply changed target: %q %v", b, err)
	}
}

func TestValidationLimitsAndPin(t *testing.T) {
	root, base := fixture(t, map[string]string{"a": "x"})
	write(t, root, "a", strings.Repeat("x", maxArtifactBytes+1), 0644)
	if _, err := Capture(context.Background(), root, base, []string{"a"}); err == nil {
		t.Fatal("oversize accepted")
	}
	if _, err := Capture(context.Background(), root, strings.Repeat("0", 40), []string{"a"}); err == nil {
		t.Fatal("bad pin accepted")
	}
}

func TestApplyRejectsMalformedContentAndHash(t *testing.T) {
	root, base := fixture(t, map[string]string{"a": "old"})
	write(t, root, "a", "new", 0644)
	c, err := Capture(context.Background(), root, base, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := fixture(t, map[string]string{"a": "old"})
	badPath := c
	badPath.Files = append([]FileChange(nil), c.Files...)
	badPath.Files[0].Path = "../a"
	if err := Apply(context.Background(), target, badPath, []string{"a"}); err == nil {
		t.Fatal("traversal accepted")
	}
	badContent := c
	badContent.Files = append([]FileChange(nil), c.Files...)
	badContent.Files[0].Content = []byte("other")
	if err := Apply(context.Background(), target, badContent, []string{"a"}); err == nil {
		t.Fatal("tampered content accepted")
	}
	badHash := c
	badHash.SHA256 = strings.Repeat("0", 64)
	if err := Apply(context.Background(), target, badHash, []string{"a"}); err == nil {
		t.Fatal("tampered hash accepted")
	}
}

func TestApplyRejectsCandidateAncestorConflict(t *testing.T) {
	_, base := fixture(t, map[string]string{})
	target, _ := fixture(t, map[string]string{})
	c := Candidate{BaseCommit: base, Files: []FileChange{
		{Path: "a", Content: []byte("file")},
		{Path: "a/b", Content: []byte("child")},
	}}
	s := sha256.Sum256(nil)
	c.SHA256 = hex.EncodeToString(s[:])
	if err := Apply(context.Background(), target, c, []string{"a", "a/b"}); err == nil {
		t.Fatal("file/descendant conflict accepted")
	}
	if _, err := os.Lstat(filepath.Join(target, "a")); !os.IsNotExist(err) {
		t.Fatalf("invalid conflict changed target: %v", err)
	}
}

func fixture(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	root := t.TempDir()
	run(t, root, "init")
	run(t, root, "config", "user.email", "t@example.test")
	run(t, root, "config", "user.name", "test")
	for p, v := range files {
		write(t, root, p, v, 0644)
	}
	run(t, root, "add", "-f", ".")
	run(t, root, "commit", "--allow-empty", "--no-gpg-sign", "-m", "base")
	return root, strings.TrimSpace(run(t, root, "rev-parse", "HEAD"))
}
func write(t *testing.T, root, path, content string, mode os.FileMode) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z")
	b, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, b)
	}
	return string(b)
}

func applyPatch(t *testing.T, dir, patch string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"apply", "--binary"}, args...)...)
	c.Dir, c.Stdin = dir, bytes.NewBufferString(patch)
	b, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git apply %v: %v: %s", args, err, b)
	}
}

func assertCandidateTree(t *testing.T, root string, c Candidate) {
	t.Helper()
	for _, f := range c.Files {
		name := filepath.Join(root, filepath.FromSlash(f.Path))
		if f.Delete {
			if _, err := os.Lstat(name); !os.IsNotExist(err) {
				t.Fatalf("deleted %s remains: %v", f.Path, err)
			}
			continue
		}
		info, err := os.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("applied %s is not regular: %v", f.Path, err)
		}
		got, err := os.ReadFile(name)
		if err != nil || !bytes.Equal(got, f.Content) || executable(info.Mode()) != f.Executable {
			t.Fatalf("applied %s differs: bytes=%q mode=%v err=%v", f.Path, got, info.Mode(), err)
		}
	}
}
