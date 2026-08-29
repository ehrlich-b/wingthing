package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

// webauthnUser wraps our User for the webauthn library interface.
type webauthnUser struct {
	id          string
	name        string
	displayName string
	credentials []webauthn.Credential
}

func (u *webauthnUser) WebAuthnID() []byte                         { return []byte(u.id) }
func (u *webauthnUser) WebAuthnName() string                       { return u.name }
func (u *webauthnUser) WebAuthnDisplayName() string                { return u.displayName }
func (u *webauthnUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

const (
	passkeyRegistrationTTL      = 10 * time.Minute
	maxPasskeyRegistrationUsers = 1000
)

type passkeyRegistrationSession struct {
	data      *webauthn.SessionData
	expiresAt time.Time
}

func (s *Server) storePasskeyRegistration(userID string, session *webauthn.SessionData, now time.Time) bool {
	s.passkeyMu.Lock()
	defer s.passkeyMu.Unlock()
	if s.passkeySessions == nil {
		s.passkeySessions = make(map[string]passkeyRegistrationSession)
	}
	for id, pending := range s.passkeySessions {
		if !pending.expiresAt.After(now) {
			delete(s.passkeySessions, id)
		}
	}
	if _, replacing := s.passkeySessions[userID]; !replacing && len(s.passkeySessions) >= maxPasskeyRegistrationUsers {
		return false
	}
	s.passkeySessions[userID] = passkeyRegistrationSession{
		data:      session,
		expiresAt: now.Add(passkeyRegistrationTTL),
	}
	return true
}

func (s *Server) takePasskeyRegistration(userID string, now time.Time) (*webauthn.SessionData, bool) {
	s.passkeyMu.Lock()
	defer s.passkeyMu.Unlock()
	pending, ok := s.passkeySessions[userID]
	if ok {
		delete(s.passkeySessions, userID)
	}
	if !ok || !pending.expiresAt.After(now) || pending.data == nil {
		return nil, false
	}
	return pending.data, true
}

func (s *Server) newWebAuthn() (*webauthn.WebAuthn, error) {
	rpID, origins := s.passkeyRelyingParty()

	return webauthn.New(&webauthn.Config{
		RPDisplayName: "Wingthing",
		RPID:          rpID,
		RPOrigins:     origins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			UserVerification: protocol.VerificationRequired,
		},
	})
}

// passkeyRelyingParty derives one policy for both account registration and the
// connected wing's assertion verification. AppHost is not assumed to belong to
// wingthing.ai: custom organization deployments use their configured hosts.
func (s *Server) passkeyRelyingParty() (string, []string) {
	rpID := "localhost"
	origins := []string{"http://localhost:5173", "http://localhost:8080", "https://localhost:8443"}

	var baseOrigin, baseHost, baseScheme string
	if parsed, err := url.Parse(s.Config.BaseURL); err == nil && parsed.Hostname() != "" &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") {
		baseHost = strings.ToLower(parsed.Hostname())
		baseScheme = parsed.Scheme
		baseOrigin = parsed.Scheme + "://" + parsed.Host
		rpID = baseHost
		origins = []string{baseOrigin}
		if baseHost == "localhost" {
			origins = appendUniqueStrings(origins, "http://localhost:5173", "http://localhost:8080", "https://localhost:8443")
		}
	}

	if s.Config.AppHost == "" {
		return rpID, origins
	}
	appURL, err := url.Parse("//" + s.Config.AppHost)
	if err != nil || appURL.Hostname() == "" {
		return rpID, origins
	}
	appHost := strings.ToLower(appURL.Hostname())
	if baseScheme == "" {
		baseScheme = "https"
	}
	appOrigin := baseScheme + "://" + appURL.Host
	// Prefer the base host when it is a valid RP parent of AppHost (the hosted
	// wingthing.ai/app.wingthing.ai layout). Otherwise scope the RP exactly to
	// AppHost; the relay acknowledgement tells wings the same result.
	if baseHost == "" || (appHost != baseHost && !strings.HasSuffix(appHost, "."+baseHost)) {
		rpID = appHost
		origins = []string{appOrigin}
		return rpID, origins
	}
	origins = appendUniqueStrings([]string{appOrigin}, baseOrigin)
	return rpID, origins
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := make(map[string]bool, len(values)+len(additions))
	for _, value := range values {
		if value != "" {
			seen[value] = true
		}
	}
	for _, value := range additions {
		if value != "" && !seen[value] {
			values = append(values, value)
			seen[value] = true
		}
	}
	return values
}

// handlePasskeyRegisterBegin starts WebAuthn registration.
// POST /api/app/passkey/register/begin
func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	wa, err := s.newWebAuthn()
	if err != nil {
		http.Error(w, "webauthn init: "+err.Error(), http.StatusInternalServerError)
		return
	}

	name := user.DisplayName
	if user.Email != nil && *user.Email != "" {
		name = *user.Email
	}
	wUser := &webauthnUser{
		id:          user.ID,
		name:        name,
		displayName: user.DisplayName,
	}

	// Load existing credentials to exclude them
	if s.Store != nil {
		creds, _ := s.Store.ListPasskeyCredentials(user.ID)
		for _, c := range creds {
			wUser.credentials = append(wUser.credentials, webauthn.Credential{
				ID: c.CredentialID,
			})
		}
	}

	options, session, err := wa.BeginRegistration(wUser,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementDiscouraged),
	)
	if err != nil {
		http.Error(w, "begin registration: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if !s.storePasskeyRegistration(user.ID, session, time.Now()) {
		http.Error(w, "too many passkey registrations in progress", http.StatusTooManyRequests)
		return
	}

	writeJSON(w, http.StatusOK, options)
}

// handlePasskeyRegisterFinish completes WebAuthn registration.
// POST /api/app/passkey/register/finish
func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	wa, err := s.newWebAuthn()
	if err != nil {
		http.Error(w, "webauthn init: "+err.Error(), http.StatusInternalServerError)
		return
	}

	session, ok := s.takePasskeyRegistration(user.ID, time.Now())
	if !ok {
		http.Error(w, "no registration session", http.StatusBadRequest)
		return
	}

	name := user.DisplayName
	if user.Email != nil && *user.Email != "" {
		name = *user.Email
	}
	wUser := &webauthnUser{
		id:          user.ID,
		name:        name,
		displayName: user.DisplayName,
	}

	credential, err := wa.FinishRegistration(wUser, *session, r)
	if err != nil {
		log.Printf("passkey: finish registration failed: %v", err)
		http.Error(w, "finish registration: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Extract raw P-256 public key (64 bytes: X||Y)
	rawPubKey, err := extractRawP256Key(credential.PublicKey)
	if err != nil {
		http.Error(w, "extract public key: "+err.Error(), http.StatusInternalServerError)
		return
	}

	id := uuid.New().String()
	label := name

	if s.Store != nil {
		if err := s.Store.CreatePasskeyCredential(id, user.ID, credential.ID, rawPubKey, label); err != nil {
			http.Error(w, "store credential: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	pubKeyB64 := base64.StdEncoding.EncodeToString(rawPubKey)

	writeJSON(w, http.StatusOK, map[string]string{
		"id":         id,
		"public_key": pubKeyB64,
		"label":      label,
	})
	log.Printf("passkey: registered credential for user %s (id=%s)", user.ID, id)

	// Notify connected wings that this user registered a passkey
	email := ""
	if user.Email != nil {
		email = *user.Email
	}
	go s.notifyPasskeyRegistered(user.ID, email)
}

// extractRawP256Key extracts the raw 64-byte P-256 public key (X||Y) from COSE-encoded key bytes.
func extractRawP256Key(coseKey []byte) ([]byte, error) {
	parsed, err := webauthncose.ParsePublicKey(coseKey)
	if err != nil {
		return nil, err
	}

	ec2, ok := parsed.(webauthncose.EC2PublicKeyData)
	if !ok {
		return nil, errors.New("not an EC2 key")
	}

	if len(ec2.XCoord) != 32 || len(ec2.YCoord) != 32 {
		return nil, errors.New("unexpected coordinate length")
	}

	raw := make([]byte, 64)
	copy(raw[:32], ec2.XCoord)
	copy(raw[32:], ec2.YCoord)
	return raw, nil
}

// handlePasskeyList returns the user's passkey credentials.
// GET /api/app/passkey
func (s *Server) handlePasskeyList(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		user = s.tokenUser(r)
	}
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if s.Store == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	creds, err := s.Store.ListPasskeyCredentials(user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type credJSON struct {
		ID           string `json:"id"`
		CredentialID string `json:"credential_id"`
		PublicKey    string `json:"public_key"`
		Label        string `json:"label"`
		CreatedAt    string `json:"created_at"`
	}

	var result []credJSON
	for _, c := range creds {
		result = append(result, credJSON{
			ID:           c.ID,
			CredentialID: base64.RawURLEncoding.EncodeToString(c.CredentialID),
			PublicKey:    base64.StdEncoding.EncodeToString(c.PublicKey),
			Label:        c.Label,
			CreatedAt:    c.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}

	if result == nil {
		result = []credJSON{}
	}

	writeJSON(w, http.StatusOK, result)
}

// handlePasskeyDelete removes a passkey credential.
// DELETE /api/app/passkey/{id}
func (s *Server) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	user := s.sessionUser(r)
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}

	if s.Store != nil {
		if err := s.Store.DeletePasskeyCredential(id, user.ID); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
	log.Printf("passkey: deleted credential %s for user %s", id, user.ID)
}

// notifyPasskeyRegistered sends a lightweight passkey.registered event to wings
// owned by or in the same org as the user who just registered a passkey.
func (s *Server) notifyPasskeyRegistered(userID, email string) {
	msg, err := json.Marshal(ws.PasskeyRegistered{
		Type:   ws.TypePasskeyRegistered,
		UserID: userID,
		Email:  email,
	})
	if err != nil {
		log.Printf("passkey.registered: marshal event: %v", err)
		return
	}

	// Find wings owned by this user directly
	sent := map[string]bool{}
	for _, w := range s.Wings.All() {
		if w.UserID == userID {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if err := w.Conn.Write(ctx, websocket.MessageText, msg); err != nil {
				log.Printf("passkey.registered: notify wing %s: %v", w.WingID, err)
			}
			cancel()
			sent[w.ID] = true
		}
	}

	// Find wings in orgs the user belongs to
	if s.Store != nil {
		orgs, err := s.Store.ListOrgsForUser(userID)
		if err != nil {
			log.Printf("passkey.registered: list orgs: %v", err)
			return
		}
		for _, org := range orgs {
			for _, w := range s.Wings.All() {
				if w.OrgID == org.ID && !sent[w.ID] {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					if err := w.Conn.Write(ctx, websocket.MessageText, msg); err != nil {
						log.Printf("passkey.registered: notify org wing %s: %v", w.WingID, err)
					}
					cancel()
					sent[w.ID] = true
				}
			}
		}
	}

	if len(sent) > 0 {
		log.Printf("passkey.registered: notified %d wings for user %s", len(sent), userID)
	}
}
