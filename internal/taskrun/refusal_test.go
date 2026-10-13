package taskrun

import (
	"errors"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"os"
	"strings"
	"testing"
)

func TestSandboxRefusalKeepsCauseAndHidesHostPaths(t *testing.T) {
	underlying := &os.PathError{Op: "mkdir", Path: "/sys/fs/cgroup/private/wt-egg-example", Err: os.ErrPermission}
	limits := &sandbox.ResourceLimitError{Cause: underlying}
	cause := &sandbox.EnforcementError{Cause: limits, Platform: limits.Error() + ". namespace or mount rejected"}
	quiet := &sandboxRefusal{cause: cause}
	if !errors.Is(quiet, os.ErrPermission) {
		t.Fatal("lost underlying error")
	}
	if text := quiet.Error(); !strings.Contains(text, "memory/process limits") || !strings.Contains(text, "delegated") || strings.Contains(text, "/sys/") || strings.Contains(text, "namespace") || strings.Count(text, "Nothing ran") != 1 {
		t.Fatal(text)
	}
	verbose := (&sandboxRefusal{cause: cause, verbose: true}).Error()
	if !strings.Contains(verbose, underlying.Error()) || strings.Contains(verbose, "namespace") || strings.Count(verbose, "Nothing ran") != 1 {
		t.Fatal(verbose)
	}
	other := (&sandboxRefusal{cause: errors.New("namespace denied")}).Error()
	if !strings.Contains(other, "wt doctor") || strings.Contains(other, "cgroups") || strings.Contains(other, "namespace denied") {
		t.Fatal(other)
	}
}
