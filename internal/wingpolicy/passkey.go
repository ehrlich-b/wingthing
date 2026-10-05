package wingpolicy

import (
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func PasskeySubject(userID, clientPublicKey string) string {
	if userID == "" || clientPublicKey == "" {
		return ""
	}
	return userID + "\x00" + clientPublicKey
}

// PasskeyRPURL picks the URL whose host anchors the WebAuthn relying party.
// The embedded roost wing connects over loopback while browsers reach the
// roost at its public base URL (WT_BASE_URL) — the same source the relay's
// passkey registration endpoint derives its RP ID from. RP ID and origin must
// match the browser-facing host or every WebAuthn ceremony fails closed.
func PasskeyRPURL(roostURL, baseURL string) string {
	if baseURL != "" {
		return baseURL
	}
	return roostURL
}

// PasskeyPolicyForRoost mirrors the relying-party configuration used by the
// relay's registration endpoint. A custom/self-hosted roost uses its own host;
// the managed websocket and app hosts share the wingthing.ai RP ID.
func PasskeyPolicyForRoost(roostURL string) auth.PasskeyPolicy {
	httpURL := strings.Replace(roostURL, "wss://", "https://", 1)
	httpURL = strings.Replace(httpURL, "ws://", "http://", 1)
	if !strings.Contains(httpURL, "://") {
		httpURL = "https://" + httpURL
	}
	u, err := url.Parse(httpURL)
	if err != nil || u.Hostname() == "" {
		return auth.PasskeyPolicy{}
	}
	hostname := strings.ToLower(u.Hostname())
	if hostname == "wingthing.ai" || strings.HasSuffix(hostname, ".wingthing.ai") {
		return auth.PasskeyPolicy{
			RPID:                    "wingthing.ai",
			Origins:                 []string{"https://app.wingthing.ai"},
			RequireUserVerification: true,
		}
	}
	origin := u.Scheme + "://" + u.Host
	origins := []string{origin}
	if hostname == "localhost" {
		for _, candidate := range []string{"http://localhost:8080", "http://localhost:5173"} {
			if candidate != origin {
				origins = append(origins, candidate)
			}
		}
	}
	return auth.PasskeyPolicy{
		RPID:                    hostname,
		Origins:                 origins,
		RequireUserVerification: true,
	}
}

// PasskeyPolicyFromRegistration accepts only a coherent RP policy delivered by
// the authenticated coordinator. Additive acknowledgement fields let new wings
// support custom AppHost and localhost HTTPS while retaining their URL-derived
// fallback when connected to an older relay.
func PasskeyPolicyFromRegistration(msg ws.RegisteredMsg) (auth.PasskeyPolicy, bool) {
	rpID := strings.ToLower(strings.TrimSpace(msg.PasskeyRPID))
	if rpID == "" || strings.ContainsAny(rpID, "/\\:@") || len(msg.PasskeyOrigins) == 0 || len(msg.PasskeyOrigins) > 8 {
		return auth.PasskeyPolicy{}, false
	}
	origins := make([]string, 0, len(msg.PasskeyOrigins))
	seen := make(map[string]bool, len(msg.PasskeyOrigins))
	for _, rawOrigin := range msg.PasskeyOrigins {
		parsed, err := url.Parse(rawOrigin)
		if err != nil || parsed.User != nil || parsed.Hostname() == "" || parsed.Path != "" ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return auth.PasskeyPolicy{}, false
		}
		host := strings.ToLower(parsed.Hostname())
		if host != rpID && !strings.HasSuffix(host, "."+rpID) {
			return auth.PasskeyPolicy{}, false
		}
		if parsed.Scheme != "https" {
			ip := net.ParseIP(host)
			loopback := strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
			if parsed.Scheme != "http" || !loopback {
				return auth.PasskeyPolicy{}, false
			}
		}
		origin := parsed.Scheme + "://" + parsed.Host
		if !seen[origin] {
			origins = append(origins, origin)
			seen[origin] = true
		}
	}
	return auth.PasskeyPolicy{
		RPID:                    rpID,
		Origins:                 origins,
		RequireUserVerification: true,
	}, true
}

func PasskeysForSubject(allowedKeys []config.AllowKey, userID string) []config.AllowKey {
	var matches []config.AllowKey
	for _, allowed := range allowedKeys {
		if allowed.Key == "" {
			continue
		}
		// Key-only entries are an intentional compatibility mode for local
		// administrators. Identity-bound entries must match the relay user ID.
		if allowed.UserID == "" || allowed.UserID == userID {
			matches = append(matches, allowed)
		}
	}
	return matches
}

func VisibleAllowKeys(req ws.TunnelRequest, allowedKeys []config.AllowKey) []config.AllowKey {
	if !IsMemberFiltered(req) {
		return append([]config.AllowKey(nil), allowedKeys...)
	}
	visible := make([]config.AllowKey, 0, 1)
	for _, allowed := range allowedKeys {
		if allowed.UserID == req.SenderUserID {
			visible = append(visible, allowed)
		}
	}
	return visible
}

func VerifySubjectPasskey(allowedKeys []config.AllowKey, userID string, challenge, authData, clientData, signature []byte, policy auth.PasskeyPolicy) ([]byte, error) {
	for _, allowed := range PasskeysForSubject(allowedKeys, userID) {
		rawKey, err := base64.StdEncoding.DecodeString(allowed.Key)
		if err != nil || len(rawKey) != 64 {
			continue
		}
		if err := auth.VerifyPasskeyAssertion(rawKey, challenge, authData, clientData, signature, policy); err == nil {
			return rawKey, nil
		}
	}
	return nil, errors.New("passkey verification failed")
}
