package egg

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
)

// Claude's ambient setting sources remain active in an ordinary wt claude.
// A role must fail visibly when one disables hooks; do not silently overwrite
// that setting or change the settings of non-role sessions.
func validateAmbientRoleHooks(args []string, home, cwd string, policies ...*sandbox.Config) error {
	sources := map[string]bool{"user": true, "project": true, "local": true}
	for i := 0; i < providerOptionsEnd(args); i++ {
		arg := args[i]
		if arg == "--restricted" {
			sources = map[string]bool{}
			break
		}
		value := ""
		if arg == "--setting-sources" && i+1 < providerOptionsEnd(args) {
			i++
			value = args[i]
		} else if strings.HasPrefix(arg, "--setting-sources=") {
			value = strings.TrimPrefix(arg, "--setting-sources=")
		} else {
			continue
		}
		sources = map[string]bool{}
		for _, source := range strings.Split(value, ",") {
			sources[source] = true
		}
	}
	var paths []string
	if sources["user"] {
		paths = append(paths, filepath.Join(home, ".claude", "settings.json"))
	}
	if cwd != "" && (sources["project"] || sources["local"]) {
		for dir := filepath.Clean(cwd); ; dir = filepath.Dir(dir) {
			if sources["project"] {
				paths = append(paths, filepath.Join(dir, ".claude", "settings.json"))
			}
			if sources["local"] {
				paths = append(paths, filepath.Join(dir, ".claude", "settings.local.json"))
			}
			if parent := filepath.Dir(dir); parent == dir {
				break
			}
		}
	}
	var policy *sandbox.Config
	if len(policies) > 0 {
		policy = policies[0]
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		file, err := openClaudeSettingsFile(path, policy)
		if err != nil {
			return fmt.Errorf("read role hook settings: %w", err)
		}
		if err := validateClaudeSettingsFile(file, policy); err != nil {
			file.Close()
			return err
		}
		data, err := io.ReadAll(io.LimitReader(file, maxLifecycleRecord+1))
		file.Close()
		if err != nil {
			return err
		}
		if len(data) > maxLifecycleRecord {
			return errors.New("role hook settings exceed 1 MiB")
		}
		var settings struct {
			DisableAllHooks bool              `json:"disableAllHooks"`
			Env             map[string]string `json:"env"`
		}
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("invalid role hook settings in %s", path)
		}
		if err := sessionrole.ValidateClaudeEnvironment(settings.Env, filepath.Join(home, ".claude")); err != nil {
			return fmt.Errorf("role hook settings in %s: %w", path, err)
		}
		if settings.DisableAllHooks {
			return fmt.Errorf("role sessions require SessionStart hooks; disableAllHooks is set in %s", path)
		}
	}
	return nil
}
