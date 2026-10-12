//go:build linux

package egg

import (
	"os"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

// Native sandbox tests re-exec the test binary as the enforcement wrapper,
// just as the production CLI dispatches this argument before Cobra starts.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_deny_init" {
		sandbox.DenyInit(os.Args[2:])
		return
	}
	os.Exit(m.Run())
}
