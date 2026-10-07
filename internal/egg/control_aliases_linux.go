//go:build linux

package egg

import "github.com/ehrlich-b/wingthing/internal/protectedfile"

func physicalControlAliases(control []string) ([]string, error) {
	return protectedfile.Aliases(control)
}
