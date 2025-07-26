package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type PermissionDecision string

const (
	AllowOnce    PermissionDecision = "allow_once"
	AlwaysAllow  PermissionDecision = "always_allow"
	Deny         PermissionDecision = "deny"
	AlwaysDeny   PermissionDecision = "always_deny"
)

type PermissionRule struct {
	Tool        string                 `json:"tool"`
	Action      string                 `json:"action"`
	ParamsHash  string                 `json:"params_hash"`
	Decision    PermissionDecision     `json:"decision"`
	Parameters  map[string]any         `json:"parameters"`
}

type PermissionEngine struct {
	rules map[string]PermissionRule
}

func NewPermissionEngine() *PermissionEngine {
	return &PermissionEngine{
		rules: make(map[string]PermissionRule),
	}
}

func (pe *PermissionEngine) CheckPermission(tool, action string, params map[string]any) (bool, error) {
	key := pe.makeKey(tool, action, params)
	
	rule, exists := pe.rules[key]
	if !exists {
		return false, nil // No rule found, need to ask user
	}
	
	switch rule.Decision {
	case AllowOnce:
		// Remove the rule after use
		delete(pe.rules, key)
		return true, nil
	case AlwaysAllow:
		return true, nil
	case Deny:
		// Remove the rule after use
		delete(pe.rules, key)
		return false, nil
	case AlwaysDeny:
		return false, nil
	default:
		return false, fmt.Errorf("unknown permission decision: %s", rule.Decision)
	}
}

func (pe *PermissionEngine) GrantPermission(tool, action string, params map[string]any, decision PermissionDecision) {
	key := pe.makeKey(tool, action, params)
	rule := PermissionRule{
		Tool:       tool,
		Action:     action,
		ParamsHash: pe.hashParams(params),
		Decision:   decision,
		Parameters: params,
	}
	pe.rules[key] = rule
}

func (pe *PermissionEngine) DenyPermission(tool, action string, params map[string]any, decision PermissionDecision) {
	key := pe.makeKey(tool, action, params)
	rule := PermissionRule{
		Tool:       tool,
		Action:     action,
		ParamsHash: pe.hashParams(params),
		Decision:   decision,
		Parameters: params,
	}
	pe.rules[key] = rule
}

func (pe *PermissionEngine) LoadFromFile(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No permissions file yet
		}
		return err
	}
	
	var rules map[string]PermissionRule
	if err := json.Unmarshal(data, &rules); err != nil {
		return err
	}
	
	pe.rules = rules
	return nil
}

func (pe *PermissionEngine) SaveToFile(filePath string) error {
	// Ensure directory exists
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	
	data, err := json.MarshalIndent(pe.rules, "", "  ")
	if err != nil {
		return err
	}
	
	return os.WriteFile(filePath, data, 0644)
}

func (pe *PermissionEngine) makeKey(tool, action string, params map[string]any) string {
	return fmt.Sprintf("%s:%s:%s", tool, action, pe.hashParams(params))
}

func (pe *PermissionEngine) hashParams(params map[string]any) string {
	// Create a deterministic hash of the parameters
	data, _ := json.Marshal(params)
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash)[:16] // Use first 16 chars
}