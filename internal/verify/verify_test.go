package verify

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/evidence"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigTemplateAndDefaults(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, `{"vm":"web1","template":"linux-basic","machine":{"mac":"52:54:00:aa:bb:cc"},
		"checks":[{"name":"health","type":"http","port":8080,"path":"/health","expect_body":"ok"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Checks) != 3 || c.Checks[0].Type != CheckBootSerial || c.Checks[1].Port != 22 || c.Checks[2].ExpectStatus != 200 {
		t.Fatalf("checks %+v", c.Checks)
	}
	if c.Machine.Arch != "x86_64" || c.Machine.Firmware != "bios" || c.BootTimeoutS != 300 || c.Machine.MemoryMiB != 1024 {
		t.Fatalf("defaults %+v", c)
	}
}

func TestConfigRejects(t *testing.T) {
	mac := `"machine":{"mac":"52:54:00:aa:bb:cc"}`
	for want, body := range map[string]string{
		"vm is required":      `{"template":"linux-basic",` + mac + `}`,
		"machine.mac":         `{"vm":"a","template":"linux-basic"}`,
		"unknown template":    `{"vm":"a","template":"windows",` + mac + `}`,
		"exactly one":         `{"vm":"a",` + mac + `,"checks":[{"name":"x","type":"tcp","port":1}]}`,
		"duplicate":           `{"vm":"a","template":"linux-basic",` + mac + `,"checks":[{"name":"ssh","type":"tcp","port":2}]}`,
		"port 0":              `{"vm":"a","template":"linux-boot",` + mac + `,"checks":[{"name":"x","type":"tcp"}]}`,
		"unknown type":        `{"vm":"a","template":"linux-boot",` + mac + `,"checks":[{"name":"x","type":"icmp"}]}`,
		"unknown field":       `{"vm":"a","template":"linux-boot",` + mac + `,"probes":[]}`,
		"pattern":             `{"vm":"a",` + mac + `,"checks":[{"name":"b","type":"boot_serial","pattern":"("}]}`,
		"machine.arch":        `{"vm":"a","template":"linux-boot","machine":{"mac":"52:54:00:aa:bb:cc","arch":"riscv"}}`,
		"invalid MAC address": `{"vm":"a","template":"linux-boot","machine":{"mac":"nope"}}`,
	} {
		_, err := LoadConfig(writeConfig(t, body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %v", want, err)
		}
	}
}

func TestConfigWindowsBootProbe(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, `{"vm":"win1","template":"windows-basic","machine":{"mac":"00:50:56:aa:bb:cc"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Checks) != 2 || !c.Checks[0].IsBoot() || c.Checks[0].Type != CheckRDP || c.Checks[1].IsBoot() || c.Checks[1].ExpectStatus != 405 {
		t.Fatalf("checks %+v", c.Checks)
	}
	mac := `"machine":{"mac":"52:54:00:aa:bb:cc"}`
	if _, err := LoadConfig(writeConfig(t, `{"vm":"a",`+mac+`,"checks":[{"name":"up","type":"tcp","port":22,"boot":true}]}`)); err != nil {
		t.Fatalf("a tcp boot check: %v", err)
	}
	if _, err := LoadConfig(writeConfig(t, `{"vm":"a","template":"linux-boot",`+mac+`,"checks":[{"name":"up","type":"rdp","port":3389,"boot":true}]}`)); err == nil ||
		!strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("two boot checks: %v", err)
	}
}

// fakeRDP serves one canned reply per connection after reading the Connection Request.
func fakeRDP(t *testing.T, reply []byte) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				req := make([]byte, len(rdpConnectionRequest))
				if _, err := io.ReadFull(c, req); err != nil || !bytes.Equal(req, rdpConnectionRequest) {
					return
				}
				if reply != nil {
					c.Write(reply)
				}
				time.Sleep(200 * time.Millisecond)
			}()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func TestRDPProbe(t *testing.T) {
	ctx := context.Background()
	cc := func(neg ...byte) []byte {
		x224 := append([]byte{6, 0xd0, 0, 0, 0x12, 0x34, 0}, neg...)
		return append([]byte{3, 0, 0, byte(4 + len(x224))}, x224...)
	}
	for _, tc := range []struct {
		reply []byte
		ok    bool
		want  string
	}{
		{cc(0x02, 0x1f, 0x08, 0x00, 0x02, 0, 0, 0), true, "selected protocol CredSSP"},
		{cc(0x03, 0x00, 0x08, 0x00, 0x05, 0, 0, 0), true, "RDP_NEG_FAILURE code 5"},
		{cc(), true, "standard RDP security"},
		{[]byte("HTTP/1.1 400 Bad Request\r\n\r\n"), false, "not a TPKT reply"},
		{[]byte{3, 0, 0, 11, 6, 0xe0, 0, 0, 0, 0, 0}, false, "not an X.224 Connection Confirm"},
		{nil, false, "no TPKT header"},
	} {
		r := Probe(ctx, Check{Type: CheckRDP, Port: fakeRDP(t, tc.reply)}, "127.0.0.1", 1500*time.Millisecond)
		if r.OK != tc.ok || !strings.Contains(r.Detail, tc.want) {
			t.Errorf("reply % x: %+v, want ok=%v and %q", tc.reply, r, tc.ok, tc.want)
		}
	}
}

func TestWorst(t *testing.T) {
	for want, in := range map[string][]string{
		OK: {OK, OK}, Warning: {OK, Warning}, Unknown: {OK, Unknown, Warning}, Critical: {Unknown, Critical, OK},
	} {
		if got := Worst(in...); got != want {
			t.Errorf("Worst(%v) = %s", in, got)
		}
	}
	if Worst() != Unknown || Worst("bogus") != Unknown || ExitCode("bogus") != 3 || ExitCode(Critical) != 2 {
		t.Fatal("unknowns must not be OK")
	}
}

func TestProbes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, "status: ok\n")
		case "/down":
			w.WriteHeader(503)
		default:
			fmt.Fprint(w, "hello")
		}
	}))
	defer srv.Close()
	host, ps, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	port, _ := strconv.Atoi(ps)
	ctx := context.Background()

	if r := Probe(ctx, Check{Type: CheckTCP, Port: port}, host, 5*time.Second); !r.OK || r.Attempts != 1 {
		t.Fatalf("tcp %+v", r)
	}
	if r := Probe(ctx, Check{Type: CheckHTTP, Port: port, Path: "/health", ExpectStatus: 200, ExpectBody: "ok"}, host, 5*time.Second); !r.OK || r.Status != 200 {
		t.Fatalf("http %+v", r)
	}
	if r := Probe(ctx, Check{Type: CheckHTTP, Port: port, Path: "/down", ExpectStatus: 200}, host, 3*time.Second); r.OK || r.Status != 503 || r.Attempts < 2 {
		t.Fatalf("http 503 %+v", r)
	}
	if r := Probe(ctx, Check{Type: CheckHTTP, Port: port, Path: "/", ExpectStatus: 200, ExpectBody: "ok"}, host, 1*time.Second); r.OK || !strings.Contains(r.Detail, "body does not contain") {
		t.Fatalf("http body %+v", r)
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if r := Probe(ctx, Check{Type: CheckTCP, Port: closed}, "127.0.0.1", 1*time.Second); r.OK || !strings.Contains(r.Detail, "gave up") {
		t.Fatalf("closed port %+v", r)
	}
}

func TestPPMToPNG(t *testing.T) {
	ppm := []byte("P6\n# qemu\n3 2\n255\n")
	pix := []byte{255, 0, 0, 0, 255, 0, 0, 0, 255, 10, 20, 30, 40, 50, 60, 70, 80, 90}
	out, err := ppmToPNG(append(ppm, pix...))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 3 || b.Dy() != 2 {
		t.Fatalf("size %v", b)
	}
	if r, g, b, _ := img.At(2, 0).RGBA(); r != 0 || g != 0 || b != 0xffff {
		t.Fatalf("pixel (2,0) = %d %d %d", r, g, b)
	}
	if r, _, _, _ := img.At(2, 1).RGBA(); r>>8 != 70 {
		t.Fatalf("pixel (2,1) red %d", r>>8)
	}
	for _, bad := range []string{"P5\n1 1\n255\n\x00", "P6\n1 1\n65535\n\x00\x00", "P6\n2 2\n255\n\x00"} {
		if _, err := ppmToPNG([]byte(bad)); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

// Without an image (e.g. the conversion failed), Run still seals a bundle: the pre-checks,
// every VM check UNKNOWN, the extra files, and a ledger entry.
func TestRunWithoutImageSealsPreChecks(t *testing.T) {
	dir := t.TempDir()
	if _, err := evidence.GenerateKey(filepath.Join(dir, "k.pem"), filepath.Join(dir, "p.pem")); err != nil {
		t.Fatal(err)
	}
	key, _ := evidence.LoadPrivateKey(filepath.Join(dir, "k.pem"))
	pub, _ := evidence.LoadPublicKey(filepath.Join(dir, "p.pem"))
	cfg, err := LoadConfig(writeConfig(t, `{"vm":"vm1","template":"linux-basic","machine":{"mac":"52:54:00:aa:bb:cc"}}`))
	if err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(dir, "v2v.log")
	os.WriteFile(extra, []byte("virt-v2v: error: no root device found\n"), 0o644)
	res, err := Run(context.Background(), Options{Config: cfg, EvidenceDir: filepath.Join(dir, "ev"), Ledger: filepath.Join(dir, "ledger.jsonl"),
		Key: key, WorkDir: dir, SerialDir: dir, SkipReason: "conversion failed",
		PreChecks: []CheckResult{{Name: "conversion", Type: "virt-v2v", Verdict: Critical, Detail: "exit 1"}},
		Extra:     map[string]string{"migration/v2v.log": extra}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != Critical || len(res.Checks) != 3 || res.Checks[1].Verdict != Unknown || res.LedgerSeq != 1 {
		t.Fatalf("result %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(res.BundleDir, "migration", "v2v.log")); !strings.Contains(string(b), "no root device") {
		t.Fatal("extra file not in the bundle")
	}
	lr, err := evidence.VerifyLedger(filepath.Join(dir, "ledger.jsonl"), filepath.Join(dir, "ev"), pub)
	if err != nil || !lr.Intact {
		t.Fatalf("ledger %+v %v", lr, err)
	}
}
