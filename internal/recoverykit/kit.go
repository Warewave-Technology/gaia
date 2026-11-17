// Package recoverykit is Gaia's recovery kit (ADR-007 B5): the secrets that Gaia's state backup leaves out on
// purpose (repository passwords, the evidence signing key, the SSH key hosts trust, the TLS key), encrypted with a
// passphrase the operator chooses, in the age format with an scrypt recipient. The plaintext is a JSON document,
// so the standalone age tool opens a kit without Gaia:
//
//	age -d gaia-recovery-kit.age
package recoverykit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
	"filippo.io/age/armor"
)

// Format names this version of the kit's plaintext.
const Format = "gaia-recovery-kit/1"

// MinPassphrase is the shortest passphrase a kit accepts.
const MinPassphrase = 12

// ErrPassphrase means the passphrase does not open the kit.
var ErrPassphrase = errors.New("the passphrase does not open this recovery kit")

// workFactor is scrypt's log2(N); age's default (18) takes about a second and 256 MiB.
var workFactor = 18

// SetWorkFactor sets scrypt's log2(N) for kits sealed from now on. Only tests lower it.
func SetWorkFactor(logN int) { workFactor = logN }

// Kit is the plaintext of a recovery kit. PEM and OpenSSH keys are the files' exact contents.
type Kit struct {
	Format          string          `json:"format"`
	Instructions    string          `json:"instructions"`
	Generation      int64           `json:"generation"` // 1 at gaia server init, +1 for each new kit
	CreatedAt       string          `json:"created_at"` // RFC 3339, UTC
	Reason          string          `json:"reason"`
	Hostname        string          `json:"hostname"`
	GaiaVersion     string          `json:"gaia_version"`
	EvidenceKeyID   string          `json:"evidence_key_id"`
	EvidenceKey     string          `json:"evidence_private_key"`
	EvidencePub     string          `json:"evidence_public_key"`
	SSHKey          string          `json:"ssh_private_key"`
	SSHPub          string          `json:"ssh_public_key"`
	TLSCert         string          `json:"tls_certificate"`
	TLSKey          string          `json:"tls_private_key"`
	ServerConfig    json.RawMessage `json:"server_config"`    // /etc/gaia/server.json when the kit was made
	StateRepository string          `json:"state_repository"` // the repository Gaia's state backups go to ("" = none yet)
	Repositories    []Repository    `json:"repositories"`
}

// Repository is what it takes to open one repository.
type Repository struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Location string `json:"location"`
	Password string `json:"password"`
	S3       *S3    `json:"s3,omitempty"` // kind s3: how to reach the bucket
}

// S3 is what opens an S3 bucket besides its location: the access key and, when the endpoint's TLS certificate is not
// signed by a CA the system trusts, the CA certificates to check it against (PEM).
type S3 struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	CAPEM           string `json:"ca_pem,omitempty"`
}

// Instructions is the text every kit carries for the person who opens it.
const Instructions = "This is a Gaia recovery kit. It holds secrets: keep it encrypted and off the Gaia server. " +
	"To rebuild a lost server, install Gaia on a new machine (docs/install.md) and run, as root: " +
	"runuser -u gaia -- gaia server restore-state -kit <this file>. " +
	"Each repository's password opens that Kopia repository on its own; the evidence key signs Gaia's evidence."

// CheckPassphrase reports whether p is acceptable for a new kit.
func CheckPassphrase(p string) error {
	switch {
	case len([]rune(p)) < MinPassphrase:
		return fmt.Errorf("a recovery kit passphrase has at least %d characters", MinPassphrase)
	case strings.TrimSpace(p) != p:
		return errors.New("a recovery kit passphrase does not start or end with white space")
	}
	return nil
}

func (k *Kit) check() error {
	switch {
	case k.Format != Format:
		return fmt.Errorf("recovery kit format %q (this Gaia reads %s)", k.Format, Format)
	case k.Generation < 1:
		return fmt.Errorf("recovery kit generation %d", k.Generation)
	case k.EvidenceKey == "" || k.EvidencePub == "" || k.EvidenceKeyID == "":
		return errors.New("the recovery kit has no evidence key")
	case k.SSHKey == "":
		return errors.New("the recovery kit has no SSH key")
	}
	names := map[string]bool{}
	for _, r := range k.Repositories {
		if r.Name == "" || r.Location == "" || r.Password == "" {
			return fmt.Errorf("recovery kit repository %q is incomplete", r.Name)
		}
		if names[r.Name] {
			return fmt.Errorf("recovery kit repository %q appears twice", r.Name)
		}
		names[r.Name] = true
	}
	if k.StateRepository != "" && !names[k.StateRepository] {
		return fmt.Errorf("the recovery kit's state repository %q is not among its repositories", k.StateRepository)
	}
	return nil
}

// Repository returns the kit's repository of that name.
func (k *Kit) Repository(name string) (Repository, bool) {
	for _, r := range k.Repositories {
		if r.Name == name {
			return r, true
		}
	}
	return Repository{}, false
}

// Seal encrypts the kit with the passphrase: ASCII-armoured age, scrypt recipient.
func Seal(k Kit, passphrase string) ([]byte, error) {
	k.Format, k.Instructions = Format, Instructions
	if err := k.check(); err != nil {
		return nil, err
	}
	if err := CheckPassphrase(passphrase); err != nil {
		return nil, err
	}
	plain, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return nil, err
	}
	r, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, err
	}
	r.SetWorkFactor(workFactor)
	var buf bytes.Buffer
	aw := armor.NewWriter(&buf)
	w, err := age.Encrypt(aw, r)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(append(plain, '\n')); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if err := aw.Close(); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// Open decrypts and checks a kit (armoured or binary age).
func Open(data []byte, passphrase string) (Kit, error) {
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return Kit{}, err
	}
	var src io.Reader = bytes.NewReader(data)
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte(armor.Header)) {
		src = armor.NewReader(bytes.NewReader(bytes.TrimSpace(data)))
	}
	r, err := age.Decrypt(src, id)
	if err != nil {
		var nm *age.NoIdentityMatchError
		if errors.As(err, &nm) {
			return Kit{}, ErrPassphrase
		}
		return Kit{}, fmt.Errorf("not a readable age file: %w", err)
	}
	plain, err := io.ReadAll(io.LimitReader(r, 16<<20))
	if err != nil {
		return Kit{}, fmt.Errorf("decrypting the recovery kit: %w", err)
	}
	var k Kit
	if err := json.Unmarshal(plain, &k); err != nil {
		return Kit{}, fmt.Errorf("the recovery kit's content: %w", err)
	}
	if err := k.check(); err != nil {
		return Kit{}, err
	}
	return k, nil
}

// SHA256 is the hex sha256 of a kit file's bytes: how the server and a copy elsewhere are compared.
func SHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
