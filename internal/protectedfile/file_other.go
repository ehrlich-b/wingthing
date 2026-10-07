//go:build !darwin && !linux

package protectedfile

import "os"

func Open(path string) (*File, error) {
	return nil, &Error{path, "descriptor validation unsupported on this platform"}
}
func inspect(f *os.File, policy bool) (os.FileInfo, uint64, uint64, error) {
	return nil, 0, 0, &Error{f.Name(), "descriptor validation unsupported on this platform"}
}

func open(path string, policy bool) (*File, error) { return Open(path) }
func OpenPolicy(path string) (*File, error)        { return Open(path) }

func inspectSecretDirectory(info os.FileInfo) error {
	return &Error{info.Name(), "secret directory validation unsupported on this platform"}
}
