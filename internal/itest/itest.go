// Package itest starts throwaway containers for integration tests. Tests
// using it run only with GAIA_INTEGRATION=1 and a working docker.
package itest

import (
	"bytes"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// PostgresImage matches the lab's PostgreSQL (design 12.2: 16 or newer).
const PostgresImage = "docker.io/library/postgres:17-alpine"

// Require skips the test unless integration tests are enabled.
func Require(t testing.TB) {
	t.Helper()
	if os.Getenv("GAIA_INTEGRATION") != "1" {
		t.Skip("integration test; set GAIA_INTEGRATION=1")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker is not available")
	}
}

// StartPostgres runs an empty PostgreSQL and returns a DSN for its owner
// role. The container is removed when the test ends.
func StartPostgres(t testing.TB) string {
	t.Helper()
	name := "gaia-itest-pg-" + randomSuffix()
	docker(t, "run", "-d", "--rm", "--name", name,
		"-e", "POSTGRES_USER=gaia_owner", "-e", "POSTGRES_PASSWORD=itest", "-e", "POSTGRES_DB=gaia",
		"-p", "127.0.0.1::5432", PostgresImage)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	// The image restarts the server once after creating the cluster.
	deadline := time.Now().Add(2 * time.Minute)
	for {
		out, _ := exec.Command("docker", "logs", name).CombinedOutput()
		if strings.Contains(string(out), "init process complete") &&
			exec.Command("docker", "exec", name, "pg_isready", "-q", "-U", "gaia_owner").Run() == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("postgres did not come up:\n%s", out)
		}
		time.Sleep(300 * time.Millisecond)
	}
	port := docker(t, "port", name, "5432/tcp")
	port = port[strings.LastIndex(port, ":")+1:]
	return fmt.Sprintf("postgres://gaia_owner:itest@127.0.0.1:%s/gaia?sslmode=disable", port)
}

func docker(t testing.TB, args ...string) string {
	t.Helper()
	var out, stderr bytes.Buffer
	cmd := exec.Command("docker", args...)
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker %s: %v: %s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(strings.Split(out.String(), "\n")[0])
}

func randomSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SambaImage is a Samba AD domain controller, standing in for Active
// Directory.
const SambaImage = "docker.io/instantlinux/samba-dc:4.23.8-r0"

// Samba is a running test domain.
type Samba struct {
	URL      string // ldaps://127.0.0.1:port
	CA       []byte // the domain controller's self-signed CA
	BaseDN   string
	Name     string // container
	Password func(user string) string
}

// StartSamba runs a Samba AD domain gaia.dev and seeds it with the script
// at seed (deploy/dev/ad-seed.sh). The container is removed when the test
// ends.
func StartSamba(t testing.TB, seed string) *Samba {
	t.Helper()
	name := "gaia-itest-ad-" + randomSuffix()
	pw, err := os.CreateTemp(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = pw.WriteString("Itest-Samba-Admin-1!")
	_ = pw.Close()
	_ = os.Chmod(pw.Name(), 0o644)
	docker(t, "run", "-d", "--rm", "--name", name, "--hostname", "dc1", "--cap-add", "SYS_ADMIN",
		"-e", "REALM=gaia.dev", "-e", "WORKGROUP=GAIA", "-e", "NETBIOS_NAME=DC1", "-e", "BIND_INTERFACES_ONLY=no",
		"-v", pw.Name()+":/run/secrets/samba-admin-password:ro",
		"-p", "127.0.0.1::636", SambaImage)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	deadline := time.Now().Add(3 * time.Minute)
	for exec.Command("docker", "exec", name, "samba-tool", "user", "list").Run() != nil {
		if time.Now().After(deadline) {
			out, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Fatalf("samba did not come up:\n%s", out)
		}
		time.Sleep(time.Second)
	}
	docker(t, "cp", seed, name+":/tmp/seed.sh")
	docker(t, "exec", name, "sh", "/tmp/seed.sh")
	// Samba writes its TLS certificate when the LDAP server first starts,
	// which can be after samba-tool answers. Its serial number is random
	// and sometimes negative, which Go refuses; Samba then makes another.
	var ca []byte
	for {
		out, err := exec.Command("docker", "exec", name, "cat", "/var/lib/samba/private/tls/ca.pem").Output()
		b, _ := pem.Decode(out)
		if err == nil && b != nil && b.Type == "CERTIFICATE" {
			if _, perr := x509.ParseCertificate(b.Bytes); perr == nil {
				ca = out
				break
			}
			docker(t, "exec", name, "sh", "-c", "rm -f /var/lib/samba/private/tls/*.pem")
			docker(t, "restart", name)
		}
		if time.Now().After(deadline) {
			t.Fatalf("samba wrote no usable TLS certificate: %v", err)
		}
		time.Sleep(time.Second)
	}
	port := docker(t, "port", name, "636/tcp")
	port = port[strings.LastIndex(port, ":")+1:]
	return &Samba{
		URL: "ldaps://127.0.0.1:" + port, CA: ca, BaseDN: "DC=gaia,DC=dev", Name: name,
		Password: func(user string) string { return "Dev-" + user + "-Password-1!" },
	}
}

// Exec runs samba-tool in the domain controller, to change the directory
// during a test.
func (s *Samba) Exec(t testing.TB, args ...string) {
	t.Helper()
	docker(t, append([]string{"exec", s.Name, "samba-tool"}, args...)...)
}
