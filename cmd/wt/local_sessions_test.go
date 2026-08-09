package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateSessionName(t *testing.T) {
	for _, valid := range []string{"", "work", "dev-server", "api_2", "repo.main"} {
		if err := validateSessionName(valid); err != nil {
			t.Errorf("validateSessionName(%q): %v", valid, err)
		}
	}
	for _, invalid := range []string{"-option", ".hidden", "two words", "a/b", "x\ncommand"} {
		if err := validateSessionName(invalid); err == nil {
			t.Errorf("validateSessionName(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestSessionNameRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := writeSessionName(dir, "api-server"); err != nil {
		t.Fatalf("writeSessionName: %v", err)
	}
	if got := readSessionName(dir); got != "api-server" {
		t.Fatalf("readSessionName = %q, want api-server", got)
	}
	info, err := os.Stat(filepath.Join(dir, sessionNameFile))
	if err != nil {
		t.Fatalf("stat session name: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("session name mode = %o, want 600", got)
	}
	if err := writeSessionName(dir, ""); err != nil {
		t.Fatalf("remove session name: %v", err)
	}
	if got := readSessionName(dir); got != "" {
		t.Fatalf("removed session name = %q, want empty", got)
	}
}

func TestReadSessionNameRejectsInvalidFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sessionNameFile), []byte("bad\x1b[2Jname\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := readSessionName(dir); got != "" {
		t.Fatalf("readSessionName = %q, want invalid name ignored", got)
	}
}

func TestReadEggMetaValuesPreservesEquals(t *testing.T) {
	dir := t.TempDir()
	data := []byte("kind=command\ncommand=\"sh\" \"-c\" \"A=B echo $A\"\ncwd=/tmp/project\n")
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), data, 0600); err != nil {
		t.Fatal(err)
	}
	meta := readEggMetaValues(dir)
	if got, want := meta["command"], `"sh" "-c" "A=B echo $A"`; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}
