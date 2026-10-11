//go:build cgo

// Package pake wraps BoringSSL SPAKE2; it contains no PAKE arithmetic.
package pake

/*
#cgo CFLAGS: -I${SRCDIR}/boringssl/include -DOPENSSL_NO_ASM
#cgo CXXFLAGS: -I${SRCDIR}/boringssl/include -DOPENSSL_NO_ASM -std=c++17 -fno-exceptions
#cgo darwin LDFLAGS: -lc++
#cgo linux LDFLAGS: -lstdc++ -lpthread
#include <CCryptoBoringSSL_curve25519.h>
*/
import "C"
import (
	"errors"
	"runtime"
	"unsafe"
)

const Available = true

type Exchange struct{ ctx *C.SPAKE2_CTX }

func ptr(b []byte) *C.uint8_t {
	if len(b) == 0 {
		return nil
	}
	return (*C.uint8_t)(unsafe.Pointer(&b[0]))
}
func New(alice bool, secret, wingIdentity string) (*Exchange, []byte, error) {
	role := C.enum_spake2_role_t(C.spake2_role_bob)
	mine, peer := []byte(wingIdentity), []byte("wt-phone-v1")
	if alice {
		role = C.enum_spake2_role_t(C.spake2_role_alice)
		mine, peer = peer, mine
	}
	ctx := C.CCryptoBoringSSL_SPAKE2_CTX_new(role, ptr(mine), C.size_t(len(mine)), ptr(peer), C.size_t(len(peer)))
	if ctx == nil {
		return nil, nil, errors.New("SPAKE2 initialization failed")
	}
	e := &Exchange{ctx: ctx}
	runtime.SetFinalizer(e, (*Exchange).Close)
	msg := make([]byte, 32)
	var n C.size_t
	password := []byte(secret)
	if C.CCryptoBoringSSL_SPAKE2_generate_msg(ctx, ptr(msg), &n, 32, ptr(password), C.size_t(len(password))) != 1 {
		e.Close()
		return nil, nil, errors.New("SPAKE2 message failed")
	}
	return e, msg[:int(n)], nil
}
func (e *Exchange) Finish(message []byte) ([]byte, error) {
	if e.ctx == nil {
		return nil, errors.New("SPAKE2 context used")
	}
	defer e.Close()
	key := make([]byte, 64)
	var n C.size_t
	if C.CCryptoBoringSSL_SPAKE2_process_msg(e.ctx, ptr(key), &n, 64, ptr(message), C.size_t(len(message))) != 1 {
		return nil, errors.New("SPAKE2 peer message refused")
	}
	return key[:int(n)], nil
}
func (e *Exchange) Close() {
	if e.ctx != nil {
		C.CCryptoBoringSSL_SPAKE2_CTX_free(e.ctx)
		e.ctx = nil
	}
	runtime.SetFinalizer(e, nil)
}
