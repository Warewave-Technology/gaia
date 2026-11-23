package hostconn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// sshServer is a minimal SSH server: public-key auth for one key, and direct-streamlocal forwarding to unix sockets
// (what Gaia uses OpenSSH's sshd for on a host). It records which channel types were asked for.
type sshServer struct {
	addr     string
	hostKey  ssh.Signer
	channels []string
}

func startServer(t *testing.T, client ssh.PublicKey) *sshServer {
	t.Helper()
	_, hk, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(hk)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if Fingerprint(k) == Fingerprint(client) {
			return nil, nil
		}
		return nil, errors.New("unknown key")
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s := &sshServer{addr: ln.Addr().String(), hostKey: signer}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					s.channels = append(s.channels, nc.ChannelType())
					if nc.ChannelType() != "direct-streamlocal@openssh.com" {
						nc.Reject(ssh.Prohibited, "only streamlocal")
						continue
					}
					var p struct {
						Path      string
						Reserved0 string
						Reserved1 uint32
					}
					if err := ssh.Unmarshal(nc.ExtraData(), &p); err != nil {
						nc.Reject(ssh.ConnectionFailed, err.Error())
						continue
					}
					u, err := net.Dial("unix", p.Path)
					if err != nil {
						nc.Reject(ssh.ConnectionFailed, err.Error())
						continue
					}
					ch, creqs, err := nc.Accept()
					if err != nil {
						u.Close()
						continue
					}
					go ssh.DiscardRequests(creqs)
					go func() { io.Copy(ch, u); ch.CloseWrite() }()
					go func() { io.Copy(u, ch); u.Close() }()
				}
			}()
		}
	}()
	return s
}

func (s *sshServer) target(user string, pin bool) Target {
	host, port, _ := net.SplitHostPort(s.addr)
	p, _ := strconv.Atoi(port)
	t := Target{Address: host, Port: p, User: user}
	if pin {
		t.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.hostKey.PublicKey())))
	}
	return t
}

func TestKeyScanPinForward(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "hc") // unix socket paths are short on macOS
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	key, err := LoadOrCreateKey(filepath.Join(dir, "id"), "gaia@test")
	if err != nil {
		t.Fatal(err)
	}
	again, err := LoadOrCreateKey(filepath.Join(dir, "id"), "gaia@test")
	if err != nil || again.AuthorizedKey != key.AuthorizedKey || !strings.HasPrefix(key.AuthorizedKey, "ssh-ed25519 ") {
		t.Fatalf("key reload: %q %q %v", key.AuthorizedKey, again.AuthorizedKey, err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "id")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v", fi.Mode())
	}
	srv := startServer(t, key.Signer.PublicKey())

	tg := srv.target("gaia-backup", false)
	sc, err := Scan(ctx, tg.Address, tg.Port)
	if err != nil || sc.Fingerprint != Fingerprint(srv.hostKey.PublicKey()) || sc.Type != "ssh-ed25519" {
		t.Fatalf("scan %+v %v", sc, err)
	}
	if _, err := Dial(ctx, tg, key); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("an unpinned host: %v", err)
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	op, _ := ssh.NewSignerFromKey(other)
	wrong := tg
	wrong.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(op.PublicKey())))
	// A host presenting another key than the pinned one (ADR-007 C17): a typed error with both fingerprints and the
	// presented key, not only a message.
	_, err = Dial(ctx, wrong, key)
	var hk *HostKeyChangedError
	if !errors.Is(err, ErrHostKeyChanged) || !errors.As(err, &hk) || errors.Is(err, ErrUnreachable) || errors.Is(err, ErrUntrusted) {
		t.Fatalf("a different host key: %v", err)
	}
	if hk.PinnedFingerprint != Fingerprint(op.PublicKey()) || hk.PresentedFingerprint != Fingerprint(srv.hostKey.PublicKey()) ||
		hk.Presented.Fingerprint != hk.PresentedFingerprint || hk.Presented.Type != "ssh-ed25519" ||
		hk.Presented.Key != strings.TrimSpace(string(ssh.MarshalAuthorizedKey(srv.hostKey.PublicKey()))) || !strings.Contains(err.Error(), "not the pinned") {
		t.Fatalf("the changed key's facts: %+v (%v)", hk, err)
	}
	stranger, _ := LoadOrCreateKey(filepath.Join(dir, "stranger"), "x")
	if _, err := Dial(ctx, srv.target("gaia-backup", true), stranger); err == nil {
		t.Fatal("a key the host does not know was let in")
	}

	sock := filepath.Join(dir, "echo.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	c, err := Dial(ctx, srv.target("gaia-backup", true), key)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	u, err := c.DialUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	u.Write([]byte("nbd"))
	buf := make([]byte, 3)
	if _, err := io.ReadFull(u, buf); err != nil || string(buf) != "nbd" {
		t.Fatalf("echo over the forwarded socket: %q %v", buf, err)
	}
	u.Close()
	if _, err := c.DialUnix(filepath.Join(dir, "missing.sock")); err == nil {
		t.Fatal("a missing socket connected")
	}
	for _, ch := range srv.channels {
		if ch != "direct-streamlocal@openssh.com" {
			t.Fatalf("channel %q: only unix socket forwarding is used", ch)
		}
	}
}
