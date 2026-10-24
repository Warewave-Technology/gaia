package itest

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// OpenBaoImage matches the lab's OpenBao.
const OpenBaoImage = "docker.io/openbao/openbao:2.7.1"

// OpenBaoRootToken is the dev server's root token.
const OpenBaoRootToken = "itest-root"

// StartOpenBao runs an OpenBao dev server (in memory, unsealed) and returns
// its address. The container is removed when the test ends.
func StartOpenBao(t testing.TB) string {
	t.Helper()
	name := "gaia-itest-bao-" + randomSuffix()
	docker(t, "run", "-d", "--rm", "--name", name, "--cap-add", "IPC_LOCK", "-p", "127.0.0.1::8200", OpenBaoImage,
		"server", "-dev", "-dev-root-token-id="+OpenBaoRootToken, "-dev-listen-address=0.0.0.0:8200")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	port := docker(t, "port", name, "8200/tcp")
	addr := fmt.Sprintf("http://127.0.0.1:%s", port[strings.LastIndex(port, ":")+1:])
	deadline := time.Now().Add(time.Minute)
	for {
		resp, err := http.Get(addr + "/v1/sys/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return addr
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("openbao did not come up: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}
