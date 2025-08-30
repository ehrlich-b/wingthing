package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"sort"
	"sync"

	"github.com/behrlich/wingthing/internal/interfaces"
)

// Use types from interfaces package
type PermissionDecision = interfaces.PermissionDecision

const (
	AllowOnce   = interfaces.AllowOnce
	AlwaysAllow = interfaces.AlwaysAllow
	Deny        = interfaces.Deny
	AlwaysDeny  = interfaces.AlwaysDeny
)

type PermissionRule struct {
	Tool       string             `json:"tool"`
	Action     string             `json:"action"`
	ParamsHash string             `json:"params_hash"`
	Decision   PermissionDecision `json:"decision"`
	Parameters map[string]any     `json:"parameters"`
}

type PermissionEngine struct {
	mu     sync.RWMutex
	rules  map[string]PermissionRule
	fs     interfaces.FileSystem
	logger *slog.Logger
}

func NewPermissionEngine(fs interfaces.FileSystem, logger *slog.Logger) *PermissionEngine {
	return &PermissionEngine{
		rules:  make(map[string]PermissionRule),
		fs:     fs,
		logger: logger,
	}
}

func (pe *PermissionEngine) CheckPermission(tool, action string, params map[string]any) (bool, error) {
	key := pe.makeKey(tool, action, params)

	pe.mu.Lock()
	defer pe.mu.Unlock()

	rule, exists := pe.rules[key]
	if !exists {
		pe.logger.Debug("No permission rule found", "key", key, "tool", tool, "action", action)
		return false, nil // No rule found, need to ask user
	}

	pe.logger.Debug("Found permission rule", "key", key, "decision", rule.Decision)

	switch rule.Decision {
	case AllowOnce:
		// Remove the rule after use
		pe.logger.Debug("About to delete AllowOnce rule", "key", key, "rulesBefore", len(pe.rules))
		delete(pe.rules, key)
		pe.logger.Debug("AllowOnce rule used and deleted", "key", key, "rulesAfter", len(pe.rules))
		// Verify deletion
		if _, stillExists := pe.rules[key]; stillExists {
			pe.logger.Error("Rule still exists after deletion!", "key", key)
		} else {
			pe.logger.Debug("Verified rule is deleted", "key", key)
		}
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

	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.rules[key] = rule
	pe.logger.Debug("Granted permission", "key", key, "decision", decision)
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

	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.rules[key] = rule
}

func (pe *PermissionEngine) LoadFromFile(filePath string) error {
	data, err := pe.fs.ReadFile(filePath)
	if err != nil {
		if pe.fs.IsNotExist(err) {
			return nil // No permissions file yet
		}
		return err
	}

	var rules map[string]PermissionRule
	if err := json.Unmarshal(data, &rules); err != nil {
		return err
	}

	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.rules = rules
	return nil
}

func (pe *PermissionEngine) SaveToFile(filePath string) error {
	// Ensure directory exists
	dir := filepath.Dir(filePath)
	if err := pe.fs.MkdirAll(dir, 0755); err != nil {
		return err
	}

	pe.mu.RLock()
	data, err := json.MarshalIndent(pe.rules, "", "  ")
	pe.mu.RUnlock()

	if err != nil {
		return err
	}

	return pe.fs.WriteFile(filePath, data, 0644)
}

func (pe *PermissionEngine) makeKey(tool, action string, params map[string]any) string {
	return fmt.Sprintf("%s:%s:%s", tool, action, pe.hashParams(params))
}

func (pe *PermissionEngine) hashParams(params map[string]any) string {
	// Create a deterministic hash of the parameters by canonicalizing them
	canonical := pe.canonicalizeParams(params)
	data, _ := json.Marshal(canonical)
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash)[:16] // Use first 16 chars
}

// canonicalizeParams recursively sorts map keys to ensure deterministic JSON marshaling
func (pe *PermissionEngine) canonicalizeParams(params any) any {
	val := reflect.ValueOf(params)

	switch val.Kind() {
	case reflect.Map:
		if val.Type().Key().Kind() != reflect.String {
			// Non-string keys, return as-is
			return params
		}

		// Create a new map with sorted keys
		result := make(map[string]any)
		keys := val.MapKeys()

		// Sort keys by string value
		sort.Slice(keys, func(i, j int) bool {
			return keys[i].String() < keys[j].String()
		})

		// Add sorted key-value pairs
		for _, key := range keys {
			value := val.MapIndex(key)
			result[key.String()] = pe.canonicalizeParams(value.Interface())
		}

		return result

	case reflect.Slice, reflect.Array:
		// Canonicalize each element in the slice/array
		result := make([]any, val.Len())
		for i := 0; i < val.Len(); i++ {
			result[i] = pe.canonicalizeParams(val.Index(i).Interface())
		}
		return result

	default:
		// Primitive types, return as-is
		return params
	}
}
