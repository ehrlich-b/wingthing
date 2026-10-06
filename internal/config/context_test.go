package config

import (
	"bytes"
	"fmt"
	"log"
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

func TestContextConfigReloadRequiresRestart(t *testing.T) {
	original := &ContextConfig{URL: "https://context.example", ClientID: "wing", SecretFile: "/config/secret", Scopes: []string{"jira"}}
	cases := []struct {
		name          string
		current, next *ContextConfig
		changed       bool
	}{
		{"unchanged", original, original, false},
		{"enable", nil, original, true},
		{"disable", original, nil, true},
		{"url", original, &ContextConfig{URL: "https://new.example", ClientID: original.ClientID, SecretFile: original.SecretFile, Scopes: original.Scopes}, true},
		{"client_id", original, &ContextConfig{URL: original.URL, ClientID: "new", SecretFile: original.SecretFile, Scopes: original.Scopes}, true},
		{"secret_file", original, &ContextConfig{URL: original.URL, ClientID: original.ClientID, SecretFile: "/config/new", Scopes: original.Scopes}, true},
		{"scopes", original, &ContextConfig{URL: original.URL, ClientID: original.ClientID, SecretFile: original.SecretFile, Scopes: []string{"happyfox"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&output)
			defer log.SetOutput(previous)
			next := &WingConfig{Context: tc.next, Debug: true}
			RetainContextConfig(next, tc.current)
			if next.Context != tc.current || !next.Debug {
				t.Fatalf("reload changed startup Context or lost other settings: %+v", next)
			}
			if strings.Contains(output.String(), "context config change requires a restart") != tc.changed {
				t.Fatalf("reload log: %s", output.String())
			}
		})
	}
}

func TestContextConfigFrozenAcrossSeparateLoads(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			dir := t.TempDir()
			var initial *ContextConfig
			if enabled {
				initial = &ContextConfig{URL: "https://context.example", ClientID: "wing", SecretFile: filepath.Join(dir, "old")}
			}
			current, release := FreezeContextConfig(dir, initial)
			defer release()
			changed := &ContextConfig{URL: "https://new.example", ClientID: "new", SecretFile: filepath.Join(dir, "new")}
			if err := SaveWingConfig(dir, &WingConfig{Context: changed}); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadContextConfig(dir)
			if err != nil || loaded != current {
				t.Fatalf("separate launch load changed Context: %+v, %v", loaded, err)
			}
			shared, sharedRelease := FreezeContextConfig(dir, changed)
			sharedRelease()
			if shared != current {
				t.Fatal("embedded wing replaced roost Context")
			}
			loaded, err = LoadContextConfig(dir)
			if err != nil || loaded != current {
				t.Fatal("embedded wing released roost Context")
			}
		})
	}
}
