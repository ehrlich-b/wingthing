package eggclient

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/protectedfile"
)

// ProtectContextSecret adds host-owned deny rules after caller policy resolution.
// Interactive and headless launches must both use this guard.
func ProtectContextSecret(policy *egg.EggConfig, c *config.ContextConfig, cwd, home string) (*egg.EggConfig, []string, error) {
	if c == nil {
		return policy, nil, nil
	}
	if err := c.Validate(); err != nil {
		return nil, nil, err
	}
	file, err := protectedfile.OpenResolved(c.SecretFile)
	if err != nil {
		return nil, nil, fmt.Errorf("context: cannot resolve secret_file: %w", err)
	}
	defer file.Close()
	resolved := file.Name()
	paths := []string{filepath.Clean(c.SecretFile)}
	if resolved != paths[0] {
		paths = append(paths, resolved)
	}
	aliases, err := file.Aliases()
	if err != nil {
		return nil, nil, fmt.Errorf("context: cannot protect secret_file aliases: %w", err)
	}
	for _, alias := range aliases {
		if !ContainsExactPath(paths, alias) {
			paths = append(paths, alias)
		}
	}
	// Resolve grants after all caller/base policy merges, using the same CWD
	// and HOME expansion as the child. Bind mounts can turn symlinks into
	// regular mountpoints, so masking only the real secret path is insufficient.
	secrets := append([]string(nil), paths...)
	fs := make([]string, 0, len(policy.FS))
	for _, entry := range policy.FS {
		mode, path, ok := strings.Cut(entry, ":")
		if !ok {
			mode, path = "rw", entry
		}
		if path == "~" {
			path = home
		} else if strings.HasPrefix(path, "~/") {
			path = filepath.Join(home, path[2:])
		} else if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		path = filepath.Clean(path)
		fs = append(fs, mode+":"+path)
		if mode == "deny" || mode == "deny-write" {
			continue
		}
		target, err := filepath.EvalSymlinks(path)
		if os.IsNotExist(err) {
			continue // Absent grants cannot be mounted by the child.
		}
		if err != nil {
			return nil, nil, fmt.Errorf("context: cannot resolve filesystem grant %q: %w", path, err)
		}
		for _, secret := range secrets {
			if contextPathWithin(target, secret) {
				return nil, nil, fmt.Errorf("context: filesystem grant %q exposes protected secret path %q", path, secret)
			}
			if target != path && contextPathWithin(secret, target) {
				suffix, _ := filepath.Rel(target, secret)
				alias := filepath.Join(path, suffix)
				if !ContainsExactPath(paths, alias) {
					paths = append(paths, alias)
				}
			}
		}
	}
	clone := *policy
	clone.FS = fs
	for _, path := range paths {
		clone.FS = append(clone.FS, "deny:"+path)
	}
	return &clone, paths, nil
}

func contextPathWithin(path, parent string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
