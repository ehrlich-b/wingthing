package egg

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestDriverSelectionAndRequirementsFailBeforePreparation(t *testing.T) {
	for _, id := range []string{"", "legacy"} {
		d, err := SelectDriver(id)
		if err != nil || d.ID() != "legacy" {
			t.Fatalf("selection %q: %v / %v", id, d, err)
		}
	}
	for _, opts := range []LaunchPreparationOptions{
		{DriverID: "unknown"},
		{RequiredCapabilities: []CapabilityRequirement{{"filesystem", "sandbox-process-tree"}}},
		{RequiredCapabilities: []CapabilityRequirement{{"controller-only-auth", "whole-harness"}}},
		{RequiredCapabilities: []CapabilityRequirement{{"unknown", "whole-harness"}}},
	} {
		// Nil state and invalid command would fail later; admission must happen
		// first, without invoking state, provider or credential preparation.
		_, err := PrepareLaunch(RunConfig{}, opts, nil)
		if err == nil || !(strings.Contains(err.Error(), "unknown egg driver") || strings.Contains(err.Error(), "cannot verify required")) {
			t.Fatalf("admission: %v", err)
		}
	}
	r := DriverReport{Capabilities: []DriverCapability{{Control: "filesystem", Scope: "shell", Status: CapabilitySupported}}}
	if err := r.Require([]CapabilityRequirement{{"filesystem", "shell"}}); err != nil {
		t.Fatal(err)
	}
	if r.Require([]CapabilityRequirement{{"filesystem", "whole-harness"}}) == nil {
		t.Fatal("shell claim satisfied whole-harness requirement")
	}
}

func TestLegacyDriverCapabilityCoverageAndHonesty(t *testing.T) {
	for _, platform := range []string{"linux", "darwin", "unknown"} {
		for _, outer := range []bool{false, true} {
			r := (LegacyDriver{}).Capabilities(platform, outer)
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			again, _ := json.Marshal((LegacyDriver{}).Capabilities(platform, outer))
			if string(data) != string(again) {
				t.Fatal("non-deterministic capability report")
			}
			var processes []string
			for _, c := range r.Coverage {
				if c.Owner == "" || c.Exception == "" {
					t.Fatalf("missing enforcing owner/exception: %+v", c)
				}
				processes = append(processes, c.Process)
			}
			if !reflect.DeepEqual(processes, []string{"harness-startup", "shell", "file-tools", "mcp", "lsp", "hooks", "setup", "git-helpers", "subagents", "wingthing-controller"}) {
				t.Fatalf("coverage: %v", processes)
			}
			for _, c := range r.Capabilities {
				if c.Status == CapabilitySupported {
					t.Fatal("static report claimed deployed capability")
				}
				if (outer || platform == "unknown") && c.Status != CapabilityUnsupported {
					t.Fatalf("unavailable boundary claimed %s", c.Status)
				}
			}
		}
	}
}

func TestLegacyDriverProbeAttestsOnlyStartup(t *testing.T) {
	r, err := (LegacyDriver{}).Probe()
	for _, c := range r.Capabilities {
		if c.Control != "sandbox-startup" && c.Status == CapabilitySupported {
			t.Fatalf("startup certified %s", c.Control)
		}
		if c.Control == "sandbox-startup" && (c.Status == CapabilitySupported) != (err == nil) {
			t.Fatalf("probe status %s disagrees with %v", c.Status, err)
		}
	}
}
