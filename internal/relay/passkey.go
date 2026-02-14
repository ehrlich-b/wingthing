package relay

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
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

// passkeySessionStore holds in-flight WebAuthn registration sessions.
var passkeySessionStore = struct {
	mu       sync.Mutex
	sessions map[string]*webauthn.SessionData // userID → session
}{sessions: make(map[string]*webauthn.SessionData)}

func (s *Server) newWebAuthn() (*webauthn.WebAuthn, error) {
	rpID := "localhost"
	origins := []string{"http://localhost:5173", "http://localhost:8080"}

	if s.Config.AppHost != "" {
		rpID = "wingthing.ai"
		origins = []string{"https://app.wingthing.ai"}
		if s.Config.BaseURL != "" {
			origins = append(origins, s.Config.BaseURL)
		}
	}

	return webauthn.New(&webauthn.Config{
		RPDisplayName: "Wingthing",
		RPID:          rpID,
		RPOrigins:     origins,
	})
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

	passkeySessionStore.mu.Lock()
	passkeySessionStore.sessions[user.ID] = session
	passkeySessionStore.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(options)
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

	passkeySessionStore.mu.Lock()
	session, ok := passkeySessionStore.sessions[user.ID]
	if ok {
		delete(passkeySessionStore.sessions, user.ID)
	}
	passkeySessionStore.mu.Unlock()

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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"id":         id,
		"public_key": pubKeyB64,
		"label":      label,
	})
	log.Printf("passkey: registered credential for user %s (id=%s)", user.ID, id)
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
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if s.Store == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
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
