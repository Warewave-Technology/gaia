package check

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/catalog"
)

type secrets map[string]map[string]string

func (s secrets) Read(_ context.Context, ref string) (map[string]string, error) {
	if v, ok := s[ref]; ok {
		return v, nil
	}
	return nil, errors.New("no such secret")
}

func TestChecks(t *testing.T) {
	pve := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "PVEAPIToken=gaia@pve!poc=s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"version":"8.4.1","release":"8.4"}}`))
	}))
	defer pve.Close()
	zbx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Method string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch {
		case req.Method == "apiinfo.version" && r.Header.Get("Authorization") == "":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":"7.0.32","id":1}`))
		case req.Method == "host.get" && r.Header.Get("Authorization") == "Bearer tok":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":"12","id":2}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32602,"message":"Invalid params.","data":"Session terminated, re-login, please."},"id":2}`))
		}
	}))
	defer zbx.Close()
	bao := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"sealed":true,"version":"2.3.1"}`))
	}))
	defer bao.Close()
	smtpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = smtpLn.Close() }()
	go func() {
		c, err := smtpLn.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		r := bufio.NewReader(c)
		_, _ = c.Write([]byte("220 mail ESMTP\r\n"))
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch strings.ToUpper(strings.Fields(line)[0]) {
			case "EHLO":
				_, _ = c.Write([]byte("250-mail\r\n250 STARTTLS\r\n"))
			case "QUIT":
				_, _ = c.Write([]byte("221 bye\r\n"))
				return
			}
		}
	}()

	s := secrets{
		"bao://kv/pve":     {"token_id": "gaia@pve!poc", "token_secret": "s3cret"},
		"bao://kv/pve-bad": {"token_id": "gaia@pve!poc", "token_secret": "wrong"},
		"bao://kv/zabbix":  {"token": "tok"},
		"bao://kv/old":     {"token": "expired"},
	}
	cases := []struct {
		name   string
		p      catalog.ProviderSpec
		status string
		want   string
	}{
		{"proxmox", catalog.ProviderSpec{Type: "proxmox", Endpoint: pve.URL, Credentials: "bao://kv/pve"}, OK, "Proxmox VE 8.4.1"},
		{"proxmox, wrong token", catalog.ProviderSpec{Type: "proxmox", Endpoint: pve.URL, Credentials: "bao://kv/pve-bad"}, Failed, "401"},
		{"zabbix", catalog.ProviderSpec{Type: "zabbix", Endpoint: zbx.URL, Credentials: "bao://kv/zabbix"}, OK, "Zabbix 7.0.32 · 12 host"},
		{"zabbix, expired token", catalog.ProviderSpec{Type: "zabbix", Endpoint: zbx.URL, Credentials: "bao://kv/old"}, Failed, "re-login"},
		{"openbao, sealed", catalog.ProviderSpec{Type: "openbao", Endpoint: bao.URL}, Failed, "sealed"},
		{"smtp", catalog.ProviderSpec{Type: "smtp", Endpoint: smtpLn.Addr().String()}, OK, "STARTTLS"},
		{"missing credentials", catalog.ProviderSpec{Type: "ipam", Endpoint: "http://127.0.0.1:1", Credentials: "bao://kv/none"}, Failed, "credentials bao://kv/none"},
		{"unreachable", catalog.ProviderSpec{Type: "ipam", Endpoint: "http://127.0.0.1:1", Credentials: "bao://kv/zabbix"}, Failed, "refused"},
		{"slack", catalog.ProviderSpec{Type: "slack", Endpoint: "https://hooks.slack.com/x"}, Skipped, "mesaj"},
		{"git", catalog.ProviderSpec{Type: "git", URL: "https://example.com/x.git"}, Skipped, "fetch"},
	}
	for _, c := range cases {
		r := Run(context.Background(), &c.p, s)
		got := r.Detail + r.Error
		if r.Status != c.status || !strings.Contains(got, c.want) {
			t.Errorf("%s: %s %q", c.name, r.Status, got)
		}
	}
}
