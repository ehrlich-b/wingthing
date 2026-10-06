package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestContextToolConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, want string
		context          *ContextConfig
	}{
		{name: "both", yaml: "name: tickets\nrun: echo x\ncontext: happyfox-tickets\n", want: "mutually exclusive"},
		{name: "missing block", yaml: "name: tickets\ncontext: happyfox-tickets\n", want: "wing context block"},
		{name: "enabled", yaml: "name: tickets\ncontext: happyfox-tickets\n", context: &ContextConfig{URL: "https://context.pants.taxi", ClientID: "wingthing-stage", SecretFile: "/opt/context.secret"}},
		{name: "legacy", yaml: "name: tickets\nrun: echo x\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeToolTestFile(t, dir, "tickets.yaml", tc.yaml)
			tools, err := LoadWingTools(dir, tc.context)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error=%v want %s", err, tc.want)
				}
				return
			}
			if err != nil || len(tools) != 1 {
				t.Fatalf("load: %v %v", tools, err)
			}
		})
	}
}

func TestWingContextRoundTripAndValidation(t *testing.T) {
	dir := t.TempDir()
	c := &ContextConfig{URL: "https://context.pants.taxi", ClientID: "wingthing-stage", SecretFile: filepath.Join(dir, "secret"), Scopes: []string{"happyfox"}}
	if err := SaveWingConfig(dir, &WingConfig{Context: c}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadWingConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Context == nil || loaded.Context.SecretFile != c.SecretFile || loaded.Context.Scopes[0] != "happyfox" {
		t.Fatalf("round trip: %+v", loaded)
	}
	clone := loaded.Clone()
	clone.Context.Scopes[0] = "jira"
	if loaded.Context.Scopes[0] != "happyfox" {
		t.Fatal("clone shares scopes")
	}
	for _, bad := range []ContextConfig{
		{URL: "https://user:secret@context.pants.taxi", ClientID: "wingthing-stage", SecretFile: c.SecretFile},
		{URL: c.URL, ClientID: "wingthing-stage", SecretFile: "relative"},
		{URL: c.URL, SecretFile: c.SecretFile},
	} {
		if err := SaveWingConfig(dir, &WingConfig{Context: &bad}); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
}

func TestContextConfigRequiresHTTPSOffLoopback(t *testing.T) {
	cfg := ContextConfig{URL: "http://context.example.test", ClientID: "wingthing-dev", SecretFile: "/tmp/secret"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("plain http to a remote host must be rejected")
	}
	for _, url := range []string{"http://127.0.0.1:8080", "http://localhost:9", "https://context.example.test"} {
		cfg.URL = url
		if err := cfg.Validate(); err != nil {
			t.Fatalf("%s: %v", url, err)
		}
	}
}
