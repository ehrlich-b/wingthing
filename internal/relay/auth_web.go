package relay

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	sessionCookieName = "wt_session"
	sessionDuration   = 30 * 24 * time.Hour
)

func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) sessionUser(r *http.Request) *SocialUser {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	user, err := s.Store.GetSession(c.Value)
	if err != nil {
		return nil
	}
	return user
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	secure := strings.HasPrefix(s.Config.BaseURL, "https")
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionDuration.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) createSessionAndRedirect(w http.ResponseWriter, r *http.Request, user *SocialUser) {
	token := generateToken()
	if err := s.Store.CreateSession(token, user.ID, time.Now().Add(sessionDuration)); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.setSessionCookie(w, token)

	// Respect ?next= redirect (stored in oauth_next cookie during OAuth flow)
	if c, err := r.Cookie("oauth_next"); err == nil && c.Value != "" {
		http.SetCookie(w, &http.Cookie{Name: "oauth_next", Path: "/auth", MaxAge: -1})
		http.Redirect(w, r, c.Value, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/social", http.StatusSeeOther)
}

// OAuth state CSRF

func (s *Server) setOAuthState(w http.ResponseWriter) string {
	state := generateToken()
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    state,
		Path:     "/auth",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return state
}

func (s *Server) validateOAuthState(w http.ResponseWriter, r *http.Request) bool {
	c, err := r.Cookie("oauth_state")
	if err != nil {
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name:   "oauth_state",
		Path:   "/auth",
		MaxAge: -1,
	})
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
	req, _ := http.NewRequest("POST", "https://github.com/login/oauth/access_token", strings.NewReader(body.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "token exchange failed", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	var tokenData struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenData); err != nil || tokenData.AccessToken == "" {
		http.Error(w, "invalid token response", http.StatusInternalServerError)
		return
	}

	// Fetch user info
	userReq, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
	userReq.Header.Set("Authorization", "Bearer "+tokenData.AccessToken)
	userResp, err := http.DefaultClient.Do(userReq)
	if err != nil {
		http.Error(w, "failed to fetch user", http.StatusInternalServerError)
		return
	}
	defer userResp.Body.Close()

	var ghUser struct {
		ID        int    `json:"id"`
		Login     string `json:"login"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.NewDecoder(userResp.Body).Decode(&ghUser); err != nil {
		http.Error(w, "invalid user response", http.StatusInternalServerError)
		return
	}

	providerID := fmt.Sprintf("%d", ghUser.ID)
	user, err := s.Store.GetSocialUserByProvider("github", providerID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if user == nil {
		user = &SocialUser{
			ID:         uuid.New().String(),
			Provider:   "github",
			ProviderID: providerID,
		}
	}
	user.DisplayName = ghUser.Login
	avatarURL := ghUser.AvatarURL
	user.AvatarURL = &avatarURL
	if err := s.Store.UpsertSocialUser(user); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	s.createSessionAndRedirect(w, r, user)
}

// Google OAuth

func (s *Server) handleGoogleAuth(w http.ResponseWriter, r *http.Request) {
	if s.Config.GoogleClientID == "" {
		http.NotFound(w, r)
		return
	}
	state := s.setOAuthState(w)
	u := fmt.Sprintf(
		"https://accounts.google.com/o/oauth2/v2/auth?client_id=%s&redirect_uri=%s&scope=openid+email+profile&response_type=code&state=%s",
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

	// Exchange code for access token
	resp, err := http.PostForm("https://oauth2.googleapis.com/token", url.Values{
		"code":          {code},
		"client_id":     {s.Config.GoogleClientID},
		"client_secret": {s.Config.GoogleClientSecret},
		"redirect_uri":  {s.Config.BaseURL + "/auth/google/callback"},
		"grant_type":    {"authorization_code"},
	})
	if err != nil {
		http.Error(w, "token exchange failed", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	var tokenData struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenData); err != nil || tokenData.AccessToken == "" {
		http.Error(w, "invalid token response", http.StatusInternalServerError)
		return
	}

	// Fetch user info
	userResp, err := http.Get("https://www.googleapis.com/oauth2/v2/userinfo?access_token=" + url.QueryEscape(tokenData.AccessToken))
	if err != nil {
		http.Error(w, "failed to fetch user", http.StatusInternalServerError)
		return
	}
	defer userResp.Body.Close()

	var gUser struct {
		ID      string `json:"id"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}
	if err := json.NewDecoder(userResp.Body).Decode(&gUser); err != nil {
		http.Error(w, "invalid user response", http.StatusInternalServerError)
		return
	}

	user, err := s.Store.GetSocialUserByProvider("google", gUser.ID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if user == nil {
		user = &SocialUser{
			ID:         uuid.New().String(),
			Provider:   "google",
			ProviderID: gUser.ID,
		}
	}
	name := gUser.Name
	if name == "" {
		name = gUser.Email
	}
	user.DisplayName = name
	if gUser.Picture != "" {
		user.AvatarURL = &gUser.Picture
	}
	if err := s.Store.UpsertSocialUser(user); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	s.createSessionAndRedirect(w, r, user)
}

// Magic Link

func (s *Server) handleMagicLink(w http.ResponseWriter, r *http.Request) {
	if s.Config.SMTPHost == "" {
		http.Error(w, "email login not configured", http.StatusNotFound)
		return
	}
	email := r.FormValue("email")
	if email == "" {
		http.Error(w, "email required", http.StatusBadRequest)
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

	user, err := s.Store.GetOrCreateSocialUserByEmail(email)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.createSessionAndRedirect(w, r, user)
}

// Logout

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookieName)
	if err == nil {
		s.Store.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:   sessionCookieName,
		Path:   "/",
		MaxAge: -1,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
