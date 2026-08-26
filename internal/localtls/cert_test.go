package localtls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestEnsureCreatesConstrainedCAAndLocalhostLeaf(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	m, err := Ensure(t.TempDir(), now)
	if err != nil {
		t.Fatal(err)
	}
	if !m.CreatedCA || !m.CreatedLeaf {
		t.Fatalf("CreatedCA=%v CreatedLeaf=%v, want both true", m.CreatedCA, m.CreatedLeaf)
	}
	if !m.CACert.IsCA || !m.CACert.PermittedDNSDomainsCritical {
		t.Fatalf("CA constraints missing: IsCA=%v critical=%v", m.CACert.IsCA, m.CACert.PermittedDNSDomainsCritical)
	}
	if !slices.Equal(m.CACert.PermittedDNSDomains, []string{"localhost"}) {
		t.Fatalf("permitted DNS domains = %v", m.CACert.PermittedDNSDomains)
	}
	if len(m.CACert.PermittedIPRanges) != 2 {
		t.Fatalf("permitted IP ranges = %v", m.CACert.PermittedIPRanges)
	}
	if err := m.Cert.VerifyHostname("localhost"); err != nil {
		t.Fatalf("localhost SAN: %v", err)
	}
	if err := m.Cert.VerifyHostname("127.0.0.1"); err != nil {
		t.Fatalf("IPv4 SAN: %v", err)
	}
	if err := m.Cert.VerifyHostname("::1"); err != nil {
		t.Fatalf("IPv6 SAN: %v", err)
	}
	if err := m.Cert.VerifyHostname("wingthing.ai"); err == nil {
		t.Fatal("certificate unexpectedly valid for wingthing.ai")
	}
	roots := x509.NewCertPool()
	roots.AddCert(m.CACert)
	if _, err := m.Cert.Verify(x509.VerifyOptions{DNSName: "localhost", Roots: roots, CurrentTime: now}); err != nil {
		t.Fatalf("verify generated chain: %v", err)
	}
}

func TestCANameConstraintsRejectEvenACertificateItSignedForPublicDNS(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	m, err := Ensure(t.TempDir(), now)
	if err != nil {
		t.Fatal(err)
	}
	caKey, err := readECPrivateKey(m.CAKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	rogueKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"wingthing.ai"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, m.CACert, &rogueKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	rogue, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(m.CACert)
	if _, err := rogue.Verify(x509.VerifyOptions{DNSName: "wingthing.ai", Roots: roots, CurrentTime: now}); err == nil {
		t.Fatal("name-constrained local CA authorized a public DNS certificate")
	}
}

func TestEnsureUsesRestrictivePermissionsAndReusesCA(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	first, err := Ensure(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Ensure(dir, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if second.CreatedCA || second.CreatedLeaf {
		t.Fatalf("second Ensure created material: CA=%v leaf=%v", second.CreatedCA, second.CreatedLeaf)
	}
	if first.Fingerprint != second.Fingerprint {
		t.Fatalf("CA changed: %s != %s", first.Fingerprint, second.Fingerprint)
	}
	for _, tc := range []struct {
		path string
		want os.FileMode
	}{
		{first.Dir, 0700},
		{first.CAKeyPath, 0600},
		{first.KeyPath, 0600},
		{first.CACertPath, 0644},
		{first.CertPath, 0644},
	} {
		info, err := os.Stat(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != tc.want {
			t.Errorf("%s mode = %o, want %o", tc.path, got, tc.want)
		}
	}
}

func TestLoadNeverCreatesOrRotatesCertificateMaterial(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load before Ensure = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, directoryName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load created directory: %v", err)
	}

	created, err := Ensure(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(created.CertPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(created.CertPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.CreatedCA || loaded.CreatedLeaf {
		t.Fatalf("Load claims creation: CA=%v leaf=%v", loaded.CreatedCA, loaded.CreatedLeaf)
	}
	if !slices.Equal(before, after) {
		t.Fatal("Load changed leaf certificate")
	}
}

func TestEnsureRotatesLeafWithoutReplacingCA(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	first, err := Ensure(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Ensure(dir, now.AddDate(0, 11, 15))
	if err != nil {
		t.Fatal(err)
	}
	if second.CreatedCA || !second.CreatedLeaf {
		t.Fatalf("rotation CreatedCA=%v CreatedLeaf=%v", second.CreatedCA, second.CreatedLeaf)
	}
	if first.Fingerprint != second.Fingerprint {
		t.Fatal("leaf rotation replaced CA")
	}
	if first.Cert.SerialNumber.Cmp(second.Cert.SerialNumber) == 0 {
		t.Fatal("leaf rotation reused serial number")
	}
}

func TestEnsureRepairsLeafCorruptionButNeverCAIdentity(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	first, err := Ensure(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first.CertPath, []byte("corrupt leaf"), 0644); err != nil {
		t.Fatal(err)
	}
	second, err := Ensure(dir, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if second.CreatedCA || !second.CreatedLeaf {
		t.Fatalf("repair CreatedCA=%v CreatedLeaf=%v", second.CreatedCA, second.CreatedLeaf)
	}
	if first.Fingerprint != second.Fingerprint {
		t.Fatal("leaf repair replaced CA")
	}

	if err := os.WriteFile(second.CACertPath, []byte("corrupt CA"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(dir, now.Add(2*time.Minute)); err == nil {
		t.Fatal("corrupt CA was silently replaced")
	}
}

func TestEnsureRefusesExpiredCARatherThanReplacingTrustedRoot(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	first, err := Ensure(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Ensure(dir, first.CACert.NotAfter.Add(time.Second))
	if err == nil || !strings.Contains(err.Error(), "not valid") {
		t.Fatalf("expired CA error = %v", err)
	}
	cert, readErr := readCertificate(first.CACertPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if certificateFingerprint(cert) != first.Fingerprint {
		t.Fatal("expired CA was replaced")
	}
}

func TestEnsureRefusesIncompleteCAAndSymlinks(t *testing.T) {
	t.Run("incomplete CA", func(t *testing.T) {
		dir := t.TempDir()
		tlsDir := filepath.Join(dir, directoryName)
		if err := os.Mkdir(tlsDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tlsDir, caCertName), []byte("broken"), 0644); err != nil {
			t.Fatal(err)
		}
		_, err := Ensure(dir, time.Now())
		if err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("err = %v, want incomplete CA", err)
		}
	})

	t.Run("symlinked key", func(t *testing.T) {
		dir := t.TempDir()
		m, err := Ensure(dir, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(m.CAKeyPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, "elsewhere"), m.CAKeyPath); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		_, err = Ensure(dir, time.Now())
		if err == nil || !strings.Contains(err.Error(), "non-regular") {
			t.Fatalf("err = %v, want symlink refusal", err)
		}
	})
}

type recordingRunner struct {
	name string
	args []string
	err  error
}

func TestSystemRunnerConnectsTrustCommandToCurrentStdin(t *testing.T) {
	cmd := trustExecCommand(context.Background(), "security", "help")
	if cmd.Stdin != os.Stdin || cmd.Stdout != os.Stdout || cmd.Stderr != os.Stderr {
		t.Fatal("trust command is not attached to the interactive terminal")
	}
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) error {
	r.name = name
	r.args = append([]string(nil), args...)
	return r.err
}

type sequenceRunner struct {
	calls int
	errs  []error
}

func (r *sequenceRunner) Run(_ context.Context, _ string, _ ...string) error {
	index := r.calls
	r.calls++
	if index < len(r.errs) {
		return r.errs[index]
	}
	return nil
}

func TestTrustCommandsNeverReceivePrivateKeyPaths(t *testing.T) {
	for _, goos := range []string{"darwin", "windows", "linux"} {
		t.Run(goos, func(t *testing.T) {
			m, err := Ensure(t.TempDir(), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			runner := &recordingRunner{}
			home := t.TempDir()
			if goos == "linux" {
				nssDir := filepath.Join(home, ".pki", "nssdb")
				if err := os.MkdirAll(nssDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(nssDir, "cert9.db"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			store := TrustStore{GOOS: goos, HomeDir: home, Runner: runner}
			installed, err := store.Install(context.Background(), m)
			if err != nil {
				t.Fatal(err)
			}
			if !installed {
				t.Fatal("Install returned false")
			}
			if info, err := os.Stat(m.MarkerPath); err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("trust marker mode = %v, %v", info, err)
			}
			joined := strings.Join(runner.args, " ")
			if !strings.Contains(joined, m.CACertPath) {
				t.Fatalf("install args omit public CA: %q", joined)
			}
			if strings.Contains(joined, m.CAKeyPath) || strings.Contains(joined, m.KeyPath) {
				t.Fatalf("private key leaked to trust command: %q", joined)
			}

			// The marker makes install idempotent.
			runner.name, runner.args = "", nil
			installed, err = store.Install(context.Background(), m)
			if err != nil || installed || runner.name != "" {
				t.Fatalf("second install = (%v, %v), command=%q", installed, err, runner.name)
			}
			removed, err := store.Remove(context.Background(), m)
			if err != nil || !removed {
				t.Fatalf("Remove = (%v, %v)", removed, err)
			}
			joined = strings.Join(runner.args, " ")
			if strings.Contains(joined, m.CAKeyPath) || strings.Contains(joined, m.KeyPath) {
				t.Fatalf("private key leaked to remove command: %q", joined)
			}
		})
	}
}

func TestTrustStoreDoesNotMarkFailedInstall(t *testing.T) {
	m, err := Ensure(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{err: errors.New("denied")}
	_, err = (TrustStore{GOOS: "darwin", Runner: runner}).Install(context.Background(), m)
	if err == nil {
		t.Fatal("Install succeeded")
	}
	if _, statErr := os.Stat(m.MarkerPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("marker exists after failed install: %v", statErr)
	}
}

func TestLinuxTrustStoreIsInitializedWithoutRoot(t *testing.T) {
	m, err := Ensure(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	runner := &sequenceRunner{}
	store := TrustStore{GOOS: "linux", HomeDir: home, Runner: runner}
	installed, err := store.Install(context.Background(), m)
	if err != nil || !installed {
		t.Fatalf("Install = (%v, %v)", installed, err)
	}
	if runner.calls != 2 {
		t.Fatalf("Linux install ran %d commands, want NSS initialize + public CA install", runner.calls)
	}
	info, err := os.Stat(filepath.Join(home, ".pki", "nssdb"))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("NSS directory = %v, %v", info, err)
	}
}

func TestUnsupportedTrustStoreExplainsWherePublicCAIs(t *testing.T) {
	m, err := Ensure(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = (TrustStore{GOOS: "plan9", Runner: &recordingRunner{}}).Install(context.Background(), m)
	if err == nil || !strings.Contains(err.Error(), m.CACertPath) {
		t.Fatalf("err = %v", err)
	}
}
