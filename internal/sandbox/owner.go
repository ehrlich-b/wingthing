package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const sandboxOwnerFile = ".owner.lock"

type sandboxOwner struct {
	PID   int    `json:"pid"`
	Start string `json:"start"`
	Boot  string `json:"boot"`
}

// sandboxDirectory retains the exclusive lock until teardown has finished.
// Close-on-exec prevents a provider from accidentally extending ownership.
type sandboxDirectory struct {
	path string
	lock *os.File
	once sync.Once
	err  error
}

func newSandboxDirectory() (*sandboxDirectory, error) {
	SweepStaleDirectories()
	boot, err := sandboxBootID()
	if err != nil {
		return nil, fmt.Errorf("sandbox boot identity: %w", err)
	}
	start, err := sandboxProcessStart(os.Getpid())
	if err != nil {
		return nil, fmt.Errorf("sandbox process identity: %w", err)
	}
	dir, err := os.MkdirTemp("", "wt-sandbox-*")
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, sandboxOwnerFile), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	}
	if err == nil {
		err = json.NewEncoder(f).Encode(sandboxOwner{PID: os.Getpid(), Start: start, Boot: boot})
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		if f != nil {
			_ = f.Close()
		}
		return nil, fmt.Errorf("sandbox ownership: %w", err)
	}
	return &sandboxDirectory{path: dir, lock: f}, nil
}

func (d *sandboxDirectory) destroy() error {
	d.once.Do(func() {
		d.err = os.RemoveAll(d.path)
		d.err = errors.Join(d.err, d.lock.Close())
	})
	return d.err
}

type sweepBudget struct {
	remaining int
	deadline  time.Time
}

var errSweepBound = errors.New("sandbox sweep count or time limit reached")

func (b *sweepBudget) take() error {
	if b.remaining <= 0 || !time.Now().Before(b.deadline) {
		return errSweepBound
	}
	b.remaining--
	return nil
}

// SweepStaleDirectories reclaims only unlocked sandboxes with a provably dead
// owner. Legacy, unreadable and malformed ownership is retained and logged.
// The temp root is pinned with os.Root: even concurrent symlink replacement
// cannot redirect traversal outside it. Symlink owner locks are refused.
// Limits include scanning unrelated entries and deleting nested contents; a
// partially cleaned directory keeps its owner record for the next startup.
func SweepStaleDirectories() {
	sweepSandboxDirectories(os.TempDir(), &sweepBudget{remaining: 4096, deadline: time.Now().Add(200 * time.Millisecond)}, log.Printf)
}

func sweepSandboxDirectories(path string, budget *sweepBudget, report func(string, ...any)) {
	root, err := os.OpenRoot(path)
	if err != nil {
		report("sandbox sweep: open root: %v", err)
		return
	}
	defer root.Close()
	entries, err := root.Open(".")
	if err != nil {
		report("sandbox sweep: read root: %v", err)
		return
	}
	defer entries.Close()
	boot, err := sandboxBootID()
	if err != nil {
		report("sandbox sweep: cannot determine boot identity: %v", err)
		return
	}
	for count := 0; ; count++ {
		if err := budget.take(); err != nil || count >= 256 {
			report("sandbox sweep: %v", errSweepBound)
			return
		}
		names, err := entries.Readdirnames(1)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			report("sandbox sweep: read root: %v", err)
			return
		}
		name := names[0]
		if !strings.HasPrefix(name, "wt-sandbox-") {
			continue
		}
		if err := sweepSandboxDirectory(root, name, boot, budget); err != nil {
			report("sandbox sweep: kept %s: %v", name, err)
		} else {
			report("sandbox sweep: removed %s: owner is dead", name)
		}
	}
}

func ownedDirectory(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid()) && info.IsDir() && info.Mode().Perm()&0077 == 0
}

func sweepSandboxDirectory(root *os.Root, name, boot string, budget *sweepBudget) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !ownedDirectory(info) {
		return errors.New("refusing symlink, non-private or foreign directory")
	}
	dir, err := root.OpenRoot(name)
	if err != nil {
		return err
	}
	defer dir.Close()
	opened, err := dir.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("directory changed during inspection")
	}
	linkInfo, err := dir.Lstat(sandboxOwnerFile)
	if err != nil || !linkInfo.Mode().IsRegular() {
		return errors.New("unreadable or symlink owner lock")
	}
	f, err := dir.OpenFile(sandboxOwnerFile, os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("unreadable owner lock: %w", err)
	}
	defer f.Close()
	lockInfo, err := f.Stat()
	if err != nil {
		return err
	}
	stat, ok := lockInfo.Sys().(*syscall.Stat_t)
	if !ok || !os.SameFile(linkInfo, lockInfo) || !lockInfo.Mode().IsRegular() || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 || lockInfo.Mode().Perm()&0600 != 0600 {
		return errors.New("unreadable or unsafe owner lock")
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("owner lock held or unavailable: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return fmt.Errorf("read owner: %w", err)
	}
	var owner sandboxOwner
	if len(data) > 4096 || json.Unmarshal(data, &owner) != nil || owner.PID <= 0 || owner.PID > 1<<31-1 || !validSandboxStart(owner.Start) || len(owner.Boot) != 36 || uuid.Validate(owner.Boot) != nil {
		return errors.New("cannot determine ownership")
	}
	if strings.EqualFold(owner.Boot, boot) {
		start, err := sandboxProcessStart(owner.PID)
		if err == nil && start == owner.Start {
			return errors.New("owner is still alive")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("cannot determine owner process: %w", err)
		}
	}
	if err := removeSandboxContents(dir, ".", budget, 0); err != nil {
		return err
	}
	// Retain the lock until all contents are gone, and recheck identities before
	// unlinking it. Concurrent sweepers cannot acquire it during deletion.
	current, err := root.Lstat(name)
	if err != nil || !os.SameFile(info, current) {
		return errors.New("directory changed during cleanup")
	}
	current, err = dir.Lstat(sandboxOwnerFile)
	if err != nil || !os.SameFile(lockInfo, current) {
		return errors.New("owner lock changed during cleanup")
	}
	if err := budget.take(); err != nil {
		return err
	}
	if err := dir.Remove(sandboxOwnerFile); err != nil {
		return err
	}
	return root.Remove(name)
}

func removeSandboxContents(dir *os.Root, path string, budget *sweepBudget, depth int) error {
	if depth > 32 {
		return errSweepBound
	}
	f, err := dir.OpenFile(path, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		if err := budget.take(); err != nil {
			return err
		}
		names, err := f.Readdirnames(1)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if path == "." && names[0] == sandboxOwnerFile {
			continue
		}
		child := filepath.Join(path, names[0])
		info, err := dir.Lstat(child)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if err := removeSandboxContents(dir, child, budget, depth+1); err != nil {
				return err
			}
		}
		// Remove unlinks a symlink itself; it never traverses its target.
		if err := dir.Remove(child); err != nil {
			return err
		}
	}
}
