// Package hostconn reaches a protected KVM host the only way ADR-007 §3 allows: one authenticated SSH connection
// (pure-Go client, Gaia's own key, the host's key pinned at registration) that carries libvirt's RPC and the NBD
// exports, both as unix sockets on the host forwarded over SSH (direct-streamlocal). Nothing listens on TCP.
package hostconn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/libvirtbackup"
)

// LibvirtSocket is libvirtd's read-write socket.
const LibvirtSocket = "/var/run/libvirt/libvirt-sock"

// Key is Gaia's SSH identity.
type Key struct {
	Signer        ssh.Signer
	AuthorizedKey string // the line for the host's authorized_keys (without options)
}

// LoadOrCreateKey reads Gaia's ed25519 SSH key from path, creating it (mode 0600) if missing.
func LoadOrCreateKey(path, comment string) (Key, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		_, priv, gerr := ed25519.GenerateKey(rand.Reader)
		if gerr != nil {
			return Key{}, gerr
		}
		blk, merr := ssh.MarshalPrivateKey(priv, comment)
		if merr != nil {
			return Key{}, merr
		}
		f, ferr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if ferr != nil {
			return Key{}, ferr
		}
		if _, werr := f.Write(pem.EncodeToMemory(blk)); werr != nil {
			f.Close()
			return Key{}, werr
		}
		if cerr := f.Close(); cerr != nil {
			return Key{}, cerr
		}
		b, err = os.ReadFile(path)
	}
	if err != nil {
		return Key{}, err
	}
	s, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return Key{}, fmt.Errorf("%s: %w", path, err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey()))) + " " + comment
	return Key{Signer: s, AuthorizedKey: line}, nil
}

// Fingerprint is OpenSSH's SHA256 fingerprint of a key ("SHA256:...", as ssh-keygen -lf prints it).
func Fingerprint(k ssh.PublicKey) string { return ssh.FingerprintSHA256(k) }

// Scanned is a host key seen without trusting it.
type Scanned struct {
	Fingerprint string `json:"fingerprint"`
	Type        string `json:"type"`
	Key         string `json:"key"` // authorized_keys form, e.g. "ssh-ed25519 AAAA..."
}

var errScanned = errors.New("host key captured")

// Scan connects to the host only far enough to read its host key (ed25519 preferred). It authenticates nothing.
func Scan(ctx context.Context, address string, port int) (Scanned, error) {
	var got ssh.PublicKey
	cfg := &ssh.ClientConfig{User: "gaia-scan", Timeout: 10 * time.Second,
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256},
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			got = k
			return errScanned
		}}
	conn, err := dialTCP(ctx, address, port)
	if err != nil {
		return Scanned{}, err
	}
	defer conn.Close()
	_, _, _, err = ssh.NewClientConn(conn, net.JoinHostPort(address, strconv.Itoa(port)), cfg)
	if got == nil {
		return Scanned{}, fmt.Errorf("SSH handshake with %s: %w", address, err)
	}
	return Scanned{Fingerprint: Fingerprint(got), Type: got.Type(), Key: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(got)))}, nil
}

func dialTCP(ctx context.Context, address string, port int) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(address, strconv.Itoa(port)))
}

// Target is a host to connect to.
type Target struct {
	Address string
	Port    int
	User    string
	HostKey string // authorized_keys form; empty means not trusted yet, and Dial refuses
}

// ErrUnreachable marks a host that did not answer at all: the TCP connection to its SSH port failed (refused, timed
// out, no route). A host that answers but refuses Gaia is not unreachable.
var ErrUnreachable = errors.New("the host does not answer")

// unreachable is an error that is ErrUnreachable and keeps its own message.
type unreachable struct{ error }

func (u unreachable) Is(target error) bool { return target == ErrUnreachable }
func (u unreachable) Unwrap() error        { return u.error }

// ErrUntrusted means the host's key was never pinned.
var ErrUntrusted = errors.New("the host's SSH key is not trusted yet (scan it and trust its fingerprint first)")

// Conn is an SSH connection to a host.
type Conn struct {
	ssh  *ssh.Client
	host string
}

// Dial connects with Gaia's key; the host must present exactly the pinned key.
func Dial(ctx context.Context, t Target, key Key) (*Conn, error) {
	if t.HostKey == "" {
		return nil, ErrUntrusted
	}
	pinned, _, _, _, err := ssh.ParseAuthorizedKey([]byte(t.HostKey))
	if err != nil {
		return nil, fmt.Errorf("pinned host key: %w", err)
	}
	cfg := &ssh.ClientConfig{User: t.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(key.Signer)}, Timeout: 15 * time.Second,
		HostKeyAlgorithms: []string{pinned.Type()},
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			if Fingerprint(k) != Fingerprint(pinned) {
				return fmt.Errorf("host key %s is not the pinned %s: refusing to connect", Fingerprint(k), Fingerprint(pinned))
			}
			return nil
		}}
	conn, err := dialTCP(ctx, t.Address, t.Port)
	if err != nil {
		return nil, unreachable{err}
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, net.JoinHostPort(t.Address, strconv.Itoa(t.Port)), cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SSH to %s@%s: %w", t.User, t.Address, err)
	}
	cl := ssh.NewClient(c, chans, reqs)
	// Keepalives: a backup can read for a long time on one connection; a dead peer must end it.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			if _, _, err := cl.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				return
			}
		}
	}()
	return &Conn{ssh: cl, host: t.Address}, nil
}

// Close closes the SSH connection and everything forwarded over it.
func (c *Conn) Close() error { return c.ssh.Close() }

// DialUnix opens a unix socket on the host through the SSH connection.
func (c *Conn) DialUnix(path string) (net.Conn, error) {
	conn, err := c.ssh.Dial("unix", path)
	if err != nil {
		return nil, fmt.Errorf("%s:%s over SSH: %w", c.host, path, err)
	}
	return conn, nil
}

// Libvirt connects to the host's libvirtd through the SSH connection.
func (c *Conn) Libvirt() (*libvirtbackup.Conn, error) {
	return libvirtbackup.ConnectDialer(func() (net.Conn, error) { return c.DialUnix(LibvirtSocket) })
}
