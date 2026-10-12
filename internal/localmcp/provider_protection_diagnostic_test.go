package localmcp

import (
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func TestUnsupportedProviderProtectionRetainsPolicyOverWire(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("unsupported-platform diagnostic")
	}
	cfg := egg.DefaultEggConfig()
	cfg.LaunchPolicy = &egg.LaunchPolicySelection{Origin: "selected policy", Path: "/work/egg.yaml"}
	_, err := modelProviderWrites(&config.Config{Dir: "/state"}, cfg, "claude", "/work", "parent", eggclient.EggIdentity{})
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("unsupported boundary lost: %v", err)
	}
	received := control.ToolError(control.ErrorResult(err))
	d, ok := control.LaunchDiagnosticOf(received)
	if !ok || d.Reason != "provider_protection_unsupported" || d.PolicyPath != "/work/egg.yaml" || d.PolicyOrigin != "selected policy" {
		t.Fatalf("wire projection lost diagnosis: %+v", d)
	}
	text := control.FormatLaunchDiagnostic(d)
	for _, want := range []string{"macOS Seatbelt", "supported macOS host", "/work/egg.yaml", "retain the selected policy"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
}
