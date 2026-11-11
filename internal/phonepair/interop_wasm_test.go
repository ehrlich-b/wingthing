//go:build !cgo

package phonepair

import (
	"github.com/ehrlich-b/wingthing/internal/pake"
	"testing"
)

// This file is selected only for GOARCH=wasm; host cross-build tests live in
// interop_release_test.go because Go interprets the _wasm suffix as GOARCH.
func TestWASMTargetPairingAvailable(t *testing.T) {
	if !pake.Available {
		t.Fatal("WASM target must retain BoringSSL pairing")
	}
}
