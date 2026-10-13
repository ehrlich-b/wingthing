//go:build linux

package sandbox

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Linux combines a Landlock write allowlist with recursive read-only mounts.
// The mounts seal protected objects even in the writable HOME overlay; self-bind
// pins prevent substitution by renaming any ancestor. No rule may intentionally
// grant persistent writes to a protected object.
func validateLinuxProtectedTargets(cfg Config) error {
	if err := ValidateProtectedWriteTargets(cfg.ProtectedWriteTargets); err != nil {
		return err
	}
	if len(cfg.ProtectedWriteTargets) == 0 {
		return nil
	}
	for _, file := range cfg.ReadOnlyFiles {
		covered := false
		for _, target := range cfg.ProtectedWriteTargets {
			if pathWithin(file, target) && file != target {
				covered = true
			}
		}
		if !covered {
			return &ProtectedWriteTargetError{Target: file, Reason: "runtime read-only file must be inside a protected tree"}
		}
	}
	for _, m := range cfg.Mounts {
		if m.Target != "" && m.Target != m.Source {
			return &ProtectedWriteTargetError{Reason: "protected sessions require identity filesystem mounts"}
		}
	}
	for _, target := range cfg.ProtectedWriteTargets {
		target, err := CanonicalPath(target)
		if err != nil {
			return &ProtectedWriteTargetError{Target: target, Reason: err.Error()}
		}
		if target == "/" || pathWithin(target, "/proc") || pathWithin(target, "/dev") || pathWithin(target, "/sys") {
			return &ProtectedWriteTargetError{Target: target, Reason: "not an ordinary host state path"}
		}
		for _, m := range cfg.Mounts {
			if m.ReadOnly {
				continue
			}
			source, err := CanonicalPath(m.Source)
			if err != nil {
				return &ProtectedWriteTargetError{Target: target, Reason: err.Error()}
			}
			if pathWithin(target, source) || pathWithin(source, target) || m.UseRegex && strings.HasPrefix(target, source) {
				return &ProtectedWriteTargetError{Target: target, Rule: m.Source, Reason: "writable sandbox rule overlaps protected target"}
			}
		}
		for _, tmp := range []string{os.TempDir(), "/tmp"} {
			tmp, err = CanonicalPath(tmp)
			if err != nil {
				return err
			}
			if pathWithin(target, tmp) || pathWithin(tmp, target) {
				return &ProtectedWriteTargetError{Target: target, Rule: tmp, Reason: "temporary write directory overlaps protected target"}
			}
		}
	}
	return nil
}

// Probe only for a protected session. Existing unprotected eggs keep their
// namespace/seccomp capability contract on kernels without Landlock.
var providerLandlockABI = landlockABI

func CheckProviderWriteProtection() error {
	abi, err := providerLandlockABI()
	return requireLandlockABI(abi, err)
}

func requireLandlockABI(abi int, err error) error {
	if err != nil {
		return &ProtectedWriteTargetError{Reason: fmt.Sprintf("Linux provider write protection requires enabled Landlock ABI >= 3: %v", err)}
	}
	if abi < 3 {
		return &ProtectedWriteTargetError{Reason: fmt.Sprintf("Linux provider write protection requires Landlock ABI >= 3 (rename/link and truncate); kernel ABI is %d", abi)}
	}
	return nil
}

// Called inside the private mount namespace, before and after HOME overlay
// construction. Missing targets are refused: there is no safe missing-name
// mountpoint on a writable parent. This never creates a host-owned target.
func sealLinuxProtectedTargets(targets []string) ([]string, error) {
	var pins []string
	seen := map[string]bool{}
	for _, target := range targets {
		if err := checkProtectedHardlinks(target); err != nil {
			return nil, err
		}
		fd, err := openPolicyPath(target, false)
		if err != nil {
			return nil, fmt.Errorf("open protected target %s: %w", target, err)
		}
		var st unix.Stat_t
		err = unix.Fstat(fd, &st)
		if err == nil && st.Mode&unix.S_IFMT != unix.S_IFDIR && st.Mode&unix.S_IFMT != unix.S_IFREG {
			err = fmt.Errorf("target must be a directory or regular file")
		}
		if err == nil && st.Mode&unix.S_IFMT == unix.S_IFREG && st.Nlink != 1 {
			err = fmt.Errorf("protected file has %d hard links", st.Nlink)
		}
		via := fmt.Sprintf("/proc/self/fd/%d", fd)
		if err == nil {
			err = unix.Mount(via, via, "", unix.MS_BIND|unix.MS_REC, "")
		}
		if err == nil {
			err = unix.MountSetattr(unix.AT_FDCWD, target, unix.AT_RECURSIVE, &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY})
		}
		unix.Close(fd)
		if err != nil {
			return nil, fmt.Errorf("recursively seal protected target %s: %w", target, err)
		}
		for parent := filepath.Dir(target); parent != "/"; parent = filepath.Dir(parent) {
			if !seen[parent] {
				seen[parent] = true
				pins = append(pins, parent)
			}
		}
	}
	return pins, verifyLinuxProtectedMounts(targets)
}

func verifyLinuxProtectedMounts(targets []string) error {
	entries, err := readMountInfo("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err := verifyMountEntries(entries, []expectedMount{{Path: target, ReadOnly: true}}); err != nil {
			return err
		}
		for path, entry := range entries {
			if pathWithin(path, target) && !entry.Options["ro"] {
				return fmt.Errorf("protected submount %s is writable", path)
			}
		}
	}
	return nil
}

// Existing links can expose protected inodes through writable paths. Reject
// hardlinks to every non-directory inode and symlinks whose referent is missing
// or outside the protected tree. Inspect metadata only; never consume contents.
func checkProtectedHardlinks(target string) error {
	return filepath.WalkDir(target, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		var stat unix.Stat_t
		if err := unix.Lstat(path, &stat); err != nil {
			return err
		}
		st := &stat
		if st.Mode&unix.S_IFMT != unix.S_IFDIR && st.Nlink != 1 {
			return &ProtectedWriteTargetError{Target: path, Reason: fmt.Sprintf("protected inode has %d hard links", st.Nlink)}
		}
		if st.Mode&unix.S_IFMT == unix.S_IFLNK {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return &ProtectedWriteTargetError{Target: path, Reason: "protected symlink referent is unavailable: " + err.Error()}
			}
			if !pathWithin(resolved, target) {
				return &ProtectedWriteTargetError{Target: path, Reason: "protected symlink resolves outside its tree"}
			}
		}
		return nil
	})
}

// Resolve mount aliases by filesystem device and mounted root, rather than by
// path spelling. A preexisting bind can expose protected inodes in a workspace
// or temporary write grant even though no lexical mount overlaps the target.
func validateLinuxProtectedAliases(targets []string) error {
	if len(targets) == 0 {
		return nil
	}
	entries, err := readMountInfo("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	return checkProtectedMountAliases(entries, targets)
}

func checkProtectedMountAliases(entries map[string]mountInfoEntry, targets []string) error {
	for _, target := range targets {
		canonical, err := CanonicalPath(target)
		if err != nil {
			return err
		}
		// Each mounted subtree has its own device/root identity, including mounts
		// below the protected directory. Check external aliases of those as well.
		segments := []string{canonical}
		for point := range entries {
			if point != canonical && pathWithin(point, canonical) {
				segments = append(segments, point)
			}
		}
		for _, segment := range segments {
			sourcePoint := ""
			for point := range entries {
				if pathWithin(segment, point) && len(point) > len(sourcePoint) {
					sourcePoint = point
				}
			}
			if sourcePoint == "" {
				return &ProtectedWriteTargetError{Target: target, Reason: "protected mount identity unavailable"}
			}
			source := entries[sourcePoint]
			rel, err := filepath.Rel(sourcePoint, segment)
			if err != nil {
				return err
			}
			objectRoot := filepath.Join(source.Root, rel)
			for point, alias := range entries {
				if alias.Device != source.Device {
					continue
				}
				aliasPath := ""
				if pathWithin(objectRoot, alias.Root) {
					rel, err := filepath.Rel(alias.Root, objectRoot)
					if err != nil {
						return err
					}
					aliasPath = filepath.Join(point, rel)
				} else if pathWithin(alias.Root, objectRoot) {
					aliasPath = point
				}
				if aliasPath != "" && !pathWithin(aliasPath, canonical) {
					return &ProtectedWriteTargetError{Target: target, Rule: aliasPath, Reason: "protected filesystem has an external mount alias; remove the alias before launch"}
				}
			}
		}
	}
	return nil
}

func openRuntimeReadOnlyFiles(paths []string) (map[string]int, error) {
	files := map[string]int{}
	for _, path := range paths {
		fd, err := openPolicyPath(path, false)
		if err == nil {
			var st unix.Stat_t
			err = unix.Fstat(fd, &st)
			if err == nil && (st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1) {
				err = fmt.Errorf("runtime file must be regular with one link")
			}
		}
		if err != nil {
			if fd >= 0 {
				unix.Close(fd)
			}
			for _, prior := range files {
				unix.Close(prior)
			}
			return nil, err
		}
		if prior, ok := files[path]; ok {
			unix.Close(prior)
		}
		files[path] = fd
	}
	return files, nil
}

func runtimeFilesWithin(files map[string]int, root string) bool {
	for path := range files {
		if pathWithin(path, root) && path != root {
			return true
		}
	}
	return false
}

func exposeRuntimeReadOnlyFiles(files map[string]int, root string) error {
	for path, fd := range files {
		if !pathWithin(path, root) || path == root {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if err = file.Close(); err != nil {
			return err
		}
		if err := unix.Mount(fmt.Sprintf("/proc/self/fd/%d", fd), path, "", unix.MS_BIND, ""); err != nil {
			return err
		}
		if err := remountBindReadonly(path); err != nil {
			return err
		}
	}
	return nil
}
