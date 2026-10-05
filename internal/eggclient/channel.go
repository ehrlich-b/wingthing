package eggclient

import (
	"os"

	"github.com/ehrlich-b/wingthing/internal/procinfo"

	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
)

func previewEggProcessMatches(pid int, sessionID string) bool {
	argv, err := procinfo.ProcessArgv(pid)
	if err != nil || len(argv) < 5 {
		return false
	}
	exe, err := os.Executable()
	if err != nil || wingpolicy.CanonicalPolicyPath(argv[0]) != wingpolicy.CanonicalPolicyPath(exe) || argv[1] != "egg" || argv[2] != "run" {
		return false
	}
	for i, arg := range argv {
		if arg == "--session-id" && i+1 < len(argv) && argv[i+1] == sessionID {
			return true
		}
	}
	return false
}
