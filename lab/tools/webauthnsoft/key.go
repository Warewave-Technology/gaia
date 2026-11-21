// Package webauthnsoft is a WebAuthn authenticator in software for lab fixtures (lab/tools/t26gate, lab/tools/softkey):
// an ES256 key, attestation "none", a signature counter, kept in a file when asked. Not part of the product.
package webauthnsoft

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
)

// Key is a security key for one relying party (origin, e.g. https://gaia-lab.test:8446).
type Key struct {
	key    *ecdsa.PrivateKey
	id     []byte
	count  uint32
	origin string
	rpID   string
	file   string // "" = not kept
}

type saved struct {
	PKCS8  []byte `json:"pkcs8"`
	ID     []byte `json:"id"`
	Count  uint32 `json:"count"`
	Origin string `json:"origin"`
}

// New makes a key for origin.
func New(origin string) *Key {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	id := make([]byte, 32)
	rand.Read(id)
	u, _ := url.Parse(origin)
	return &Key{key: k, id: id, origin: origin, rpID: u.Hostname()}
}

// Load reads a key kept by Save; a file that does not exist is (nil, nil).
func Load(file string) (*Key, error) {
	b, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s saved
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	pk, err := x509.ParsePKCS8PrivateKey(s.PKCS8)
	if err != nil {
		return nil, err
	}
	ek, ok := pk.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an ECDSA key", file)
	}
	u, _ := url.Parse(s.Origin)
	return &Key{key: ek, id: s.ID, count: s.Count, origin: s.Origin, rpID: u.Hostname(), file: file}, nil
}

// Save keeps the key (mode 0600); after each use the counter is written again.
func (k *Key) Save(file string) error {
	k.file = file
	return k.write()
}

func (k *Key) write() error {
	if k.file == "" {
		return nil
	}
	p, err := x509.MarshalPKCS8PrivateKey(k.key)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(saved{PKCS8: p, ID: k.id, Count: k.count, Origin: k.origin})
	return os.WriteFile(k.file, b, 0o600)
}

var b64 = base64.RawURLEncoding

func cborHead(major byte, n uint64) []byte {
	switch {
	case n < 24:
		return []byte{major<<5 | byte(n)}
	case n < 256:
		return []byte{major<<5 | 24, byte(n)}
	}
	b := []byte{major<<5 | 25, 0, 0}
	binary.BigEndian.PutUint16(b[1:], uint16(n))
	return b
}

func cborInt(v int) []byte {
	if v >= 0 {
		return cborHead(0, uint64(v))
	}
	return cborHead(1, uint64(-1-v))
}

func cborBytes(b []byte) []byte { return append(cborHead(2, uint64(len(b))), b...) }
func cborText(s string) []byte  { return append(cborHead(3, uint64(len(s))), s...) }

func (k *Key) authData(attested bool) []byte {
	h := sha256.Sum256([]byte(k.rpID))
	flags := byte(0x05) // user present, user verified
	if attested {
		flags |= 0x40
	}
	k.count++
	k.write()
	out := append(h[:], flags, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(out[33:], k.count)
	if attested {
		x, y := make([]byte, 32), make([]byte, 32)
		k.key.PublicKey.X.FillBytes(x)
		k.key.PublicKey.Y.FillBytes(y)
		cose := cborHead(5, 5)
		for _, kv := range [][2][]byte{{cborInt(1), cborInt(2)}, {cborInt(3), cborInt(-7)}, {cborInt(-1), cborInt(1)}, {cborInt(-2), cborBytes(x)}, {cborInt(-3), cborBytes(y)}} {
			cose = append(append(cose, kv[0]...), kv[1]...)
		}
		out = append(out, make([]byte, 16)...)
		out = binary.BigEndian.AppendUint16(out, uint16(len(k.id)))
		out = append(append(out, k.id...), cose...)
	}
	return out
}

func (k *Key) clientData(typ string, opts json.RawMessage) []byte {
	var o struct {
		PublicKey struct{ Challenge string } `json:"publicKey"`
	}
	json.Unmarshal(opts, &o)
	return []byte(fmt.Sprintf(`{"type":%q,"challenge":%q,"origin":%q,"crossOrigin":false}`, typ, o.PublicKey.Challenge, k.origin))
}

// Create answers registration options (a CredentialCreation) as a browser would: the PublicKeyCredential as JSON.
func (k *Key) Create(opts json.RawMessage) json.RawMessage {
	cd := k.clientData("webauthn.create", opts)
	att := cborHead(5, 3)
	att = append(append(att, cborText("fmt")...), cborText("none")...)
	att = append(append(att, cborText("attStmt")...), cborHead(5, 0)...)
	att = append(append(att, cborText("authData")...), cborBytes(k.authData(true))...)
	b, _ := json.Marshal(map[string]any{"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response":               map[string]any{"clientDataJSON": b64.EncodeToString(cd), "attestationObject": b64.EncodeToString(att), "transports": []string{"usb"}},
		"clientExtensionResults": map[string]any{}})
	return b
}

// Get answers assertion options (a CredentialAssertion).
func (k *Key) Get(opts json.RawMessage) json.RawMessage {
	cd := k.clientData("webauthn.get", opts)
	ad := k.authData(false)
	h := sha256.Sum256(cd)
	d := sha256.Sum256(append(append([]byte{}, ad...), h[:]...))
	sig, _ := ecdsa.SignASN1(rand.Reader, k.key, d[:])
	b, _ := json.Marshal(map[string]any{"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response":               map[string]any{"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(ad), "signature": b64.EncodeToString(sig)},
		"clientExtensionResults": map[string]any{}})
	return b
}
