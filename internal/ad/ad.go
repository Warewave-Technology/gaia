// Package ad reads Active Directory (design 14): groups, their members,
// nested groups included, and whether accounts are enabled. Gaia never
// writes users or groups; a read-only account is enough.
package ad

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// Config is how to reach a directory. Credentials come from OpenBao:
// username (a DN or user@domain), password, and optionally ca (PEM) and
// server_name (when the certificate does not name the endpoint's host).
type Config struct {
	Endpoint   string // ldaps://host:636, or ldap://host:389 (StartTLS)
	BaseDN     string // empty: the directory's defaultNamingContext
	Username   string
	Password   string
	CA         []byte
	ServerName string
	Timeout    time.Duration
}

// ConfigFrom builds a Config from a provider's endpoint, base DN and
// secret.
func ConfigFrom(endpoint, baseDN string, secret map[string]string) Config {
	return Config{Endpoint: endpoint, BaseDN: baseDN, Username: secret["username"], Password: secret["password"],
		CA: []byte(secret["ca"]), ServerName: secret["server_name"]}
}

// Directory is a bound connection.
type Directory struct {
	conn   *ldap.Conn
	BaseDN string
}

// Open connects over TLS (ldaps, or ldap with StartTLS: Gaia never sends
// a password in clear) and binds.
func Open(ctx context.Context, c Config) (*Directory, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil || (u.Scheme != "ldaps" && u.Scheme != "ldap") || u.Host == "" {
		return nil, fmt.Errorf("ad: endpoint %q: want ldaps://host[:636] or ldap://host[:389]", c.Endpoint)
	}
	host := u.Hostname()
	tc := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if c.ServerName != "" {
		tc.ServerName = c.ServerName
	}
	if len(c.CA) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(c.CA) {
			return nil, errors.New("ad: ca: no PEM certificate")
		}
		tc.RootCAs = pool
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	d := &net.Dialer{Timeout: timeout}
	addr := u.Host
	if u.Port() == "" {
		port := "636"
		if u.Scheme == "ldap" {
			port = "389"
		}
		addr = net.JoinHostPort(host, port)
	}
	var conn *ldap.Conn
	if u.Scheme == "ldaps" {
		conn, err = ldap.DialURL("ldaps://"+addr, ldap.DialWithDialer(d), ldap.DialWithTLSConfig(tc))
	} else {
		conn, err = ldap.DialURL("ldap://"+addr, ldap.DialWithDialer(d))
		if err == nil {
			if serr := conn.StartTLS(tc); serr != nil {
				_ = conn.Close()
				return nil, fmt.Errorf("ad: StartTLS: %w", serr)
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("ad: %w", err)
	}
	conn.SetTimeout(timeout)
	if dl, ok := ctx.Deadline(); ok {
		conn.SetTimeout(time.Until(dl))
	}
	if err := conn.Bind(c.Username, c.Password); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ad: bind as %s: %w", c.Username, err)
	}
	dir := &Directory{conn: conn, BaseDN: c.BaseDN}
	if dir.BaseDN == "" {
		res, err := conn.Search(ldap.NewSearchRequest("", ldap.ScopeBaseObject, ldap.NeverDerefAliases, 0, 0, false,
			"(objectClass=*)", []string{"defaultNamingContext"}, nil))
		if err != nil || len(res.Entries) == 0 {
			_ = conn.Close()
			return nil, fmt.Errorf("ad: no base DN and no defaultNamingContext: %v", err)
		}
		dir.BaseDN = res.Entries[0].GetAttributeValue("defaultNamingContext")
	}
	return dir, nil
}

// Close ends the connection.
func (d *Directory) Close() error { return d.conn.Close() }

// Group is a directory group.
type Group struct {
	DN          string `json:"dn"`
	CN          string `json:"cn"`
	Description string `json:"description,omitempty"`
	GUID        string `json:"guid"`
}

// User is a directory account.
type User struct {
	DN          string `json:"dn"`
	Username    string `json:"username"` // sAMAccountName, lower case
	GUID        string `json:"guid"`
	DisplayName string `json:"displayName,omitempty"`
	Mail        string `json:"mail,omitempty"`
	Disabled    bool   `json:"disabled"`
}

var groupAttrs = []string{"cn", "description", "objectGUID"}
var userAttrs = []string{"sAMAccountName", "objectGUID", "displayName", "mail", "userAccountControl"}

func (d *Directory) search(base, filter string, attrs []string) ([]*ldap.Entry, error) {
	res, err := d.conn.SearchWithPaging(ldap.NewSearchRequest(base, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		filter, attrs, nil), 500)
	if err != nil {
		return nil, fmt.Errorf("ad: search %s: %w", filter, err)
	}
	return res.Entries, nil
}

// Groups returns every group under the base DN.
func (d *Directory) Groups() ([]Group, error) {
	entries, err := d.search(d.BaseDN, "(objectClass=group)", groupAttrs)
	if err != nil {
		return nil, err
	}
	out := make([]Group, 0, len(entries))
	for _, e := range entries {
		out = append(out, Group{DN: e.DN, CN: e.GetAttributeValue("cn"), Description: e.GetAttributeValue("description"),
			GUID: guid(e.GetRawAttributeValue("objectGUID"))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DN < out[j].DN })
	return out, nil
}

// Members returns the accounts in a group: with recursive, also those of
// the groups inside it (LDAP_MATCHING_RULE_IN_CHAIN).
func (d *Directory) Members(groupDN string, recursive bool) ([]User, error) {
	rule := "memberOf"
	if recursive {
		rule = "memberOf:1.2.840.113556.1.4.1941:"
	}
	filter := fmt.Sprintf("(&(objectCategory=person)(objectClass=user)(%s=%s))", rule, ldap.EscapeFilter(groupDN))
	entries, err := d.search(d.BaseDN, filter, userAttrs)
	if err != nil {
		return nil, err
	}
	out := make([]User, 0, len(entries))
	for _, e := range entries {
		out = append(out, user(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out, nil
}

// User finds an account by its sAMAccountName.
func (d *Directory) User(username string) (User, error) {
	entries, err := d.search(d.BaseDN, fmt.Sprintf("(&(objectCategory=person)(objectClass=user)(sAMAccountName=%s))", ldap.EscapeFilter(username)), userAttrs)
	if err != nil {
		return User{}, err
	}
	if len(entries) != 1 {
		return User{}, fmt.Errorf("ad: %d accounts named %s", len(entries), username)
	}
	return user(entries[0]), nil
}

// ErrNoAccount: the directory has no such account (any more).
var ErrNoAccount = errors.New("ad: no such account")

// UserByGUID finds an account by its objectGUID, as User.GUID writes it.
func (d *Directory) UserByGUID(id string) (User, error) {
	raw, err := guidBytes(id)
	if err != nil {
		return User{}, err
	}
	var esc strings.Builder
	for _, b := range raw {
		fmt.Fprintf(&esc, "\\%02x", b)
	}
	entries, err := d.search(d.BaseDN, "(&(objectCategory=person)(objectClass=user)(objectGUID="+esc.String()+"))", userAttrs)
	if err != nil {
		return User{}, err
	}
	if len(entries) == 0 {
		return User{}, ErrNoAccount
	}
	return user(entries[0]), nil
}

func guidBytes(s string) ([]byte, error) {
	h := strings.ReplaceAll(s, "-", "")
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 16 {
		return nil, fmt.Errorf("ad: %q is not a GUID", s)
	}
	// The first three fields are little-endian on the wire.
	out := make([]byte, 16)
	binary.LittleEndian.PutUint32(out[0:4], binary.BigEndian.Uint32(b[0:4]))
	binary.LittleEndian.PutUint16(out[4:6], binary.BigEndian.Uint16(b[4:6]))
	binary.LittleEndian.PutUint16(out[6:8], binary.BigEndian.Uint16(b[6:8]))
	copy(out[8:], b[8:])
	return out, nil
}

// accountDisable is userAccountControl's ACCOUNTDISABLE bit.
const accountDisable = 0x2

func user(e *ldap.Entry) User {
	uac, _ := strconv.Atoi(e.GetAttributeValue("userAccountControl"))
	return User{
		DN: e.DN, Username: strings.ToLower(e.GetAttributeValue("sAMAccountName")),
		GUID: guid(e.GetRawAttributeValue("objectGUID")), DisplayName: e.GetAttributeValue("displayName"),
		Mail: e.GetAttributeValue("mail"), Disabled: uac&accountDisable != 0,
	}
}

// guid formats an objectGUID as Windows does: the first three fields are
// little-endian.
func guid(b []byte) string {
	if len(b) != 16 {
		return ""
	}
	return fmt.Sprintf("%08x-%04x-%04x-%x-%x", binary.LittleEndian.Uint32(b[0:4]), binary.LittleEndian.Uint16(b[4:6]),
		binary.LittleEndian.Uint16(b[6:8]), b[8:10], b[10:16])
}
