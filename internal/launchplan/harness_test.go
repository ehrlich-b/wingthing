package launchplan

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLaunchPlanClaudeHarnessPolicy(t *testing.T) {
	req, _ := setup(t)
	req.Agent = "claude"
	req.Unattended = false
	policy(t, &req, "harness:\n  driver: claude-code\n  version: 2.1.285\n  permission_mode: dontAsk\n")
	evidence(t, &req, "2.1.285 (Claude Code)", "", false)
	report, err := Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	if report.Harness == nil || report.Harness.PermissionMode != "dontAsk" {
		t.Fatal("native policy omitted from launch report")
	}
	hasGate(t, report, "harness.native", Unrun, "runtime conformance")
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"failIfUnavailable":true`) {
		t.Fatal("effective settings omitted")
	}
	evidence(t, &req, "2.1.284 (Claude Code)", "", false)
	report, err = Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	hasGate(t, report, "provider.preparation", Fail, "does not match")
}
