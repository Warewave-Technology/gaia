package evidence

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
)

// GenerateKey writes a new ed25519 key pair: the private key as PKCS#8 PEM (mode 0600), the public key as PKIX PEM.
// It returns the key's key_id.
func GenerateKey(privPath, pubPath string) (keyID string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	if err := writeNew(privPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}), 0o600); err != nil {
		return "", err
	}
	if err := writeNew(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), 0o644); err != nil {
		return "", err
	}
	return KeyID(pub), nil
}

func writeNew(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// KeyID is the specification's key identifier: the lowercase hex sha256 of the 32-byte raw public key.
// (v0 called the same value the key fingerprint.)
func KeyID(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:])
}

// Fingerprint is KeyID, under its v0 name.
func Fingerprint(pub ed25519.PublicKey) string { return KeyID(pub) }

// LoadPrivateKey reads a PKCS#8 PEM ed25519 private key.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	der, err := readPEM(path, "PRIVATE KEY")
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an ed25519 key", path)
	}
	return priv, nil
}

// LoadPublicKey reads an ed25519 public key: PKIX PEM, or the raw 32 bytes in standard base64 (the form the
// specification uses for a trust anchor).
func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(b), "-----BEGIN") {
		raw, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%s: neither PEM nor a base64 32-byte ed25519 public key", path)
		}
		return ed25519.PublicKey(raw), nil
	}
	der, err := readPEM(path, "PUBLIC KEY")
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	pub, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an ed25519 key", path)
	}
	return pub, nil
}

func readPEM(path, typ string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != typ {
		return nil, fmt.Errorf("%s: no %s PEM block", path, typ)
	}
	return blk.Bytes, nil
}
