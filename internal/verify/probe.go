package verify

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ProbeResult is what one probe saw. It is printed as JSON by `gaia probe`, which runs
// inside the sandbox's network namespace.
type ProbeResult struct {
	OK       bool    `json:"ok"`
	Attempts int     `json:"attempts"`
	Seconds  float64 `json:"seconds"` // until success, or until giving up
	Detail   string  `json:"detail"`
	Status   int     `json:"status,omitempty"`
	BodyHead string  `json:"body_head,omitempty"`
}

// Probe retries check c against host until it passes or timeout expires.
// Only tcp and http checks are probes.
func Probe(ctx context.Context, c Check, host string, timeout time.Duration) ProbeResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var r ProbeResult
	for {
		r.Attempts++
		ok, detail, status, body := probeOnce(ctx, c, host)
		r.Detail, r.Status, r.BodyHead = detail, status, body
		if ok {
			r.OK = true
			break
		}
		select {
		case <-ctx.Done():
			r.Detail = fmt.Sprintf("gave up after %s: %s", timeout, detail)
			r.Seconds = time.Since(start).Seconds()
			return r
		case <-time.After(2 * time.Second):
		}
	}
	r.Seconds = time.Since(start).Seconds()
	return r
}

func probeOnce(ctx context.Context, c Check, host string) (ok bool, detail string, status int, body string) {
	addr := net.JoinHostPort(host, strconv.Itoa(c.Port))
	actx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	switch c.Type {
	case CheckTCP:
		var d net.Dialer
		conn, err := d.DialContext(actx, "tcp", addr)
		if err != nil {
			return false, err.Error(), 0, ""
		}
		conn.Close()
		return true, "connected to " + addr, 0, ""
	case CheckHTTP:
		url := "http://" + addr + c.Path
		req, err := http.NewRequestWithContext(actx, http.MethodGet, url, nil)
		if err != nil {
			return false, err.Error(), 0, ""
		}
		resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			return false, err.Error(), 0, ""
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		head := string(b[:min(len(b), 256)])
		switch {
		case resp.StatusCode != c.ExpectStatus:
			return false, fmt.Sprintf("GET %s: status %d, want %d", url, resp.StatusCode, c.ExpectStatus), resp.StatusCode, head
		case c.ExpectBody != "" && !strings.Contains(string(b), c.ExpectBody):
			return false, fmt.Sprintf("GET %s: body does not contain %q", url, c.ExpectBody), resp.StatusCode, head
		}
		return true, fmt.Sprintf("GET %s: %d", url, resp.StatusCode), resp.StatusCode, head
	case CheckRDP:
		var d net.Dialer
		conn, err := d.DialContext(actx, "tcp", addr)
		if err != nil {
			return false, err.Error(), 0, ""
		}
		defer conn.Close()
		if dl, ok := actx.Deadline(); ok {
			conn.SetDeadline(dl)
		}
		ok, detail := rdpHandshake(conn)
		return ok, addr + ": " + detail, 0, ""
	case CheckWinRM:
		return winrmIdentify(actx, addr)
	}
	return false, "not a probe: " + c.Type, 0, ""
}

// winrmIdentifyRequest is a WS-Management Identify (DSP0226 §11), the request Test-WSMan sends without credentials.
const winrmIdentifyRequest = `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" ` +
	`xmlns:wsmid="http://schemas.dmtf.org/wbem/wsman/identity/1/wsmanidentity.xsd"><s:Header/><s:Body><wsmid:Identify/></s:Body></s:Envelope>`

// winrmIdentify proves a WinRM listener, without credentials, in one of two ways (ADR-005 §3):
//   - an unauthenticated WS-Management Identify, answered with a SOAP IdentifyResponse naming a WS-Management
//     protocol and a vendor (WinRM configured to allow it);
//   - Windows Server's default, which refuses an unauthenticated Identify with 401 (T21 lab run): the first message of
//     an NTLM negotiation (MS-NLMP 2.2.1.1) on /wsman, answered with an NTLM CHALLENGE that carries the computer's
//     names and Windows version. Only Windows' WinRM service registers /wsman with HTTP.sys and has SSPI answer it.
//
// A port that is merely open, any other HTTP server, or a 401 without a Windows NTLM challenge does not pass. The
// server's own reply is kept as evidence.
func winrmIdentify(ctx context.Context, addr string) (ok bool, detail string, status int, body string) {
	var last string
	for _, path := range []string{"/wsman-anon/identify", "/wsman"} {
		url := "http://" + addr + path
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(winrmIdentifyRequest))
		if err != nil {
			return false, err.Error(), 0, ""
		}
		req.Header.Set("Content-Type", "application/soap+xml;charset=UTF-8")
		req.Header.Set("WSMANIDENTIFY", "unauthenticated")
		resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			return false, err.Error(), 0, ""
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
		resp.Body.Close()
		head := string(b[:min(len(b), 1024)])
		if resp.StatusCode == http.StatusNotFound {
			last = fmt.Sprintf("POST %s: 404", url)
			continue
		}
		var r struct {
			XMLName xml.Name
			Body    struct {
				Identify *struct {
					ProtocolVersion string `xml:"ProtocolVersion"`
					ProductVendor   string `xml:"ProductVendor"`
					ProductVersion  string `xml:"ProductVersion"`
				} `xml:"http://schemas.dmtf.org/wbem/wsman/identity/1/wsmanidentity.xsd IdentifyResponse"`
			} `xml:"http://www.w3.org/2003/05/soap-envelope Body"`
		}
		if resp.StatusCode == http.StatusUnauthorized && path == "/wsman" && strings.Contains(resp.Header.Get("WWW-Authenticate"), "Negotiate") {
			return winrmNTLM(ctx, url, resp.Header.Get("Server"))
		}
		if resp.StatusCode != http.StatusOK {
			return false, fmt.Sprintf("POST %s: status %d, want 200 with an IdentifyResponse", url, resp.StatusCode), resp.StatusCode, head
		}
		if err := xml.Unmarshal(b, &r); err != nil || r.Body.Identify == nil {
			return false, fmt.Sprintf("POST %s: 200, but not a WS-Management IdentifyResponse", url), resp.StatusCode, head
		}
		id := r.Body.Identify
		if !strings.Contains(id.ProtocolVersion, "wsman") || id.ProductVendor == "" {
			return false, fmt.Sprintf("POST %s: IdentifyResponse without a WS-Management protocol or vendor", url), resp.StatusCode, head
		}
		return true, fmt.Sprintf("POST %s: WS-Management IdentifyResponse, vendor %q, version %q, protocol %s, server %q (reply %q)",
			url, id.ProductVendor, id.ProductVersion, id.ProtocolVersion, resp.Header.Get("Server"), head), resp.StatusCode, head
	}
	return false, last + ": no WS-Management endpoint", http.StatusNotFound, ""
}

// ntlmNegotiate is an NTLM NEGOTIATE_MESSAGE (MS-NLMP 2.2.1.1) asking for Unicode, NTLM, the target's information and
// its version, with no domain or workstation.
var ntlmNegotiate = func() []byte {
	m := append([]byte("NTLMSSP\x00"), 1, 0, 0, 0)
	m = binary.LittleEndian.AppendUint32(m, 0x00000001|0x00000004|0x00000200|0x00008000|0x00080000|0x00800000|0x02000000|0x20000000|0x80000000)
	m = append(m, make([]byte, 16)...)                 // empty DomainNameFields, WorkstationFields
	return append(m, 10, 0, 0x7c, 0x4f, 0, 0, 0, 0x0f) // Version: 10.0 build 20348, NTLMSSP_REVISION_W2K3
}()

// winrmNTLM sends the NTLM NEGOTIATE to WinRM and reads the CHALLENGE in its 401 (MS-NLMP 2.2.1.2): the computer's
// NetBIOS and DNS names and its Windows version, which the guest's own SSPI fills in.
func winrmNTLM(ctx context.Context, url, server string) (bool, string, int, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(winrmIdentifyRequest))
	if err != nil {
		return false, err.Error(), 0, ""
	}
	req.Header.Set("Content-Type", "application/soap+xml;charset=UTF-8")
	req.Header.Set("Authorization", "Negotiate "+base64.StdEncoding.EncodeToString(ntlmNegotiate))
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return false, err.Error(), 0, ""
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 16384))
	resp.Body.Close()
	auth := resp.Header.Get("WWW-Authenticate")
	tok, found := strings.CutPrefix(auth, "Negotiate ")
	if resp.StatusCode != http.StatusUnauthorized || !found {
		return false, fmt.Sprintf("POST %s with an NTLM NEGOTIATE: status %d, %q: no NTLM challenge", url, resp.StatusCode, auth), resp.StatusCode, auth
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(tok))
	if err != nil {
		return false, fmt.Sprintf("POST %s: the challenge is not base64: %v", url, err), resp.StatusCode, auth
	}
	ch, err := parseNTLMChallenge(raw)
	if err != nil {
		return false, fmt.Sprintf("POST %s: not an NTLM CHALLENGE: %v (reply % x)", url, err, raw[:min(len(raw), 64)]), resp.StatusCode, auth
	}
	return true, fmt.Sprintf("POST %s: unauthenticated Identify refused (401, as Windows Server's default), and the WinRM listener "+
		"answered an NTLM NEGOTIATE with a CHALLENGE from computer %q (DNS %q, domain %q), Windows %s, server %q (challenge % x)",
		url, ch.nbComputer, ch.dnsComputer, ch.nbDomain, ch.version, server, raw), resp.StatusCode, auth
}

type ntlmChallenge struct {
	nbComputer, dnsComputer, nbDomain, version string
}

// parseNTLMChallenge reads a CHALLENGE_MESSAGE's target information (AV pairs) and version.
func parseNTLMChallenge(b []byte) (ntlmChallenge, error) {
	var c ntlmChallenge
	if len(b) < 48 || string(b[:8]) != "NTLMSSP\x00" || binary.LittleEndian.Uint32(b[8:12]) != 2 {
		return c, fmt.Errorf("no NTLMSSP type 2 header")
	}
	flags := binary.LittleEndian.Uint32(b[20:24])
	tlen, toff := int(binary.LittleEndian.Uint16(b[40:42])), int(binary.LittleEndian.Uint32(b[44:48]))
	if toff < 48 || toff+tlen > len(b) || tlen == 0 {
		return c, fmt.Errorf("target information out of range (%d bytes at %d)", tlen, toff)
	}
	if flags&0x02000000 != 0 && len(b) >= 56 {
		c.version = fmt.Sprintf("%d.%d build %d", b[48], b[49], binary.LittleEndian.Uint16(b[50:52]))
	} else {
		c.version = "(no version)"
	}
	utf16 := func(v []byte) string {
		r := make([]rune, 0, len(v)/2)
		for i := 0; i+1 < len(v); i += 2 {
			r = append(r, rune(binary.LittleEndian.Uint16(v[i:])))
		}
		return string(r)
	}
	for p := b[toff : toff+tlen]; len(p) >= 4; {
		id, n := binary.LittleEndian.Uint16(p), int(binary.LittleEndian.Uint16(p[2:]))
		if id == 0 {
			break
		}
		if 4+n > len(p) {
			return c, fmt.Errorf("an AV pair runs past the target information")
		}
		switch id {
		case 1:
			c.nbComputer = utf16(p[4 : 4+n])
		case 2:
			c.nbDomain = utf16(p[4 : 4+n])
		case 3:
			c.dnsComputer = utf16(p[4 : 4+n])
		}
		p = p[4+n:]
	}
	if c.nbComputer == "" {
		return c, fmt.Errorf("no computer name in the target information")
	}
	return c, nil
}

// rdpConnectionRequest is a TPKT-framed X.224 Connection Request carrying an RDP Negotiation
// Request for TLS and CredSSP (MS-RDPBCGR 2.2.1.1): the first message of every RDP connection.
var rdpConnectionRequest = []byte{
	0x03, 0x00, 0x00, 0x13, // TPKT: version 3, length 19
	0x0e, 0xe0, 0x00, 0x00, 0x00, 0x00, 0x00, // X.224: length 14, Connection Request, dst-ref 0, src-ref 0, class 0
	0x01, 0x00, 0x08, 0x00, 0x03, 0x00, 0x00, 0x00, // RDP_NEG_REQ: PROTOCOL_SSL | PROTOCOL_HYBRID
}

// rdpHandshake sends the Connection Request and accepts only an X.224 Connection Confirm: a port
// that is merely open, or anything that is not an RDP server, does not pass.
func rdpHandshake(conn net.Conn) (bool, string) {
	if _, err := conn.Write(rdpConnectionRequest); err != nil {
		return false, "sending the X.224 Connection Request: " + err.Error()
	}
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return false, "no TPKT header in reply: " + err.Error()
	}
	n := int(binary.BigEndian.Uint16(hdr[2:]))
	if hdr[0] != 3 || n < 11 || n > 512 {
		return false, fmt.Sprintf("not a TPKT reply: % x", hdr)
	}
	body := make([]byte, n-4)
	if _, err := io.ReadFull(conn, body); err != nil {
		return false, "short TPKT reply: " + err.Error()
	}
	// X.224 CC: LI, code 0xd0, dst-ref, src-ref, class (7 bytes), then the RDP negotiation response, which LI counts.
	if int(body[0]) < 6 || int(body[0]) >= len(body) || body[1] != 0xd0 {
		return false, fmt.Sprintf("not an X.224 Connection Confirm: % x", body[:min(len(body), 16)])
	}
	reply := fmt.Sprintf(" (reply % x)", append(hdr, body...)) // the server's own bytes, as evidence
	neg := body[7 : 1+int(body[0])]
	if len(neg) < 8 {
		return true, "X.224 Connection Confirm, no RDP negotiation response: standard RDP security" + reply
	}
	v := binary.LittleEndian.Uint32(neg[4:8])
	switch neg[0] {
	case 0x02: // RDP_NEG_RSP
		names := map[uint32]string{0: "RDP", 1: "TLS", 2: "CredSSP", 8: "RDSTLS", 0x10: "CredSSP-EX"}
		name, ok := names[v]
		if !ok {
			name = fmt.Sprintf("0x%x", v)
		}
		return true, "X.224 Connection Confirm, RDP_NEG_RSP selected protocol " + name + reply
	case 0x03: // RDP_NEG_FAILURE: an RDP server that refused the requested security
		return true, fmt.Sprintf("X.224 Connection Confirm, RDP_NEG_FAILURE code %d%s", v, reply)
	}
	return false, fmt.Sprintf("X.224 Connection Confirm with an unknown negotiation type 0x%02x%s", neg[0], reply)
}
