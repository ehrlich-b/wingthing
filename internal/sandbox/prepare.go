package sandbox

import (
	"fmt"
	"strings"
)

type DirectoryWriteCheck struct {
	Allowed, Verified              bool
	Reason, DenialKind, DenialPath string
}

// CheckDirectoryWrites is also used by native preparation. Metadata-only
// admission can establish declared denials, while a successful mount grant on
// Linux still requires its native namespace operations. Seatbelt checks the
// actual emitted profile through the existing write-rule parser.
func CheckDirectoryWrites(cfg Config, path, platform string) DirectoryWriteCheck {
	path, _ = CanonicalPath(path)
	for _, entry := range []struct {
		kind  string
		paths []string
	}{{"deny", cfg.Deny}, {"deny-write", cfg.DenyWrite}} {
		if entry.kind == "deny-write" && platform != "linux" {
			continue
		}
		for _, denied := range entry.paths {
			denied, _ = CanonicalPath(denied)
			if pathWithin(path, denied) {
				if denied == "/" && platform == "linux" {
					return DirectoryWriteCheck{Reason: "Linux root-deny policy requires native verification of its mount exceptions", DenialKind: entry.kind, DenialPath: denied}
				}
				return DirectoryWriteCheck{Verified: true, Reason: "effective " + entry.kind + " rule blocks the required path: " + path, DenialKind: entry.kind, DenialPath: denied}
			}
		}
	}
	granted := false
	for _, mount := range cfg.Mounts {
		source, _ := CanonicalPath(mount.Source)
		if !mount.ReadOnly && (pathWithin(path, source) || platform == "darwin" && mount.UseRegex && strings.HasPrefix(path, source)) {
			granted = true
		}
	}
	if !granted {
		return DirectoryWriteCheck{Reason: "no declared writable grant for required path: " + path}
	}
	if platform == "darwin" {
		if err := checkSeatbeltDirectoryWrites(cfg, path); err != nil {
			return DirectoryWriteCheck{Verified: true, Reason: err.Error()}
		}
	}
	return DirectoryWriteCheck{Allowed: true, Verified: true}
}

func validateDeclaredCallbackDirs(cfg Config, platform string) error {
	for _, dir := range cfg.RequiredWriteDirs {
		check := CheckDirectoryWrites(cfg, dir, platform)
		if check.Verified && !check.Allowed {
			return fmt.Errorf("required callback spool: %s", check.Reason)
		}
	}
	return nil
}
