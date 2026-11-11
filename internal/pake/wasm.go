//go:build !cgo

// Package pake wraps BoringSSL SPAKE2; it contains no PAKE arithmetic.
package pake

import (
	"context"
	"crypto/rand"
	_ "embed"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const Available = true

// This is a byte-for-byte copy of web/src/pake/pake.wasm, built by
// web/pake/build.sh from the same vendored BoringSSL sources as the CGO path.
// Keep the copies in sync; TestEmbeddedWASMMatchesBrowser checks their hashes.
//
//go:embed pake.wasm
var wasmBytes []byte

type wasmCode struct {
	runtime wazero.Runtime
	module  wazero.CompiledModule
}

var compiledWASM = sync.OnceValues(func() (*wasmCode, error) {
	ctx := context.Background()
	r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(256))
	_, err := r.NewHostModuleBuilder("host").
		NewFunctionBuilder().WithFunc(func(_ context.Context, m api.Module, ptr, size uint32) {
		// The vetted SPAKE2 generator requests 64 bytes from RAND_bytes.
		// A host failure traps the call; its isolated instance is discarded.
		if size != 64 {
			panic("unexpected SPAKE2 entropy request")
		}
		out, ok := m.Memory().Read(ptr, size)
		if !ok {
			panic("SPAKE2 entropy out of bounds")
		}
		if _, err := rand.Read(out); err != nil {
			panic("SPAKE2 entropy unavailable")
		}
	}).Export("random").
		NewFunctionBuilder().WithFunc(func() { panic("BoringSSL aborted") }).Export("abort").
		Instantiate(ctx)
	if err != nil {
		_ = r.Close(ctx)
		return nil, err
	}
	compiled, err := r.CompileModule(ctx, wasmBytes)
	if err != nil {
		_ = r.Close(ctx)
		return nil, err
	}
	return &wasmCode{r, compiled}, nil
})
