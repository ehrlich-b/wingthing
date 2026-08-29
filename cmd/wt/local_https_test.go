package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/localtls"
)

func TestRequireLoopbackAddress(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8080", "localhost:8443", "[::1]:8443"} {
		if err := requireLoopbackAddress("test", addr); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", ":8080", "0.0.0.0:8080", "192.168.1.5:8080", "example.com:443", "bad"} {
		if err := requireLoopbackAddress("test", addr); err == nil {
			t.Errorf("%s unexpectedly accepted", addr)
		}
	}
}

func TestLocalHTTPSRewritesOnlyTheImplicitWildcardDefault(t *testing.T) {
	if got := localHTTPAddrForHTTPS(":8080", false); got != defaultLocalHTTPAddr {
		t.Fatalf("implicit default = %q", got)
	}
	if got := localHTTPAddrForHTTPS(":8080", true); got != ":8080" {
		t.Fatalf("explicit address changed to %q", got)
	}
	if got := localHTTPAddrForHTTPS("127.0.0.1:9000", false); got != "127.0.0.1:9000" {
		t.Fatalf("custom address changed to %q", got)
	}
}

func TestUnauthenticatedLocalHTTPDefaultsToLoopbackAndRejectsExposure(t *testing.T) {
	got, err := prepareLocalHTTPAddress(":8080", false)
	if err != nil || got != defaultLocalHTTPAddr {
		t.Fatalf("implicit local address = %q, %v", got, err)
	}
	for _, addr := range []string{":8080", "0.0.0.0:8080", "192.168.1.20:8080"} {
		if _, err := prepareLocalHTTPAddress(addr, true); err == nil {
			t.Errorf("explicit unauthenticated bind %q was accepted", addr)
		}
	}
	if got, err := prepareLocalHTTPAddress("[::1]:8080", true); err != nil || got != "[::1]:8080" {
		t.Fatalf("explicit IPv6 loopback = %q, %v", got, err)
	}
}

func TestPrepareLocalHTTPSRejectsUnsafeAddressesBeforeCreatingAKey(t *testing.T) {
	for _, tc := range []struct {
		httpAddr  string
		httpsAddr string
		explicit  bool
	}{
		{"0.0.0.0:8080", defaultLocalHTTPSAddr, true},
		{":8080", defaultLocalHTTPSAddr, true},
		{defaultLocalHTTPAddr, "192.168.1.20:8443", true},
		{defaultLocalHTTPAddr, "127.0.0.2:8443", true},
		{"127.0.0.1:8443", "127.1.2.3:8443", true},
	} {
		dir := t.TempDir()
		if _, err := prepareLocalHTTPS(context.Background(), dir, tc.httpAddr, tc.httpsAddr, tc.explicit); err == nil {
			t.Fatalf("prepareLocalHTTPS(%q, %q) succeeded", tc.httpAddr, tc.httpsAddr)
		}
		if _, err := os.Stat(dir + "/local-tls"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsafe address created TLS material: %v", err)
		}
	}
}

func TestBrowserHTTPSURL(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want string
	}{
		{"127.0.0.1:8443", "https://localhost:8443"},
		{"localhost:443", "https://localhost"},
		{"[::1]:9443", "https://[::1]:9443"},
	} {
		got, err := browserHTTPSURL(tc.addr)
		if err != nil || got != tc.want {
			t.Errorf("browserHTTPSURL(%q) = %q, %v; want %q", tc.addr, got, err, tc.want)
		}
	}
}

func TestLocalHTTPURLPreservesLegacyAndExplicitLoopbackForms(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want string
	}{
		{":8080", "http://localhost:8080"},
		{"127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"[::1]:8080", "http://[::1]:8080"},
	} {
		if got := localHTTPURL(tc.addr); got != tc.want {
			t.Errorf("localHTTPURL(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

func TestSameAddressRecognizesEquivalentLoopbackListeners(t *testing.T) {
	if !sameAddress("127.0.0.1:8443", "127.1.2.3:8443") {
		t.Fatal("IPv4 loopback aliases should collide")
	}
	if !sameAddress("localhost:8443", "127.0.0.1:8443") {
		t.Fatal("localhost may resolve to the IPv4 loopback listener")
	}
	if !sameAddress("localhost:8443", "[::1]:8443") {
		t.Fatal("localhost may resolve to the IPv6 loopback listener")
	}
	if sameAddress("127.0.0.1:8080", "127.0.0.1:8443") {
		t.Fatal("different ports should not collide")
	}
	if sameAddress("127.0.0.1:8443", "[::1]:8443") {
		t.Fatal("IPv4 and IPv6 require distinct sockets")
	}
}

func TestDefaultBaseURLPreservesHostedHTTPS(t *testing.T) {
	t.Setenv("WT_BASE_URL", "https://wingthing.example.test")
	got := defaultBaseURL(nil)
	if got != "https://wingthing.example.test" {
		t.Fatalf("defaultBaseURL = %q", got)
	}
}

func TestDefaultBaseURLChangesOnlyWhenLocalHTTPSRequested(t *testing.T) {
	t.Setenv("WT_BASE_URL", "")
	if got := defaultBaseURL(nil); got != "http://localhost:8080" {
		t.Fatalf("legacy default = %q", got)
	}
	if got := defaultBaseURL(&localHTTPSConfig{URL: "https://localhost:8443"}); got != "https://localhost:8443" {
		t.Fatalf("local HTTPS default = %q", got)
	}
}

func TestLocalHTTPSCannotInheritAStalePublicBaseURL(t *testing.T) {
	t.Setenv("WT_BASE_URL", "https://public.example.test")
	if got := defaultBaseURL(&localHTTPSConfig{URL: "https://localhost:8443"}); got != "https://localhost:8443" {
		t.Fatalf("local HTTPS inherited public origin: %q", got)
	}
}

func TestListenerResultIgnoresServerClosedOnly(t *testing.T) {
	if err := listenerResult(namedServerError{}); err != nil {
		t.Fatal(err)
	}
	if err := listenerResult(namedServerError{err: http.ErrServerClosed}); err != nil {
		t.Fatal(err)
	}
	err := listenerResult(namedServerError{listener: "browser HTTPS", err: os.ErrPermission})
	if err == nil || !strings.Contains(err.Error(), "browser HTTPS") {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("wrapped err = %v", err)
	}
}

func TestRelayListenersBoundRequestHeadersWithoutTimingOutWebSockets(t *testing.T) {
	listeners := newRelayListeners(http.NotFoundHandler(), "127.0.0.1:8080", &localHTTPSConfig{HTTPSAddr: "127.0.0.1:8443"})
	for name, server := range map[string]*http.Server{"http": listeners.http, "https": listeners.https} {
		if server.ReadHeaderTimeout != relayReadHeaderTimeout || server.IdleTimeout != relayIdleTimeout || server.MaxHeaderBytes != relayMaxHeaderBytes {
			t.Errorf("%s limits = header %s idle %s bytes %d", name, server.ReadHeaderTimeout, server.IdleTimeout, server.MaxHeaderBytes)
		}
		if server.ReadTimeout != 0 || server.WriteTimeout != 0 {
			t.Errorf("%s has whole-connection timeout that would terminate long-lived WebSockets: read=%s write=%s", name, server.ReadTimeout, server.WriteTimeout)
		}
	}
}

func TestRelayListenersServeSameHandlerOverLoopbackHTTPAndHTTPS(t *testing.T) {
	m, err := localtls.Ensure(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	httpAddr := unusedLoopbackAddress(t)
	httpsAddr := unusedLoopbackAddress(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Wingthing-Test", r.URL.Path)
		_, _ = w.Write([]byte("local HTTPS works"))
	})
	localHTTPS := &localHTTPSConfig{
		HTTPAddr:  httpAddr,
		HTTPSAddr: httpsAddr,
		URL:       "https://" + httpsAddr,
		Material:  m,
	}
	listeners := newRelayListeners(handler, httpAddr, localHTTPS)
	if err := listeners.Start(localHTTPS); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = listeners.http.Shutdown(ctx)
		_ = listeners.https.Shutdown(ctx)
	})

	roots := x509.NewCertPool()
	roots.AddCert(m.CACert)
	httpsClient := &http.Client{
		Timeout: time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:    roots,
			ServerName: "localhost",
			MinVersion: tls.VersionTLS12,
		}},
	}
	assertEventuallyResponse(t, http.DefaultClient, "http://"+httpAddr+"/plain", "local HTTPS works")
	assertEventuallyResponse(t, httpsClient, "https://"+httpsAddr+"/secure", "local HTTPS works")

	untrustedClient := &http.Client{Timeout: time.Second}
	if response, err := untrustedClient.Get("https://" + httpsAddr + "/untrusted"); err == nil {
		closeForTest(t, "unexpected untrusted response body", response.Body)
		t.Fatal("locally generated CA was unexpectedly trusted before installation")
	}
}

func TestRelayListenersFailAtomicallyWhenHTTPSAddressIsOccupied(t *testing.T) {
	m, err := localtls.Ensure(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	httpAddr := unusedLoopbackAddress(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, "occupied listener", occupied)
	localHTTPS := &localHTTPSConfig{HTTPSAddr: occupied.Addr().String(), Material: m}
	listeners := newRelayListeners(http.NotFoundHandler(), httpAddr, localHTTPS)
	if err := listeners.Start(localHTTPS); err == nil || !strings.Contains(err.Error(), "browser HTTPS") {
		t.Fatalf("Start error = %v, want browser HTTPS bind failure", err)
	}

	// The first listener must be closed again when the second bind fails. A
	// partially running roost is especially confusing because its printed HTTPS
	// URL can never become usable.
	rebound, err := net.Listen("tcp", httpAddr)
	if err != nil {
		t.Fatalf("wing HTTP listener leaked after HTTPS failure: %v", err)
	}
	_ = rebound.Close()
}

func TestRelayListenersRejectMissingHTTPSMaterialBeforeBinding(t *testing.T) {
	httpAddr := unusedLoopbackAddress(t)
	listeners := newRelayListeners(http.NotFoundHandler(), httpAddr, &localHTTPSConfig{HTTPSAddr: unusedLoopbackAddress(t)})
	if err := listeners.Start(nil); err == nil || !strings.Contains(err.Error(), "certificate material") {
		t.Fatalf("Start error = %v", err)
	}
	rebound, err := net.Listen("tcp", httpAddr)
	if err != nil {
		t.Fatalf("listener bound before certificate validation: %v", err)
	}
	_ = rebound.Close()
}

func unusedLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skipf("sandbox does not permit loopback listeners: %v", err)
		}
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func assertEventuallyResponse(t *testing.T, client *http.Client, rawURL, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		response, err := client.Get(rawURL)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			closeForTest(t, "eventual response body", response.Body)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(body) != want {
				t.Fatalf("%s body = %q, want %q", rawURL, body, want)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s: %v", rawURL, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLocalCertHelpStatesPrivateKeyBoundary(t *testing.T) {
	cmd := localCertCmd()
	if !strings.Contains(cmd.Long, "private key") || !strings.Contains(cmd.Long, "never leaves this machine") {
		t.Fatalf("local-cert help omits key boundary: %q", cmd.Long)
	}
}

func TestValidateLocalHTTPSModeLeavesHostedAndOrgModesAlone(t *testing.T) {
	if err := validateLocalHTTPSMode(false, false, false); err != nil {
		t.Fatalf("ordinary hosted mode changed: %v", err)
	}
	if err := validateLocalHTTPSMode(false, true, false); err != nil {
		t.Fatalf("ordinary local HTTP mode changed: %v", err)
	}
	if err := validateLocalHTTPSMode(true, false, false); err == nil || !strings.Contains(err.Error(), "hosted and org") {
		t.Fatalf("authenticated deployment accepted local CA mode: %v", err)
	}
	if err := validateLocalHTTPSMode(true, true, true); err == nil || !strings.Contains(err.Error(), "edge mode") {
		t.Fatalf("edge deployment accepted local CA mode: %v", err)
	}
	if err := validateLocalHTTPSMode(true, true, false); err != nil {
		t.Fatalf("local HTTPS rejected: %v", err)
	}
}

func TestAuthProvidersConfiguredMatchesRoostAuthModes(t *testing.T) {
	for _, key := range []string{"GITHUB_CLIENT_ID", "GOOGLE_CLIENT_ID", "SMTP_HOST"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("GITHUB_CLIENT_ID", "")
			t.Setenv("GOOGLE_CLIENT_ID", "")
			t.Setenv("SMTP_HOST", "")
			if authProvidersConfigured() {
				t.Fatal("empty auth environment detected")
			}
			t.Setenv(key, "configured")
			if !authProvidersConfigured() {
				t.Fatalf("%s was ignored", key)
			}
		})
	}

	t.Setenv("GITHUB_CLIENT_ID", " \t ")
	t.Setenv("GOOGLE_CLIENT_ID", "")
	t.Setenv("SMTP_HOST", "")
	if authProvidersConfigured() {
		t.Fatal("whitespace-only auth environment detected")
	}
}

func TestValidateAuthProviderEnvironmentRejectsPartialOAuthPairs(t *testing.T) {
	for _, key := range []string{
		"GITHUB_CLIENT_ID",
		"GITHUB_CLIENT_SECRET",
		"GOOGLE_CLIENT_ID",
		"GOOGLE_CLIENT_SECRET",
	} {
		t.Run(key, func(t *testing.T) {
			for _, env := range []string{
				"GITHUB_CLIENT_ID",
				"GITHUB_CLIENT_SECRET",
				"GOOGLE_CLIENT_ID",
				"GOOGLE_CLIENT_SECRET",
			} {
				t.Setenv(env, "")
			}
			t.Setenv(key, "configured")
			if err := validateAuthProviderEnvironment(); err == nil || !strings.Contains(err.Error(), "incomplete") {
				t.Fatalf("partial %s configuration error = %v", key, err)
			}
		})
	}
}

func TestValidateAuthProviderEnvironmentAcceptsCompleteAndSMTPModes(t *testing.T) {
	for _, env := range []string{
		"GITHUB_CLIENT_ID",
		"GITHUB_CLIENT_SECRET",
		"GOOGLE_CLIENT_ID",
		"GOOGLE_CLIENT_SECRET",
		"SMTP_HOST",
	} {
		t.Setenv(env, "")
	}
	if err := validateAuthProviderEnvironment(); err != nil {
		t.Fatalf("empty local mode: %v", err)
	}

	t.Setenv("GITHUB_CLIENT_ID", "id")
	t.Setenv("GITHUB_CLIENT_SECRET", "secret")
	if err := validateAuthProviderEnvironment(); err != nil {
		t.Fatalf("complete GitHub mode: %v", err)
	}

	t.Setenv("GITHUB_CLIENT_ID", "")
	t.Setenv("GITHUB_CLIENT_SECRET", "")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	if err := validateAuthProviderEnvironment(); err != nil {
		t.Fatalf("SMTP mode: %v", err)
	}
}

func TestServeHTTPSFlagsAreOptIn(t *testing.T) {
	cmd := serveCmd()
	https, err := cmd.Flags().GetBool("https")
	if err != nil {
		t.Fatal(err)
	}
	if https {
		t.Fatal("hosted/upstream serve unexpectedly enables a local CA")
	}
	if got, err := cmd.Flags().GetString("https-addr"); err != nil || got != defaultLocalHTTPSAddr {
		t.Fatalf("https-addr = %q, %v", got, err)
	}
}

func TestRoostHTTPSFlagsAreOptIn(t *testing.T) {
	cmd := roostStartCmd()
	https, err := cmd.Flags().GetBool("https")
	if err != nil {
		t.Fatal(err)
	}
	if https {
		t.Fatal("existing roost unexpectedly enables trust-store mutation")
	}
	if got, err := cmd.Flags().GetString("https-addr"); err != nil || got != defaultLocalHTTPSAddr {
		t.Fatalf("https-addr = %q, %v", got, err)
	}
}
