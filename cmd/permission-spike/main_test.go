package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestSettingsHookTimeoutPreservesDeadlineHeadroom(t *testing.T) {
	for _, tc := range []struct {
		deadline string
		timeout  int
	}{{"250ms", 4}, {"1s", 4}, {"1.5s", 5}, {"10s", 13}} {
		t.Run(tc.deadline, func(t *testing.T) {
			output, err := os.CreateTemp(testsocket.Dir(t), "settings")
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			previousStdout := os.Stdout
			os.Stdout = output
			defer func() { os.Stdout = previousStdout }()
			err = run("settings", []string{"--socket", "/fixture/b.sock", "--session", "fixture", "--deadline", tc.deadline})
			os.Stdout = previousStdout
			if err != nil {
				t.Fatal(err)
			}
			if _, err := output.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			var settings struct {
				Hooks map[string][]struct {
					Matcher string `json:"matcher"`
					Hooks   []struct {
						Command string `json:"command"`
						Timeout int    `json:"timeout"`
					} `json:"hooks"`
				} `json:"hooks"`
			}
			if err := json.NewDecoder(output).Decode(&settings); err != nil {
				t.Fatal(err)
			}
			requests := settings.Hooks["PermissionRequest"]
			if len(settings.Hooks) != 1 || len(requests) != 1 || requests[0].Matcher != "Bash" || len(requests[0].Hooks) != 1 {
				t.Fatalf("unexpected hook settings: %+v", settings)
			}
			hook := requests[0].Hooks[0]
			if hook.Timeout != tc.timeout || !strings.Contains(hook.Command, "--deadline '"+tc.deadline+"'") {
				t.Fatalf("deadline %s needs timeout %d with unchanged application deadline, got %+v", tc.deadline, tc.timeout, hook)
			}
		})
	}
}
