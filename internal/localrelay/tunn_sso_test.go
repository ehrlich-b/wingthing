package localrelay

import "testing"

func TestTunnSSOPartialConfigurationCannotFallBackToLocalAuth(t *testing.T) {
	for _, name := range []string{"WT_TUNN_ORIGIN", "WT_TUNN_ISSUER", "WT_TUNN_ACCOUNT_ID", "WT_TUNN_JWKS_FILE", "WT_BASE_URL"} {
		t.Setenv(name, "")
	}
	if v, err := TunnSSOFromEnv(); err != nil || v != nil {
		t.Fatal("ordinary local configuration changed")
	}
	t.Setenv("WT_TUNN_ORIGIN", "https://phone.example")
	if _, err := TunnSSOFromEnv(); err == nil {
		t.Fatal("partial SSO silently disabled")
	}
	t.Setenv("WT_TUNN_ISSUER", "https://auth.example")
	t.Setenv("WT_TUNN_ACCOUNT_ID", "b56ce660-7801-49f2-bfcc-168392716895")
	t.Setenv("WT_TUNN_JWKS_FILE", "public.json")
	if _, err := TunnSSOFromEnv(); err == nil {
		t.Fatal("base URL mismatch admitted")
	}
}
