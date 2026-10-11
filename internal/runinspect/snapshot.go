package runinspect

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxFileBytes = 128 << 20
const maxSnapshotBytes = 256 << 20
const maxFiles = 4096

type snapshot struct {
	root          *os.Root
	path          string
	manifest      Manifest
	manifestBytes []byte
	files         map[string]File
}

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// ValidateDirectory confines reads (and CLI report writes) to the explicit
// workspace. Ancestor aliases are resolved; aliases inside it are refused.
func ValidateDirectory(workspace, path string) (string, error) {
	if workspace == "" || path == "" {
		return "", errors.New("explicit workspace and snapshot/output directory are required")
	}
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	base, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", errors.New("workspace is unavailable")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", errors.New("directory must already exist")
	}
	// Normalize the path's workspace prefix, which may use a different ancestor
	// alias from workspace (for example /var versus /private/var on macOS).
	// Keep the suffix lexical so a symlink inside the workspace still fails.
	// Use the outermost match to also refuse links back to the workspace itself.
	unaliased := absolute
	for prefix := absolute; ; {
		canonical, err := filepath.EvalSymlinks(prefix)
		if err != nil {
			return "", errors.New("directory must already exist")
		}
		if canonical == base {
			rel, err := filepath.Rel(prefix, absolute)
			if err != nil {
				return "", err
			}
			unaliased = filepath.Join(base, rel)
		}
		parent := filepath.Dir(prefix)
		if parent == prefix {
			break
		}
		prefix = parent
	}
	if resolved != unaliased || !within(base, resolved) {
		return "", errors.New("directory must be an unaliased path inside the workspace")
	}
	for _, part := range strings.Split(filepath.ToSlash(resolved), "/") {
		if part == ".wingthing" || part == ".wingthing-preview" {
			return "", errors.New("live default state paths are refused; use a completed copy")
		}
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("directory is unavailable")
	}
	return resolved, nil
}

func openSnapshot(ctx context.Context, path string, opts Options) (*snapshot, error) {
	path, err := ValidateDirectory(opts.WorkspaceRoot, path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, errors.New("snapshot is unavailable")
	}
	s := &snapshot{root: root, path: path, files: map[string]File{}}
	ok := false
	defer func() {
		if !ok {
			_ = root.Close()
		}
	}()
	data, err := s.readRaw(ManifestName)
	if err != nil {
		return nil, errors.New("snapshot.json completed-copy manifest is required")
	}
	if len(data) > 1<<20 {
		return nil, errors.New("snapshot manifest exceeds limit")
	}
	if uniqueJSON(data, &s.manifest) != nil {
		return nil, errors.New("invalid or ambiguous snapshot manifest")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&s.manifest) != nil || dec.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid snapshot manifest")
	}
	m := s.manifest
	if m.Version != 1 || !m.Completed || (m.Kind != "completed_copy" && m.Kind != "fixture") || m.CapturedAt.IsZero() {
		return nil, errors.New("snapshot requires version 1, completed copy/fixture and captured_at")
	}
	if len(m.Files) > maxFiles {
		return nil, errors.New("snapshot file count exceeds limit")
	}
	var total int64
	for _, f := range m.Files {
		digest, err := hex.DecodeString(f.SHA256)
		if !fs.ValidPath(f.Path) || f.Path == ManifestName || strings.Contains(f.Path, "\\") || len(digest) != sha256.Size || err != nil || f.SHA256 != strings.ToLower(f.SHA256) || f.Bytes < 0 || f.Bytes > maxFileBytes {
			return nil, errors.New("invalid snapshot file metadata")
		}
		if _, exists := s.files[f.Path]; exists {
			return nil, errors.New("duplicate snapshot file")
		}
		if strings.HasSuffix(f.Path, "-wal") || strings.HasSuffix(f.Path, "-shm") || strings.HasSuffix(f.Path, "-journal") {
			return nil, errors.New("snapshot requires a consolidated SQLite backup without sidecars")
		}
		total += f.Bytes
		if total > maxSnapshotBytes {
			return nil, errors.New("snapshot byte count exceeds limit")
		}
		s.files[f.Path] = f
	}
	s.manifestBytes = data
	if err := s.verify(ctx); err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}

func (s *snapshot) regular(path string) error {
	parts := strings.Split(path, "/")
	for i := range parts {
		info, err := s.root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("snapshot aliases are refused")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return errors.New("invalid snapshot directory")
		}
		if i == len(parts)-1 && !info.Mode().IsRegular() {
			return errors.New("snapshot files must be regular")
		}
	}
	return nil
}

func (s *snapshot) read(path string) ([]byte, error) {
	data, err := s.readRaw(path)
	if err != nil {
		return nil, err
	}
	// Bind the exact buffer consumed by every decoder to the inventory. Walks
	// before and after decoding cannot detect a replacement restored between them.
	if path == ManifestName {
		if !bytes.Equal(data, s.manifestBytes) {
			return nil, errors.New("snapshot manifest changed during inspection")
		}
	} else if f, exists := s.files[path]; !exists || int64(len(data)) != f.Bytes || digest(data) != f.SHA256 {
		return nil, errors.New("snapshot bytes do not match completed-copy manifest")
	}
	return data, nil
}

// Only the initial manifest read may precede the hash-verified inventory.
func (s *snapshot) readRaw(path string) ([]byte, error) {
	if !fs.ValidPath(path) || s.regular(path) != nil {
		return nil, errors.New("snapshot file is unavailable")
	}
	file, err := s.root.Open(path)
	if err != nil {
		return nil, errors.New("snapshot file is unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return nil, errors.New("snapshot file exceeds limit or is not regular")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil || len(data) > maxFileBytes {
		return nil, errors.New("snapshot file could not be read within limit")
	}
	return data, nil
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func (s *snapshot) verify(ctx context.Context) error {
	var seen, nodes int
	err := fs.WalkDir(s.root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return errors.New("snapshot inventory is unavailable")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		nodes++
		if nodes > 2*maxFiles+1 {
			return errors.New("snapshot directory count exceeds limit")
		}
		if path == "." {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("snapshot aliases are refused")
		}
		if d.IsDir() {
			return nil
		}
		seen++
		if seen > maxFiles+1 {
			return errors.New("snapshot file count exceeds limit")
		}
		if _, err := s.read(path); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if seen != len(s.files)+1 {
		return errors.New("snapshot file is missing")
	}
	return nil
}

func (s *snapshot) paths() []string {
	paths := make([]string, 0, len(s.files))
	for path := range s.files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
