package config

import (
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
