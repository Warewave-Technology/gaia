package verify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
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
	if len(c.Checks) != 1 || !c.Checks[0].IsBoot() || c.Checks[0].Type != CheckWinRM || c.Checks[0].Port != 5985 {
		t.Fatalf("checks %+v", c.Checks)
	}
	c, err = LoadConfig(writeConfig(t, `{"vm":"win1","template":"windows-rdp","machine":{"mac":"00:50:56:aa:bb:cc"}}`))
	if err != nil || len(c.Checks) != 2 || c.Checks[0].Type != CheckWinRM || c.Checks[1].Type != CheckRDP || c.Checks[1].IsBoot() {
		t.Fatalf("windows-rdp: %v %+v", err, c.Checks)
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
	cc := func(neg ...byte) []byte { // LI counts the negotiation response (MS-RDPBCGR 2.2.1.2)
		x224 := append([]byte{byte(6 + len(neg)), 0xd0, 0, 0, 0x12, 0x34, 0}, neg...)
		return append([]byte{3, 0, 0, byte(4 + len(x224))}, x224...)
	}
	for _, tc := range []struct {
		reply []byte
		ok    bool
		want  string
	}{
		{cc(0x02, 0x1f, 0x08, 0x00, 0x02, 0, 0, 0), true, "selected protocol CredSSP (reply 03 00 00 13 0e d0"},
		// Windows Server 2022's reply, as the T11 lab run recorded it (bundle detail, 2026-11-16).
		{[]byte{0x03, 0x00, 0x00, 0x13, 0x0e, 0xd0, 0x00, 0x00, 0x12, 0x34, 0x00, 0x02, 0x1f, 0x08, 0x00, 0x02, 0x00, 0x00, 0x00}, true, "selected protocol CredSSP"},
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

// The reply Windows Server's WinRM gives an unauthenticated Identify (shape of Test-WSMan's exchange).
const winrmReply = `<s:Envelope xml:lang="en-US" xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Header/><s:Body>` +
	`<wsmid:IdentifyResponse xmlns:wsmid="http://schemas.dmtf.org/wbem/wsman/identity/1/wsmanidentity.xsd">` +
	`<wsmid:ProtocolVersion>http://schemas.dmtf.org/wbem/wsman/1/wsman.xsd</wsmid:ProtocolVersion>` +
	`<wsmid:ProductVendor>Microsoft Corporation</wsmid:ProductVendor><wsmid:ProductVersion>OS: 0.0.0 SP: 0.0 Stack: 3.0</wsmid:ProductVersion>` +
	`</wsmid:IdentifyResponse></s:Body></s:Envelope>`

// The WinRM probe passes only on a WS-Management IdentifyResponse: not on an open port, another HTTP server, an
// HTTP error, or a SOAP reply that is not an Identify response.
func TestProbeWinRM(t *testing.T) {
	ctx := context.Background()
	serve := func(h http.HandlerFunc) int {
		ts := httptest.NewServer(h)
		t.Cleanup(ts.Close)
		_, p, _ := net.SplitHostPort(ts.Listener.Addr().String())
		port, _ := strconv.Atoi(p)
		return port
	}
	windows := serve(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/wsman-anon/identify" || r.Header.Get("WSMANIDENTIFY") != "unauthenticated" ||
			!strings.Contains(string(b), "<wsmid:Identify/>") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Server", "Microsoft-HTTPAPI/2.0")
		w.Header().Set("Content-Type", "application/soap+xml;charset=UTF-8")
		io.WriteString(w, winrmReply)
	})
	onlyWsman := serve(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wsman" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, winrmReply)
	})
	web := serve(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "<html>it works</html>") })
	denied := serve(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	soap := serve(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><x:Fault xmlns:x="urn:x"/></s:Body></s:Envelope>`)
	})
	// Windows Server as installed: no anonymous Identify, /wsman wants Negotiate, and SSPI answers an NTLM NEGOTIATE.
	ntlm := func(challenge []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/wsman" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Server", "Microsoft-HTTPAPI/2.0")
			tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Negotiate ")
			b, _ := base64.StdEncoding.DecodeString(tok)
			if !ok || len(b) < 12 || string(b[:8]) != "NTLMSSP\x00" || b[8] != 1 {
				w.Header().Add("WWW-Authenticate", "Negotiate")
				w.Header().Add("WWW-Authenticate", "Kerberos")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if challenge != nil {
				w.Header().Set("WWW-Authenticate", "Negotiate "+base64.StdEncoding.EncodeToString(challenge))
			} else {
				w.Header().Set("WWW-Authenticate", "Negotiate")
			}
			w.WriteHeader(http.StatusUnauthorized)
		}
	}
	windowsDefault := serve(ntlm(ntlmChallengeFor("GAIA-T11-WIN", "gaia-t11-win", "WORKGROUP", 20348)))
	noChallenge := serve(ntlm(nil))
	garbage := serve(ntlm([]byte("NTLMSSP\x00\x02\x00\x00\x00 not a challenge")))
	nothing := serve(http.NotFound)
	for _, tc := range []struct {
		name string
		port int
		ok   bool
		want string
	}{
		{"windows", windows, true, `vendor "Microsoft Corporation", version "OS: 0.0.0 SP: 0.0 Stack: 3.0", protocol http://schemas.dmtf.org/wbem/wsman/1/wsman.xsd, server "Microsoft-HTTPAPI/2.0"`},
		{"identify on /wsman", onlyWsman, true, "/wsman: WS-Management IdentifyResponse"},
		{"a web server", web, false, "not a WS-Management IdentifyResponse"},
		{"401", denied, false, "status 401"},
		{"another SOAP reply", soap, false, "not a WS-Management IdentifyResponse"},
		{"windows default (NTLM challenge)", windowsDefault, true, `CHALLENGE from computer "GAIA-T11-WIN" (DNS "gaia-t11-win", domain "WORKGROUP"), Windows 10.0 build 20348`},
		{"401 without a challenge", noChallenge, false, "no NTLM challenge"},
		{"a broken challenge", garbage, false, "not an NTLM CHALLENGE"},
		{"no endpoint", nothing, false, "no WS-Management endpoint"},
		{"closed port", freePort(t), false, "refused"},
	} {
		r := Probe(ctx, Check{Type: CheckWinRM, Port: tc.port}, "127.0.0.1", 1500*time.Millisecond)
		if r.OK != tc.ok || !strings.Contains(r.Detail, tc.want) {
			t.Errorf("%s: %+v, want ok=%v and %q", tc.name, r, tc.ok, tc.want)
		}
	}
}

// ntlmChallengeFor builds a CHALLENGE_MESSAGE (MS-NLMP 2.2.1.2) as Windows' SSPI sends it: names as AV pairs, the
// version 10.0 with the given build.
func ntlmChallengeFor(nb, dns, domain string, build uint16) []byte {
	u := func(s string) []byte {
		var b []byte
		for _, r := range s {
			b = binary.LittleEndian.AppendUint16(b, uint16(r))
		}
		return b
	}
	var info []byte
	for _, av := range []struct {
		id uint16
		v  []byte
	}{{2, u(domain)}, {1, u(nb)}, {4, u(dns)}, {3, u(dns)}, {0, nil}} {
		info = binary.LittleEndian.AppendUint16(info, av.id)
		info = binary.LittleEndian.AppendUint16(info, uint16(len(av.v)))
		info = append(info, av.v...)
	}
	m := append([]byte("NTLMSSP\x00"), 2, 0, 0, 0)
	m = append(m, 0, 0, 0, 0, 56, 0, 0, 0) // empty TargetNameFields at 56
	m = binary.LittleEndian.AppendUint32(m, 0x02000000|0x00800000|0x00000001)
	m = append(m, []byte("12345678")...) // server challenge
	m = append(m, make([]byte, 8)...)    // reserved
	m = binary.LittleEndian.AppendUint16(m, uint16(len(info)))
	m = binary.LittleEndian.AppendUint16(m, uint16(len(info)))
	m = binary.LittleEndian.AppendUint32(m, 56)
	m = append(m, 10, 0)
	m = binary.LittleEndian.AppendUint16(m, build)
	m = append(m, 0, 0, 0, 15)
	return append(m, info...)
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
	if res.Verdict != Critical || len(res.Checks) != 3 || res.Checks[1].Verdict != Unknown || res.LedgerSeq != 2 { // seq 1: the genesis key entry
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

// A guest with two disks gets one overlay per disk, in order, on the configured bus.
func TestDomainXMLTwoDisks(t *testing.T) {
	c := &Config{VM: "vm", Template: "linux-boot", Machine: Machine{MAC: "52:54:00:00:00:01"}}
	if err := c.resolve(); err != nil {
		t.Fatal(err)
	}
	x := &run{o: Options{Config: c}, c: c, r: &Result{}}
	x.disks = []DiskImage{{Path: "/i/a.raw", Format: "raw", Target: "vda"}, {Path: "/i/b.raw", Format: "raw"}}
	x.disks[1].Target = defaultTarget(c.Machine.DiskBus, 1)
	x.overlays = []string{"/w/overlay.qcow2", "/w/overlay-vdb.qcow2"}
	xml := x.domainXML()
	for _, want := range []string{"<source file='/w/overlay.qcow2'/><target dev='vda' bus='virtio'/>",
		"<source file='/w/overlay-vdb.qcow2'/><target dev='vdb' bus='virtio'/>"} {
		if !strings.Contains(xml, want) {
			t.Fatalf("missing %q in\n%s", want, xml)
		}
	}
	if strings.Index(xml, "overlay.qcow2") > strings.Index(xml, "overlay-vdb.qcow2") {
		t.Fatal("the boot disk must come first")
	}
	if defaultTarget("sata", 2) != "sdc" {
		t.Fatal(defaultTarget("sata", 2))
	}
}

// freePort is a local port nothing listens on.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// A guest booted by a network probe must keep passing it: one that stops answering (it restarted) or stops is
// CRITICAL with the probe's own detail; one that keeps answering is OK after the whole period.
func TestSettle(t *testing.T) {
	ctx := context.Background()
	k := Check{Name: "boot", Type: CheckWinRM, Port: 5985, Boot: true}
	answers := func(ok ...bool) func(int) CheckResult {
		n := 0
		return func(int) CheckResult {
			n++
			if n <= len(ok) && !ok[n-1] {
				return CheckResult{Verdict: Critical, Detail: "connection refused"}
			}
			return CheckResult{Verdict: OK}
		}
	}
	up := func() error { return nil }
	for _, c := range []struct {
		name  string
		probe func(int) CheckResult
		alive func() error
		want  string
		text  string
	}{
		{"keeps answering", answers(), up, OK, "kept answering for 50ms"},
		{"restarts", answers(true, true, false), up, Critical, "stopped answering"},
		{"stops", answers(), func() error { return errors.New("the guest stopped (libvirt state 5)") }, Critical, "the guest stopped"},
		{"probe could not run", func(int) CheckResult { return CheckResult{Verdict: Unknown, Detail: "probe runner failed"} }, up, Unknown, "probe runner failed"},
	} {
		r := settle(ctx, k, 50*time.Millisecond, 10*time.Millisecond, c.probe, c.alive)
		if r.Verdict != c.want || r.Name != "stable" || !strings.Contains(r.Detail, c.text) {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
	c := Config{VM: "w", Template: "windows-basic"}
	c.Validate() // defaults apply first (a missing MAC is reported after)
	if c.SettleS != 60 {
		t.Errorf("default settle_s: %+v", c)
	}
}

// A guest whose own checks did not pass leaves what it wrote for the caller (Options.KeepDisks, ADR-005 C); one that
// passed, or that never started, leaves nothing.
func TestKeepDisks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		started bool
		checks  []CheckResult
		kept    bool
	}{
		{"stable failed", true, []CheckResult{{Name: "conversion", Verdict: Warning}, {Name: "boot", Verdict: OK}, {Name: "stable", Verdict: Critical}}, true},
		{"all passed", true, []CheckResult{{Name: "conversion", Verdict: Warning}, {Name: "boot", Verdict: OK}, {Name: "stable", Verdict: OK}}, false},
		{"never started", false, []CheckResult{{Name: "conversion", Verdict: OK}, {Name: "boot", Verdict: Critical}}, false},
	} {
		work, keep := t.TempDir(), t.TempDir()
		ov := filepath.Join(work, "overlay.qcow2")
		os.WriteFile(ov, []byte("what the guest wrote"), 0o600)
		x := &run{o: Options{KeepDisks: keep, PreChecks: tc.checks[:1]}, r: &Result{BundleID: "vm1-20261123T080000Z-abc123", Checks: tc.checks},
			overlays: []string{ov}, disks: []DiskImage{{Target: "vda"}}}
		if tc.started {
			x.t0 = time.Now()
		}
		x.keepDisks()
		_, err := os.Stat(filepath.Join(keep, "vm1-20261123T080000Z-abc123-after-vda.qcow2"))
		if (err == nil) != tc.kept || (len(x.r.KeptDisks) == 1) != tc.kept || len(x.r.Errors) > 0 {
			t.Errorf("%s: kept %v (%v), errors %v", tc.name, x.r.KeptDisks, err, x.r.Errors)
		}
		if tc.kept {
			if fi, _ := os.Stat(filepath.Join(keep, x.r.KeptDisks[0])); fi.Mode().Perm() != 0o640 {
				t.Errorf("%s: mode %v", tc.name, fi.Mode())
			}
		}
	}
}
