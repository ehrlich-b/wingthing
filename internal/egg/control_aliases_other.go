//go:build !linux

package egg

func physicalControlAliases(control []string) ([]string, error) { return control, nil }
