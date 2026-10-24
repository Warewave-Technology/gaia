package ipam

import (
	"net/netip"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/ipam/db"
)

func pool(cidr, gw, start, end string) db.Pool {
	p := db.Pool{Cidr: netip.MustParsePrefix(cidr), RangeStart: netip.MustParseAddr(start), RangeEnd: netip.MustParseAddr(end)}
	if gw != "" {
		a := netip.MustParseAddr(gw)
		p.Gateway = &a
	}
	return p
}

func addrs(s ...string) []netip.Addr {
	var out []netip.Addr
	for _, a := range s {
		out = append(out, netip.MustParseAddr(a))
	}
	return out
}

func TestNextFree(t *testing.T) {
	for _, tc := range []struct {
		name string
		pool db.Pool
		used []netip.Addr
		want string // "" = exhausted
	}{
		{"first of range", pool("10.50.0.0/24", "10.50.0.1", "10.50.0.100", "10.50.0.199"), nil, "10.50.0.100"},
		{"skips used", pool("10.50.0.0/24", "10.50.0.1", "10.50.0.100", "10.50.0.199"), addrs("10.50.0.100", "10.50.0.101"), "10.50.0.102"},
		{"fills a gap", pool("10.50.0.0/24", "", "10.50.0.100", "10.50.0.199"), addrs("10.50.0.100", "10.50.0.102"), "10.50.0.101"},
		{"skips network and gateway", pool("10.50.0.0/24", "10.50.0.1", "10.50.0.0", "10.50.0.3"), nil, "10.50.0.2"},
		{"skips broadcast", pool("10.50.0.0/30", "", "10.50.0.3", "10.50.0.3"), nil, ""},
		{"exhausted", pool("10.50.0.0/24", "", "10.50.0.100", "10.50.0.101"), addrs("10.50.0.100", "10.50.0.101"), ""},
		{"/31 has no network or broadcast", pool("10.50.0.0/31", "", "10.50.0.0", "10.50.0.1"), nil, "10.50.0.0"},
	} {
		got, ok := nextFree(tc.pool, tc.used)
		if tc.want == "" {
			if ok {
				t.Errorf("%s: got %s, want exhausted", tc.name, got)
			}
			continue
		}
		if !ok || got.String() != tc.want {
			t.Errorf("%s: got %v (%v), want %s", tc.name, got, ok, tc.want)
		}
	}
}

func TestLastAddr(t *testing.T) {
	for cidr, want := range map[string]string{
		"10.50.0.0/24": "10.50.0.255", "10.0.0.0/8": "10.255.255.255", "10.50.0.4/30": "10.50.0.7", "10.50.0.9/32": "10.50.0.9",
	} {
		if got := lastAddr(netip.MustParsePrefix(cidr)).String(); got != want {
			t.Errorf("lastAddr(%s) = %s, want %s", cidr, got, want)
		}
	}
}
