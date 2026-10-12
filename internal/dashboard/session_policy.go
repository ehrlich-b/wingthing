package dashboard

import (
	"fmt"
	"strings"
)

func sessionPolicyLines(r row) []string {
	p := r.Policy
	if p == nil || p.Version != 1 || p.SessionID != r.ID || p.Workspace != r.CWD {
		return []string{"Workspace: " + r.CWD + " (reported cwd; permissions unverified)",
			"Provider state: not reported", "Network: not reported", "Denied: not reported",
			"Effective launch policy unavailable; no confinement claim."}
	}
	lines := []string{"Workspace: " + p.Workspace, "Provider state: " + p.ProviderHome}
	if !p.Sandboxed {
		return append(lines, "Privileged mode: full OS-user authority; Wingthing sandbox disabled",
			"Network: full OS-user authority", "Denied: no Wingthing confinement")
	}
	if p.Source != "" {
		lines = append(lines, "Policy source at admission: "+p.Source)
	}
	lines = append(lines, "Prepared runtime policy; native confinement requires host verification.")
	for _, mount := range p.Mounts {
		access := "read/write"
		if mount.ReadOnly {
			access = "read-only"
		}
		kind := "subtree"
		if mount.UseRegex {
			kind = "pattern"
		}
		lines = append(lines, fmt.Sprintf("Filesystem: %s %s (%s)", access, mount.Source, kind))
	}
	lines = append(lines, "Denied: "+policyList(p.Denied), "Denied writes: "+policyList(p.DeniedWrite))
	mode := p.NetworkMode
	if mode == "" {
		mode = "enforce"
	}
	lines = append(lines, fmt.Sprintf("Network: %s; domain decisions: %s", p.Network, mode),
		"Network domains: "+policyList(p.Domains), fmt.Sprintf("Network loopback ports: %v", p.LocalPorts))
	return lines
}

func policyList(values []string) string {
	if len(values) == 0 {
		return "none declared"
	}
	return strings.Join(values, ", ")
}
