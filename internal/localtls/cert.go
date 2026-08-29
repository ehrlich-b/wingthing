// Package localtls manages the deliberately small certificate authority used
// by Wingthing's local HTTPS listener. The authority is constrained to
// localhost and loopback IP addresses and its private key never leaves the
// local Wingthing configuration directory.
package localtls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/ehrlich-b/wingthing/internal/fsutil"
)

const (
	directoryName    = "local-tls"
	caCertName       = "ca.pem"
	caKeyName        = "ca-key.pem"
	leafCertName     = "localhost.pem"
	leafKeyName      = "localhost-key.pem"
	markerName       = "trusted"
	materialLockName = ".material.lock"
)

// ErrNotFound means WT has not created local TLS material in this profile.
var ErrNotFound = errors.New("local HTTPS certificate has not been created")

// Material is the localhost CA and server certificate stored on this box.
// Only CACertPath is ever handed to an operating-system trust command.
type Material struct {
	Dir         string
	CACertPath  string
	CAKeyPath   string
	CertPath    string
	KeyPath     string
	MarkerPath  string
	CACert      *x509.Certificate
	Cert        *x509.Certificate
	Fingerprint string
	CreatedCA   bool
	CreatedLeaf bool
}

// Ensure loads or creates a localhost-constrained CA and server certificate.
// It never replaces an existing CA: a corrupt or incomplete CA is reported so
// WT cannot silently orphan a root that may already be trusted.
func Ensure(configDir string, now time.Time) (*Material, error) {
	m, err := materialPaths(configDir)
	if err != nil {
		return nil, err
	}
	if err := ensurePrivateDir(m.Dir); err != nil {
		return nil, err
	}
	lock, err := acquireMaterialLock(m.Dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()

	caCert, caKey, created, err := ensureCA(m, now)
	if err != nil {
		return nil, err
	}
	m.CACert = caCert
	m.CreatedCA = created
	m.Fingerprint = certificateFingerprint(caCert)

	leaf, created, err := ensureLeaf(m, caCert, caKey, now)
	if err != nil {
		return nil, err
	}
	m.Cert = leaf
	m.CreatedLeaf = created
	return m, nil
}

func acquireMaterialLock(dir string) (*os.File, error) {
	path := filepath.Join(dir, materialLockName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open local TLS material lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("protect local TLS material lock: %w", err)
	}
	if err := lockMaterialFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock local TLS material: %w", err)
	}
	return file, nil
}

// Load returns the existing CA without creating or rotating any files. This is
// used by status and removal commands, which must not create a new authority as
// a side effect.
func Load(configDir string) (*Material, error) {
	m, err := materialPaths(configDir)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(m.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("inspect local TLS directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("local TLS path %s is not a real directory", m.Dir)
	}
	exists, err := regularFileExists(m.CACertPath)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	cert, err := readCertificate(m.CACertPath)
	if err != nil {
		return nil, fmt.Errorf("read existing local CA certificate: %w", err)
	}
	if err := validateLocalCA(cert); err != nil {
		return nil, fmt.Errorf("existing local CA certificate: %w", err)
	}
	m.CACert = cert
	m.Fingerprint = certificateFingerprint(cert)
	return m, nil
}

func materialPaths(configDir string) (*Material, error) {
	dir, err := filepath.Abs(filepath.Join(configDir, directoryName))
	if err != nil {
		return nil, fmt.Errorf("resolve local TLS directory: %w", err)
	}
	return &Material{
		Dir:        dir,
		CACertPath: filepath.Join(dir, caCertName),
		CAKeyPath:  filepath.Join(dir, caKeyName),
		CertPath:   filepath.Join(dir, leafCertName),
		KeyPath:    filepath.Join(dir, leafKeyName),
		MarkerPath: filepath.Join(dir, markerName),
	}, nil
}

func ensurePrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create local TLS directory: %w", err)
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return fmt.Errorf("inspect local TLS directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("local TLS path %s is not a real directory", dir)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("secure local TLS directory: %w", err)
	}
	return nil
}

func ensureCA(m *Material, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, bool, error) {
	certExists, err := regularFileExists(m.CACertPath)
	if err != nil {
		return nil, nil, false, err
	}
	keyExists, err := regularFileExists(m.CAKeyPath)
	if err != nil {
		return nil, nil, false, err
	}
	if certExists != keyExists {
		return nil, nil, false, fmt.Errorf("local CA is incomplete in %s; restore or remove the directory explicitly", m.Dir)
	}
	if certExists {
		cert, err := readCertificate(m.CACertPath)
		if err != nil {
			return nil, nil, false, fmt.Errorf("read existing local CA certificate: %w", err)
		}
		key, err := readECPrivateKey(m.CAKeyPath)
		if err != nil {
			return nil, nil, false, fmt.Errorf("read existing local CA private key: %w", err)
		}
		if validateLocalCA(cert) != nil || !publicKeysEqual(cert.PublicKey, &key.PublicKey) {
			return nil, nil, false, fmt.Errorf("existing local CA certificate and private key do not form a valid self-signed CA")
		}
		if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
			return nil, nil, false, fmt.Errorf("existing local CA is not valid at %s; remove it explicitly before creating a replacement", now.Format(time.RFC3339))
		}
		if err := os.Chmod(m.CAKeyPath, 0600); err != nil {
			return nil, nil, false, fmt.Errorf("secure local CA private key: %w", err)
		}
		return cert, key, false, nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, false, fmt.Errorf("generate local CA private key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, false, err
	}
	loopback4 := &net.IPNet{IP: net.ParseIP("127.0.0.0").To4(), Mask: net.CIDRMask(8, 32)}
	loopback6 := &net.IPNet{IP: net.ParseIP("::1"), Mask: net.CIDRMask(128, 128)}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Wingthing Localhost CA",
			Organization: []string{"Wingthing local HTTPS (this device only)"},
		},
		NotBefore:                   now.Add(-5 * time.Minute),
		NotAfter:                    now.AddDate(10, 0, 0),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLen:                  0,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{"localhost"},
		PermittedIPRanges:           []*net.IPNet{loopback4, loopback6},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, false, fmt.Errorf("create local CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, false, fmt.Errorf("parse generated local CA certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, false, fmt.Errorf("encode local CA private key: %w", err)
	}
	if err := writePEMAtomic(m.CAKeyPath, 0600, "PRIVATE KEY", keyDER); err != nil {
		return nil, nil, false, fmt.Errorf("write local CA private key: %w", err)
	}
	if err := writePEMAtomic(m.CACertPath, 0644, "CERTIFICATE", der); err != nil {
		return nil, nil, false, fmt.Errorf("write local CA certificate: %w", err)
	}
	return cert, key, true, nil
}

func validateLocalCA(cert *x509.Certificate) error {
	if cert == nil || !cert.IsCA || !cert.BasicConstraintsValid || cert.CheckSignatureFrom(cert) != nil {
		return fmt.Errorf("is not a valid self-signed CA")
	}
	if !cert.MaxPathLenZero || cert.MaxPathLen != 0 {
		return fmt.Errorf("does not prohibit subordinate certificate authorities")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return fmt.Errorf("cannot sign localhost certificates")
	}
	if !cert.PermittedDNSDomainsCritical || len(cert.PermittedDNSDomains) != 1 || cert.PermittedDNSDomains[0] != "localhost" {
		return fmt.Errorf("is not constrained to the localhost DNS name")
	}
	wantRanges := map[string]bool{"127.0.0.0/8": true, "::1/128": true}
	if len(cert.PermittedIPRanges) != len(wantRanges) {
		return fmt.Errorf("is not constrained to loopback IP addresses")
	}
	for _, network := range cert.PermittedIPRanges {
		if network == nil || !wantRanges[network.String()] {
			return fmt.Errorf("is not constrained to loopback IP addresses")
		}
	}
	return nil
}

func ensureLeaf(m *Material, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, now time.Time) (*x509.Certificate, bool, error) {
	certExists, certErr := regularFileExists(m.CertPath)
	keyExists, keyErr := regularFileExists(m.KeyPath)
	if certErr != nil || keyErr != nil {
		return nil, false, errors.Join(certErr, keyErr)
	}
	if certExists && keyExists {
		cert, certErr := readCertificate(m.CertPath)
		key, keyErr := readECPrivateKey(m.KeyPath)
		if certErr == nil && keyErr == nil && publicKeysEqual(cert.PublicKey, &key.PublicKey) && leafValid(cert, caCert, now) {
			if err := os.Chmod(m.KeyPath, 0600); err != nil {
				return nil, false, fmt.Errorf("secure localhost private key: %w", err)
			}
			return cert, false, nil
		}
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, false, fmt.Errorf("generate localhost private key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, false, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "localhost",
			Organization: []string{"Wingthing local HTTPS"},
		},
		NotBefore:   now.Add(-5 * time.Minute),
		NotAfter:    now.AddDate(1, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, false, fmt.Errorf("create localhost certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, false, fmt.Errorf("parse generated localhost certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, false, fmt.Errorf("encode localhost private key: %w", err)
	}
	if err := writePEMAtomic(m.KeyPath, 0600, "PRIVATE KEY", keyDER); err != nil {
		return nil, false, fmt.Errorf("write localhost private key: %w", err)
	}
	if err := writePEMAtomic(m.CertPath, 0644, "CERTIFICATE", der); err != nil {
		return nil, false, fmt.Errorf("write localhost certificate: %w", err)
	}
	return cert, true, nil
}

func leafValid(cert, caCert *x509.Certificate, now time.Time) bool {
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	_, err := cert.Verify(x509.VerifyOptions{
		DNSName:     "localhost",
		Roots:       roots,
		CurrentTime: now,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	return err == nil && cert.NotAfter.After(now.Add(30*24*time.Hour))
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("refusing non-regular local TLS file %s", path)
	}
	return true, nil
}

func readCertificate(path string) (*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("invalid certificate PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

func readECPrivateKey(path string) (*ecdsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(b)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("invalid private key PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is %T, not ECDSA", key)
	}
	return ec, nil
}

func publicKeysEqual(a any, b *ecdsa.PublicKey) bool {
	pub, ok := a.(*ecdsa.PublicKey)
	return ok && pub.Equal(b)
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	return serial, nil
}

func writePEMAtomic(path string, mode os.FileMode, blockType string, der []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := pem.Encode(tmp, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return fsutil.SyncDirectory(dir)
}

func writeFileAtomic(path string, mode os.FileMode, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return fsutil.SyncDirectory(dir)
}

func certificateFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}
