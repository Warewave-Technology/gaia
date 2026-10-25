package apply

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// ScanHostKey reads the host key a server offers for algorithm: an SSH
// handshake stopped right after the key exchange, without logging in.
func ScanHostKey(ctx context.Context, addr, algorithm string) (ssh.PublicKey, error) {
	return scanHostKeyPort(ctx, addr, "22", algorithm)
}

func scanHostKeyPort(ctx context.Context, addr, port, algorithm string) (ssh.PublicKey, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(addr, port))
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	}
	var key ssh.PublicKey
	errSeen := errors.New("seen")
	_, _, _, err = ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User:              "gaia",
		HostKeyAlgorithms: []string{algorithm},
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			key = k
			return errSeen
		},
	})
	if key != nil {
		return key, nil
	}
	return nil, err
}

// hostKeyAlgorithms are tried in this order; the first the server has is
// recorded.
var hostKeyAlgorithms = []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoRSASHA512}

// knownHosts returns known_hosts lines for servers (UID → address): the
// keys Gaia recorded, and for a server it has none of, the key it offers
// now, recorded. A server is newest when Gaia first reaches it: right
// after it was created, on Gaia's own network. A server that answers
// with another key later fails its run.
func (r *runner) knownHosts(ctx context.Context, addrs map[string]string) (string, error) {
	q := r.d.Store.Q()
	var b strings.Builder
	for uid, addr := range addrs {
		keys, err := q.HostKeys(ctx, uid)
		if err != nil {
			return "", err
		}
		if len(keys) == 0 {
			k, err := r.recordHostKey(ctx, uid, addr)
			if err != nil {
				return "", err
			}
			keys = []db.HostKey{k}
		}
		for _, k := range keys {
			fmt.Fprintf(&b, "%s %s\n", addr, k.PublicKey)
		}
	}
	return b.String(), nil
}

// recordHostKey scans a server's host key, a few times while its SSH
// server comes up, and records it.
func (r *runner) recordHostKey(ctx context.Context, uid, addr string) (db.HostKey, error) {
	if r.d.ScanHostKey == nil {
		return db.HostKey{}, errors.New("no host key scanner")
	}
	var last error
	for try := 0; try < 12; try++ {
		for _, alg := range hostKeyAlgorithms {
			k, err := r.d.ScanHostKey(ctx, addr, alg)
			if err != nil {
				last = err
				continue
			}
			pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
			if err := r.d.Store.Q().SetHostKey(ctx, db.SetHostKeyParams{Uid: uid, KeyType: k.Type(), PublicKey: pub}); err != nil {
				return db.HostKey{}, err
			}
			r.printf("  host key of %s recorded: %s\n", addr, ssh.FingerprintSHA256(k))
			return db.HostKey{Uid: uid, KeyType: k.Type(), PublicKey: pub}, nil
		}
		select {
		case <-ctx.Done():
			return db.HostKey{}, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return db.HostKey{}, fmt.Errorf("host key of %s: %w", addr, last)
}
