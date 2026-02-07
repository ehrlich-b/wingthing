package sandbox

// Level defines the isolation level for a sandbox.
type Level int

const (
	Strict     Level = iota // No network, minimal fs, short TTL
	Standard                // No network, mounted dirs only
	Network                 // Network allowed, mounted dirs only
	Privileged              // Full access (for trusted skills)
)

func (l Level) String() string {
	switch l {
	case Strict:
		return "strict"
	case Standard:
		return "standard"
	case Network:
		return "network"
	case Privileged:
		return "privileged"
	default:
		return "unknown"
	}
}

// ParseLevel converts a string to a Level.
func ParseLevel(s string) Level {
	switch s {
	case "strict":
		return Strict
	case "standard":
		return Standard
	case "network":
		return Network
	case "privileged":
		return Privileged
	default:
		return Standard
	}
}
