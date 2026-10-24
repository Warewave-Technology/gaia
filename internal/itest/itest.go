// Package itest starts throwaway containers for integration tests. Tests
// using it run only with GAIA_INTEGRATION=1 and a working docker.
package itest

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
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
