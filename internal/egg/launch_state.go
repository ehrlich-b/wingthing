package egg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

// LaunchState isolates preparation's filesystem effects. Dry preparation runs
// the same MkdirAll and pinned provider-directory opens in a disposable mirror.
// It copies directory-entry metadata only, never provider files or credentials.
type LaunchState interface {
	MkdirAll(string) error
	ProviderSpool(home, relative string) error
	ClaudeSettings(args []string, home, eggDir, providerID, cwd string, policy *sandbox.Config) (argv []string, deferred bool, err error)
}

type nativeLaunchState struct{}

func NativeLaunchState() LaunchState { return nativeLaunchState{} }

func (nativeLaunchState) MkdirAll(path string) error { return os.MkdirAll(path, 0700) }

func pinProviderDirectory(home, relative string) error {
	root, err := openProviderHome(home)
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := openProviderPath(root, relative, true)
	if err != nil {
		return err
	}
	return dir.Close()
}

func (s nativeLaunchState) ProviderSpool(home, relative string) error {
	if err := s.MkdirAll(filepath.Join(home, relative)); err != nil {
		return err
	}
	return pinProviderDirectory(home, relative)
}

func (nativeLaunchState) ClaudeSettings(args []string, home, eggDir, providerID, cwd string, policy *sandbox.Config) ([]string, bool, error) {
	argv, err := prepareClaudeLifecycleArgs(args, home, eggDir, providerID, cwd, policy)
	return argv, false, err
}

type dryLaunchState struct {
	root string
	seen map[string]bool
}

// NewDryLaunchState owns a fresh, empty root supplied by the caller. All absolute
// symlink destinations are rebased into it, including dangling ones. Relative
// links are also rebased: even a link containing many .. components cannot write
// outside the mirror. The caller removes the root after preparation.
func NewDryLaunchState(root string) (LaunchState, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("absolute disposable state root required")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("disposable state root must be a directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		return nil, errors.New("disposable state root must be empty")
	}
	return &dryLaunchState{root: root, seen: map[string]bool{}}, nil
}

func (s *dryLaunchState) mapped(path string) string {
	return filepath.Join(s.root, "fs", strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator)))
}

// snapshot walks only the selected paths and their symlink destinations; it
// never lists a source directory or reads a source file. Existing non-directory
// entries become empty regular placeholders, retaining MkdirAll's refusal.
func (s *dryLaunchState) snapshot(path string, depth int) error {
	if !filepath.IsAbs(path) || strings.IndexByte(path, 0) >= 0 {
		return errors.New("absolute state path required")
	}
	path = filepath.Clean(path)
	if s.seen[path] {
		return nil
	}
	if depth > 64 || len(s.seen) >= 512 {
		return errors.New("state metadata mirror exceeds traversal bound")
	}
	s.seen[path] = true
	parent := filepath.Dir(path)
	if parent != path {
		if err := s.snapshot(parent, depth+1); err != nil {
			return err
		}
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	destination := s.mapped(path)
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(parent, target)
		}
		if err := s.snapshot(target, depth+1); err != nil {
			return err
		}
		err = os.Symlink(s.mapped(target), destination)
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return os.MkdirAll(destination, 0700)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return file.Close()
}

func (s *dryLaunchState) originalError(err error) error {
	if err == nil {
		return nil
	}
	// Scratch identities must never affect deterministic report output.
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), filepath.Join(s.root, "fs"), ""))
}

func (s *dryLaunchState) MkdirAll(path string) error {
	if err := s.snapshot(path, 0); err != nil {
		return s.originalError(err)
	}
	return s.originalError(os.MkdirAll(s.mapped(path), 0700))
}

func (s *dryLaunchState) ProviderSpool(home, relative string) error {
	if err := s.MkdirAll(filepath.Join(home, relative)); err != nil {
		return err
	}
	return s.originalError(pinProviderDirectory(s.mapped(home), relative))
}

func (*dryLaunchState) ClaudeSettings(args []string, _, _, _, _ string, _ *sandbox.Config) ([]string, bool, error) {
	// Merging caller settings can read secrets. Keep this exact step behind
	// LaunchState and explicitly defer it instead of copying credential content.
	return args, true, nil
}

type recordingLaunchState struct {
	LaunchState
	spools []string
}

func (s *recordingLaunchState) ProviderSpool(home, relative string) error {
	s.spools = append(s.spools, filepath.Join(home, relative))
	return s.LaunchState.ProviderSpool(home, relative)
}
