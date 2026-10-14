package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDeployGuardStopsNormalEntryPointBeforeChecksAndFly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("make deployment uses a Unix shell")
	}
	_, source, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	root := t.TempDir()
	checker := filepath.Join(root, "checker")
	build := exec.Command("nice", "-n", "10", "go", "build", "-p", "2", "-o", checker, "./cmd/check-downloads")
	build.Dir = repo
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	makefile, err := os.ReadFile(filepath.Join(repo, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	// Replacement check/gate recipes make reaching either prerequisite observable.
	makefile = append(makefile, []byte("\ncheck gate:\n\t@touch checks-ran\n")...)
	if err := os.WriteFile(filepath.Join(root, "Makefile"), makefile, 0644); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(root, "tools")
	if err := os.Mkdir(tools, 0755); err != nil {
		t.Fatal(err)
	}
	// The Go shim runs the actual built validator; no test invokes a real fly CLI.
	shim := "#!/bin/sh\nexec '" + checker + "' downloads\n"
	if err := os.WriteFile(filepath.Join(tools, "go"), []byte(shim), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "fly"), []byte("#!/bin/sh\ntouch fly-ran\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, parallel := range []string{"-j1", "-j2"} {
		cmd := exec.Command("make", parallel, "deploy", "GO=go")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "PATH="+tools+":"+os.Getenv("PATH"))
		output, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "stage the verified release tree with cp -R") {
			t.Fatalf("expected actionable refusal: %v\n%s", err, output)
		}
		for _, marker := range []string{"fly-ran", "checks-ran"} {
			if _, err := os.Stat(filepath.Join(root, marker)); !os.IsNotExist(err) {
				t.Fatalf("%s ran despite missing downloads", marker)
			}
		}
	}
}
