package relay

import (
	"log"
	"os"
	"strconv"
	"time"
)

// ResourceLimits bounds relay state. Zero values select generous defaults;
// WT_RELAY_* environment variables override defaults, and explicit config wins.
type ResourceLimits struct {
	PendingGrants      int
	PendingGrantsPerIP int
	MCPRegistrations   int
	MCPClients         int
	MCPRegistrationTTL time.Duration
}

func (l ResourceLimits) withDefaults() ResourceLimits {
	l.PendingGrants = relayLimitInt(l.PendingGrants, "PENDING_GRANTS", 10000)
	l.PendingGrantsPerIP = relayLimitInt(l.PendingGrantsPerIP, "PENDING_GRANTS_PER_IP", 64)
	l.MCPRegistrations = relayLimitInt(l.MCPRegistrations, "MCP_REGISTRATIONS", 1000)
	l.MCPClients = relayLimitInt(l.MCPClients, "MCP_CLIENTS", 1000)
	l.MCPRegistrationTTL = relayLimitDuration(l.MCPRegistrationTTL, "MCP_REGISTRATION_TTL", time.Hour)
	return l
}

func relayLimitInt(value int, name string, fallback int) int {
	if value > 0 {
		return value
	}
	if raw := os.Getenv("WT_RELAY_" + name); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
		log.Printf("invalid WT_RELAY_%s; using %d", name, fallback)
	}
	return fallback
}

func relayLimitDuration(value time.Duration, name string, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	if raw := os.Getenv("WT_RELAY_" + name); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
		log.Printf("invalid WT_RELAY_%s; using %s", name, fallback)
	}
	return fallback
}
