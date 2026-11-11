//go:build !cgo

package pake

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// Each exchange owns a separate module, allocator and BoringSSL context. Only
// compiled code is shared; closing an exchange releases its entire linear memory.
type Exchange struct {
	module api.Module
	ctx    uint64
}

func New(alice bool, secret, wingIdentity string) (*Exchange, []byte, error) {
	code, err := compiledWASM()
	if err != nil {
		return nil, nil, fmt.Errorf("SPAKE2 initialization failed: %w", err)
	}
	module, err := code.runtime.InstantiateModule(context.Background(), code.module,
		wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize"))
	if err != nil {
		return nil, nil, fmt.Errorf("SPAKE2 initialization failed: %w", err)
	}
	e := &Exchange{module: module}
	message, err := e.generate(alice, secret, wingIdentity)
	if err != nil {
		e.Close()
		return nil, nil, err
	}
	runtime.SetFinalizer(e, (*Exchange).Close)
	return e, message, nil
}

func (e *Exchange) generate(alice bool, secret, wingIdentity string) ([]byte, error) {
	mine, peer, role := []byte(wingIdentity), []byte("wt-phone-v1"), uint64(0)
	if alice {
		mine, peer, role = peer, mine, 1
	}
	m, err := e.alloc(mine)
	if err != nil {
		return nil, err
	}
	defer e.free(m, len(mine))
	p, err := e.alloc(peer)
	if err != nil {
		return nil, err
	}
	defer e.free(p, len(peer))
	e.ctx, err = e.call("pake_new", role, m, uint64(len(mine)), p, uint64(len(peer)))
	if err != nil || e.ctx == 0 {
		return nil, fmt.Errorf("SPAKE2 initialization failed: %v", err)
	}
	password := []byte(secret)
	defer clear(password)
	s, err := e.alloc(password)
	if err != nil {
		return nil, err
	}
	defer e.free(s, len(password))
	out, err := e.alloc(make([]byte, 32))
	if err != nil {
		return nil, err
	}
	defer e.free(out, 32)
	ok, err := e.call("pake_generate", e.ctx, s, uint64(len(password)), out)
	if err != nil || ok != 1 {
		return nil, fmt.Errorf("SPAKE2 message failed: %v", err)
	}
	return e.read(out, 32)
}

func (e *Exchange) Finish(message []byte) ([]byte, error) {
	if e.module == nil || e.ctx == 0 {
		return nil, errors.New("SPAKE2 context used")
	}
	defer e.Close()
	if len(message) != 32 {
		return nil, errors.New("SPAKE2 peer message refused")
	}
	p, err := e.alloc(message)
	if err != nil {
		return nil, err
	}
	defer e.free(p, len(message))
	out, err := e.alloc(make([]byte, 64))
	if err != nil {
		return nil, err
	}
	defer e.free(out, 64)
	ok, err := e.call("pake_finish", e.ctx, p, uint64(len(message)), out)
	if err != nil || ok != 1 {
		return nil, fmt.Errorf("SPAKE2 peer message refused: %v", err)
	}
	return e.read(out, 64)
}

func (e *Exchange) Close() {
	if e.module != nil {
		if e.ctx != 0 {
			_, _ = e.call("pake_close", e.ctx)
		}
		_ = e.module.Close(context.Background())
		e.module, e.ctx = nil, 0
	}
	runtime.SetFinalizer(e, nil)
}

func (e *Exchange) call(name string, args ...uint64) (uint64, error) {
	result, err := e.module.ExportedFunction(name).Call(context.Background(), args...)
	if err != nil {
		return 0, err
	}
	if len(result) == 0 {
		return 0, nil
	}
	return result[0], nil
}

func (e *Exchange) alloc(data []byte) (uint64, error) {
	p, err := e.call("pake_alloc", uint64(max(1, len(data))))
	if err != nil || p == 0 {
		return 0, fmt.Errorf("SPAKE2 allocation failed: %v", err)
	}
	if !e.module.Memory().Write(uint32(p), data) {
		return 0, errors.New("SPAKE2 memory out of bounds")
	}
	return p, nil
}

func (e *Exchange) free(p uint64, size int) {
	if b, ok := e.module.Memory().Read(uint32(p), uint32(size)); ok {
		clear(b)
	}
	_, _ = e.call("pake_free", p)
}

func (e *Exchange) read(p uint64, size uint32) ([]byte, error) {
	b, ok := e.module.Memory().Read(uint32(p), size)
	if !ok {
		return nil, errors.New("SPAKE2 memory out of bounds")
	}
	return append([]byte(nil), b...), nil
}
