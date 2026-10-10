package egg

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestInitialRunPrivateFileTransport(t *testing.T) {
	dir := t.TempDir()
	request := &RunTurnRequest{RunID: "private-initial", Prompt: strings.Repeat("Ω🙂\n", MaxRunPromptBytes/7), Deadline: time.Now().UTC().Add(time.Hour)}
	path, err := WriteInitialRunFile(dir, request)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private file permissions: %v %v", info, err)
	}
	if _, err := WriteInitialRunFile(dir, request); err == nil {
		t.Fatal("overwrote an existing request")
	}
	got, err := ReadInitialRunFile(dir)
	if err != nil || !reflect.DeepEqual(got, request) {
		t.Fatalf("initial run transport changed request: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("initial run file was not deleted after read")
	}
	if _, err := ReadInitialRunFile(dir); err == nil {
		t.Fatal("consumed request was replayed")
	}
}

func TestInitialRunFileRejectsUnsafeAndMalformedFiles(t *testing.T) {
	for _, test := range []struct {
		name, data string
		mode       os.FileMode
	}{
		{"public", `{}`, 0644},
		{"unknown_field", `{"unknown":true}`, 0600},
		{"trailing_json", `{} {}`, 0600},
		{"malformed", `{`, 0600},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, initialRunFile), []byte(test.data), test.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadInitialRunFile(dir); err == nil {
				t.Fatal("accepted unsafe or malformed initial request")
			}
		})
	}
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "request")
	if err := os.WriteFile(target, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, initialRunFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadInitialRunFile(dir); err == nil {
		t.Fatal("followed a request symlink")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("deleted symlink target")
	}
}
