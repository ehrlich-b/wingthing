package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSetMachineContextFixtures(t *testing.T) {
	for _, tc := range []struct {
		name, machine, value, filename, key string
		wantErr                             bool
	}{
		{"local", "local", "work", "wing.yaml", "context", false},
		{"remote", "studio", "work", "remotes.yaml", "context", false},
		{"unknown", "missing", "home", "remotes.yaml", "context", true},
		{"local-none", "local", "none", "wing.yaml", "context", false},
		{"remote-none", "studio", "none", "remotes.yaml", "context", false},
		{"service", "local", "home", "wing.yaml", "machine_context", false},
		{"service-none", "local", "none", "wing.yaml", "machine_context", false},
		{"invalid", "local", "office", "wing.yaml", "context", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			before := map[string][]byte{}
			for _, filename := range []string{"wing.yaml", "remotes.yaml"} {
				fixture := "context-wing.yaml"
				if filename == "remotes.yaml" {
					fixture = "context-remotes.yaml"
				}
				data, err := os.ReadFile(filepath.Join("testdata", fixture))
				if err != nil {
					t.Fatal(err)
				}
				if filename == "wing.yaml" && tc.key == "machine_context" {
					data = []byte("machine_context: work\ncontext:\n  url: https://context.example\n  client_id: wing\n  secret_file: /private/secret\nfuture_setting: [false, 7, '007']\n")
				}
				before[filename] = data
				if err := os.WriteFile(filepath.Join(dir, filename), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := SetMachineContext(dir, tc.machine, tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("SetMachineContext = %v", err)
			}
			for filename, data := range before {
				after, err := os.ReadFile(filepath.Join(dir, filename))
				if err != nil {
					t.Fatal(err)
				}
				if tc.wantErr || filename != tc.filename {
					if string(after) != string(data) {
						t.Fatalf("%s unexpectedly written", filename)
					}
					continue
				}
				var expected, actual map[string]any
				if err := yaml.Unmarshal(data, &expected); err != nil {
					t.Fatal(err)
				}
				if err := yaml.Unmarshal(after, &actual); err != nil {
					t.Fatal(err)
				}
				target := expected
				if tc.machine != "local" {
					target = expected["remotes"].(map[string]any)[tc.machine].(map[string]any)
				}
				if tc.value == "none" {
					delete(target, tc.key)
				} else {
					target[tc.key] = tc.value
				}
				if !reflect.DeepEqual(actual, expected) {
					t.Fatalf("unrelated YAML changed:\nwant %#v\ngot %#v", expected, actual)
				}
				info, err := os.Stat(filepath.Join(dir, filename))
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("config is not private: %v %v", info, err)
				}
			}
			if !tc.wantErr {
				contexts, err := MachineContexts(dir)
				if err != nil {
					t.Fatal(err)
				}
				want := tc.value
				if want == "none" {
					want = ""
				}
				if contexts[tc.machine] != want || len(contexts) != 3 {
					t.Fatalf("contexts = %#v", contexts)
				}
			}
		})
	}
}

func TestMachineContextMissingAndMalformed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	if err := SetMachineContext(dir, "missing", "home"); err == nil {
		t.Fatal("accepted unknown machine")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("unknown machine created state")
	}
	contexts, err := MachineContexts(dir)
	if err != nil || !reflect.DeepEqual(contexts, map[string]string{"local": ""}) {
		t.Fatalf("missing configs: %v %v", contexts, err)
	}
	if err := SetMachineContext(dir, "local", "home"); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"[home]\n", "context: home\ncontext: work\n", "context: home\n---\ncontext: work\n", "defaults: &d {context: home}\n<<: *d\n", "context: &c home\nother: *c\n"} {
		path := filepath.Join(dir, "wing.yaml")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if err := SetMachineContext(dir, "local", "work"); err == nil {
			t.Fatalf("accepted unsafe document %q", data)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != data {
			t.Fatal("refused edit changed file")
		}
	}
}

func TestSetMachineContextPreservesLegacyWingID(t *testing.T) {
	legacy, err := os.ReadFile("testdata/context-legacy-wing-id")
	if err != nil {
		t.Fatal(err)
	}
	for _, first := range []string{"home", "work", "none"} {
		t.Run(first, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "wing-id"), legacy, 0600); err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{first, "home", "work", "none"} {
				if err := SetMachineContext(dir, "local", value); err != nil {
					t.Fatal(err)
				}
				cfg, err := LoadWingConfig(dir)
				if err != nil || cfg.WingID != "0123456789abcdef0123" {
					t.Fatalf("after %s: cfg=%+v, err=%v", value, cfg, err)
				}
				after, err := os.ReadFile(filepath.Join(dir, "wing-id"))
				if err != nil || !bytes.Equal(after, legacy) {
					t.Fatalf("legacy bytes changed after %s: %q, %v", value, after, err)
				}
			}
		})
	}
	for _, explicit := range []string{"explicit-wing-id", ""} {
		t.Run("explicit-"+explicit, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "wing-id"), legacy, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "wing.yaml"), []byte("wing_id: '"+explicit+"'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{"home", "work", "none"} {
				if err := SetMachineContext(dir, "local", value); err != nil {
					t.Fatal(err)
				}
				cfg, err := LoadWingConfig(dir)
				if err != nil || cfg.WingID != explicit {
					t.Fatalf("explicit ID after %s: cfg=%+v, err=%v", value, cfg, err)
				}
			}
		})
	}
}

func TestSetMachineContextRejectsLoaderErrors(t *testing.T) {
	conflict, err := os.ReadFile("testdata/context-conflicting-wing.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{string(conflict), "context: 7\n", "machine_context: [home]\n", "hosted_relay: invalid\n"} {
		for _, value := range []string{"none", "home", "work"} {
			t.Run(body+value, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "wing.yaml")
				if err := os.WriteFile(path, []byte(body), 0640); err != nil {
					t.Fatal(err)
				}
				// Establish the permission fixture independently of the caller's umask.
				if err := os.Chmod(path, 0640); err != nil {
					t.Fatal(err)
				}
				if _, err := LoadWingConfig(dir); err == nil {
					t.Fatal("loader accepted malformed fixture")
				}
				if err := SetMachineContext(dir, "local", value); err == nil {
					t.Errorf("accepted malformed document for %s", value)
				}
				after, err := os.ReadFile(path)
				if err != nil || string(after) != body {
					t.Errorf("refusal changed bytes: %q, %v", after, err)
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0640 {
					t.Errorf("refusal changed permissions: %v, %v", info, err)
				}
				if _, err := MachineContexts(dir); err == nil {
					t.Error("context list accepted a document the loader rejects")
				}
			})
		}
	}
}
