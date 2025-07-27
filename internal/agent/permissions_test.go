package agent

import (
	"sync"
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

func TestCanonicalParameterHashing(t *testing.T) {
	fs := interfaces.NewOSFileSystem()
	engine := NewPermissionEngine(fs)

	tool := "test"
	action := "execute"

	// Test that parameter order doesn't matter
	params1 := map[string]any{
		"command": "ls",
		"dir":     "/tmp",
		"flags":   []string{"-l", "-a"},
	}
	params2 := map[string]any{
		"dir":     "/tmp",
		"flags":   []string{"-l", "-a"},
		"command": "ls",
	}

	// Both should produce the same hash
	hash1 := engine.hashParams(params1)
	hash2 := engine.hashParams(params2)

	if hash1 != hash2 {
		t.Errorf("Expected identical hashes for reordered parameters: %s != %s", hash1, hash2)
	}

	// Grant permission with params1
	engine.GrantPermission(tool, action, params1, AlwaysAllow)

	// Should work with params2 (different order, same content)
	allowed, err := engine.CheckPermission(tool, action, params2)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !allowed {
		t.Error("Expected parameters with different ordering to match")
	}
}

func TestNestedParameterCanonicalization(t *testing.T) {
	fs := interfaces.NewOSFileSystem()
	engine := NewPermissionEngine(fs)

	// Test nested maps with different key ordering
	params1 := map[string]any{
		"config": map[string]any{
			"timeout": 30,
			"retries": 3,
		},
		"command": "test",
	}
	params2 := map[string]any{
		"command": "test",
		"config": map[string]any{
			"retries": 3,
			"timeout": 30,
		},
	}

	hash1 := engine.hashParams(params1)
	hash2 := engine.hashParams(params2)

	if hash1 != hash2 {
		t.Errorf("Expected identical hashes for nested maps with reordered keys: %s != %s", hash1, hash2)
	}
}

func TestConcurrentPermissionAccess(t *testing.T) {
	fs := interfaces.NewOSFileSystem()
	engine := NewPermissionEngine(fs)

	tool := "bash"
	action := "execute"

	var wg sync.WaitGroup
	numGoroutines := 100

	// Test concurrent writes
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			params := map[string]any{"command": "test", "id": id}
			engine.GrantPermission(tool, action, params, AlwaysAllow)
		}(i)
	}
	wg.Wait()

	// Test concurrent reads and writes
	wg.Add(numGoroutines * 2)
	for i := 0; i < numGoroutines; i++ {
		// Reader
		go func(id int) {
			defer wg.Done()
			params := map[string]any{"command": "test", "id": id}
			_, err := engine.CheckPermission(tool, action, params)
			if err != nil {
				t.Errorf("Unexpected error in reader %d: %v", id, err)
			}
		}(i)

		// Writer
		go func(id int) {
			defer wg.Done()
			params := map[string]any{"command": "test", "id": id + 1000}
			engine.DenyPermission(tool, action, params, Deny)
		}(i)
	}
	wg.Wait()
}

func TestDenyPermissionLogic(t *testing.T) {
	fs := interfaces.NewOSFileSystem()
	engine := NewPermissionEngine(fs)

	tool := "bash"
	action := "execute"
	params := map[string]any{"command": "rm -rf /"}

	// Test Deny (one-time denial)
	engine.DenyPermission(tool, action, params, Deny)
	allowed, err := engine.CheckPermission(tool, action, params)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if allowed {
		t.Error("Expected Deny to block permission")
	}

	// Check that Deny rule was removed after use
	allowed, err = engine.CheckPermission(tool, action, params)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if allowed {
		t.Error("Expected no rule to exist after Deny was used")
	}

	// Test AlwaysDeny (persistent denial)
	engine.DenyPermission(tool, action, params, AlwaysDeny)
	allowed, err = engine.CheckPermission(tool, action, params)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if allowed {
		t.Error("Expected AlwaysDeny to block permission")
	}

	// Check that AlwaysDeny rule persists
	allowed, err = engine.CheckPermission(tool, action, params)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if allowed {
		t.Error("Expected AlwaysDeny rule to persist")
	}
}

func TestParameterHashStability(t *testing.T) {
	fs := interfaces.NewOSFileSystem()
	engine := NewPermissionEngine(fs)

	params := map[string]any{
		"command": "test",
		"nested": map[string]any{
			"key2": "value2",
			"key1": "value1",
		},
		"array": []string{"b", "a", "c"},
	}

	// Hash should be stable across multiple calls
	hash1 := engine.hashParams(params)
	hash2 := engine.hashParams(params)
	hash3 := engine.hashParams(params)

	if hash1 != hash2 || hash2 != hash3 {
		t.Errorf("Expected stable hashing: %s, %s, %s", hash1, hash2, hash3)
	}

	// Hash should be reasonably short (16 chars as per implementation)
	if len(hash1) != 16 {
		t.Errorf("Expected hash length of 16, got %d", len(hash1))
	}
}
