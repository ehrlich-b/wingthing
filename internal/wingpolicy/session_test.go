package wingpolicy

import (
	"path/filepath"
	"testing"
)

func TestSessionPolicyContainsFilesystemRoot(t *testing.T) {
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	if !SessionPolicyContains(root, filepath.Join(root, "private", "data.txt")) {
		t.Fatal("filesystem root did not contain an absolute child")
	}
}
