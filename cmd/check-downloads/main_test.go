package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func stageFixture(t *testing.T, root string) {
	t.Helper()
	tag := "v0.149.5"
	dir := filepath.Join(root, "downloads", tag)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	var sums strings.Builder
	assets := []map[string]string{}
	for _, name := range []string{"wt-linux-amd64", "wt-linux-arm64", "wt-darwin-amd64", "wt-darwin-arm64"} {
		data := []byte("fixture " + name)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sums, "%x *%s\n", sha256.Sum256(data), name)
		assets = append(assets, map[string]string{"name": name, "browser_download_url": "https://wingthing.ai/dl/" + tag + "/" + name})
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0644); err != nil {
		t.Fatal(err)
	}
	assets = append(assets, map[string]string{"name": "SHA256SUMS", "browser_download_url": "https://wingthing.ai/dl/" + tag + "/SHA256SUMS"})
	feed, _ := json.Marshal(map[string]any{"tag_name": tag, "draft": false, "prerelease": false, "assets": assets})
	if err := os.WriteFile(filepath.Join(root, "downloads/latest.json"), feed, 0644); err != nil {
		t.Fatal(err)
	}
}

func buildChecker(t *testing.T) (string, string) {
	t.Helper()
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	checker := filepath.Join(t.TempDir(), "checker")
	build := exec.Command("nice", "-n", "10", "go", "build", "-p", "2", "-o", checker, "./cmd/check-downloads")
	build.Dir = repo
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	return checker, repo
}

// deploymentFixture uses the real receipt verifier in a tiny committed repository.
// Its receipt describes synthetic release bytes; no promotion or Fly process runs.
func deploymentFixture(t *testing.T, repo, root string, staged bool) {
	t.Helper()
	for _, name := range []string{"Makefile", "scripts/ship-gate.py"} {
		data, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if name == "Makefile" {
			// Any attempt to run the old local suite must fail this test.
			data = append(data, []byte("\ncheck gate:\n\t@touch checks-ran\n\t@exit 99\n")...)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/downloads/\n/.ship-downloads/\n/tools/\n/receipt.txt\n/*-ran\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Keep a baseline for receipt validation even when the staged feed is absent.
	stageFixture(t, root)
	if !staged {
		if err := os.Remove(filepath.Join(root, "downloads/latest.json")); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "Makefile", "scripts/ship-gate.py", ".gitignore"}, {"commit", "-qm", "Create deployment guard fixture."}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Bryan Ehrlich", "GIT_AUTHOR_EMAIL=ehrlich.bryan@gmail.com", "GIT_COMMITTER_NAME=Bryan Ehrlich", "GIT_COMMITTER_EMAIL=ehrlich.bryan@gmail.com")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v\n%s", err, output)
		}
	}
	cmd := exec.Command("python3", "-B", "-c", `import importlib.util, json
from pathlib import Path
spec = importlib.util.spec_from_file_location("ship", "scripts/ship-gate.py")
ship = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ship)
artifact = Path("downloads/v0.149.5/wt-linux-amd64")
Path("receipt.txt").write_text(json.dumps(dict(version=1, head=ship.source(), platform="Linux", gates=ship.GATES, baseline=str(artifact), baseline_sha256=ship.digest(artifact))))`)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("receipt fixture: %v\n%s", err, output)
	}
}

func TestDeployGuardNormalEntryPoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("make deployment uses a Unix shell")
	}
	checker, repo := buildChecker(t)
	for _, staged := range []bool{false, true} {
		for _, parallel := range []string{"-j1", "-j2"} {
			for _, receipt := range []string{"missing", "mismatched", "valid"} {
				t.Run(fmt.Sprintf("staged=%v/%s/%s", staged, parallel, receipt), func(t *testing.T) {
					root := t.TempDir()
					deploymentFixture(t, repo, root, staged)
					receiptPath := "receipt.txt"
					switch receipt {
					case "missing":
						receiptPath = "missing.txt"
					case "mismatched":
						path := filepath.Join(root, receiptPath)
						data, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						var value map[string]any
						if err := json.Unmarshal(data, &value); err != nil {
							t.Fatal(err)
						}
						value["head"] = strings.Repeat("0", 40)
						data, err = json.Marshal(value)
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(path, data, 0644); err != nil {
							t.Fatal(err)
						}
					}
					tools := filepath.Join(root, "tools")
					if err := os.Mkdir(tools, 0755); err != nil {
						t.Fatal(err)
					}
					shim := "#!/bin/sh\ntouch checker-ran\nexec '" + checker + "' downloads\n"
					if err := os.WriteFile(filepath.Join(tools, "go"), []byte(shim), 0755); err != nil {
						t.Fatal(err)
					}
					fly := "#!/bin/sh\nset -e\ntest ! -e checks-ran\ndiff -r downloads .ship-downloads\ntouch fly-ran\n"
					if err := os.WriteFile(filepath.Join(tools, "fly"), []byte(fly), 0755); err != nil {
						t.Fatal(err)
					}
					cmd := exec.Command("make", parallel, "deploy", "GO=go", "RECEIPT="+receiptPath)
					cmd.Dir = root
					cmd.Env = append(os.Environ(), "PATH="+tools+":"+os.Getenv("PATH"), "WT_RELEASE_SKEW_BINARY="+filepath.Join(root, "downloads/v0.149.5/wt-linux-amd64"))
					output, err := cmd.CombinedOutput()
					success := staged && receipt == "valid"
					if success && err != nil {
						t.Fatalf("staged deploy: %v\n%s", err, output)
					}
					if !success {
						message := "ship-gate:"
						if receipt == "valid" {
							message = "stage the verified release tree with cp -R"
						}
						if err == nil || !strings.Contains(string(output), message) {
							t.Fatalf("expected actionable refusal: %v\n%s", err, output)
						}
					}
					for marker, want := range map[string]bool{"fly-ran": success, "checker-ran": receipt == "valid", "checks-ran": false} {
						_, err := os.Stat(filepath.Join(root, marker))
						if want && err != nil || !want && !os.IsNotExist(err) {
							t.Fatalf("%s want=%v: %v", marker, want, err)
						}
					}
				})
			}
		}
	}
}

func TestDeployGuardImageAndSelfHostConfig(t *testing.T) {
	checker, repo := buildChecker(t)
	dockerfile, err := os.ReadFile(filepath.Join(repo, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerfile), "ARG REQUIRE_DOWNLOADS=0") {
		t.Fatal("plain docker build must default to self-host mode")
	}
	var guard string
	for _, line := range strings.Split(string(dockerfile), "\n") {
		if strings.HasPrefix(line, "RUN ") && strings.Contains(line, "go run -p 2 ./cmd/check-downloads") {
			guard = strings.TrimPrefix(line, "RUN ")
		}
	}
	if guard == "" {
		t.Fatal("missing Dockerfile production guard")
	}
	for _, tc := range []struct {
		name, config              string
		required, staged, corrupt bool
	}{
		{"plain-docker", "", false, false, false},
		{"self-host-fly", "fly.selfhost.toml", false, false, false},
		{"production-fly-missing", "fly.toml", true, false, false},
		{"production-fly-staged", "fly.toml", true, true, false},
		{"production-fly-corrupt-context", "fly.toml", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.config != "" {
				config, err := os.ReadFile(filepath.Join(repo, tc.config))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(config), "REQUIRE_DOWNLOADS = \"1\"") != tc.required {
					t.Fatal("Fly build argument disagrees with deployment mode")
				}
			}
			root := t.TempDir()
			if tc.staged {
				stageFixture(t, root)
				stage := exec.Command("python3", "-B", filepath.Join(repo, "scripts/ship-gate.py"), "stage-downloads")
				stage.Dir = root
				if output, err := stage.CombinedOutput(); err != nil {
					t.Fatalf("stage Docker context: %v\n%s", err, output)
				}
				if tc.corrupt {
					if err := os.WriteFile(filepath.Join(root, ".ship-downloads/v0.149.5/wt-linux-amd64"), []byte("corrupt"), 0644); err != nil {
						t.Fatal(err)
					}
				}
			}
			command := strings.Replace(guard, "nice -n 10 go run -p 2 ./cmd/check-downloads", "'"+checker+"'", 1)
			cmd := exec.Command("sh", "-c", command)
			cmd.Dir = root
			required := "0"
			if tc.required {
				required = "1"
			}
			cmd.Env = append(os.Environ(), "REQUIRE_DOWNLOADS="+required)
			output, err := cmd.CombinedOutput()
			if tc.required && (!tc.staged || tc.corrupt) {
				if err == nil || !strings.Contains(string(output), "Deployment refused") {
					t.Fatalf("expected production refusal: %v\n%s", err, output)
				}
			} else if err != nil {
				t.Fatalf("image guard: %v\n%s", err, output)
			}
			if info, err := os.Stat(filepath.Join(root, ".ship-downloads")); err != nil || !info.IsDir() {
				t.Fatalf("Docker context directory must exist even for self-host builds: %v", err)
			}
		})
	}
}
