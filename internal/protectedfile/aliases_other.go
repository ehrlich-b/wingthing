//go:build !linux

package protectedfile

func Aliases(paths []string) ([]string, error) { return paths, nil }
func (f *File) Aliases() ([]string, error) {
	if _, _, _, err := inspect(f.File); err != nil {
		return nil, err
	}
	resolved, err := f.ResolvedPath()
	if err != nil {
		return nil, err
	}
	if resolved != f.Name() {
		return []string{f.Name(), resolved}, nil
	}
	return []string{f.Name()}, nil
}
