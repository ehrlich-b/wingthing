package agent

import (
	"testing"

	"github.com/behrlich/wingthing/internal/interfaces"
)

func TestPermissionDecisionLogic(t *testing.T) {
	fs := interfaces.NewOSFileSystem()
	engine := NewPermissionEngine(fs)

	tool := "bash"
	action := "ls"
	params := map[string]any{"command": "ls -la"}

	// Test no permission set - should return false
	allowed, err := engine.CheckPermission(tool, action, params)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if allowed {
		t.Error("Expected permission to be denied when no rule exists")
	}

	// Test AllowOnce - should return true and remove rule
	engine.GrantPermission(tool, action, params, AllowOnce)
	allowed, err = engine.CheckPermission(tool, action, params)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !allowed {
		t.Error("Expected AllowOnce to grant permission")
	}

	// Check that rule was removed
	allowed, err = engine.CheckPermission(tool, action, params)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if allowed {
		t.Error("Expected AllowOnce rule to be removed after use")
	}

	// Test AlwaysAllow - should return true and keep rule
	engine.GrantPermission(tool, action, params, AlwaysAllow)
	allowed, err = engine.CheckPermission(tool, action, params)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !allowed {
		t.Error("Expected AlwaysAllow to grant permission")
	}

	// Check that rule persists
	allowed, err = engine.CheckPermission(tool, action, params)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !allowed {
		t.Error("Expected AlwaysAllow rule to persist")
	}

	// Test AlwaysDeny
	engine.DenyPermission(tool, action, params, AlwaysDeny)
	allowed, err = engine.CheckPermission(tool, action, params)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if allowed {
		t.Error("Expected AlwaysDeny to deny permission")
	}
}

func TestPermissionParameterHashing(t *testing.T) {
	fs := interfaces.NewOSFileSystem()
	engine := NewPermissionEngine(fs)

	tool := "bash"
	action := "ls"
	params1 := map[string]any{"command": "ls -la"}
	params2 := map[string]any{"command": "ls -la"} // Same command
	params3 := map[string]any{"command": "ls -l"}  // Different command

	// Grant permission for params1
	engine.GrantPermission(tool, action, params1, AlwaysAllow)

	// Check params2 (should match)
	allowed, err := engine.CheckPermission(tool, action, params2)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !allowed {
		t.Error("Expected identical parameters to match")
	}

	// Check params3 (should not match)
	allowed, err = engine.CheckPermission(tool, action, params3)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if allowed {
		t.Error("Expected different parameters to not match")
	}
}
