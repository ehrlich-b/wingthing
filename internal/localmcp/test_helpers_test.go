package localmcp

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func writeResumeSessionFixture(t *testing.T, cfg *config.Config, sessionID, owner, agent, cwd, providerID, content string) string {
	t.Helper()
	dir := filepath.Join(cfg.Dir, "eggs", sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.owner"), []byte(owner+"\nowner@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent="+agent+"\ncwd="+cwd+"\nstarted_at=100\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chat.meta"), []byte("agent_session_id="+providerID+"\nagent="+agent+"\nformat=jsonl\ncwd="+cwd+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, eggclient.ProviderResumeMetadataFile), []byte("agent="+agent+"\nprovider_session_id="+providerID+"\nsource_session_id=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(dir, "chat.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	writer := gzip.NewWriter(file)
	if _, err := writer.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func closeForTest(t *testing.T, name string, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Errorf("close %s: %v", name, err)
	}
}

func fakeInventorySSH(t *testing.T, inventory eggclient.RemoteSessionInventory) string {
	t.Helper()
	data, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	return writeFakeRemoteSSH(t, "case \"$3\" in\n"+
		"*\"'--version'\"*) printf '%s\\n' 'wt version remote-test' ;;\n"+
		"*\"'session' 'ps' '--json' '--remote-inventory'\"*) printf '%s\\n' "+remotepkg.ShellQuote(string(data))+" ;;\n"+
		"*) printf 'unexpected command: %s\\n' \"$3\" >&2; exit 9 ;;\nesac\n")
}

func seedRemoteListSession(t *testing.T, cfg *config.Config, id, principal string) {
	t.Helper()
	dir := filepath.Join(cfg.Dir, "eggs", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for file, value := range map[string]string{
		"egg.pid": fmt.Sprint(os.Getpid()), "egg.meta": "kind=command\ncommand=/bin/sh\ncwd=/tmp/work\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := eggclient.WriteEggOwner(dir, "fixture-user", ""); err != nil {
		t.Fatal(err)
	}
	if err := eggclient.WriteSessionPrincipal(dir, principal); err != nil {
		t.Fatal(err)
	}
}

func writeFakeRemoteSSH(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
