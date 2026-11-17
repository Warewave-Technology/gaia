// Command notify-sink is a lab fixture for T19 (lab/lab-vm/t19-notify.sh): a test SMTP server and a webhook receiver
// that keep every message they get, so the runner can check what Gaia sent. Not part of the product.
//
// SMTP: STARTTLS is required before AUTH PLAIN, and AUTH (with -smtp-user and the password in $SINK_SMTP_PASSWORD)
// before MAIL. Each message is written to DIR/mail/NNNN.eml. Webhook: HTTPS POST to any path; each request is
// written to DIR/hook/NNNN.json as {"headers": {...}, "body": <the body as a string>}. Both use -cert/-key.
//
//	notify-sink -dir DIR -smtp 127.0.0.1:2587 -https 127.0.0.1:2443 -cert server.crt -key server.key -smtp-user gaia
package main

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	mu  sync.Mutex
	seq int
)

// save writes b to DIR/sub/NNNN.ext, numbering messages of both kinds in one sequence.
func save(dir, sub, ext string, b []byte) (string, error) {
	mu.Lock()
	seq++
	n := seq
	mu.Unlock()
	p := filepath.Join(dir, sub, fmt.Sprintf("%04d.%s", n, ext))
	return p, os.WriteFile(p, b, 0o644)
}

func main() {
	dir := flag.String("dir", "", "where messages go")
	smtpAddr := flag.String("smtp", "127.0.0.1:2587", "SMTP listen address")
	httpsAddr := flag.String("https", "127.0.0.1:2443", "HTTPS listen address")
	certFile := flag.String("cert", "", "server certificate (PEM)")
	keyFile := flag.String("key", "", "server key (PEM)")
	user := flag.String("smtp-user", "gaia", "the SMTP user")
	flag.Parse()
	pw := os.Getenv("SINK_SMTP_PASSWORD")
	if *dir == "" || *certFile == "" || *keyFile == "" || pw == "" {
		log.Fatal("-dir, -cert, -key and $SINK_SMTP_PASSWORD are required")
	}
	cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		log.Fatal(err)
	}
	for _, sub := range []string{"mail", "hook"} {
		if err := os.MkdirAll(filepath.Join(*dir, sub), 0o755); err != nil {
			log.Fatal(err)
		}
	}
	tc := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	ln, err := net.Listen("tcp", *smtpAddr)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				log.Fatal(err)
			}
			go smtpSession(c, tc, *dir, *user, pw)
		}
	}()
	srv := &http.Server{Addr: *httpsAddr, TLSConfig: tc, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil || r.Method != http.MethodPost {
			http.Error(w, "POST a body", http.StatusBadRequest)
			return
		}
		h := map[string]string{}
		for k := range r.Header {
			h[k] = r.Header.Get(k)
		}
		out, _ := json.Marshal(map[string]any{"path": r.URL.Path, "headers": h, "body": string(b)})
		p, err := save(*dir, "hook", "json", out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("webhook %s %s -> %s", r.Header.Get("X-Gaia-Event"), r.Header.Get("X-Gaia-Delivery"), p)
		w.WriteHeader(http.StatusNoContent)
	})}
	log.Printf("SMTP on %s, HTTPS on %s, messages in %s", *smtpAddr, *httpsAddr, *dir)
	log.Fatal(srv.ListenAndServeTLS("", ""))
}

func smtpSession(c net.Conn, tc *tls.Config, dir, user, pw string) {
	defer c.Close()
	r, w := bufio.NewReader(c), bufio.NewWriter(c)
	say := func(l string) { w.WriteString(l + "\r\n"); w.Flush() }
	say("220 notify-sink ESMTP (lab fixture)")
	secure, authed, rcpts := false, false, 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			say("500 empty")
			continue
		}
		switch strings.ToUpper(f[0]) {
		case "EHLO", "HELO":
			if secure {
				say("250-notify-sink\r\n250-AUTH PLAIN\r\n250 8BITMIME")
			} else {
				say("250-notify-sink\r\n250 STARTTLS")
			}
		case "STARTTLS":
			say("220 ready for TLS")
			t := tls.Server(c, tc)
			if err := t.Handshake(); err != nil {
				log.Printf("smtp: TLS: %v", err)
				return
			}
			c, secure, authed = t, true, false
			r, w = bufio.NewReader(t), bufio.NewWriter(t)
		case "AUTH":
			b, _ := base64.StdEncoding.DecodeString(f[len(f)-1])
			if !secure || len(f) != 3 || strings.ToUpper(f[1]) != "PLAIN" || string(b) != "\x00"+user+"\x00"+pw {
				log.Printf("smtp: authentication refused (secure %v)", secure)
				say("535 5.7.8 authentication failed")
				continue
			}
			authed = true
			say("235 2.7.0 authenticated")
		case "MAIL":
			if !secure || !authed {
				say("530 5.7.0 STARTTLS and AUTH first")
				continue
			}
			rcpts = 0
			say("250 ok")
		case "RCPT":
			if !authed {
				say("530 5.7.0 AUTH first")
				continue
			}
			rcpts++
			say("250 ok")
		case "DATA":
			if !authed || rcpts == 0 {
				say("503 MAIL and RCPT first")
				continue
			}
			say("354 end with <CRLF>.<CRLF>")
			var data strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				data.WriteString(strings.TrimPrefix(l, "."))
			}
			p, err := save(dir, "mail", "eml", []byte(data.String()))
			if err != nil {
				say("451 " + err.Error())
				continue
			}
			log.Printf("mail -> %s", p)
			say("250 queued as " + filepath.Base(p))
		case "RSET", "NOOP":
			say("250 ok")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("502 not implemented")
		}
	}
}
