package repo

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/gitsrc"
	"github.com/Warewave-Technology/gaia/internal/gitsrc/gittest"
	"github.com/Warewave-Technology/gaia/internal/signature"
)

func gaiaKey(t *testing.T) (signature.SSHSigner, *signature.Keyring) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	k := signature.Key{Username: "gaia", Kind: "ssh", Fingerprint: ssh.FingerprintSHA256(s.PublicKey()),
		PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey())))}
	return signature.SSHSigner{Key: s}, signature.NewKeyring([]signature.Key{k})
}

// read returns a file at the head of the followed branch, or "" when it
// is not there.
func read(t *testing.T, m *Mirrors, r Repo, path string) string {
	t.Helper()
	head, err := m.Sync(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	g, err := gitsrc.Open(m.Path(r))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := g.CommitObject(plumbing.NewHash(head))
	f, err := c.File(path)
	if err != nil {
		return ""
	}
	s, _ := f.Contents()
	return s
}

func TestRevert(t *testing.T) {
	for _, mirrored := range []bool{false, true} {
		name := "local"
		if mirrored {
			name = "mirrored"
		}
		t.Run(name, func(t *testing.T) {
			if !mirrored {
				src := gittest.New(t)
				src.Write("a", "a")
				c := src.Commit("a")
				signer, _ := gaiaKey(t)
				w := &Writer{Mirrors: &Mirrors{Dir: t.TempDir()}, Signer: signer}
				if _, err := w.Revert(context.Background(), Repo{Name: "p", URL: src.Dir}, c, "ayse"); err == nil || !strings.Contains(err.Error(), "local working copy") {
					t.Errorf("a local working copy: %v", err)
				}
				return
			}
			ctx := context.Background()
			src := gittest.New(t)
			src.Write("a.yaml", "a: 1\n", "keep.yaml", "k\n", "gone.yaml", "g\n")
			src.Commit("first")
			src.Write("a.yaml", "a: 2\n", "new.yaml", "n\n")
			if err := os.Remove(filepath.Join(src.Dir, "gone.yaml")); err != nil {
				t.Fatal(err)
			}
			target := src.Commit("the change\n\nwith a body")
			src.Write("keep.yaml", "k2\n")
			later := src.Commit("later")

			r := Repo{Name: "platform", URL: src.Dir}
			if mirrored {
				// A bare repository as the server, fetched over file://.
				bare := t.TempDir()
				if _, err := git.PlainClone(bare, true, &git.CloneOptions{URL: src.Dir}); err != nil {
					t.Fatal(err)
				}
				r = Repo{Name: "platform", URL: "file://" + bare, Ref: "master"}
			}
			m := &Mirrors{Dir: t.TempDir()}
			signer, keys := gaiaKey(t)
			w := &Writer{Mirrors: m, Signer: signer, Name: "Gaia", Email: "gaia@example.com"}

			h, err := w.Revert(ctx, r, target, "ayse")
			if err != nil {
				t.Fatal(err)
			}
			if got := read(t, m, r, "a.yaml"); got != "a: 1\n" {
				t.Errorf("a.yaml = %q", got)
			}
			if got := read(t, m, r, "gone.yaml"); got != "g\n" {
				t.Errorf("gone.yaml = %q", got)
			}
			if got := read(t, m, r, "new.yaml"); got != "" {
				t.Errorf("new.yaml = %q", got)
			}
			if got := read(t, m, r, "keep.yaml"); got != "k2\n" {
				t.Errorf("a later commit's file = %q", got)
			}
			g, _ := gitsrc.Open(m.Path(r))
			c, err := g.CommitObject(plumbing.NewHash(h))
			if err != nil {
				t.Fatal(err)
			}
			if k, err := keys.Signer(c); err != nil || k.Username != "gaia" {
				t.Errorf("signer = %+v, %v", k, err)
			}
			if Trailer(c.Message, TrailerRequestedBy) != "ayse" || Trailer(c.Message, TrailerReverts) != target {
				t.Errorf("message = %q", c.Message)
			}
			if !strings.HasPrefix(c.Message, `Revert "the change"`) || c.ParentHashes[0].String() != later {
				t.Errorf("commit = %q on %v", c.Message, c.ParentHashes)
			}

			// Reverting it again: the files are no longer what it left.
			if _, err := w.Revert(ctx, r, target, "ayse"); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "a.yaml, gone.yaml, new.yaml") {
				t.Errorf("revert twice: %v", err)
			}
			// Other files are free: "later" reverts, and so does the revert.
			if _, err := w.Revert(ctx, r, later, "ayse"); err != nil {
				t.Fatalf("revert later: %v", err)
			}
			if _, err := w.Revert(ctx, r, h, "mehmet"); err != nil {
				t.Fatalf("revert the revert: %v", err)
			}
			if got := read(t, m, r, "a.yaml") + read(t, m, r, "keep.yaml"); got != "a: 2\nk\n" {
				t.Errorf("after both = %q", got)
			}
			if _, err := w.Revert(ctx, r, "0123456789012345678901234567890123456789", "ayse"); err == nil {
				t.Error("an unknown commit reverted")
			}
		})
	}
}
