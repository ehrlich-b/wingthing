package relay

import (
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
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
