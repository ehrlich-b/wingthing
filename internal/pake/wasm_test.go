//go:build !cgo

package pake

import (
	"bytes"
	"crypto/sha256"
	"os"
	"testing"
)

func TestEmbeddedWASMMatchesBrowser(t *testing.T) {
	browser, err := os.ReadFile("../../web/src/pake/pake.wasm")
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(wasmBytes) != sha256.Sum256(browser) {
		t.Fatal("embedded BoringSSL WASM differs from web/src/pake/pake.wasm; copy the browser asset after rebuilding it")
	}
}

func TestWASMRejectsMalformedMessageAndCloses(t *testing.T) {
	for _, message := range [][]byte{nil, make([]byte, 31), make([]byte, 33)} {
		x, _, err := New(false, "secret", "wing")
		if err != nil {
			t.Fatal(err)
		}
		module := x.module
		if _, err := x.Finish(message); err == nil {
			t.Fatal("accepted malformed peer message")
		}
		if !module.IsClosed() {
			t.Fatal("failed exchange retained WASM memory")
		}
		if _, err := x.Finish(make([]byte, 32)); err == nil {
			t.Fatal("reused failed exchange")
		}
		x.Close()
	}
}

func TestWASMExchangesAreIsolated(t *testing.T) {
	for range 4 {
		t.Run("exchange", func(t *testing.T) {
			t.Parallel()
			a, am, err := New(true, "secret", "wing")
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			b, bm, err := New(false, "secret", "wing")
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if a.module == b.module || bytes.Equal(am, bm) {
				t.Fatal("shared instance or reused entropy")
			}
			module := a.module
			ak, err := a.Finish(bm)
			if err != nil {
				t.Fatal(err)
			}
			bk, err := b.Finish(am)
			if err != nil || len(ak) != 64 || !bytes.Equal(ak, bk) {
				t.Fatalf("isolated exchange failed: %v", err)
			}
			if !module.IsClosed() {
				t.Fatal("completed exchange retained WASM memory")
			}
		})
	}
}
