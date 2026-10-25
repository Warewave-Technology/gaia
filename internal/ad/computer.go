package ad

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode/utf16"

	"github.com/go-ldap/ldap/v3"
)

// Computer accounts (design 14.6): Gaia creates a server's account in an
// OU before the server exists, with a one-time password the server joins
// with (realm join --one-time-password, or an unsecured join on Windows).
// Joining changes the password, so the account is the server's alone.

// Computer is a computer account.
type Computer struct {
	DN   string
	Name string // sAMAccountName without the $, upper case
	GUID string
	// Joined: a host has joined with the account; it wrote its DNS host
	// name, which Gaia never does.
	Joined   bool
	Disabled bool
	Info     string
}

// ErrNoComputer: the directory has no such computer account.
var ErrNoComputer = errors.New("ad: no such computer account")

var computerAttrs = []string{"sAMAccountName", "objectGUID", "dNSHostName", "userAccountControl", "info"}

// workstationTrust is userAccountControl's WORKSTATION_TRUST_ACCOUNT bit.
const workstationTrust = 0x1000

// Domain is the directory's DNS domain name, as realm join takes it.
func (d *Directory) Domain() string {
	var labels []string
	for _, rdn := range strings.Split(d.root, ",") {
		k, v, ok := strings.Cut(rdn, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "DC") {
			labels = append(labels, strings.ToLower(v))
		}
	}
	return strings.Join(labels, ".")
}

// ComputerName checks a host name can name a computer account (NetBIOS:
// at most 15 characters) and returns it upper case.
func ComputerName(host string) (string, error) {
	if host == "" || len(host) > 15 || strings.ContainsAny(host, ". \\/:*?\"<>|") {
		return "", fmt.Errorf("ad: %q cannot name a computer account (at most 15 characters, no dots)", host)
	}
	return strings.ToUpper(host), nil
}

// Computer finds an account by name, anywhere in the domain.
func (d *Directory) Computer(name string) (Computer, error) {
	return d.computer(fmt.Sprintf("(&(objectClass=computer)(sAMAccountName=%s$))", ldap.EscapeFilter(strings.ToUpper(name))))
}

// ComputerByGUID finds an account by its objectGUID.
func (d *Directory) ComputerByGUID(id string) (Computer, error) {
	raw, err := guidBytes(id)
	if err != nil {
		return Computer{}, err
	}
	var esc strings.Builder
	for _, b := range raw {
		fmt.Fprintf(&esc, "\\%02x", b)
	}
	return d.computer("(&(objectClass=computer)(objectGUID=" + esc.String() + "))")
}

func (d *Directory) computer(filter string) (Computer, error) {
	entries, err := d.search(d.root, filter, computerAttrs)
	if err != nil {
		return Computer{}, err
	}
	if len(entries) == 0 {
		return Computer{}, ErrNoComputer
	}
	e := entries[0]
	u := user(e)
	return Computer{
		DN: e.DN, Name: strings.ToUpper(strings.TrimSuffix(u.Username, "$")), GUID: u.GUID,
		Joined: e.GetAttributeValue("dNSHostName") != "", Disabled: u.Disabled, Info: e.GetAttributeValue("info"),
	}, nil
}

// CreateComputer makes an account in ou with a one-time password; info
// marks it as Gaia's.
func (d *Directory) CreateComputer(ou, name, info, password string) (Computer, error) {
	name, err := ComputerName(name)
	if err != nil {
		return Computer{}, err
	}
	dn := fmt.Sprintf("CN=%s,%s", ldap.EscapeDN(name), ou)
	add := ldap.NewAddRequest(dn, nil)
	add.Attribute("objectClass", []string{"top", "person", "organizationalPerson", "user", "computer"})
	add.Attribute("sAMAccountName", []string{name + "$"})
	add.Attribute("userAccountControl", []string{fmt.Sprint(workstationTrust)})
	add.Attribute("unicodePwd", []string{quotedUTF16(password)})
	if info != "" {
		add.Attribute("info", []string{info})
	}
	if err := d.conn.Add(add); err != nil {
		return Computer{}, fmt.Errorf("ad: create computer %s in %s: %w", name, ou, err)
	}
	return d.Computer(name)
}

// ResetComputer gives an account a new one-time password, so that a host
// can join with it again.
func (d *Directory) ResetComputer(dn, password string) error {
	mod := ldap.NewModifyRequest(dn, nil)
	mod.Replace("unicodePwd", []string{quotedUTF16(password)})
	if err := d.conn.Modify(mod); err != nil {
		return fmt.Errorf("ad: reset %s: %w", dn, err)
	}
	return nil
}

// treeDelete is LDAP_SERVER_TREE_DELETE_OID: a joined computer can hold
// objects of its own (service connection points, recovery keys).
const treeDelete = "1.2.840.113556.1.4.805"

// DeleteComputer deletes an account with what it holds.
func (d *Directory) DeleteComputer(dn string) error {
	if err := d.conn.Del(ldap.NewDelRequest(dn, []ldap.Control{ldap.NewControlString(treeDelete, true, "")})); err != nil {
		return fmt.Errorf("ad: delete %s: %w", dn, err)
	}
	return nil
}

// quotedUTF16 is how unicodePwd takes a password: quoted, UTF-16LE.
func quotedUTF16(s string) string {
	u := utf16.Encode([]rune(`"` + s + `"`))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	return string(b)
}

// OneTimePassword is a random password with upper and lower case letters
// and digits, as domain password rules want; no symbols, so that it
// passes through command lines unquoted.
func OneTimePassword() (string, error) {
	const sets = "ABCDEFGHJKLMNPQRSTUVWXYZ" + "abcdefghijkmnopqrstuvwxyz" + "23456789"
	for {
		b := make([]byte, 24)
		for i := range b {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(sets))))
			if err != nil {
				return "", err
			}
			b[i] = sets[n.Int64()]
		}
		s := string(b)
		if strings.ContainsAny(s, sets[:24]) && strings.ContainsAny(s, sets[24:49]) && strings.ContainsAny(s, sets[49:]) {
			return s, nil
		}
	}
}
