package verify

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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
