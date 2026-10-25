package ad

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/itest"
)

func TestComputerName(t *testing.T) {
	if n, err := ComputerName("labtpoc01"); err != nil || n != "LABTPOC01" {
		t.Errorf("name = %s %v", n, err)
	}
	for _, bad := range []string{"", "a-name-of-16-chr", "web.lab"} {
		if _, err := ComputerName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	p, err := OneTimePassword()
	if err != nil || len(p) != 24 || strings.Trim(p, "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789") != "" {
		t.Errorf("password = %q %v", p, err)
	}
}

func TestComputerAccounts(t *testing.T) {
	itest.Require(t)
	s := itest.StartSamba(t, filepath.Join("..", "..", "deploy", "dev", "ad-seed.sh"))
	ctx := context.Background()
	open := func(user string) *Directory {
		d, err := Open(ctx, Config{Endpoint: s.URL, Username: user + "@gaia.dev", Password: s.Password(user), CA: s.CA, ServerName: "dc1.gaia.dev"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		return d
	}
	d := open("gaia-writer")
	if d.Domain() != "gaia.dev" {
		t.Errorf("domain = %s", d.Domain())
	}
	ou := "OU=Servers,DC=gaia,DC=dev"
	otp, _ := OneTimePassword()
	c, err := d.CreateComputer(ou, "labtpoc01", "gaia.uid=u1", otp)
	if err != nil {
		t.Fatal(err)
	}
	if c.DN != "CN=LABTPOC01,"+ou || c.Name != "LABTPOC01" || c.GUID == "" || c.Joined || c.Disabled || c.Info != "gaia.uid=u1" {
		t.Errorf("computer = %+v", c)
	}
	if again, err := d.ComputerByGUID(c.GUID); err != nil || again.DN != c.DN {
		t.Errorf("by GUID = %+v %v", again, err)
	}
	// The one-time password works: the account can bind with it.
	if j, err := Open(ctx, Config{Endpoint: s.URL, Username: "LABTPOC01$@gaia.dev", Password: otp, CA: s.CA, ServerName: "dc1.gaia.dev"}); err != nil {
		t.Errorf("bind with the one-time password: %v", err)
	} else {
		_ = j.Close()
	}
	if _, err := d.CreateComputer(ou, "labtpoc01", "", otp); err == nil {
		t.Error("created the account twice")
	}
	if err := d.ResetComputer(c.DN, "Another-1-Password"); err != nil {
		t.Error(err)
	}
	// Elsewhere Gaia may not create accounts; the reader nowhere.
	if _, err := d.CreateComputer("CN=Computers,DC=gaia,DC=dev", "elsewhere", "", otp); err == nil {
		t.Error("the writer created an account outside its OU")
	}
	if _, err := open("gaia-reader").CreateComputer(ou, "labtpoc02", "", otp); err == nil {
		t.Error("the reader created an account")
	}
	for _, on := range []bool{false, false, true} {
		if err := d.SetComputerEnabled(c.DN, on); err != nil {
			t.Fatal(err)
		}
		if got, _ := d.ComputerByGUID(c.GUID); got.Disabled == on {
			t.Errorf("enabled %v: %+v", on, got)
		}
	}
	if err := d.DeleteComputer(c.DN); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ComputerByGUID(c.GUID); !errors.Is(err, ErrNoComputer) {
		t.Errorf("after delete: %v", err)
	}
}
