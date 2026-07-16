package mcp

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func TestPolicyMaximumSubsetAcrossEnabledRoles(t *testing.T) {
	p := &Policy{
		Roles: map[string]*RolePolicy{
			"eng": {
				Enabled: true,
				Allow:   []string{"slide-db"},
				Members: []string{"both@example.com"},
			},
			"sales": {
				Enabled: false,
				Members: []string{"disabled-plus-support@example.com"},
			},
			"support": {
				Enabled: true,
				Deny:    []string{"slide-db"},
				Members: []string{"both@example.com", "disabled-plus-support@example.com"},
			},
		},
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}

	roles := p.RolesForEmail("disabled-plus-support@example.com")
	if want := []string{"sales", "support"}; !reflect.DeepEqual(roles, want) {
		t.Fatalf("roles = %v, want %v", roles, want)
	}
	if !p.EnabledAny(roles) {
		t.Fatal("an enabled support role should enable MCP even when sales is disabled")
	}
	if want := []string{"support"}; !reflect.DeepEqual(p.EnabledRoles(roles), want) {
		t.Fatalf("enabled roles = %v, want %v", p.EnabledRoles(roles), want)
	}
	if !p.AllowedAny(roles, "slide-jira") {
		t.Fatal("disabled sales must not suppress support's effective tool set")
	}
	if p.AllowedAny(roles, "slide-db") {
		t.Fatal("support denies slide-db and disabled sales contributes no grant")
	}

	roles = p.RolesForEmail("both@example.com")
	if !p.AllowedAny(roles, "slide-db") {
		t.Fatal("eng's grant must win over support's deny under maximum-subset semantics")
	}
}

func TestPolicyDisabledRolesDoNotEnableMCP(t *testing.T) {
	p := &Policy{Roles: map[string]*RolePolicy{
		"sales": {Enabled: false, Members: []string{"sales@example.com"}},
	}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	roles := p.RolesForEmail("sales@example.com")
	if p.EnabledAny(roles) {
		t.Fatal("a user with only disabled roles must not be MCP-enabled")
	}
	if p.AllowedAny(roles, "anything") {
		t.Fatal("disabled roles must contribute no tools")
	}
}

func TestWingConfigRejectsUnknownAndAmbiguousMCPFields(t *testing.T) {
	for name, body := range map[string]string{
		"unknown":        "mcp:\n  enabled: true\n  roles:\n    eng:\n      enable: true\n",
		"allow-and-deny": "mcp:\n  enabled: true\n  roles:\n    eng:\n      enabled: true\n      allow: [a]\n      deny: [b]\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "wing.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := config.LoadWingConfig(dir); err == nil {
				t.Fatal("expected invalid policy to fail")
			}
		})
	}
}
