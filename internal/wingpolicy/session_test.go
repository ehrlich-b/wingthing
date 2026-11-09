package wingpolicy

import (
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestSessionPolicyContainsFilesystemRoot(t *testing.T) {
	root := filepath.VolumeName(testsocket.Dir(t)) + string(filepath.Separator)
	if !SessionPolicyContains(root, filepath.Join(root, "private", "data.txt")) {
		t.Fatal("filesystem root did not contain an absolute child")
	}
}
