package localrelay

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/localtls"
	"github.com/ehrlich-b/wingthing/internal/relay"
)

const (
	defaultLocalHTTPAddr  = "127.0.0.1:8080"
	DefaultLocalHTTPSAddr = "127.0.0.1:8443"
)

type LocalHTTPSConfig struct {
	HTTPAddr  string
	HTTPSAddr string
	URL       string
	Material  *localtls.Material
}

func PrepareLocalHTTPS(ctx context.Context, configDir, httpAddr, httpsAddr string, httpAddrExplicit bool) (*LocalHTTPSConfig, error) {
	httpAddr = localHTTPAddrForHTTPS(httpAddr, httpAddrExplicit)
	if err := requireLoopbackAddress("wing HTTP", httpAddr); err != nil {
		return nil, err
	}
	if err := requireLoopbackAddress("browser HTTPS", httpsAddr); err != nil {
		return nil, err
	}
	if err := requireLocalCertificateAddress(httpsAddr); err != nil {
		return nil, err
	}
	if sameAddress(httpAddr, httpsAddr) {
		return nil, fmt.Errorf("wing HTTP and browser HTTPS listeners must use different addresses")
	}

	m, err := localtls.Ensure(configDir, time.Now())
	if err != nil {
		return nil, fmt.Errorf("prepare local HTTPS certificate: %w", err)
	}
	PrintLocalCertDisclosure(m)
	installed, err := InstallLocalTrust(ctx, m)
	if err != nil {
		return nil, err
	}
	if installed {
		fmt.Println("  installed: public CA certificate → current user's trust store")
	} else {
		fmt.Println("  trusted:   public CA certificate is already installed for this user")
	}
	browserURL, err := browserHTTPSURL(httpsAddr)
	if err != nil {
		return nil, err
	}
	return &LocalHTTPSConfig{HTTPAddr: httpAddr, HTTPSAddr: httpsAddr, URL: browserURL, Material: m}, nil
}

func InstallLocalTrust(ctx context.Context, m *localtls.Material) (bool, error) {
	store := localtls.SystemTrustStore()
	trusted, err := store.Trusted(ctx, m)
	if err != nil {
		return false, err
	}
	if !trusted {
		printTrustPrompt(m)
	}
	return store.Install(ctx, m)
}

func localHTTPAddrForHTTPS(addr string, explicit bool) string {
	if !explicit && addr == ":8080" {
		return defaultLocalHTTPAddr
	}
	return addr
}

func PrepareLocalHTTPAddress(addr string, explicit bool) (string, error) {
	addr = localHTTPAddrForHTTPS(addr, explicit)
	if err := requireLoopbackAddress("local roost HTTP", addr); err != nil {
		return "", err
	}
	return addr, nil
}

func printTrustPrompt(m *localtls.Material) {
	fmt.Printf("  installing: %s (public certificate only)\n", m.CACertPath)
	if runtime.GOOS == "darwin" {
		fmt.Println("  approval:   macOS may ask you to approve this user trust-store change")
	}
}

func PrintLocalCertDisclosure(m *localtls.Material) {
	action := "using the existing"
	if m.CreatedCA {
		action = "created an on-demand"
	}
	fmt.Printf("local HTTPS: %s localhost-only certificate authority\n", action)
	fmt.Printf("  private CA:  %s (mode 0600; stays on this machine)\n", m.CAKeyPath)
	fmt.Printf("  private TLS: %s (mode 0600; stays on this machine)\n", m.KeyPath)
	fmt.Printf("  public CA:   %s\n", m.CACertPath)
	fmt.Println("  scope:       certificate is constrained to localhost and loopback IPs")
	fmt.Println("  trust:       only the public CA certificate is installed; no private key leaves this box")
}

func requireLoopbackAddress(label, addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s address %q: %w", label, addr, err)
	}
	if host == "" {
		return fmt.Errorf("%s address %q listens on every interface; local HTTPS requires an explicit loopback host", label, addr)
	}
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("%s address %q is not loopback; local HTTPS refuses LAN or public listeners", label, addr)
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("%s address %q has an invalid port", label, addr)
	}
	return nil
}

func requireLocalCertificateAddress(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("browser HTTPS address %q: %w", addr, err)
	}
	if strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	return fmt.Errorf("browser HTTPS address %q is not covered by the localhost certificate; use localhost, 127.0.0.1, or ::1", addr)
}

func sameAddress(a, b string) bool {
	aHost, aPort, aErr := net.SplitHostPort(a)
	bHost, bPort, bErr := net.SplitHostPort(b)
	if aErr != nil || bErr != nil || aPort != bPort {
		return false
	}
	if strings.EqualFold(aHost, "localhost") || strings.EqualFold(bHost, "localhost") {
		return isLoopbackListenerHost(aHost) && isLoopbackListenerHost(bHost)
	}
	return normalizeLoopbackHost(aHost) == normalizeLoopbackHost(bHost)
}

func isLoopbackListenerHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func normalizeLoopbackHost(host string) string {
	if strings.EqualFold(host, "localhost") {
		return "localhost"
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		if ip.To4() != nil {
			return "ipv4-loopback"
		}
		return "ipv6-loopback"
	}
	return strings.ToLower(host)
}

func browserHTTPSURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("browser HTTPS address %q: %w", addr, err)
	}
	if err := requireLocalCertificateAddress(addr); err != nil {
		return "", err
	}
	browserHost := "localhost"
	if host == "::1" {
		browserHost = "[::1]"
	}
	if port == "443" {
		return "https://" + browserHost, nil
	}
	return "https://" + net.JoinHostPort(strings.Trim(browserHost, "[]"), port), nil
}

func LocalHTTPURL(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	return "http://" + addr
}

func DefaultBaseURL(localHTTPS *LocalHTTPSConfig) string {
	if localHTTPS != nil {
		return localHTTPS.URL
	}
	if value := os.Getenv("WT_BASE_URL"); value != "" {
		return value
	}
	return "http://localhost:8080"
}

func ValidateLocalHTTPSMode(requested, localMode, edgeMode bool) error {
	if !requested {
		return nil
	}
	if edgeMode {
		return fmt.Errorf("--https is for a self-hosted local roost and is not compatible with edge mode")
	}
	if !localMode {
		return fmt.Errorf("--https installs a device-local CA and is only available in single-user local mode; hosted and org deployments keep their existing HTTPS configuration")
	}
	return nil
}

func AuthProvidersConfigured() bool {
	return strings.TrimSpace(os.Getenv("GITHUB_CLIENT_ID")) != "" ||
		strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_ID")) != "" ||
		strings.TrimSpace(os.Getenv("SMTP_HOST")) != ""
}

func ValidateAuthProviderEnvironment() error {
	for _, provider := range []struct {
		name      string
		idEnv     string
		secretEnv string
	}{
		{name: "GitHub", idEnv: "GITHUB_CLIENT_ID", secretEnv: "GITHUB_CLIENT_SECRET"},
		{name: "Google", idEnv: "GOOGLE_CLIENT_ID", secretEnv: "GOOGLE_CLIENT_SECRET"},
	} {
		hasID := strings.TrimSpace(os.Getenv(provider.idEnv)) != ""
		hasSecret := strings.TrimSpace(os.Getenv(provider.secretEnv)) != ""
		if hasID == hasSecret {
			continue
		}
		missing := provider.idEnv
		present := provider.secretEnv
		if hasID {
			missing = provider.secretEnv
			present = provider.idEnv
		}
		return fmt.Errorf("incomplete %s OAuth configuration: %s is required when %s is set", provider.name, missing, present)
	}
	return nil
}

type relayListeners struct {
	http  *http.Server
	https *http.Server
	ErrCh chan namedServerError
}

type namedServerError struct {
	listener string
	err      error
}

const (
	relayReadHeaderTimeout = 10 * time.Second
	relayIdleTimeout       = 2 * time.Minute
	relayMaxHeaderBytes    = 1 << 20
)

func newRelayHTTPServer(handler http.Handler, address string) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: relayReadHeaderTimeout,
		IdleTimeout:       relayIdleTimeout,
		MaxHeaderBytes:    relayMaxHeaderBytes,
	}
}

func NewRelayListeners(handler http.Handler, httpAddr string, localHTTPS *LocalHTTPSConfig) *relayListeners {
	count := 1
	if localHTTPS != nil {
		count++
	}
	listeners := &relayListeners{
		http:  newRelayHTTPServer(handler, httpAddr),
		ErrCh: make(chan namedServerError, count),
	}
	if localHTTPS != nil {
		listeners.https = newRelayHTTPServer(handler, localHTTPS.HTTPSAddr)
	}
	return listeners
}

func (l *relayListeners) Start(localHTTPS *LocalHTTPSConfig) error {
	if l.https != nil {
		if localHTTPS == nil || localHTTPS.Material == nil {
			return fmt.Errorf("browser HTTPS listener is missing certificate material")
		}
		if _, err := tls.LoadX509KeyPair(localHTTPS.Material.CertPath, localHTTPS.Material.KeyPath); err != nil {
			return fmt.Errorf("browser HTTPS certificate: %w", err)
		}
	}

	httpListener, err := net.Listen("tcp", l.http.Addr)
	if err != nil {
		return ListenerResult(namedServerError{listener: "wing HTTP", err: err})
	}
	var httpsListener net.Listener
	if l.https != nil {
		httpsListener, err = net.Listen("tcp", l.https.Addr)
		if err != nil {
			_ = httpListener.Close()
			return ListenerResult(namedServerError{listener: "browser HTTPS", err: err})
		}
	}

	go func() {
		l.ErrCh <- namedServerError{listener: "wing HTTP", err: l.http.Serve(httpListener)}
	}()
	if l.https != nil {
		go func() {
			l.ErrCh <- namedServerError{
				listener: "browser HTTPS",
				err:      l.https.ServeTLS(httpsListener, localHTTPS.Material.CertPath, localHTTPS.Material.KeyPath),
			}
		}()
	}
	return nil
}

func (l *relayListeners) Shutdown(srv *relay.Server, timeout time.Duration) error {
	// GracefulShutdown broadcasts relay.restart and closes wing connections once.
	httpErr := srv.GracefulShutdown(l.http, timeout)
	if l.https == nil {
		return httpErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return errors.Join(httpErr, l.https.Shutdown(ctx))
}

func ListenerResult(result namedServerError) error {
	if result.err == nil || errors.Is(result.err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("%s listener: %w", result.listener, result.err)
}
