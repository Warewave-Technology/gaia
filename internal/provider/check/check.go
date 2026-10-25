// Package check tells whether Gaia can reach each provider with its
// credentials (design 11.5): one cheap call per provider that reads and
// changes nothing.
package check

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vmware/govmomi/session"

	"github.com/Warewave-Technology/gaia/internal/ad"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	ipamclient "github.com/Warewave-Technology/gaia/internal/provider/ipam"
	"github.com/Warewave-Technology/gaia/internal/provider/nexus"
	"github.com/Warewave-Technology/gaia/internal/provider/proxmox"
	"github.com/Warewave-Technology/gaia/internal/provider/vsphere"
	"github.com/Warewave-Technology/gaia/internal/provider/warpgate"
	"github.com/Warewave-Technology/gaia/internal/provider/zabbix"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
)

// Statuses of a check.
const (
	OK      = "ok"
	Failed  = "failed"
	Skipped = "skipped" // not checked: the provider has no harmless call
)

// Result is one provider's check.
type Result struct {
	Status  string
	Detail  string // what it answered: a version, a count
	Error   string
	Latency time.Duration
}

// Secrets reads a credential reference.
type Secrets interface {
	Read(ctx context.Context, ref string) (map[string]string, error)
}

// Timeout bounds one provider's check.
var Timeout = 20 * time.Second

// Run checks one provider.
func Run(ctx context.Context, p *catalog.ProviderSpec, secrets Secrets) Result {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	start := time.Now()
	detail, err := run(ctx, p, secrets)
	r := Result{Latency: time.Since(start)}
	switch {
	case errors.Is(err, errSkip):
		r.Status, r.Detail = Skipped, detail
	case err != nil:
		r.Status, r.Error = Failed, err.Error()
	default:
		r.Status, r.Detail = OK, detail
	}
	return r
}

var errSkip = errors.New("skipped")

func run(ctx context.Context, p *catalog.ProviderSpec, secrets Secrets) (string, error) {
	creds := map[string]string{}
	if p.Credentials != "" {
		var err error
		if creds, err = secrets.Read(ctx, p.Credentials); err != nil {
			return "", fmt.Errorf("credentials %s: %w", p.Credentials, err)
		}
	}
	insecure, _ := strconv.ParseBool(creds["insecure"])
	switch p.Type {
	case "proxmox":
		v, err := proxmox.New(proxmox.Credentials{Endpoint: p.Endpoint, TokenID: creds["token_id"], TokenSecret: creds["token_secret"], Insecure: insecure}).Version(ctx)
		return "Proxmox VE " + v, err
	case "vsphere":
		c, err := vsphere.Connect(ctx, vsphere.Credentials{Endpoint: p.Endpoint, Username: creds["username"], Password: creds["password"], Insecure: insecure})
		if err != nil {
			return "", err
		}
		defer func() { _ = session.NewManager(c).Logout(context.WithoutCancel(ctx)) }()
		return c.ServiceContent.About.FullName, nil
	case "ipam":
		pools, err := ipamclient.New(p.Endpoint, creds["token"]).Pools(ctx)
		return fmt.Sprintf("%d pools", len(pools)), err
	case "zabbix":
		v, n, err := zabbix.New(p.Endpoint, creds["token"]).Check(ctx)
		return fmt.Sprintf("Zabbix %s · %d host", v, n), err
	case "warpgate":
		w, err := warpgate.New(p.Endpoint, creds["token"], insecure, []byte(creds["ca"]))
		if err != nil {
			return "", err
		}
		roles, err := w.Roles(ctx)
		return fmt.Sprintf("%d roles", len(roles)), err
	case "activedirectory":
		d, err := ad.Open(ctx, ad.ConfigFrom(p.Endpoint, p.BaseDN, creds))
		if err != nil {
			return "", err
		}
		_ = d.Close()
		return "LDAP bind", nil
	case "nexus":
		return "", nexus.New(p.Endpoint, creds["username"], creds["password"]).Status(ctx)
	case "s3":
		s, err := runrecord.NewS3(p.Endpoint, p.Bucket, creds["access_key"], creds["secret_key"])
		if err != nil {
			return "", err
		}
		return "bucket " + p.Bucket, s.Check(ctx)
	case "openbao":
		return openbao(ctx, p.Endpoint)
	case "postgres":
		u, err := url.Parse(p.Endpoint)
		if err != nil || u.Host == "" {
			return "", fmt.Errorf("endpoint %q: want a URL", p.Endpoint)
		}
		u.User = url.UserPassword(creds["username"], creds["password"])
		c, err := pgx.Connect(ctx, u.String())
		if err != nil {
			return "", err
		}
		defer func() { _ = c.Close(context.WithoutCancel(ctx)) }()
		var v string
		err = c.QueryRow(ctx, "show server_version").Scan(&v)
		return "PostgreSQL " + v, err
	case "smtp":
		return smtpHello(ctx, p.Endpoint)
	case "git":
		return "repositories are checked at every fetch (see Sources)", errSkip
	case "slack", "msteams", "webhook":
		return "checking would send a message", errSkip
	}
	return "no check for this type", errSkip
}

func openbao(ctx context.Context, addr string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(addr, "/")+"/v1/sys/health?standbyok=true", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("openbao: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var h struct {
		Sealed  bool   `json:"sealed"`
		Version string `json:"version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&h)
	if h.Sealed {
		return "", errors.New("openbao is sealed")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openbao health: HTTP %d", resp.StatusCode)
	}
	return "OpenBao " + h.Version, nil
}

// smtpHello says EHLO and quits: the relay is there and talks SMTP.
func smtpHello(ctx context.Context, endpoint string) (string, error) {
	host := endpoint
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		host = u.Host
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "25")
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return "", err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	name, _, _ := net.SplitHostPort(host)
	c, err := smtp.NewClient(conn, name)
	if err != nil {
		_ = conn.Close()
		return "", err
	}
	defer func() { _ = c.Close() }()
	if err := c.Hello("gaia"); err != nil {
		return "", err
	}
	tlsOK, _ := c.Extension("STARTTLS")
	_ = c.Quit()
	if tlsOK {
		return "SMTP · STARTTLS", nil
	}
	return "SMTP", nil
}
