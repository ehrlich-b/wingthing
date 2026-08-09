package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/promptmgr"
)

func TestParsePromptValues(t *testing.T) {
	values, err := parsePromptValues([]string{"target=parser", "note=a=b"})
	if err != nil {
		t.Fatal(err)
	}
	if values["target"] != "parser" || values["note"] != "a=b" {
		t.Fatalf("values = %#v", values)
	}
	if _, err := parsePromptValues([]string{"broken"}); err == nil {
		t.Fatal("invalid variable was accepted")
	}
	if _, err := parsePromptValues([]string{"a=1", "a=2"}); err == nil {
		t.Fatal("duplicate variable was accepted")
	}
}

func TestPromptSaveAndListCLI(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("WINGTHING_DIR", dir)

	var saved bytes.Buffer
	save := promptSaveCmd()
	save.SetOut(&saved)
	save.SetErr(&saved)
	save.SetArgs([]string{
		"review", "--template", "Review {{.target}}", "--description", "Review code",
		"--variable", "target", "--agent", "opencode", "--cwd", cwd, "--json",
	})
	if err := save.Execute(); err != nil {
		t.Fatal(err)
	}
	var asset promptmgr.Asset
	if err := json.Unmarshal(saved.Bytes(), &asset); err != nil {
		t.Fatalf("decode save output: %v\n%s", err, saved.String())
	}
	if asset.Name != "review" || asset.Revision == "" || asset.CWD != cwd {
		t.Fatalf("saved asset = %+v", asset)
	}

	var listed bytes.Buffer
	list := promptListCmd()
	list.SetOut(&listed)
	list.SetErr(&listed)
	list.SetArgs([]string{"--json"})
	if err := list.Execute(); err != nil {
		t.Fatal(err)
	}
	var assets []promptmgr.Asset
	if err := json.Unmarshal(listed.Bytes(), &assets); err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 || assets[0].Revision != asset.Revision {
		t.Fatalf("listed assets = %+v", assets)
	}
}
