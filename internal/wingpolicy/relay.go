package wingpolicy

import (
	"net/url"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/config"
)

// ResolveRelayHTTPURL returns the relay's HTTP base URL from config.
func ResolveRelayHTTPURL(cfg *config.Config) string {
	relayURL := cfg.RoostURL
	if relayURL == "" {
		if wc, err := config.LoadWingConfig(cfg.Dir); err == nil && wc.Roost != "" {
			relayURL = wc.Roost
		}
	}
	if relayURL == "" {
		relayURL = config.DefaultRelayURL()
	}
	return NormalizeRelayHTTPURL(relayURL)
}

// NormalizeRelayHTTPURL converts a wing/coordinator URL to an HTTP base URL.
func NormalizeRelayHTTPURL(relayURL string) string {
	if relayURL == "" {
		return ""
	}
	relayURL = strings.TrimRight(relayURL, "/")
	relayURL = strings.Replace(relayURL, "wss://", "https://", 1)
	relayURL = strings.Replace(relayURL, "ws://", "http://", 1)
	if !strings.HasPrefix(relayURL, "http://") && !strings.HasPrefix(relayURL, "https://") {
		relayURL = "https://" + relayURL
	}
	return relayURL
}

// RelayMetadataURL removes URL components that must not be persisted or copied
// into support bundles. Coordinator API routing can retain a path prefix, but
// userinfo, queries, and fragments are never part of the coordinator identity.
func RelayMetadataURL(relayURL string) string {
	normalized := NormalizeRelayHTTPURL(relayURL)
	parsed, err := url.Parse(normalized)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

// ResolveWingRelayHTTPURL mirrors the daemon's precedence: explicit flag,
// wing.yaml, local default, config.yaml, then the hosted coordinator.
func ResolveWingRelayHTTPURL(cfg *config.Config, explicit string, local bool) string {
	relayURL := explicit
	if relayURL == "" && cfg != nil {
		if wingCfg, err := config.LoadWingConfig(cfg.Dir); err == nil {
			relayURL = wingCfg.Roost
		}
	}
	if relayURL == "" && local {
		relayURL = config.DefaultLocalRelayURL()
	}
	if relayURL == "" && cfg != nil {
		relayURL = cfg.RoostURL
	}
	if relayURL == "" {
		relayURL = config.DefaultRelayURL()
	}
	return NormalizeRelayHTTPURL(relayURL)
}

// RoostBrowserURL returns the UI served by the selected coordinator. The
// public service has split ws/app hosts; a self-hosted roost serves its app at
// /app/ on the same origin.
func RoostBrowserURL(roostURL string) string {
	httpURL := NormalizeRelayHTTPURL(roostURL)
	parsed, err := url.Parse(httpURL)
	if err != nil || parsed.Hostname() == "" {
		return httpURL
	}
	// The browser destination is display output. Never echo URL credentials,
	// even if a caller supplied a credentialed coordinator URL.
	parsed.User = nil
	switch strings.ToLower(parsed.Hostname()) {
	case "ws.wingthing.ai", "wingthing.ai", "app.wingthing.ai":
		parsed.Scheme = "https"
		parsed.Host = "app.wingthing.ai"
		parsed.Path = "/"
	default:
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/app/"
	}
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
