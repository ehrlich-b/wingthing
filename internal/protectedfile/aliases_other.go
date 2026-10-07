//go:build !linux

package protectedfile

func Aliases(paths []string) ([]string, error) { return paths, nil }
func (f *File) Aliases() ([]string, error)     { return []string{f.Name()}, nil }
