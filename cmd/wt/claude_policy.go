package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Only deployment model policy crosses from the host profile into an isolated
// session. Pass it to Claude for this launch; never merge it into the user's file.
func isolatedClaudePolicyArgs(agentName string, isolated bool) ([]string, error) {
	if agentName != "claude" || !isolated {
		return nil, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
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
			Effort string `json:"CLAUDE_CODE_EFFORT_LEVEL"`
		} `json:"env"`
	}
	if err := json.Unmarshal(data, &source); err != nil {
		return nil, errors.New("invalid host Claude model policy")
	}
	policy := make(map[string]any)
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
			policy["env"] = map[string]string{"CLAUDE_CODE_EFFORT_LEVEL": source.Env.Effort}
		default:
			return nil, errors.New("invalid host Claude effort environment policy")
		}
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
