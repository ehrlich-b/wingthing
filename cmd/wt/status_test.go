package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestStatusJSONIsMachineReadable(t *testing.T) {
	t.Setenv("WINGTHING_DIR", t.TempDir())
	cmd := statusCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	var status struct {
		PendingTasks int `json:"pending_tasks"`
		RunningTasks int `json:"running_tasks"`
		Agents       int `json:"agents"`
		TokensToday  int `json:"tokens_today"`
		TokensWeek   int `json:"tokens_week"`
	}
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		t.Fatalf("decode status JSON: %v\n%s", err, output.String())
	}
	if status.PendingTasks != 0 || status.RunningTasks != 0 || status.Agents != 0 ||
		status.TokensToday != 0 || status.TokensWeek != 0 {
		t.Fatalf("empty status = %#v", status)
	}
}

func TestStatusKeepsHumanReadableOutput(t *testing.T) {
	t.Setenv("WINGTHING_DIR", t.TempDir())
	cmd := statusCmd()
	cmd.SetArgs([]string{})
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	const want = "pending: 0\nrunning: 0\nagents:  0\ntokens:  0 today / 0 this week\n"
	if output.String() != want {
		t.Fatalf("status output = %q, want %q", output.String(), want)
	}
}
