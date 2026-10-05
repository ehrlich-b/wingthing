package procinfo

import (
	"syscall"
	"testing"
)

func TestOwnedProcessSignalPermissionDeniedMeansRecycledPID(t *testing.T) {
	if ownedProcessSignalIndicatesAlive(syscall.EPERM) {
		t.Fatal("permission-denied owned-process probe was treated as the recorded child")
	}
}
