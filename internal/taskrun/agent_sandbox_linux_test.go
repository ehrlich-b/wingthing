//go:build linux

package taskrun

import (
	"os"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func init() {
	if len(os.Args) > 1 && os.Args[1] == "_deny_init" {
		sandbox.DenyInit(os.Args[2:])
	}
}
