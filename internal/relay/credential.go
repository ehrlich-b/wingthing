package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var errCredentialValidationUnavailable = errors.New("credential validation unavailable")

// validateWingCredential makes the device-token row authoritative, including
// for legacy JWTs without a jti. Deleting or rotating that row revokes the JWT;
// signature validation alone must never grant access. Edges consult the login
// node on every authentication attempt, without caching positive decisions.
func (s *Server) validateWingCredential(ctx context.Context, token string) (*WingClaims, error) {
	var claims *WingClaims
	if strings.Contains(token, ".") {
		if s.JWTPubKey() == nil {
			return nil, fmt.Errorf("no JWT verification key")
		}
		var err error
		claims, err = ValidateWingJWT(s.JWTPubKey(), token)
		if err != nil {
			return nil, err
		}
	}

	var userID, wingID string
	if s.IsEdge() {
		var result struct {
			UserID string `json:"user_id"`
			WingID string `json:"wing_id"`
		}
		if err := s.remoteCredential(ctx, "/internal/tokens/", token, &result); err != nil {
			return nil, err
		}
		if result.UserID == "" || result.WingID == "" {
			return nil, fmt.Errorf("%w: missing credential identity", errCredentialValidationUnavailable)
		}
		userID, wingID = result.UserID, result.WingID
	} else {
		if s.Store == nil {
			return nil, fmt.Errorf("no credential store")
		}
		var err error
		userID, wingID, err = s.Store.ValidateToken(token)
		if err != nil {
			return nil, err
		}
	}
	if userID == "" || wingID == "" || (claims != nil && (claims.Subject != userID || claims.WingID != wingID)) {
		return nil, fmt.Errorf("invalid credential identity")
	}
	if claims == nil {
		claims = &WingClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: userID}, WingID: wingID}
	}
	return claims, nil
}

func (s *Server) remoteCredential(ctx context.Context, path, token string, result any) error {
	if s.Config.LoginNodeAddr == "" {
		return fmt.Errorf("%w: no login node", errCredentialValidationUnavailable)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(s.Config.LoginNodeAddr, "/")+path+url.PathEscape(token), nil)
	if err != nil {
		return fmt.Errorf("%w: %v", errCredentialValidationUnavailable, err)
	}
	s.authorizeInternalRequest(req)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", errCredentialValidationUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("credential validation denied")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: HTTP %d", errCredentialValidationUnavailable, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSessionValidationBytes+1))
	if err != nil {
		return fmt.Errorf("%w: %v", errCredentialValidationUnavailable, err)
	}
	if len(body) > maxSessionValidationBytes {
		return fmt.Errorf("%w: credential response too large", errCredentialValidationUnavailable)
	}
	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("%w: %v", errCredentialValidationUnavailable, err)
	}
	return nil
}

func (s *Server) handleInternalToken(w http.ResponseWriter, r *http.Request) {
	if s.IsEdge() || s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "no credential store")
		return
	}
	claims, err := s.validateWingCredential(r.Context(), r.PathValue("token"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	if !s.roostUserIDAllowed(claims.Subject) {
		writeError(w, http.StatusForbidden, "this account is not enrolled in this roost")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"user_id": claims.Subject, "wing_id": claims.WingID})
}
