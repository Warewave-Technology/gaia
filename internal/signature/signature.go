// Package signature tells who signed a commit (design 7.6): OpenPGP and
// SSH signatures, checked against the keys people registered in Gaia. A
// commit nobody known signed is not applied.
package signature

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	pgperrors "github.com/ProtonMail/go-crypto/openpgp/errors"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"golang.org/x/crypto/ssh"
)

// Key is a registered public key and whose it is.
type Key struct {
	UserID, Username string
	Kind             string // ssh | gpg
	Fingerprint      string // SHA256:... for ssh, upper-case hex for gpg
	PublicKey        string
}

// Errors a caller tells apart.
var (
	ErrUnsigned = errors.New("the commit is not signed")
	ErrUnknown  = errors.New("the commit is signed with a key no one registered")
	ErrInvalid  = errors.New("the commit's signature is not valid")
)

// Keyring is the keys a commit may be signed with.
type Keyring struct {
	ssh map[string]Key // by fingerprint
	gpg openpgp.EntityList
	pgp map[string]Key // by primary key fingerprint
}

// NewKeyring reads registered keys; a key that does not parse is skipped.
func NewKeyring(keys []Key) *Keyring {
	k := &Keyring{ssh: map[string]Key{}, pgp: map[string]Key{}}
	for _, key := range keys {
		switch key.Kind {
		case "ssh":
			k.ssh[key.Fingerprint] = key
		case "gpg":
			ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(key.PublicKey))
			if err != nil || len(ring) != 1 {
				continue
			}
			k.gpg = append(k.gpg, ring[0])
			k.pgp[strings.ToUpper(fmt.Sprintf("%X", ring[0].PrimaryKey.Fingerprint))] = key
		}
	}
	return k
}

// Signer returns who signed c.
func (k *Keyring) Signer(c *object.Commit) (Key, error) {
	sig := strings.TrimSpace(c.PGPSignature)
	if sig == "" {
		return Key{}, ErrUnsigned
	}
	var payload plumbing.MemoryObject
	if err := c.EncodeWithoutSignature(&payload); err != nil {
		return Key{}, err
	}
	r, err := payload.Reader()
	if err != nil {
		return Key{}, err
	}
	msg, err := io.ReadAll(r)
	if err != nil {
		return Key{}, err
	}
	switch {
	case strings.HasPrefix(sig, "-----BEGIN SSH SIGNATURE-----"):
		pub, err := VerifySSH(msg, sig, "git")
		if err != nil {
			return Key{}, err
		}
		key, ok := k.ssh[ssh.FingerprintSHA256(pub)]
		if !ok {
			return Key{}, ErrUnknown
		}
		return key, nil
	case strings.HasPrefix(sig, "-----BEGIN PGP SIGNATURE-----"):
		e, err := openpgp.CheckArmoredDetachedSignature(k.gpg, bytes.NewReader(msg), strings.NewReader(sig), nil)
		if errors.Is(err, pgperrors.ErrUnknownIssuer) {
			return Key{}, ErrUnknown
		}
		if err != nil {
			return Key{}, ErrInvalid
		}
		key, ok := k.pgp[strings.ToUpper(fmt.Sprintf("%X", e.PrimaryKey.Fingerprint))]
		if !ok {
			return Key{}, ErrUnknown
		}
		return key, nil
	default:
		return Key{}, fmt.Errorf("%w: unknown format", ErrInvalid)
	}
}

// Signed is a commit and who signed it.
type Signed struct {
	Commit  string
	Signer  Key
	Problem error // nil when a registered key signed it
}

// maxRange bounds how many commits one change may bring.
const maxRange = 1000

// Range checks every commit reachable from to but not from from: what a
// change brings. With from empty only to itself is checked (the first
// time Gaia applies a repository). Merge commits are checked like any.
func Range(repo *git.Repository, from, to string, k *Keyring) ([]Signed, error) {
	head, err := repo.CommitObject(plumbing.NewHash(to))
	if err != nil {
		return nil, err
	}
	if from == "" {
		return []Signed{check(head, k)}, nil
	}
	if from == to {
		return nil, nil
	}
	seen := map[plumbing.Hash]bool{}
	base, err := repo.CommitObject(plumbing.NewHash(from))
	if err != nil {
		return nil, fmt.Errorf("the applied commit %s: %w", short(from), err)
	}
	if err := walk(base, func(c *object.Commit) bool { seen[c.Hash] = true; return true }); err != nil {
		return nil, err
	}
	var out []Signed
	err = walk(head, func(c *object.Commit) bool {
		if seen[c.Hash] {
			return false
		}
		out = append(out, check(c, k))
		return len(out) <= maxRange
	})
	if err != nil {
		return nil, err
	}
	if len(out) > maxRange {
		return nil, fmt.Errorf("more than %d commits since %s", maxRange, short(from))
	}
	return out, nil
}

func check(c *object.Commit, k *Keyring) Signed {
	key, err := k.Signer(c)
	return Signed{Commit: c.Hash.String(), Signer: key, Problem: err}
}

// walk visits c and its ancestors once each; visit returns false to not
// go further back from a commit.
func walk(c *object.Commit, visit func(*object.Commit) bool) error {
	done := map[plumbing.Hash]bool{}
	stack := []*object.Commit{c}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if done[cur.Hash] {
			continue
		}
		done[cur.Hash] = true
		if !visit(cur) {
			continue
		}
		err := cur.Parents().ForEach(func(p *object.Commit) error {
			stack = append(stack, p)
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func short(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// SSHSIG (OpenSSH PROTOCOL.sshsig): what signs and verifies git's SSH
// signatures.
const (
	sshsigMagic   = "SSHSIG"
	sshsigVersion = 1
)

type sshsigBlob struct {
	PublicKey     []byte
	Namespace     string
	Reserved      string
	HashAlgorithm string
	Signature     []byte
}

func sshsigSignedData(namespace, hashAlg string, msg []byte) ([]byte, error) {
	var h hash.Hash
	switch hashAlg {
	case "sha512":
		h = sha512.New()
	case "sha256":
		h = sha256.New()
	default:
		return nil, fmt.Errorf("%w: hash %s", ErrInvalid, hashAlg)
	}
	h.Write(msg)
	data := []byte(sshsigMagic)
	data = append(data, ssh.Marshal(struct {
		Namespace, Reserved, HashAlgorithm string
		Hash                               []byte
	}{namespace, "", hashAlg, h.Sum(nil)})...)
	return data, nil
}

// VerifySSH checks an armored SSH signature of msg in namespace and returns
// the key that made it.
func VerifySSH(msg []byte, armored, namespace string) (ssh.PublicKey, error) {
	body := strings.TrimSpace(armored)
	body = strings.TrimPrefix(body, "-----BEGIN SSH SIGNATURE-----")
	body = strings.TrimSuffix(body, "-----END SSH SIGNATURE-----")
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(body), ""))
	if err != nil || len(raw) < len(sshsigMagic)+4 || string(raw[:len(sshsigMagic)]) != sshsigMagic {
		return nil, fmt.Errorf("%w: not an SSH signature", ErrInvalid)
	}
	if v := binary.BigEndian.Uint32(raw[len(sshsigMagic):]); v != sshsigVersion {
		return nil, fmt.Errorf("%w: version %d", ErrInvalid, v)
	}
	var blob sshsigBlob
	if err := ssh.Unmarshal(raw[len(sshsigMagic)+4:], &blob); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if blob.Namespace != namespace {
		return nil, fmt.Errorf("%w: namespace %q, want %q", ErrInvalid, blob.Namespace, namespace)
	}
	pub, err := ssh.ParsePublicKey(blob.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var sig ssh.Signature
	if err := ssh.Unmarshal(blob.Signature, &sig); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	data, err := sshsigSignedData(blob.Namespace, blob.HashAlgorithm, msg)
	if err != nil {
		return nil, err
	}
	if err := pub.Verify(data, &sig); err != nil {
		return nil, ErrInvalid
	}
	return pub, nil
}

// SSHSigner signs commits with an SSH key, as git does with
// gpg.format=ssh. Gaia signs its own commits this way.
type SSHSigner struct{ Key ssh.Signer }

// Sign returns the armored SSH signature of message in the git namespace.
func (s SSHSigner) Sign(message io.Reader) ([]byte, error) {
	msg, err := io.ReadAll(message)
	if err != nil {
		return nil, err
	}
	data, err := sshsigSignedData("git", "sha512", msg)
	if err != nil {
		return nil, err
	}
	var sig *ssh.Signature
	if as, ok := s.Key.(ssh.AlgorithmSigner); ok && s.Key.PublicKey().Type() == ssh.KeyAlgoRSA {
		// RSA keys sign with SHA-512, as ssh-keygen does.
		sig, err = as.SignWithAlgorithm(rand.Reader, data, ssh.KeyAlgoRSASHA512)
	} else {
		sig, err = s.Key.Sign(rand.Reader, data)
	}
	if err != nil {
		return nil, err
	}
	blob := append([]byte(sshsigMagic), binary.BigEndian.AppendUint32(nil, sshsigVersion)...)
	blob = append(blob, ssh.Marshal(sshsigBlob{
		PublicKey: s.Key.PublicKey().Marshal(), Namespace: "git", HashAlgorithm: "sha512", Signature: ssh.Marshal(sig),
	})...)
	enc := base64.StdEncoding.EncodeToString(blob)
	var out strings.Builder
	out.WriteString("-----BEGIN SSH SIGNATURE-----\n")
	for len(enc) > 70 {
		out.WriteString(enc[:70] + "\n")
		enc = enc[70:]
	}
	out.WriteString(enc + "\n-----END SSH SIGNATURE-----\n")
	return []byte(out.String()), nil
}
