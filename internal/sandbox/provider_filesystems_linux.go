//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// This is deliberately a bounded contract, not a backing-layer resolver.
// Validate the host trees before any Wingthing bind, mask or HOME overlay exists.
// HOME is included because the trusted overlay grants writes to its merged view
// and the parent persists provider files back to its backing tree.
func validateLinuxProviderFilesystems(targets, writable []string, home string) error {
	if len(targets) == 0 {
		return nil
	}
	entries, err := readMountInfo("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	// Btrfs assigns different device numbers to subvolumes of one filesystem.
	// Normalize to the filesystem UUID before comparing mounted roots. Refuse
	// unavailable identities, including mounts we cannot inspect safely.
	if err := normalizeProviderFilesystemIdentities(entries, btrfsFilesystemIdentity); err != nil {
		return err
	}
	paths := append([]string(nil), targets...)
	paths = append(paths, writable...)
	paths = append(paths, os.TempDir(), "/tmp")
	if home != "" {
		paths = append(paths, home)
	}
	for _, path := range paths {
		canonical, err := CanonicalPath(path)
		if err != nil {
			return providerFilesystemError(path, "unknown", err.Error())
		}
		if err := checkLinuxProviderFilesystemTree(entries, canonical); err != nil {
			return err
		}
	}
	return nil
}

func providerFilesystemError(path, fsType, reason string) error {
	if fsType == "" {
		fsType = "unknown"
	}
	return &ProtectedWriteTargetError{Target: path, Reason: fmt.Sprintf("Linux protected provider filesystem %q rejected: %s; use plain ext4, xfs, btrfs without shared subvolume aliases, zfs or tmpfs, and remove preexisting layered/bind mounts before launch", fsType, reason)}
}

func normalizeProviderFilesystemIdentities(entries map[string]mountInfoEntry, btrfsIdentity func(string) (string, error)) error {
	for point, entry := range entries {
		if entry.FSType != "btrfs" {
			continue
		}
		identity, err := btrfsIdentity(point)
		if err != nil || identity == "" {
			return providerFilesystemError(point, entry.FSType, fmt.Sprintf("backing identity unavailable: %v", err))
		}
		entry.Device = identity
		entries[point] = entry
	}
	return nil
}

func checkLinuxProviderFilesystemTree(entries map[string]mountInfoEntry, path string) error {
	containing := ""
	for point := range entries {
		if pathWithin(path, point) && len(point) > len(containing) {
			containing = point
		}
	}
	if containing == "" {
		return providerFilesystemError(path, "unknown", "mount identity unavailable")
	}
	for point, entry := range entries {
		if point != containing && !pathWithin(point, path) {
			continue
		}
		reportedPath := point
		if point == containing {
			reportedPath = path
		}
		switch entry.FSType {
		case "ext4", "xfs", "btrfs", "zfs", "tmpfs":
		default:
			return providerFilesystemError(reportedPath, entry.FSType, "unsupported or unknown filesystem type")
		}
		if entry.Device == "" || entry.Root == "" {
			return providerFilesystemError(reportedPath, entry.FSType, "backing identity unavailable")
		}
		// Ordinary supported filesystems mount at their root. Btrfs subvolume
		// mounts may have a non-root mountinfo root, but must be disjoint from
		// every other mounted view of that filesystem, even when read-only.
		if entry.FSType != "btrfs" && entry.Root != "/" {
			return providerFilesystemError(reportedPath, entry.FSType, "preexisting bind mount")
		}
		for aliasPoint, alias := range entries {
			if aliasPoint == point || alias.Device != entry.Device {
				continue
			}
			if pathWithin(entry.Root, alias.Root) || pathWithin(alias.Root, entry.Root) {
				return providerFilesystemError(reportedPath, entry.FSType, "shared backing mount alias at "+aliasPoint)
			}
		}
	}
	return nil
}

func btrfsFilesystemIdentity(path string) (string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)
	// Linux UAPI: BTRFS_IOC_FS_INFO = _IOR(0x94, 31, 1024). The
	// filesystem UUID occupies bytes 16..31 of btrfs_ioctl_fs_info_args.
	var info [1024]byte
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), 0x8400941f, uintptr(unsafe.Pointer(&info[0])))
	if errno != 0 {
		return "", errno
	}
	var zero [16]byte
	var uuid [16]byte
	copy(uuid[:], info[16:32])
	if uuid == zero {
		return "", fmt.Errorf("empty Btrfs filesystem UUID")
	}
	return fmt.Sprintf("btrfs:%x", uuid), nil
}

func providerWritablePaths(cfg Config) []string {
	var paths []string
	for _, mount := range cfg.Mounts {
		if !mount.ReadOnly {
			paths = append(paths, mount.Source)
		}
	}
	return paths
}

func providerHome(cfg Config) string {
	if cfg.UserHome != "" {
		return cfg.UserHome
	}
	home, _ := os.UserHomeDir()
	return home
}
