package egg

import "strings"

// LaunchEnvironment applies the runtime's profile reinjection to an explicit
// caller-selected environment and host snapshot. It never reads ambient values.
func LaunchEnvironment(rc RunConfig, selected map[string]string, inherited []string) map[string]string {
	env := make(map[string]string, len(selected))
	for k, v := range selected {
		env[k] = v
	}
	host := make(map[string]string, len(inherited))
	for _, entry := range inherited {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			host[k] = v
		}
	}
	profile := Profile(rc.Agent)
	if !rc.SkipHostAgentEnv {
		for _, k := range append(append([]string(nil), profile.EnvVars...), profile.PlatformEnv...) {
			if _, ok := env[k]; !ok && host[k] != "" {
				env[k] = host[k]
			}
		}
	}
	for k, v := range profile.SetEnv {
		if _, ok := env[k]; !ok {
			env[k] = v
		}
	}
	for _, k := range []string{"HOME", "PATH", "TERM"} {
		if _, ok := env[k]; !ok && host[k] != "" {
			env[k] = host[k]
		}
	}
	if env["TERM"] == "" {
		env["TERM"] = "xterm-256color"
	}
	if env["GOTRACEBACK"] == "" {
		env["GOTRACEBACK"] = "all"
	}
	if rc.Kind == "agent" {
		if _, ok := env["GIT_TERMINAL_PROMPT"]; !ok {
			env["GIT_TERMINAL_PROMPT"] = "0"
		}
	}
	if rc.UserHome != "" {
		env["HOME"] = rc.UserHome
	}
	return env
}
