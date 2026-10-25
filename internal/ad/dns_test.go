package ad

import (
	"context"
	"errors"
	"net/netip"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/itest"
)

func TestCountName(t *testing.T) {
	b, err := countName("dc1.gaia.dev.")
	if err != nil {
		t.Fatal(err)
	}
	// As Samba writes the NS record of gaia.dev.
	if want := "\x0e\x03\x03dc1\x04gaia\x03dev\x00"; string(b) != want {
		t.Errorf("countName = %q, want %q", b, want)
	}
	if name, ok := parseCountName(b); !ok || name != "dc1.gaia.dev" {
		t.Errorf("parse = %q %v", name, ok)
	}
}

// dig asks the domain controller's own DNS server.
func dig(t *testing.T, s *itest.Samba, name, typ string) string {
	t.Helper()
	out, err := exec.Command("docker", "exec", s.Name, "dig", "@127.0.0.1", "+short", name, typ).Output()
	if err != nil {
		t.Fatalf("dig %s %s: %v", name, typ, err)
	}
	return strings.TrimSpace(string(out))
}

func TestDNSRecords(t *testing.T) {
	itest.Require(t)
	s := itest.StartSamba(t, filepath.Join("..", "..", "deploy", "dev", "ad-seed.sh"))
	ctx := context.Background()
	d, err := Open(ctx, Config{Endpoint: s.URL, Username: "gaia-writer@gaia.dev", Password: s.Password("gaia-writer"), CA: s.CA, ServerName: "dc1.gaia.dev"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	if _, _, err := d.Zone("nope.dev"); !errors.Is(err, ErrNoZone) {
		t.Errorf("unknown zone: %v", err)
	}
	ip := netip.MustParseAddr("10.50.0.100")
	zone, node, err := d.ReverseZone(ip)
	if err != nil || zone != "0.50.10.in-addr.arpa" || node != "100" {
		t.Fatalf("reverse zone = %s %s %v", zone, node, err)
	}
	if _, _, err := d.ReverseZone(netip.MustParseAddr("10.60.0.1")); !errors.Is(err, ErrNoZone) {
		t.Errorf("no reverse zone: %v", err)
	}

	a := Record{Type: TypeA, TTL: 600, Data: "10.50.0.100"}
	ptr := Record{Type: TypePTR, TTL: 600, Data: "web01.gaia.dev"}
	for i, want := range []bool{true, false} {
		changed, err := d.SetRecord("gaia.dev", "web01", a, false)
		if err != nil || changed != want {
			t.Fatalf("set A #%d: %v %v", i, changed, err)
		}
		if changed, err = d.SetRecord(zone, node, ptr, false); err != nil || changed != want {
			t.Fatalf("set PTR #%d: %v %v", i, changed, err)
		}
	}
	if got := dig(t, s, "web01.gaia.dev", "A"); got != "10.50.0.100" {
		t.Errorf("dig A = %q", got)
	}
	if got := dig(t, s, "100.0.50.10.in-addr.arpa", "PTR"); got != "web01.gaia.dev." {
		t.Errorf("dig PTR = %q", got)
	}

	// A name with another address is not Gaia's to change, unless owned.
	other := Record{Type: TypeA, TTL: 600, Data: "10.50.0.101"}
	if _, err := d.SetRecord("gaia.dev", "web01", other, false); !errors.Is(err, ErrTaken) || !strings.Contains(err.Error(), "A 10.50.0.100") {
		t.Errorf("taken: %v", err)
	}
	if changed, err := d.SetRecord("gaia.dev", "web01", other, true); err != nil || !changed {
		t.Fatalf("owned: %v %v", changed, err)
	}
	if got := dig(t, s, "web01.gaia.dev", "A"); got != "10.50.0.101" {
		t.Errorf("dig A after replace = %q", got)
	}
	recs, err := d.Records("gaia.dev", "web01")
	if err != nil || len(recs) != 1 || recs[0] != other {
		t.Errorf("records = %+v %v", recs, err)
	}

	// Records made by samba-tool are read too.
	s.Exec(t, "dns", "add", "127.0.0.1", "gaia.dev", "legacy", "A", "10.50.0.7", "-U", "administrator", "--password=Itest-Samba-Admin-1!")
	if recs, err := d.Records("gaia.dev", "legacy"); err != nil || len(recs) != 1 || recs[0].Data != "10.50.0.7" {
		t.Errorf("legacy = %+v %v", recs, err)
	}

	for i, want := range []bool{true, false} {
		removed, err := d.DeleteRecord("gaia.dev", "web01", other)
		if err != nil || removed != want {
			t.Fatalf("delete A #%d: %v %v", i, removed, err)
		}
		if removed, err = d.DeleteRecord(zone, node, ptr); err != nil || removed != want {
			t.Fatalf("delete PTR #%d: %v %v", i, removed, err)
		}
	}
	if got := dig(t, s, "web01.gaia.dev", "A"); got != "" {
		t.Errorf("dig A after delete = %q", got)
	}
	if recs, _ := d.Records("gaia.dev", "web01"); len(recs) != 0 {
		t.Errorf("records after delete = %+v", recs)
	}

	// As in Windows, any account may add a new name to a zone (secure
	// dynamic update), but not change another's.
	if _, err := d.SetRecord("gaia.dev", "web03", a, false); err != nil {
		t.Fatal(err)
	}
	r, err := Open(ctx, Config{Endpoint: s.URL, Username: "gaia-reader@gaia.dev", Password: s.Password("gaia-reader"), CA: s.CA, ServerName: "dc1.gaia.dev"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if _, err := r.SetRecord("gaia.dev", "web03", other, true); err == nil {
		t.Error("the reader changed the writer's record")
	}
}
