//go:build !cgo

package pake

import "errors"

const Available = false

var ErrUnavailable = errors.New("phone pairing requires a native build with CGO_ENABLED=1 and a C++17 compiler; see docs/pairing/PROTOCOL.md")

type Exchange struct{}

func New(bool, string, string) (*Exchange, []byte, error) { return nil, nil, ErrUnavailable }
func (*Exchange) Finish([]byte) ([]byte, error)           { return nil, ErrUnavailable }
func (*Exchange) Close()                                  {}
