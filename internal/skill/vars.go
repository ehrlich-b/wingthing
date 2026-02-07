package skill

import "strings"

func ResolveVars(mounts []string, vars map[string]string) []string {
	resolved := make([]string, len(mounts))
	for i, m := range mounts {
		r := m
		for k, v := range vars {
			r = strings.ReplaceAll(r, "$"+k, v)
		}
		resolved[i] = r
	}
	return resolved
}
