package eggclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/config"
)

// Only deployment model policy, and the host's choice to disable Claude's
// self-updater, cross from the host profile into an isolated session. Pass it to
// Claude for this launch; never merge it into the user's file.
func IsolatedClaudePolicyArgs(agentName string, isolated bool) ([]string, error) {
	return isolatedClaudePolicyArgs(agentName, isolated, "")
}

func isolatedClaudePolicyArgs(agentName string, isolated bool, settingsSource string) ([]string, error) {
	if agentName != "claude" || !isolated {
		return nil, nil
	}
	// Personal preview has its own selected provider/model. Organization or
	// host profile preferences must not cross this independent namespace.
	if config.Channel() == "preview" {
		return nil, nil
	}
	if settingsSource == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		settingsSource = filepath.Join(home, ".claude", "settings.json")
	}
	data, err := os.ReadFile(settingsSource)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read host Claude model policy: %w", err)
	}
	var source struct {
		Model       string `json:"model"`
		EffortLevel string `json:"effortLevel"`
		Env         struct {
			Effort            string `json:"CLAUDE_CODE_EFFORT_LEVEL"`
			DisableAutoupdate string `json:"DISABLE_AUTOUPDATER"`
		} `json:"env"`
	}
	if err := json.Unmarshal(data, &source); err != nil {
		return nil, errors.New("invalid host Claude model policy")
	}
	policy := make(map[string]any)
	env := make(map[string]string)
	var args []string
	if source.Model != "" {
		args = append(args, "--model", source.Model)
	}
	if source.EffortLevel != "" {
		switch source.EffortLevel {
		case "low", "medium", "high", "xhigh":
			policy["effortLevel"] = source.EffortLevel
		default:
			return nil, errors.New("invalid host Claude effortLevel")
		}
	}
	if source.Env.Effort != "" {
		switch source.Env.Effort {
		case "low", "medium", "high", "xhigh", "max", "auto":
			env["CLAUDE_CODE_EFFORT_LEVEL"] = source.Env.Effort
		default:
			return nil, errors.New("invalid host Claude effort environment policy")
		}
	}
	// A host-managed install is read-only inside the sandbox, so its updater can
	// only fail there. Only the disabling value crosses.
	if source.Env.DisableAutoupdate == "1" {
		env["DISABLE_AUTOUPDATER"] = "1"
	}
	if len(env) > 0 {
		policy["env"] = env
	}
	if len(policy) == 0 {
		return args, nil
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return nil, err
	}
	return append(args, "--settings", string(encoded)), nil
}
