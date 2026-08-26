package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/localtls"
	"github.com/ehrlich-b/wingthing/internal/relay"
	"github.com/spf13/cobra"
)

const (
	defaultLocalHTTPAddr  = "127.0.0.1:8080"
	defaultLocalHTTPSAddr = "127.0.0.1:8443"
)

type localHTTPSConfig struct {
	HTTPAddr  string
	HTTPSAddr string
	URL       string
	Material  *localtls.Material
}

func prepareLocalHTTPS(ctx context.Context, configDir, httpAddr, httpsAddr string, httpAddrExplicit bool) (*localHTTPSConfig, error) {
	httpAddr = localHTTPAddrForHTTPS(httpAddr, httpAddrExplicit)
	if err := requireLoopbackAddress("wing HTTP", httpAddr); err != nil {
		return nil, err
	}
	if err := requireLoopbackAddress("browser HTTPS", httpsAddr); err != nil {
		return nil, err
	}
	if sameAddress(httpAddr, httpsAddr) {
		return nil, fmt.Errorf("wing HTTP and browser HTTPS listeners must use different addresses")
	}

	m, err := localtls.Ensure(configDir, time.Now())
	if err != nil {
		return nil, fmt.Errorf("prepare local HTTPS certificate: %w", err)
	}
	printLocalCertDisclosure(m)
	installed, err := installLocalTrust(ctx, m)
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
	return &localHTTPSConfig{HTTPAddr: httpAddr, HTTPSAddr: httpsAddr, URL: browserURL, Material: m}, nil
}

func installLocalTrust(ctx context.Context, m *localtls.Material) (bool, error) {
	store := localtls.SystemTrustStore()
	trusted, err := store.Trusted(m)
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

func printTrustPrompt(m *localtls.Material) {
	fmt.Printf("  installing: %s (public certificate only)\n", m.CACertPath)
	if runtime.GOOS == "darwin" {
		fmt.Println("  approval:   macOS may ask you to approve this user trust-store change")
	}
}

func printLocalCertDisclosure(m *localtls.Material) {
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
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("browser HTTPS address %q: %w", addr, err)
	}
	if port == "443" {
		return "https://localhost", nil
	}
	return "https://" + net.JoinHostPort("localhost", port), nil
}

func localHTTPURL(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	return "http://" + addr
}

func defaultBaseURL(localHTTPS *localHTTPSConfig) string {
	if localHTTPS != nil {
		return localHTTPS.URL
	}
	if value := os.Getenv("WT_BASE_URL"); value != "" {
		return value
	}
	return "http://localhost:8080"
}

func validateLocalHTTPSMode(requested, localMode, edgeMode bool) error {
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

func authProvidersConfigured() bool {
	return os.Getenv("GITHUB_CLIENT_ID") != "" ||
		os.Getenv("GOOGLE_CLIENT_ID") != "" ||
		os.Getenv("SMTP_HOST") != ""
}

type relayListeners struct {
	http  *http.Server
	https *http.Server
	errCh chan namedServerError
}

type namedServerError struct {
	listener string
	err      error
}

func newRelayListeners(handler http.Handler, httpAddr string, localHTTPS *localHTTPSConfig) *relayListeners {
	count := 1
	if localHTTPS != nil {
		count++
	}
	listeners := &relayListeners{
		http:  &http.Server{Addr: httpAddr, Handler: handler},
		errCh: make(chan namedServerError, count),
	}
	if localHTTPS != nil {
		listeners.https = &http.Server{Addr: localHTTPS.HTTPSAddr, Handler: handler}
	}
	return listeners
}

func (l *relayListeners) Start(localHTTPS *localHTTPSConfig) {
	go func() {
		l.errCh <- namedServerError{listener: "wing HTTP", err: l.http.ListenAndServe()}
	}()
	if l.https != nil {
		go func() {
			l.errCh <- namedServerError{
				listener: "browser HTTPS",
				err:      l.https.ListenAndServeTLS(localHTTPS.Material.CertPath, localHTTPS.Material.KeyPath),
			}
		}()
	}
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

func listenerResult(result namedServerError) error {
	if result.err == nil || errors.Is(result.err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("%s listener: %w", result.listener, result.err)
}

func localCertCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "local-cert",
		Short: "Manage the on-demand localhost HTTPS certificate",
		Long:  "Create and trust Wingthing's localhost-only CA. The private key is created on demand in WINGTHING_DIR, remains mode 0600, and never leaves this machine. Only the public CA certificate is installed in the current user's trust store.",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Create and trust the localhost certificate",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			m, err := localtls.Ensure(cfg.Dir, time.Now())
			if err != nil {
				return err
			}
			printLocalCertDisclosure(m)
			installed, err := installLocalTrust(cmd.Context(), m)
			if err != nil {
				return err
			}
			if installed {
				fmt.Println("installed only the public CA certificate in the current user's trust store")
			} else {
				fmt.Println("public CA certificate is already trusted for the current user")
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "remove",
		Short: "Remove the public CA from this user's trust store",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			m, err := localtls.Load(cfg.Dir)
			if errors.Is(err, localtls.ErrNotFound) {
				fmt.Println("no Wingthing localhost certificate has been created for this profile")
				return nil
			}
			if err != nil {
				return err
			}
			removed, err := localtls.SystemTrustStore().Remove(cmd.Context(), m)
			if err != nil {
				return err
			}
			if removed {
				fmt.Println("removed the public Wingthing localhost CA from this user's trust store")
			} else {
				fmt.Println("the public Wingthing localhost CA is not marked as trusted")
			}
			fmt.Printf("private keys were left on this machine in %s\n", m.Dir)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show local certificate and trust status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			m, err := localtls.Load(cfg.Dir)
			if errors.Is(err, localtls.ErrNotFound) {
				fmt.Println("no Wingthing localhost certificate has been created for this profile")
				return nil
			}
			if err != nil {
				return err
			}
			trusted, err := localtls.SystemTrustStore().Trusted(m)
			if err != nil {
				return err
			}
			printLocalCertDisclosure(m)
			fmt.Printf("  installed: %t\n", trusted)
			return nil
		},
	})
	return cmd
}
