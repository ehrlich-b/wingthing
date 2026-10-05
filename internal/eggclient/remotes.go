package eggclient

import (
	"sort"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func SortedRemoteNames(remotes map[string]config.Remote) []string {
	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
