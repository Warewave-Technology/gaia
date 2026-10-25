package itest

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// WarpgateImage is the bastion Gaia registers servers in.
const WarpgateImage = "ghcr.io/warp-tech/warpgate:0.29.2"

// Warpgate is a running test bastion.
type Warpgate struct {
	URL   string // https://127.0.0.1:port, self-signed
	Token string // an admin API token
	Name  string // container
}

// StartWarpgate runs a Warpgate with SSH and RDP, and makes an admin API
// token. The container is removed when the test ends.
func StartWarpgate(t testing.TB) *Warpgate {
	t.Helper()
	name := "gaia-itest-wg-" + randomSuffix()
	const pw = "Itest-Warpgate-1"
	docker(t, "run", "-d", "--rm", "--name", name, "--entrypoint", "sh", "-p", "127.0.0.1::8888", WarpgateImage, "-c",
		"warpgate unattended-setup --data-path /data --http-port 8888 --ssh-port 2222 --rdp-port 3389 --host-key-verification auto-reject --admin-password "+pw+
			" && exec warpgate --config /data/warpgate.yaml run")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	port := docker(t, "port", name, "8888/tcp")
	base := "https://127.0.0.1:" + port[strings.LastIndex(port, ":")+1:]
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // the test bastion's own certificate
	post := func(path string, body any) (*http.Response, error) {
		data, _ := json.Marshal(body)
		return hc.Post(base+path, "application/json", bytes.NewReader(data))
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		resp, err := post("/@warpgate/api/auth/login", map[string]string{"username": "admin", "password": pw})
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusCreated {
				break
			}
		}
		if time.Now().After(deadline) {
			out, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Fatalf("warpgate did not come up: %v\n%s", err, out)
		}
		time.Sleep(time.Second)
	}
	resp, err := post("/@warpgate/api/profile/api-tokens", map[string]string{"label": "itest", "expiry": "2099-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var tok struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil || tok.Secret == "" {
		t.Fatalf("api token: %v (HTTP %d)", err, resp.StatusCode)
	}
	return &Warpgate{URL: base, Token: tok.Secret, Name: name}
}
