package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
)

// softKey is a WebAuthn authenticator in software, for tests: an ES256 key, attestation "none", a signature counter.
// It answers the server's options as a browser would pass them to navigator.credentials and back.
type softKey struct {
	key    *ecdsa.PrivateKey
	id     []byte
	count  uint32
	origin string
	rpID   string
}

func newSoftKey(t *testing.T, publicURL string) *softKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	rand.Read(id)
	u, _ := url.Parse(publicURL)
	return &softKey{key: k, id: id, origin: publicURL, rpID: u.Hostname()}
}

var b64 = base64.RawURLEncoding

// A minimal CBOR encoder (RFC 8949): what an attestation object and a COSE key need.
func cborHead(major byte, n uint64) []byte {
	switch {
	case n < 24:
		return []byte{major<<5 | byte(n)}
	case n < 256:
		return []byte{major<<5 | 24, byte(n)}
	default:
		b := []byte{major<<5 | 25, 0, 0}
		binary.BigEndian.PutUint16(b[1:], uint16(n))
		return b
	}
}

func cborInt(v int) []byte {
	if v >= 0 {
		return cborHead(0, uint64(v))
	}
	return cborHead(1, uint64(-1-v))
}

func cborBytes(b []byte) []byte { return append(cborHead(2, uint64(len(b))), b...) }
func cborText(s string) []byte  { return append(cborHead(3, uint64(len(s))), s...) }

func (k *softKey) coseKey() []byte {
	x, y := make([]byte, 32), make([]byte, 32)
	k.key.PublicKey.X.FillBytes(x)
	k.key.PublicKey.Y.FillBytes(y)
	out := cborHead(5, 5)
	for _, kv := range [][2][]byte{{cborInt(1), cborInt(2)}, {cborInt(3), cborInt(-7)}, {cborInt(-1), cborInt(1)}, {cborInt(-2), cborBytes(x)}, {cborInt(-3), cborBytes(y)}} {
		out = append(append(out, kv[0]...), kv[1]...)
	}
	return out
}

func (k *softKey) authData(attested bool) []byte {
	h := sha256.Sum256([]byte(k.rpID))
	flags := byte(0x01 | 0x04) // user present, user verified
	if attested {
		flags |= 0x40
	}
	k.count++
	out := append(h[:], flags, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(out[33:], k.count)
	if attested {
		out = append(out, make([]byte, 16)...) // AAGUID
		out = binary.BigEndian.AppendUint16(out, uint16(len(k.id)))
		out = append(out, k.id...)
		out = append(out, k.coseKey()...)
	}
	return out
}

// challenge reads publicKey.challenge from the server's options (CredentialCreation or CredentialAssertion).
func challengeOf(t *testing.T, opts any) string {
	t.Helper()
	b, _ := json.Marshal(opts)
	var o struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(b, &o); err != nil || o.PublicKey.Challenge == "" {
		t.Fatalf("no challenge in %s", b)
	}
	return o.PublicKey.Challenge
}

func (k *softKey) clientData(typ, challenge string) []byte {
	return []byte(fmt.Sprintf(`{"type":%q,"challenge":%q,"origin":%q,"crossOrigin":false}`, typ, challenge, k.origin))
}

// create answers registration options: the PublicKeyCredential as JSON.
func (k *softKey) create(t *testing.T, opts any) json.RawMessage {
	t.Helper()
	cd := k.clientData("webauthn.create", challengeOf(t, opts))
	att := cborHead(5, 3)
	att = append(append(att, cborText("fmt")...), cborText("none")...)
	att = append(append(att, cborText("attStmt")...), cborHead(5, 0)...)
	att = append(append(att, cborText("authData")...), cborBytes(k.authData(true))...)
	b, _ := json.Marshal(map[string]any{"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response":               map[string]any{"clientDataJSON": b64.EncodeToString(cd), "attestationObject": b64.EncodeToString(att), "transports": []string{"usb"}},
		"clientExtensionResults": map[string]any{}})
	return b
}

// get answers assertion options.
func (k *softKey) get(t *testing.T, opts any) json.RawMessage {
	t.Helper()
	cd := k.clientData("webauthn.get", challengeOf(t, opts))
	ad := k.authData(false)
	h := sha256.Sum256(cd)
	d := sha256.Sum256(append(append([]byte{}, ad...), h[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, k.key, d[:])
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response":               map[string]any{"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(ad), "signature": b64.EncodeToString(sig)},
		"clientExtensionResults": map[string]any{}})
	return b
}
