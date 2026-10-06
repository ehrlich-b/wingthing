package localrelay

import (
	"io"
	"testing"
)

func closeForTest(t *testing.T, name string, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Errorf("close %s: %v", name, err)
	}
}
