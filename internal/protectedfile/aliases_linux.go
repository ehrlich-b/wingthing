//go:build linux

package protectedfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type physicalMount struct{ device, root, target string }

// Resolve bind aliases from the mount table, then verify their device/inode
// identity. Path canonicalization alone cannot recognize a physical bind.
func Aliases(control []string) ([]string, error) {
	if len(control) == 0 {
		return nil, nil
	}
	return aliases(control, nil)
}

func aliases(control []string, identities map[string]os.FileInfo) ([]string, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	var mounts []physicalMount
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 6 {
			return nil, fmt.Errorf("invalid mountinfo entry: %q", line)
		}
		mounts = append(mounts, physicalMount{fields[2], unescape.Replace(fields[3]), unescape.Replace(fields[4])})
	}
	result := append([]string(nil), control...)
	for _, path := range control {
		// Missing control files are protected through their existing ancestor too.
		existing := path
		info, err := os.Stat(existing)
		for os.IsNotExist(err) && existing != "/" {
			existing = filepath.Dir(existing)
			info, err = os.Stat(existing)
		}
		if identity := identities[path]; identity != nil && err == nil {
			if !os.SameFile(identity, info) {
				return nil, &Error{path, "changed before resolving mount aliases"}
			}
			info = identity
		}
		if err != nil {
			return nil, fmt.Errorf("inspect protected identity %s: %w", path, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, fmt.Errorf("no device/inode identity for %s", path)
		}
		device := fmt.Sprintf("%d:%d", unix.Major(uint64(stat.Dev)), unix.Minor(uint64(stat.Dev)))
		var backing physicalMount
		for _, mount := range mounts {
			if mount.device == device && pathWithin(existing, mount.target) && len(mount.target) >= len(backing.target) {
				backing = mount
			}
		}
		if backing.target == "" {
			return nil, fmt.Errorf("protected path %s has no backing mount", path)
		}
		relative, _ := filepath.Rel(backing.target, path)
		physical := filepath.Join(backing.root, relative)
		for _, mount := range mounts {
			if mount.device != device {
				continue
			}
			var original, alias string
			switch {
			case pathWithin(physical, mount.root):
				relative, _ := filepath.Rel(mount.root, physical)
				alias = filepath.Join(mount.target, relative)
				original = path
			case info.IsDir() && existing == path && pathWithin(mount.root, physical):
				relative, _ := filepath.Rel(physical, mount.root)
				original = filepath.Join(path, relative)
				alias = mount.target
			default:
				continue
			}
			// Walk missing suffixes in lockstep before comparing physical identities.
			left, right := original, alias
			li, le := os.Stat(left)
			ri, re := os.Stat(right)
			for os.IsNotExist(le) && os.IsNotExist(re) && left != "/" && right != "/" {
				left, right = filepath.Dir(left), filepath.Dir(right)
				li, le = os.Stat(left)
				ri, re = os.Stat(right)
			}
			if le != nil && !os.IsNotExist(le) {
				return nil, fmt.Errorf("inspect protected alias source %s: %w", original, le)
			}
			if re != nil && !os.IsNotExist(re) {
				return nil, fmt.Errorf("inspect protected alias %s: %w", alias, re)
			}
			if le == nil && re == nil && os.SameFile(li, ri) {
				result = append(result, alias)
			}
		}
	}
	return result, nil
}

func pathWithin(path, root string) bool {
	return path == root || strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/")
}

// Aliases returns all visible mount-table names for this opened identity.
func (f *File) Aliases() ([]string, error) {
	if _, _, _, err := inspect(f.File); err != nil {
		return nil, err
	}
	return aliases([]string{f.Name()}, map[string]os.FileInfo{f.Name(): f.Info})
}
