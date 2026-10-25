package apply

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

// sshServer serves SSH handshakes with an ed25519 host key and returns
// its address and key.
func sshServer(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: false, PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _, _, _ = ssh.NewServerConn(c, cfg); _ = c.Close() }()
		}
	}()
	return ln.Addr().String(), signer.PublicKey()
}

func TestScanHostKey(t *testing.T) {
	addr, want := sshServer(t)
	host, port, _ := net.SplitHostPort(addr)
	// ScanHostKey dials port 22; reach the test server through its port.
	got, err := scanHostKeyPort(context.Background(), host, port, ssh.KeyAlgoED25519)
	if err != nil || string(got.Marshal()) != string(want.Marshal()) {
		t.Fatalf("scanned %v, %v", got, err)
	}
	if _, err := scanHostKeyPort(context.Background(), host, port, ssh.KeyAlgoRSASHA512); err == nil {
		t.Error("an algorithm the server has no key for")
	}
}
