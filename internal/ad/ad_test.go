package ad

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/itest"
)

func names(us []User) string {
	var out []string
	for _, u := range us {
		s := u.Username
		if u.Disabled {
			s += "(disabled)"
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

func TestReadTheDirectory(t *testing.T) {
	itest.Require(t)
	s := itest.StartSamba(t, filepath.Join("..", "..", "deploy", "dev", "ad-seed.sh"))
	ctx := context.Background()
	cfg := Config{Endpoint: s.URL, Username: "gaia-reader@gaia.dev", Password: s.Password("gaia-reader"), CA: s.CA, ServerName: "dc1.gaia.dev"}
	d, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if d.BaseDN != "DC=gaia,DC=dev" {
		t.Errorf("base DN = %s", d.BaseDN)
	}
	groups, err := d.Groups()
	if err != nil {
		t.Fatal(err)
	}
	var teams []string
	for _, g := range groups {
		if strings.HasSuffix(g.DN, ",OU=Teams,DC=gaia,DC=dev") {
			teams = append(teams, g.CN)
			if g.GUID == "" || g.Description != g.CN {
				t.Errorf("group = %+v", g)
			}
		}
	}
	if strings.Join(teams, ",") != "SG-Team-Platform,SG-Team-Platform-Approvers,SG-Team-Web,SG-Team-Web-Approvers,SG-Web-Contractors" {
		t.Errorf("teams = %v", teams)
	}
	web := "CN=SG-Team-Web,OU=Teams,DC=gaia,DC=dev"
	direct, err := d.Members(web, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(direct); got != "ali(disabled),can" {
		t.Errorf("direct members = %s", got)
	}
	all, err := d.Members(web, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(all); got != "ali(disabled),can,zeynep" {
		t.Errorf("all members = %s", got)
	}
	u, err := d.User("Ayse")
	if err != nil || u.Mail != "ayse@gaia.dev" || u.DisplayName == "" || len(u.GUID) != 36 {
		t.Errorf("ayse = %+v, %v", u, err)
	}

	if byID, err := d.UserByGUID(u.GUID); err != nil || byID.Username != "ayse" {
		t.Errorf("by GUID = %+v, %v", byID, err)
	}
	if _, err := d.UserByGUID("00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNoAccount) {
		t.Errorf("an unknown GUID: %v", err)
	}

	// The certificate names dc1.gaia.dev, not 127.0.0.1; a wrong password
	// does not bind.
	bad := cfg
	bad.ServerName = ""
	if _, err := Open(ctx, bad); err == nil {
		t.Error("connected although the certificate names another host")
	}
	bad = cfg
	bad.Password = "nope"
	if _, err := Open(ctx, bad); err == nil {
		t.Error("bound with a wrong password")
	}
}

func TestAuthenticate(t *testing.T) {
	itest.Require(t)
	s := itest.StartSamba(t, filepath.Join("..", "..", "deploy", "dev", "ad-seed.sh"))
	ctx := context.Background()
	cfg := Config{Endpoint: s.URL, Username: "gaia-reader@gaia.dev", Password: s.Password("gaia-reader"), CA: s.CA, ServerName: "dc1.gaia.dev"}
	u, err := Authenticate(ctx, cfg, " Can ", s.Password("can"))
	if err != nil || u.Username != "can" || u.GUID == "" || u.Mail != "can@gaia.dev" {
		t.Fatalf("can: %+v %v", u, err)
	}
	for _, c := range []struct{ user, password string }{{"can", "wrong"}, {"can", ""}, {"nobody", "x"}, {"", "x"}} {
		if _, err := Authenticate(ctx, cfg, c.user, c.password); !errors.Is(err, ErrBadCredentials) {
			t.Errorf("%s/%q: %v", c.user, c.password, err)
		}
	}
	if _, err := Authenticate(ctx, cfg, "ali", s.Password("ali")); !errors.Is(err, ErrDisabled) {
		t.Errorf("ali: %v", err)
	}
}
