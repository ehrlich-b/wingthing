//go:build !darwin && !linux

package protectedfile

import "os"

func Open(path string) (*File, error) {
	return nil, &Error{path, "descriptor validation unsupported on this platform"}
}
func inspect(f *os.File) (os.FileInfo, uint64, uint64, error) {
	return nil, 0, 0, &Error{f.Name(), "descriptor validation unsupported on this platform"}
}
