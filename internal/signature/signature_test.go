package signature

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"golang.org/x/crypto/ssh"
)

type repoT struct {
	t   *testing.T
	dir string
	r   *git.Repository
	n   int
}

func newRepo(t *testing.T) *repoT {
	dir := t.TempDir()
	r, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	return &repoT{t: t, dir: dir, r: r}
}

// commit writes a file and commits it, signed with signer or key when set.
func (x *repoT) commit(signer git.Signer, key *openpgp.Entity) string {
	x.t.Helper()
	x.n++
	if err := os.WriteFile(filepath.Join(x.dir, "f.txt"), []byte(strings.Repeat("x", x.n)), 0o644); err != nil {
		x.t.Fatal(err)
	}
	wt, _ := x.r.Worktree()
	if _, err := wt.Add("f.txt"); err != nil {
		x.t.Fatal(err)
	}
	h, err := wt.Commit("c", &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@x", When: time.Unix(1700000000+int64(x.n), 0)},
		Signer: signer, SignKey: key,
	})
	if err != nil {
		x.t.Fatal(err)
	}
	return h.String()
}

func (x *repoT) obj(h string) *object.Commit {
	c, err := x.r.CommitObject(plumbing.NewHash(h))
	if err != nil {
		x.t.Fatal(err)
	}
	return c
}

func sshKey(t *testing.T) (ssh.Signer, Key) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s, Key{UserID: "usr-1", Username: "ayse", Kind: "ssh", Fingerprint: ssh.FingerprintSHA256(s.PublicKey()),
		PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey())))}
}

func TestSSHSignedCommits(t *testing.T) {
	x := newRepo(t)
	s, key := sshKey(t)
	h := x.commit(SSHSigner{Key: s}, nil)
	got, err := NewKeyring([]Key{key}).Signer(x.obj(h))
	if err != nil || got.Username != "ayse" {
		t.Fatalf("signer = %+v, %v", got, err)
	}
	other, _ := sshKey(t)
	if _, err := NewKeyring(nil).Signer(x.obj(h)); !errors.Is(err, ErrUnknown) {
		t.Errorf("no keys: %v", err)
	}
	h2 := x.commit(SSHSigner{Key: other}, nil)
	if _, err := NewKeyring([]Key{key}).Signer(x.obj(h2)); !errors.Is(err, ErrUnknown) {
		t.Errorf("someone else's key: %v", err)
	}
	h3 := x.commit(nil, nil)
	if _, err := NewKeyring([]Key{key}).Signer(x.obj(h3)); !errors.Is(err, ErrUnsigned) {
		t.Errorf("unsigned: %v", err)
	}
	// A signature does not fit another message.
	c := x.obj(h)
	sig := c.PGPSignature
	if _, err := VerifySSH([]byte("something else"), sig, "git"); !errors.Is(err, ErrInvalid) {
		t.Errorf("other message: %v", err)
	}
	if _, err := VerifySSH([]byte("x"), sig, "file"); !errors.Is(err, ErrInvalid) {
		t.Errorf("other namespace: %v", err)
	}
}

func TestOpenPGPSignedCommits(t *testing.T) {
	x := newRepo(t)
	e, err := openpgp.NewEntity("Ayse", "", "ayse@x", nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, _ := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	_ = e.Serialize(w)
	_ = w.Close()
	key := Key{Username: "ayse", Kind: "gpg", PublicKey: buf.String()}
	h := x.commit(nil, e)
	got, err := NewKeyring([]Key{key}).Signer(x.obj(h))
	if err != nil || got.Username != "ayse" {
		t.Fatalf("signer = %+v, %v", got, err)
	}
	if _, err := NewKeyring(nil).Signer(x.obj(h)); !errors.Is(err, ErrUnknown) {
		t.Errorf("no keys: %v", err)
	}
}

func TestRange(t *testing.T) {
	x := newRepo(t)
	s, key := sshKey(t)
	k := NewKeyring([]Key{key})
	a := x.commit(nil, nil) // before Gaia: not checked
	b := x.commit(SSHSigner{Key: s}, nil)
	c := x.commit(SSHSigner{Key: s}, nil)
	got, err := Range(x.r, a, c, k)
	if err != nil || len(got) != 2 || got[0].Problem != nil || got[1].Problem != nil {
		t.Fatalf("range a..c = %+v, %v", got, err)
	}
	d := x.commit(nil, nil)
	got, _ = Range(x.r, c, d, k)
	if len(got) != 1 || got[0].Commit != d || !errors.Is(got[0].Problem, ErrUnsigned) {
		t.Errorf("range c..d = %+v", got)
	}
	// The first time only the head is checked.
	got, _ = Range(x.r, "", b, k)
	if len(got) != 1 || got[0].Commit != b || got[0].Problem != nil {
		t.Errorf("range ..b = %+v", got)
	}
	if got, _ := Range(x.r, c, c, k); len(got) != 0 {
		t.Errorf("empty range = %+v", got)
	}
}

// TestGitInterop: commits git signs with an SSH key verify here, and git
// verifies commits Gaia signs.
func TestGitInterop(t *testing.T) {
	for _, tool := range []string{"git", "ssh-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not found")
		}
	}
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	run := func(name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = filepath.Join(dir, "repo")
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "ayse", "-f", keyPath).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(dir, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	run("git", "init", "-q")
	cfg := []string{"-c", "user.name=t", "-c", "user.email=t@x", "-c", "gpg.format=ssh", "-c", "user.signingkey=" + keyPath + ".pub"}
	if err := os.WriteFile(filepath.Join(dir, "repo", "a"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "a")
	run("git", append(cfg, "commit", "-q", "-S", "-m", "signed by git")...)
	head := run("git", "rev-parse", "HEAD")

	pub, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	k := NewKeyring([]Key{{Username: "ayse", Kind: "ssh", Fingerprint: ssh.FingerprintSHA256(pk), PublicKey: string(pub)}})
	r, err := git.PlainOpen(filepath.Join(dir, "repo"))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.CommitObject(plumbing.NewHash(head))
	if got, err := k.Signer(c); err != nil || got.Username != "ayse" {
		t.Fatalf("a commit git signed: %+v %v", got, err)
	}

	// The other way: Gaia signs, git verifies with an allowed-signers file.
	priv, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "repo", "b"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, _ := r.Worktree()
	_, _ = wt.Add("b")
	h, err := wt.Commit("signed by gaia", &git.CommitOptions{Author: &object.Signature{Name: "gaia", Email: "gaia@x", When: time.Now()}, Signer: SSHSigner{Key: signer}})
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(dir, "allowed")
	if err := os.WriteFile(allowed, []byte("gaia@x "+string(pub)), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", append(cfg, "-c", "gpg.ssh.allowedSignersFile="+allowed, "verify-commit", h.String())...)
}
