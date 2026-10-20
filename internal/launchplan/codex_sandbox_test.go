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
	if !contains(report.ProviderArgv, "sandbox_mode=\"workspace-write\"") || contains(report.ProviderArgv, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("incorrect argv: %v", report.ProviderArgv)
	}
	hasGate(t, report, "provider.preparation.deferred", Unrun, "Codex native tool sandbox availability")
	for _, gate := range report.Gates {
		if gate.Code == "sandbox.nested" && gate.Status == Fail {
			t.Fatal("explicit native opt-in treated as legacy nesting error")
		}
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
