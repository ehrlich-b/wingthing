package egg

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func requireProtectedTargetError(t *testing.T, err error, reason string) {
	t.Helper()
	var pe *sandbox.ProtectedWriteTargetError
	if !errors.As(err, &pe) {
		t.Fatalf("expected ProtectedWriteTargetError, got %T: %v", err, err)
	}
	if !strings.Contains(pe.Reason, reason) {
		t.Fatalf("reason %q does not contain %q", pe.Reason, reason)
	}
}

func TestProtectedWriteTargetsEmptySetPreservesBothBoundaries(t *testing.T) {
	for _, hasSandbox := range []bool{true, false} {
		if err := ValidateProtectedWriteTargetBoundary(nil, hasSandbox); err != nil {
			t.Fatalf("hasSandbox=%v: empty protected set refused: %v", hasSandbox, err)
		}
	}
}

func TestProtectedWriteTargetsRequireSandboxPolicy(t *testing.T) {
	if err := ValidateProtectedWriteTargetBoundary([]string{"/state/parent"}, true); err != nil {
		t.Fatalf("sandboxed protected set refused early: %v", err)
	}
	requireProtectedTargetError(t, ValidateProtectedWriteTargetBoundary([]string{"/state/parent"}, false), "outer-boundary")
	requireProtectedTargetError(t, ValidateProtectedWriteTargetBoundary([]string{"state/parent"}, true), "absolute")
}

// RunSession must refuse before resolving or starting the command, so these
// cases never probe the sandbox or spawn a process.
func TestRunSessionRefusesUnenforceableProtectedWriteTargets(t *testing.T) {
	for name, tc := range map[string]struct {
		rc     RunConfig
		reason string
	}{
		"outer boundary": {RunConfig{OuterBoundary: true, Network: []string{"*"}, ProtectedWriteTargets: []string{"/state/parent"}}, "outer-boundary"},
		"relative":       {RunConfig{ProtectedWriteTargets: []string{"state/parent"}}, "absolute"},
	} {
		t.Run(name, func(t *testing.T) {
			srv, err := NewServer(shortEndpointTempDir(t))
			if err != nil {
				t.Fatal(err)
			}
			rc := tc.rc
			rc.Command = []string{"/nonexistent/wt-protected-target-test"}
			rc.Rows, rc.Cols = 24, 80
			requireProtectedTargetError(t, srv.RunSession(context.Background(), rc), tc.reason)
		})
	}
}
