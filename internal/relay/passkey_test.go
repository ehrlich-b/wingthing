package relay

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

func TestWebAuthnRegistrationRequiresUserVerification(t *testing.T) {
	server := &Server{Config: ServerConfig{
		AppHost: "app.wingthing.ai",
		BaseURL: "https://wingthing.ai",
	}}
	wa, err := server.newWebAuthn()
	if err != nil {
		t.Fatal(err)
	}
	if wa.Config.RPID != "wingthing.ai" {
		t.Fatalf("RP ID = %q", wa.Config.RPID)
	}
	if wa.Config.AuthenticatorSelection.UserVerification != protocol.VerificationRequired {
		t.Fatalf("user verification = %q, want required", wa.Config.AuthenticatorSelection.UserVerification)
	}
}

func TestWebAuthnRegistrationUsesCustomAppHost(t *testing.T) {
	server := &Server{Config: ServerConfig{
		AppHost: "app.roost.example.test",
		BaseURL: "https://roost.example.test",
	}}
	wa, err := server.newWebAuthn()
	if err != nil {
		t.Fatal(err)
	}
	if wa.Config.RPID != "roost.example.test" {
		t.Fatalf("custom RP ID = %q", wa.Config.RPID)
	}
	wantOrigins := []string{"https://app.roost.example.test", "https://roost.example.test"}
	if !reflect.DeepEqual(wa.Config.RPOrigins, wantOrigins) {
		t.Fatalf("custom RP origins = %#v, want %#v", wa.Config.RPOrigins, wantOrigins)
	}
}

func TestLocalHTTPSWebAuthnOriginIsAllowed(t *testing.T) {
	server := &Server{Config: ServerConfig{BaseURL: "https://localhost:8443"}}
	wa, err := server.newWebAuthn()
	if err != nil {
		t.Fatal(err)
	}
	if wa.Config.RPID != "localhost" {
		t.Fatalf("local HTTPS RP ID = %q", wa.Config.RPID)
	}
	found := false
	for _, origin := range wa.Config.RPOrigins {
		if origin == "https://localhost:8443" {
			found = true
		}
	}
	if !found {
		t.Fatalf("local HTTPS origins = %#v", wa.Config.RPOrigins)
	}
}

func TestPasskeyRegistrationSessionsAreServerLocalSingleUseAndExpiring(t *testing.T) {
	now := time.Now()
	first := NewServer(nil, ServerConfig{})
	second := NewServer(nil, ServerConfig{})
	session := &webauthn.SessionData{Challenge: "challenge"}
	if !first.storePasskeyRegistration("alice", session, now) {
		t.Fatal("store registration session")
	}
	if _, ok := second.takePasskeyRegistration("alice", now); ok {
		t.Fatal("registration session leaked across servers")
	}
	if got, ok := first.takePasskeyRegistration("alice", now); !ok || got != session {
		t.Fatalf("take registration session = %#v, %v", got, ok)
	}
	if _, ok := first.takePasskeyRegistration("alice", now); ok {
		t.Fatal("registration session was reusable")
	}
	if !first.storePasskeyRegistration("bob", session, now) {
		t.Fatal("store expiring registration session")
	}
	if _, ok := first.takePasskeyRegistration("bob", now.Add(passkeyRegistrationTTL)); ok {
		t.Fatal("expired registration session was accepted")
	}
}

func TestPasskeyRegistrationSessionCapacityReclaimsExpiredEntries(t *testing.T) {
	now := time.Now()
	server := NewServer(nil, ServerConfig{})
	for index := 0; index < maxPasskeyRegistrationUsers; index++ {
		if !server.storePasskeyRegistration(fmt.Sprintf("user-%d", index), &webauthn.SessionData{}, now) {
			t.Fatalf("store registration %d", index)
		}
	}
	if server.storePasskeyRegistration("overflow", &webauthn.SessionData{}, now) {
		t.Fatal("registration capacity was not enforced")
	}
	if !server.storePasskeyRegistration("replacement", &webauthn.SessionData{}, now.Add(passkeyRegistrationTTL)) {
		t.Fatal("expired registrations were not reclaimed")
	}
}
