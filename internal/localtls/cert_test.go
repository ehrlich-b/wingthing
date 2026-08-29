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
	"sync"
	"testing"
	"time"
)

func TestConcurrentFirstUseCreatesOneCoherentAuthority(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	const callers = 32
	start := make(chan struct{})
	results := make(chan *Material, callers)
	errors := make(chan error, callers)
	var group sync.WaitGroup
	for index := 0; index < callers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			material, err := Ensure(dir, now)
			if err != nil {
				errors <- err
				return
			}
			results <- material
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Errorf("concurrent Ensure: %v", err)
	}
	var fingerprint string
	for material := range results {
		if fingerprint == "" {
			fingerprint = material.Fingerprint
		}
		if material.Fingerprint != fingerprint {
			t.Errorf("concurrent authority fingerprint = %s, want %s", material.Fingerprint, fingerprint)
		}
	}
	if fingerprint == "" {
		t.Fatal("no concurrent Ensure call succeeded")
	}
	material, err := Ensure(dir, now)
	if err != nil {
		t.Fatalf("reload coherent authority: %v", err)
	}
	if material.Fingerprint != fingerprint || !publicKeysEqual(material.CACert.PublicKey, mustReadECPrivateKey(t, material.CAKeyPath)) {
		t.Fatal("persisted CA certificate and key are not the concurrently returned authority")
	}
}

func mustReadECPrivateKey(t *testing.T, path string) *ecdsa.PublicKey {
	t.Helper()
	key, err := readECPrivateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	return &key.PublicKey
}

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

func TestEnsureAndLoadRefuseAnUnconstrainedReplacementCA(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	material, err := Ensure(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(99),
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePEMAtomic(material.CACertPath, 0644, "CERTIFICATE", der); err != nil {
		t.Fatal(err)
	}
	if err := writePEMAtomic(material.CAKeyPath, 0600, "PRIVATE KEY", keyDER); err != nil {
		t.Fatal(err)
	}

	if _, err := Ensure(dir, now); err == nil || !strings.Contains(err.Error(), "valid self-signed CA") {
		t.Fatalf("Ensure unconstrained CA error = %v", err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "not constrained") {
		t.Fatalf("Load unconstrained CA error = %v", err)
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
	name  string
	args  []string
	err   error
	calls []runnerCall
}

type runnerCall struct {
	name string
	args []string
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
	r.calls = append(r.calls, runnerCall{name: name, args: append([]string(nil), args...)})
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
			var allArgs []string
			for _, call := range runner.calls {
				allArgs = append(allArgs, call.args...)
			}
			joined := strings.Join(allArgs, " ")
			if !strings.Contains(joined, m.CACertPath) {
				t.Fatalf("install args omit public CA: %q", joined)
			}
			if strings.Contains(joined, m.CAKeyPath) || strings.Contains(joined, m.KeyPath) {
				t.Fatalf("private key leaked to trust command: %q", joined)
			}

			// A verified marker makes install idempotent, while the real trust
			// store is still checked in case another tool removed the CA.
			runner.name, runner.args, runner.calls = "", nil, nil
			installed, err = store.Install(context.Background(), m)
			if err != nil || installed || len(runner.calls) != 1 {
				t.Fatalf("second install = (%v, %v), calls=%#v", installed, err, runner.calls)
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

func TestTrustMarkerAtomicWriteDoesNotFollowExistingSymlink(t *testing.T) {
	m, err := Ensure(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("do not change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, m.MarkerPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	runner := &recordingRunner{}
	installed, err := (TrustStore{GOOS: "darwin", HomeDir: t.TempDir(), Runner: runner}).Install(context.Background(), m)
	if err != nil || !installed {
		t.Fatalf("Install = (%v, %v)", installed, err)
	}
	if info, err := os.Lstat(m.MarkerPath); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatalf("marker = %v, %v", info, err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "do not change" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
}

func TestDarwinTrustUsesExplicitLoginKeychainAndVerifiesLocalhost(t *testing.T) {
	m, err := Ensure(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	runner := &recordingRunner{}
	installed, err := (TrustStore{GOOS: "darwin", HomeDir: home, Runner: runner}).Install(context.Background(), m)
	if err != nil || !installed {
		t.Fatalf("Install = (%v, %v)", installed, err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %v, want install + verification", runner.calls)
	}
	wantKeychain := filepath.Join(home, "Library", "Keychains", "login.keychain-db")
	installArgs := strings.Join(runner.calls[0].args, " ")
	if runner.calls[0].name != "security" || !strings.Contains(installArgs, "add-trusted-cert") || !strings.Contains(installArgs, "-k "+wantKeychain) {
		t.Fatalf("install call = %#v", runner.calls[0])
	}
	if strings.Contains(installArgs, "-p ssl") {
		t.Fatalf("macOS install used ineffective policy-scoped trust: %#v", runner.calls[0])
	}
	verifyArgs := strings.Join(runner.calls[1].args, " ")
	if runner.calls[1].name != "security" || !strings.Contains(verifyArgs, "verify-cert") || !strings.Contains(verifyArgs, "-n localhost") || !strings.Contains(verifyArgs, "-k "+wantKeychain) || !strings.Contains(verifyArgs, m.CertPath) {
		t.Fatalf("verification call = %#v", runner.calls[1])
	}
}

func TestDarwinRemovalClearsTrustRuleAndPublicCertificate(t *testing.T) {
	m, err := Ensure(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.WriteFile(m.MarkerPath, []byte("darwin\n"+m.Fingerprint+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	removed, err := (TrustStore{GOOS: "darwin", HomeDir: home, Runner: runner}).Remove(context.Background(), m)
	if err != nil || !removed {
		t.Fatalf("Remove = (%v, %v)", removed, err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %v, want trust-rule removal + certificate deletion", runner.calls)
	}
	if runner.calls[0].name != "security" || !slices.Equal(runner.calls[0].args, []string{"remove-trusted-cert", m.CACertPath}) {
		t.Fatalf("trust-rule removal = %#v", runner.calls[0])
	}
	wantKeychain := filepath.Join(home, "Library", "Keychains", "login.keychain-db")
	deleteArgs := strings.Join(runner.calls[1].args, " ")
	if runner.calls[1].name != "security" || !strings.Contains(deleteArgs, "delete-certificate -Z") || !strings.HasSuffix(deleteArgs, wantKeychain) {
		t.Fatalf("certificate deletion = %#v", runner.calls[1])
	}
	if _, err := os.Stat(m.MarkerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker remains after removal: %v", err)
	}
}

func TestRemovalRecoversWhenTrustMarkerWasLost(t *testing.T) {
	m, err := Ensure(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	removed, err := (TrustStore{GOOS: "darwin", HomeDir: t.TempDir(), Runner: runner}).Remove(context.Background(), m)
	if err != nil || !removed {
		t.Fatalf("Remove = (%v, %v)", removed, err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("calls = %v, want verification + trust-rule removal + certificate deletion", runner.calls)
	}
	if got := strings.Join(runner.calls[0].args, " "); !strings.Contains(got, "verify-cert") || !strings.Contains(got, m.CertPath) {
		t.Fatalf("markerless removal did not verify the exact local certificate: %#v", runner.calls[0])
	}
}

func TestMarkerlessRemovalDoesNotDeleteAnUnverifiedCertificate(t *testing.T) {
	m, err := Ensure(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	runner := &sequenceRunner{errs: []error{errors.New("not present")}}
	removed, err := (TrustStore{GOOS: "darwin", HomeDir: t.TempDir(), Runner: runner}).Remove(context.Background(), m)
	if err != nil || removed {
		t.Fatalf("Remove = (%v, %v)", removed, err)
	}
	if runner.calls != 1 {
		t.Fatalf("markerless removal ran %d commands, want verification only", runner.calls)
	}
}

func TestLegacyTrustMarkerIsReinstalledAndUpgraded(t *testing.T) {
	m, err := Ensure(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	legacy := "darwin\n" + m.Fingerprint + "\n"
	if err := os.WriteFile(m.MarkerPath, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	installed, err := (TrustStore{GOOS: "darwin", HomeDir: t.TempDir(), Runner: runner}).Install(context.Background(), m)
	if err != nil || !installed {
		t.Fatalf("Install = (%v, %v)", installed, err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("legacy marker skipped install: calls = %v", runner.calls)
	}
	b, err := os.ReadFile(m.MarkerPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), trustMarkerVersion+"\n") {
		t.Fatalf("marker was not upgraded: %q", b)
	}
}

func TestTrustedRequiresVerifiedMarkerForThisPlatformAndCA(t *testing.T) {
	m, err := Ensure(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	store := TrustStore{GOOS: "darwin", HomeDir: t.TempDir(), Runner: runner}
	tests := []struct {
		name   string
		marker string
		want   bool
	}{
		{name: "missing"},
		{name: "legacy", marker: "darwin\n" + m.Fingerprint + "\n"},
		{name: "malformed", marker: trustMarkerVersion + "\ndarwin\n"},
		{name: "wrong platform", marker: trustMarkerVersion + "\nlinux\n" + m.Fingerprint + "\n"},
		{name: "wrong CA", marker: trustMarkerVersion + "\ndarwin\nnot-the-fingerprint\n"},
		{name: "verified", marker: trustMarkerVersion + "\ndarwin\n" + m.Fingerprint + "\n", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runner.calls = nil
			if err := os.Remove(m.MarkerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if tc.marker != "" {
				if err := os.WriteFile(m.MarkerPath, []byte(tc.marker), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := store.Trusted(context.Background(), m)
			if err != nil || got != tc.want {
				t.Fatalf("Trusted = (%v, %v), want %v", got, err, tc.want)
			}
			if tc.want && len(runner.calls) != 1 {
				t.Fatalf("verified marker did not check the platform store: %#v", runner.calls)
			}
		})
	}
}

func TestStaleVerifiedMarkerReinstallsPlatformTrust(t *testing.T) {
	m, err := Ensure(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	marker := trustMarkerVersion + "\ndarwin\n" + m.Fingerprint + "\n"
	if err := os.WriteFile(m.MarkerPath, []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &sequenceRunner{errs: []error{errors.New("certificate missing"), nil, nil}}
	installed, err := (TrustStore{GOOS: "darwin", HomeDir: t.TempDir(), Runner: runner}).Install(context.Background(), m)
	if err != nil || !installed {
		t.Fatalf("stale marker reinstall = (%v, %v)", installed, err)
	}
	if runner.calls != 3 {
		t.Fatalf("stale marker ran %d calls, want check + install + verify", runner.calls)
	}
}

func TestTrustStoreRequiresRunnerOnlyWhenItMustChangeState(t *testing.T) {
	m, err := Ensure(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	store := TrustStore{GOOS: "darwin", HomeDir: t.TempDir()}
	if _, err := store.Install(context.Background(), m); err == nil || !strings.Contains(err.Error(), "runner") {
		t.Fatalf("Install error = %v", err)
	}
	removed, err := store.Remove(context.Background(), m)
	if err != nil || removed {
		t.Fatalf("unmarked Remove = (%v, %v)", removed, err)
	}
}

func TestFailedDarwinRemovalPreservesMarkerForRecovery(t *testing.T) {
	for _, tc := range []struct {
		name string
		errs []error
	}{
		{name: "trust rule", errs: []error{errors.New("denied")}},
		{name: "public certificate", errs: []error{nil, errors.New("keychain locked")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Ensure(t.TempDir(), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			marker := trustMarkerVersion + "\ndarwin\n" + m.Fingerprint + "\n"
			if err := os.WriteFile(m.MarkerPath, []byte(marker), 0600); err != nil {
				t.Fatal(err)
			}
			runner := &sequenceRunner{errs: tc.errs}
			removed, err := (TrustStore{GOOS: "darwin", HomeDir: t.TempDir(), Runner: runner}).Remove(context.Background(), m)
			if err == nil || removed {
				t.Fatalf("Remove = (%v, %v)", removed, err)
			}
			b, readErr := os.ReadFile(m.MarkerPath)
			if readErr != nil || string(b) != marker {
				t.Fatalf("recovery marker = %q, %v", b, readErr)
			}
		})
	}
}

func TestTrustCommandsRejectMissingHomeAndUnsupportedVerification(t *testing.T) {
	m, err := Ensure(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, goos := range []string{"darwin", "linux"} {
		if _, _, err := trustCommand(goos, "install", m, ""); err == nil || !strings.Contains(err.Error(), "home directory is empty") {
			t.Fatalf("%s install error = %v", goos, err)
		}
		if _, err := trustRemovalCommands(goos, m, ""); err == nil || !strings.Contains(err.Error(), "home directory is empty") {
			t.Fatalf("%s removal error = %v", goos, err)
		}
		if _, _, err := trustVerificationCommand(goos, m, ""); err == nil || !strings.Contains(err.Error(), "home directory is empty") {
			t.Fatalf("%s verification error = %v", goos, err)
		}
	}
	if _, _, err := trustVerificationCommand("plan9", m, t.TempDir()); err == nil || !strings.Contains(err.Error(), "not yet supported") {
		t.Fatalf("unsupported verification error = %v", err)
	}
}

func TestTrustStoreDoesNotMarkFailedInstall(t *testing.T) {
	m, err := Ensure(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{err: errors.New("denied")}
	_, err = (TrustStore{GOOS: "darwin", HomeDir: t.TempDir(), Runner: runner}).Install(context.Background(), m)
	if err == nil {
		t.Fatal("Install succeeded")
	}
	if _, statErr := os.Stat(m.MarkerPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("marker exists after failed install: %v", statErr)
	}
}

func TestTrustStoreDoesNotMarkFailedVerification(t *testing.T) {
	m, err := Ensure(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	runner := &sequenceRunner{errs: []error{nil, errors.New("not trusted")}}
	_, err = (TrustStore{GOOS: "darwin", HomeDir: t.TempDir(), Runner: runner}).Install(context.Background(), m)
	if err == nil || !strings.Contains(err.Error(), "verify Wingthing localhost certificate") {
		t.Fatalf("Install error = %v", err)
	}
	if _, statErr := os.Stat(m.MarkerPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("marker exists after failed verification: %v", statErr)
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
	if runner.calls != 3 {
		t.Fatalf("Linux install ran %d commands, want NSS initialize + public CA install + verification", runner.calls)
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
