package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/egg"
)

func TestSessionReportCLIRecordsExactSessionAndRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WINGTHING_DIR", dir)
	sessionDir := filepath.Join(dir, "eggs", "report-target")
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "egg.meta"), []byte("agent=codex\ncwd="+dir+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"goal":"a","update":"b","next_action":"c","approve":true}`, `{"goal":"a","update":"b","next_action":"c"} {}`} {
		cmd := sessionReportCmd()
		cmd.SetArgs([]string{"report-target", "--json"})
		cmd.SetIn(strings.NewReader(body))
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	cmd := sessionReportCmd()
	cmd.SetArgs([]string{"report-target", "--json"})
	cmd.SetIn(strings.NewReader(`{"goal":"Finish","update":"Read results","next_action":"Review","presence":"asleep"}`))
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Session string           `json:"session"`
		Event   egg.SessionEvent `json:"event"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Session != "report-target" || result.Event.Sequence != 1 || result.Event.Report.Goal != "Finish" {
		t.Fatalf("result=%+v", result)
	}
}
