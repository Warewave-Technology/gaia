package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/recoverykit"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// InitOptions set up a new installation.
type InitOptions struct {
	ConfigPath    string   // e.g. /etc/gaia/server.json; must not exist
	DataDir       string   // e.g. /var/lib/gaia; must not hold a database
	Listen        string   // default :8443
	PublicURL     string   // https://NAME[:PORT] browsers use (WebAuthn needs a name, not an address); default from the host name
	Hostnames     []string // names and addresses clients use; the host name, localhost and loopback are added
	AdminUser     string
	AdminPassword string
	KitPassphrase string // encrypts the recovery kit (ADR-007 B5)
}

// InitResult is what the operator must write down.
type InitResult struct {
	Config        Config   `json:"config"`
	CertSHA256    string   `json:"tls_certificate_sha256"` // clients pin it at first login
	CertNames     []string `json:"tls_certificate_names"`
	EvidenceKeyID string   `json:"evidence_key_id"` // the evidence trust anchor (ADR-006, spec §3)
	Admin         string   `json:"admin"`
	Kit           string   `json:"recovery_kit"` // the file; it must be stored off the server
	KitGeneration int64    `json:"recovery_kit_generation"`
}

// Init creates the data directory, the TLS certificate, the evidence signing key, the database and the first
// admin, then writes the config. It refuses to touch an existing installation.
func Init(ctx context.Context, o InitOptions) (InitResult, error) {
	cfg := Config{Listen: o.Listen, DataDir: filepath.Clean(o.DataDir), PublicURL: o.PublicURL}
	if err := cfg.resolve(); err != nil {
		return InitResult{}, err
	}
	if _, err := os.Stat(o.ConfigPath); err == nil {
		return InitResult{}, fmt.Errorf("%s exists: this host is already set up", o.ConfigPath)
	}
	if err := recoverykit.CheckPassphrase(o.KitPassphrase); err != nil {
		return InitResult{}, err
	}
	if _, err := os.Stat(cfg.dbPath()); err == nil {
		return InitResult{}, fmt.Errorf("%s exists: this data directory is already in use", cfg.dbPath())
	}
	for dir, mode := range map[string]os.FileMode{cfg.DataDir: 0o750, filepath.Dir(cfg.TLSCert): 0o700, filepath.Dir(cfg.keyPath()): 0o700,
		cfg.bundlesDir(): 0o750, cfg.secretsDir(): 0o700, cfg.kopiaDir(): 0o700, cfg.cacheDir(): 0o700, cfg.tmpDir(): 0o700} {
		if err := os.MkdirAll(dir, mode); err != nil {
			return InitResult{}, err
		}
		if err := os.Chmod(dir, mode); err != nil {
			return InitResult{}, err
		}
	}
	names := certNames(append(o.Hostnames, cfg.rpID())) // the public URL's name is in the certificate
	fp, err := generateCert(cfg.TLSCert, cfg.TLSKey, names)
	if err != nil {
		return InitResult{}, fmt.Errorf("TLS certificate: %w", err)
	}
	keyID, err := evidence.GenerateKey(cfg.keyPath(), cfg.pubPath())
	if err != nil {
		return InitResult{}, fmt.Errorf("evidence key: %w", err)
	}
	if _, err := loadSSHKey(cfg); err != nil {
		return InitResult{}, fmt.Errorf("SSH key: %w", err)
	}
	db, err := store.Open(cfg.dbPath())
	if err != nil {
		return InitResult{}, err
	}
	defer db.Close()
	if _, err := db.Migrate(ctx, 0); err != nil {
		return InitResult{}, err
	}
	db.SetInstance(instanceName())
	// The first admin is break-glass: the way in when single sign-on is on and the IdP is down (ADR-008 §1). It enrols
	// its security key at the first sign-in.
	if _, err := db.CreateUser(ctx, "gaia server init", store.NewUser{Username: o.AdminUser, Role: store.RoleAdmin, Password: o.AdminPassword,
		BreakGlass: true}); err != nil {
		return InitResult{}, fmt.Errorf("admin user: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(o.ConfigPath), 0o750); err != nil {
		return InitResult{}, err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return InitResult{}, err
	}
	if err := writeExclusive(o.ConfigPath, append(b, '\n'), 0o640); err != nil {
		return InitResult{}, err
	}
	kit, err := writeKit(ctx, cfg, o.ConfigPath, db, "gaia server init", "server init", o.KitPassphrase, buildVersion())
	if err != nil {
		return InitResult{}, fmt.Errorf("recovery kit: %w", err)
	}
	return InitResult{Config: cfg, CertSHA256: fp, CertNames: names, EvidenceKeyID: keyID, Admin: o.AdminUser, Kit: cfg.kitPath(),
		KitGeneration: kit.Generation}, nil
}

func certNames(extra []string) []string {
	names := []string{"localhost", "127.0.0.1", "::1"}
	if h, err := os.Hostname(); err == nil && h != "" {
		names = append(names, h)
	}
	for _, n := range extra {
		if n = strings.TrimSpace(n); n != "" && !contains(names, n) {
			names = append(names, n)
		}
	}
	return names
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// generateCert writes a self-signed ECDSA P-256 server certificate and key; it returns the certificate's sha256.
func generateCert(certPath, keyPath string, names []string) (string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: names[len(names)-1], Organization: []string{"Gaia"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(0, 0, 825),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", err
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", err
	}
	if err := writeExclusive(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		return "", err
	}
	if err := writeExclusive(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return "", err
	}
	return CertFingerprint(der), nil
}

// CertFingerprint is the sha256 of a certificate's DER bytes, as colon-separated hex pairs.
func CertFingerprint(der []byte) string {
	h := sha256.Sum256(der)
	s := strings.ToUpper(hex.EncodeToString(h[:]))
	var parts []string
	for i := 0; i < len(s); i += 2 {
		parts = append(parts, s[i:i+2])
	}
	return strings.Join(parts, ":")
}

// CertFileFingerprint reads a PEM certificate and returns its fingerprint.
func CertFileFingerprint(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "CERTIFICATE" {
		return "", errors.New(path + ": no certificate")
	}
	return CertFingerprint(blk.Bytes), nil
}
