package launchplan

import (
	"strings"
	"testing"
)

func TestCodexSandboxDoctorConformance(t *testing.T) {
	req, _ := setup(t)
	policy(t, &req, "codex_native_sandbox: true\nnetwork: none\n")
	evidence(t, &req, "codex-cli 0.159.3", " -c, --config <KEY=VALUE>\n --no-daemon\n", true)
	report, err := Inspect(req)
	if err != nil {
		t.Fatal(err)
	}
	if report.CodexSandbox == nil || report.CodexSandbox.ApprovalPolicy != "never" || report.CodexSandbox.NetworkAccess {
		t.Fatalf("missing tool policy: %+v", report)
	}
	if !contains(report.CodexSandbox.Args, "sandbox_mode=\"workspace-write\"") || len(report.ProviderArgv) != 0 {
		t.Fatalf("missing render or launchable argv despite rejection: %+v", report)
	}
	hasGate(t, report, "sandbox.nested", Fail, "nesting")
	if report.Readiness != "blocked" {
		t.Fatalf("unverified nesting admitted: %s", report.Readiness)
	}
	found := false
	for _, source := range report.PolicyRules {
		if source.Rule == "codex_native_sandbox=true" && strings.HasSuffix(source.Source, "egg.yaml") {
			found = true
		}
	}
	if !found {
		t.Fatal("missing native policy provenance")
	}
}
