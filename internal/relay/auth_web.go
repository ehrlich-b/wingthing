package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/publicsuffix"
)

const (
	sessionCookieName             = "wt_session"
	sessionDuration               = 30 * 24 * time.Hour
	maxOAuthProviderResponseBytes = 1 << 20
)

func (s *Server) oauthClient() *http.Client {
	if s.oauthHTTPClient != nil {
		return s.oauthHTTPClient
	}
	return newOAuthHTTPClient()
}

func newOAuthHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		// Provider URLs are fixed by the application. Refusing redirects keeps a
		// 307/308 from forwarding the token-exchange body (and client secret) to
		// a different endpoint and makes provider endpoint changes explicit.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return fmt.Errorf("OAuth provider redirect refused")
		},
	}
}

func decodeOAuthProviderJSON(resp *http.Response, dst any) error {
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("provider returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthProviderResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxOAuthProviderResponseBytes {
		return fmt.Errorf("provider response exceeds %d bytes", maxOAuthProviderResponseBytes)
	}
	return json.Unmarshal(data, dst)
}

func generateToken() string {
	token, err := generateTokenFrom(rand.Reader)
	if err != nil {
		panic(fmt.Sprintf("crypto/rand failed while generating an authentication token: %v", err))
	}
	return token
}

func generateTokenFrom(reader io.Reader) (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(reader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Server) sessionUser(r *http.Request) *User {
	if s.LocalMode && s.localUser != nil {
		return s.localUser
	}
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	// Edge node: validate session via login node
	if s.IsEdge() && s.sessionCache != nil {
		user := s.sessionCache.Validate(c.Value, s.Config.LoginNodeAddr)
		if user == nil || !s.roostUserIDAllowed(user.ID) {
			return nil
		}
		return user
	}
	if s.Store == nil {
		return nil
	}
	user, err := s.Store.GetSession(c.Value)
	if err != nil {
		return nil
	}
	if !s.roostUserAllowed(user) {
		return nil
	}
	return user
}

func (s *Server) roostUserAllowed(user *User) bool {
	if user == nil {
		return false
	}
	if s.LocalMode || len(s.Config.RoostAllowedEmails) == 0 || user.ID == roostWingServiceUserID {
		return true
	}
	if user.Email == nil {
		return false
	}
	return s.roostEmailAllowed(*user.Email)
}

func (s *Server) roostEmailAllowed(email string) bool {
	if s.LocalMode || len(s.Config.RoostAllowedEmails) == 0 {
		return true
	}
	email = strings.TrimSpace(email)
	for _, allowed := range s.Config.RoostAllowedEmails {
		if strings.EqualFold(email, strings.TrimSpace(allowed)) {
			return true
		}
	}
	return false
}

// roostProviderEmailAllowed is the OAuth enrollment boundary. Operators that
// configure an email list are relying on the identity provider to prove the
// current address, so an unverified or missing provider email must fail closed.
// Roosts without a list retain their historical accept-authenticated behavior.
func (s *Server) roostProviderEmailAllowed(email string, verified bool) bool {
	if s.LocalMode || len(s.Config.RoostAllowedEmails) == 0 {
		return true
	}
	return verified && s.roostEmailAllowed(email)
}

func (s *Server) clearDeniedOAuthEnrollment(user *User) error {
	if user == nil {
		return nil
	}
	if err := s.Store.ClearUserEmail(user.ID); err != nil {
		return err
	}
	user.Email = nil
	return nil
}

func (s *Server) roostUserIDAllowed(userID string) bool {
	if s.LocalMode || len(s.Config.RoostAllowedEmails) == 0 || userID == roostWingServiceUserID {
		return true
	}
	// Edge stores contain process-local plumbing, not authoritative users. Reuse
	// the login node's synchronized per-user decision instead of denying every
	// enrolled account because it is absent from the edge database. A cache miss
	// fails closed until the first successful synchronization.
	if s.IsEdge() {
		if s.EntitlementCache == nil {
			return false
		}
		allowed, known := s.EntitlementCache.GetEnrollment(userID)
		return known && allowed
	}
	if s.Store == nil {
		return false
	}
	user, _ := s.Store.GetUserByID(userID)
	return s.roostUserAllowed(user)
}

// cookieDomain returns the longest safe DNS suffix shared by the login and app
// hosts. Host-only cookies are safer whenever the hosts do not share one (or
// are localhost/IP addresses). In particular, never scope a cookie to a public
// suffix such as co.uk or unnecessarily widen login.roost.example.com and
// app.roost.example.com to all of example.com.
func (s *Server) cookieDomain() string {
	if s.Config.AppHost == "" {
		return "" // single host, no cross-subdomain needed
	}
	baseURL, err := url.Parse(s.Config.BaseURL)
	if err != nil || baseURL.Hostname() == "" {
		return ""
	}
	appURL, err := url.Parse("//" + s.Config.AppHost)
	if err != nil || appURL.Hostname() == "" {
		return ""
	}

	baseHost := strings.ToLower(strings.TrimSuffix(baseURL.Hostname(), "."))
	appHost := strings.ToLower(strings.TrimSuffix(appURL.Hostname(), "."))
	if baseHost == "localhost" || appHost == "localhost" {
		return ""
	}
	if _, err := netip.ParseAddr(baseHost); err == nil {
		return ""
	}
	if _, err := netip.ParseAddr(appHost); err == nil {
		return ""
	}

	if baseHost == appHost {
		return ""
	}
	candidate := longestSharedDNSName(baseHost, appHost)
	if candidate == "" {
		return ""
	}
	if _, err := publicsuffix.EffectiveTLDPlusOne(candidate); err != nil {
		return ""
	}
	return "." + candidate
}

func longestSharedDNSName(first, second string) string {
	firstLabels := strings.Split(strings.ToLower(strings.TrimSuffix(first, ".")), ".")
	secondLabels := strings.Split(strings.ToLower(strings.TrimSuffix(second, ".")), ".")
	firstIndex := len(firstLabels) - 1
	secondIndex := len(secondLabels) - 1
	for firstIndex >= 0 && secondIndex >= 0 && firstLabels[firstIndex] == secondLabels[secondIndex] {
		firstIndex--
		secondIndex--
	}
	shared := firstLabels[firstIndex+1:]
	if len(shared) == 0 {
		return ""
	}
	return strings.Join(shared, ".")
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Domain:   s.cookieDomain(),
		MaxAge:   int(sessionDuration.Seconds()),
		HttpOnly: true,
		Secure:   s.secureBrowserOrigin(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) expireCookie(w http.ResponseWriter, name, path string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Path:     path,
		Domain:   s.cookieDomain(),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secureBrowserOrigin(),
		SameSite: http.SameSiteLaxMode,
	})
}

// setAuthFlowCookie keeps short-lived OAuth state on the login host. Unlike the
// session cookie, these values never need to reach AppHost; parent-domain scope
// would let any sibling subdomain inject or replace an in-progress login flow.
func (s *Server) setAuthFlowCookie(w http.ResponseWriter, name, value, path string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.secureBrowserOrigin(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) expireAuthFlowCookie(w http.ResponseWriter, name, path string) {
	s.setAuthFlowCookie(w, name, "", path, -1)
}

// handleDevLogin creates a session for the dev user. Only works in dev mode.
func (s *Server) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	if !s.DevMode {
		http.Error(w, "not available", http.StatusNotFound)
		return
	}
	user, err := s.Store.CreateUserDev()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.createSessionAndRedirect(w, r, user)
}

func (s *Server) createSessionAndRedirect(w http.ResponseWriter, r *http.Request, user *User) {
	if !s.roostUserAllowed(user) {
		http.Error(w, "this account is not enrolled in this roost", http.StatusForbidden)
		return
	}
	token := generateToken()
	if err := s.Store.CreateSession(token, user.ID, time.Now().Add(sessionDuration)); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Roost mode: auto-grant pro to all OAuth users (self-hosted = unlimited)
	if s.RoostMode && !s.Store.IsUserPro(user.ID) {
		subID := uuid.New().String()
		sub := &Subscription{ID: subID, UserID: &user.ID, Plan: "roost", Status: "active", Seats: 1}
		_, _, err := s.Store.EnsurePersonalSubscription(
			sub,
			&Entitlement{ID: uuid.New().String(), UserID: user.ID, SubscriptionID: subID},
		)
		if err != nil {
			_ = s.Store.DeleteSession(token)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	s.setSessionCookie(w, token)

	// Check for pending org invite token — redirect to invite page for explicit accept
	if c, err := r.Cookie("invite_token"); err == nil && c.Value != "" {
		http.Redirect(w, r, "/invite/"+c.Value, http.StatusSeeOther)
		return
	}

	// Respect ?next= redirect (stored in oauth_next cookie during OAuth flow)
	if c, err := r.Cookie("oauth_next"); err == nil && c.Value != "" {
		s.expireAuthFlowCookie(w, "oauth_next", "/auth")
		if isSafeRedirect(c.Value) {
			http.Redirect(w, r, c.Value, http.StatusSeeOther)
			return
		}
	}
	// Default: send to app dashboard if AppHost is configured, otherwise /
	if s.Config.AppHost != "" {
		proto := s.browserOriginScheme()
		http.Redirect(w, r, proto+"://"+s.Config.AppHost+"/", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) browserOriginScheme() string {
	parsed, err := url.Parse(s.Config.BaseURL)
	if err == nil {
		switch strings.ToLower(parsed.Scheme) {
		case "http":
			return "http"
		case "https":
			return "https"
		}
	}
	// Hosted/split deployments have historically defaulted the app host to
	// HTTPS when BaseURL is absent or malformed.
	return "https"
}

func (s *Server) secureBrowserOrigin() bool {
	parsed, err := url.Parse(s.Config.BaseURL)
	return err == nil && strings.EqualFold(parsed.Scheme, "https")
}

// OAuth state CSRF

func (s *Server) setOAuthState(w http.ResponseWriter) string {
	state := generateToken()
	s.setAuthFlowCookie(w, "oauth_state", state, "/auth", 600)
	return state
}

func (s *Server) validateOAuthState(w http.ResponseWriter, r *http.Request) bool {
	c, err := r.Cookie("oauth_state")
	if err != nil {
		return false
	}
	s.expireAuthFlowCookie(w, "oauth_state", "/auth")
	return c.Value == r.URL.Query().Get("state")
}

// GitHub OAuth

func (s *Server) handleGitHubAuth(w http.ResponseWriter, r *http.Request) {
	if s.Config.GitHubClientID == "" {
		http.NotFound(w, r)
		return
	}
	state := s.setOAuthState(w)
	u := fmt.Sprintf(
		"https://github.com/login/oauth/authorize?client_id=%s&redirect_uri=%s&scope=read:user,user:email&state=%s",
		url.QueryEscape(s.Config.GitHubClientID),
		url.QueryEscape(s.Config.BaseURL+"/auth/github/callback"),
		url.QueryEscape(state),
	)
	http.Redirect(w, r, u, http.StatusTemporaryRedirect)
}

func (s *Server) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	if !s.validateOAuthState(w, r) {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	// Exchange code for access token
	body := url.Values{
		"client_id":     {s.Config.GitHubClientID},
		"client_secret": {s.Config.GitHubClientSecret},
		"code":          {code},
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(body.Encode()))
	if err != nil {
		http.Error(w, "token exchange failed", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.oauthClient().Do(req)
	if err != nil {
		http.Error(w, "token exchange failed", http.StatusInternalServerError)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	var tokenData struct {
		AccessToken string `json:"access_token"`
	}
	if err := decodeOAuthProviderJSON(resp, &tokenData); err != nil || tokenData.AccessToken == "" {
		http.Error(w, "invalid token response", http.StatusInternalServerError)
		return
	}

	// Fetch user info
	userReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		http.Error(w, "failed to fetch user", http.StatusInternalServerError)
		return
	}
	userReq.Header.Set("Authorization", "Bearer "+tokenData.AccessToken)
	userResp, err := s.oauthClient().Do(userReq)
	if err != nil {
		http.Error(w, "failed to fetch user", http.StatusInternalServerError)
		return
	}
	defer func() { _ = userResp.Body.Close() }()

	var ghUser struct {
		ID        int    `json:"id"`
		Login     string `json:"login"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := decodeOAuthProviderJSON(userResp, &ghUser); err != nil || ghUser.ID == 0 {
		http.Error(w, "invalid user response", http.StatusInternalServerError)
		return
	}

	providerID := fmt.Sprintf("%d", ghUser.ID)
	user, err := s.Store.GetUserByProvider("github", providerID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	existingUser := user != nil
	if !existingUser {
		user = &User{
			ID:         uuid.New().String(),
			Provider:   "github",
			ProviderID: providerID,
		}
	}
	user.DisplayName = ghUser.Login
	avatarURL := ghUser.AvatarURL
	user.AvatarURL = &avatarURL

	// Fetch the current primary verified email. A configured enrollment list
	// must never fall back to a stale email already stored for this provider ID.
	ghEmail := s.fetchGitHubEmail(r.Context(), tokenData.AccessToken)
	if !s.roostProviderEmailAllowed(ghEmail, ghEmail != "") {
		// The current provider response no longer proves an enrolled identity.
		// Clear the old address before returning so previously issued cookies and
		// device tokens stop authorizing this account. Do not try to store the new
		// unlisted address: it may collide with another account's unique email and
		// accidentally leave the stale allowed address in place.
		if existingUser {
			if err := s.clearDeniedOAuthEnrollment(user); err != nil {
				http.Error(w, "db error", http.StatusInternalServerError)
				return
			}
		}
		http.Error(w, "this account is not enrolled in this roost", http.StatusForbidden)
		return
	}
	if ghEmail != "" {
		user.Email = &ghEmail
	}
	if err := s.Store.UpsertUser(user); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if user.Email != nil {
		if err := s.Store.UpdateUserEmail(user.ID, *user.Email); err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
	}

	s.createSessionAndRedirect(w, r, user)
}

// fetchGitHubEmail fetches the primary verified email from GitHub.
func (s *Server) fetchGitHubEmail(ctx context.Context, accessToken string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user/emails", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := s.oauthClient().Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := decodeOAuthProviderJSON(resp, &emails); err != nil {
		return ""
	}
	for _, e := range emails {
		if e.Primary && e.Verified {
			return e.Email
		}
	}
	return ""
}

// Google OAuth

func (s *Server) handleGoogleAuth(w http.ResponseWriter, r *http.Request) {
	if s.Config.GoogleClientID == "" {
		http.NotFound(w, r)
		return
	}
	state := s.setOAuthState(w)
	u := fmt.Sprintf(
		"https://accounts.google.com/o/oauth2/v2/auth?client_id=%s&redirect_uri=%s&response_type=code&scope=openid+email+profile&state=%s",
		url.QueryEscape(s.Config.GoogleClientID),
		url.QueryEscape(s.Config.BaseURL+"/auth/google/callback"),
		url.QueryEscape(state),
	)
	http.Redirect(w, r, u, http.StatusTemporaryRedirect)
}

func (s *Server) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	if !s.validateOAuthState(w, r) {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	// Exchange code for tokens
	body := url.Values{
		"client_id":     {s.Config.GoogleClientID},
		"client_secret": {s.Config.GoogleClientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {s.Config.BaseURL + "/auth/google/callback"},
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(body.Encode()))
	if err != nil {
		http.Error(w, "token exchange failed", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.oauthClient().Do(req)
	if err != nil {
		http.Error(w, "token exchange failed", http.StatusInternalServerError)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	var tokenData struct {
		AccessToken string `json:"access_token"`
	}
	if err := decodeOAuthProviderJSON(resp, &tokenData); err != nil || tokenData.AccessToken == "" {
		http.Error(w, "invalid token response", http.StatusInternalServerError)
		return
	}

	// Fetch user info
	userReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		http.Error(w, "failed to fetch user", http.StatusInternalServerError)
		return
	}
	userReq.Header.Set("Authorization", "Bearer "+tokenData.AccessToken)
	userResp, err := s.oauthClient().Do(userReq)
	if err != nil {
		http.Error(w, "failed to fetch user", http.StatusInternalServerError)
		return
	}
	defer func() { _ = userResp.Body.Close() }()

	var gUser struct {
		ID            string `json:"id"`
		Email         string `json:"email"`
		VerifiedEmail bool   `json:"verified_email"`
		Name          string `json:"name"`
		Picture       string `json:"picture"`
	}
	if err := decodeOAuthProviderJSON(userResp, &gUser); err != nil || gUser.ID == "" {
		http.Error(w, "invalid user response", http.StatusInternalServerError)
		return
	}

	user, err := s.Store.GetUserByProvider("google", gUser.ID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	existingUser := user != nil
	if !existingUser {
		user = &User{
			ID:         uuid.New().String(),
			Provider:   "google",
			ProviderID: gUser.ID,
		}
	}
	displayName := gUser.Name
	if displayName == "" {
		displayName = gUser.Email
	}
	user.DisplayName = displayName
	if gUser.Picture != "" {
		user.AvatarURL = &gUser.Picture
	}
	if !s.roostProviderEmailAllowed(gUser.Email, gUser.VerifiedEmail) {
		if existingUser {
			if err := s.clearDeniedOAuthEnrollment(user); err != nil {
				http.Error(w, "db error", http.StatusInternalServerError)
				return
			}
		}
		http.Error(w, "this account is not enrolled in this roost", http.StatusForbidden)
		return
	}
	// Preserve the historical non-enrollment behavior for operators without an
	// allowlist. When an allowlist is active, the check above requires this same
	// address to have been provider-verified.
	if gUser.Email != "" {
		user.Email = &gUser.Email
	}
	if err := s.Store.UpsertUser(user); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if user.Email != nil {
		if err := s.Store.UpdateUserEmail(user.ID, *user.Email); err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
	}

	s.createSessionAndRedirect(w, r, user)
}

// Magic Link

func (s *Server) handleMagicLink(w http.ResponseWriter, r *http.Request) {
	if s.Config.SMTPHost == "" {
		http.Error(w, "email login not configured", http.StatusNotFound)
		return
	}
	email, err := normalizeBareEmail(r.FormValue("email"))
	if err != nil {
		http.Error(w, "valid email required", http.StatusBadRequest)
		return
	}
	if !s.roostEmailAllowed(email) {
		http.Error(w, "this account is not enrolled in this roost", http.StatusForbidden)
		return
	}

	id := uuid.New().String()
	token := generateToken()
	expiresAt := time.Now().Add(15 * time.Minute)

	if err := s.Store.CreateMagicLink(id, email, token, expiresAt); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	link := fmt.Sprintf("%s/auth/magic/verify?token=%s", s.Config.BaseURL, token)
	if err := s.sendMagicLinkEmail(email, link); err != nil {
		http.Error(w, "failed to send email", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/login?sent=1", http.StatusSeeOther)
}

func (s *Server) sendMagicLinkEmail(to, link string) error {
	to, err := normalizeBareEmail(to)
	if err != nil {
		return err
	}
	from := s.Config.SMTPFrom
	subject := "Your wingthing login link"
	body := fmt.Sprintf("Click here to log in:\n\n%s\n\nThis link expires in 15 minutes.", link)
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s", from, to, subject, body)

	addr := s.Config.SMTPHost + ":" + s.Config.SMTPPort
	auth := smtp.PlainAuth("", s.Config.SMTPUser, s.Config.SMTPPass, s.Config.SMTPHost)
	return smtp.SendMail(addr, auth, from, []string{to}, []byte(msg))
}

func (s *Server) handleMagicVerify(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	email, err := s.Store.ConsumeMagicLink(token)
	if err != nil {
		http.Error(w, "invalid or expired link", http.StatusBadRequest)
		return
	}
	if !s.roostEmailAllowed(email) {
		http.Error(w, "this account is not enrolled in this roost", http.StatusForbidden)
		return
	}

	user, err := s.Store.GetOrCreateUserByEmail(email)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.createSessionAndRedirect(w, r, user)
}

// Logout

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookieName)
	if err == nil && s.Store != nil {
		if err := s.Store.DeleteSession(c.Value); err != nil {
			log.Printf("logout: delete session: %v", err)
		}
	}
	s.expireCookie(w, sessionCookieName, "/")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
