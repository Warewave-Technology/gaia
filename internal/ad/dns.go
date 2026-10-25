package ad

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/go-ldap/ldap/v3"
)

// Records in AD-integrated DNS zones (design 14.6). A zone is a dnsZone
// object in the DomainDnsZones or ForestDnsZones partition; each name in
// it is a dnsNode whose dnsRecord values are the records (MS-DNSP
// 2.3.2.2). Gaia writes them over LDAP, so the account needs write rights
// on its zones only. Domain controllers read them from the directory;
// AD replication carries them, so the zone serial is not raised.

// Record types Gaia writes.
const (
	TypeA   uint16 = 1
	TypePTR uint16 = 12
	typeSOA uint16 = 6
)

// ErrNoZone: the directory holds no such zone.
var ErrNoZone = errors.New("ad: no such DNS zone")

// ErrTaken: the name has another record of the type, not Gaia's.
var ErrTaken = errors.New("ad: the name has another record")

// Record is one DNS record.
type Record struct {
	Type uint16
	TTL  uint32
	// Data is an IPv4 address for A, a fully qualified name for PTR.
	Data string
}

// Zone finds a zone's object and its serial.
func (d *Directory) Zone(name string) (dn string, serial uint32, err error) {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	for _, part := range []string{"DC=DomainDnsZones," + d.root, "DC=ForestDnsZones," + d.root} {
		res, err := d.conn.Search(ldap.NewSearchRequest("CN=MicrosoftDNS,"+part, ldap.ScopeSingleLevel, ldap.NeverDerefAliases, 0, 0, false,
			fmt.Sprintf("(&(objectClass=dnsZone)(name=%s))", ldap.EscapeFilter(name)), []string{"name"}, nil))
		if ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject) {
			continue
		}
		if err != nil {
			return "", 0, fmt.Errorf("ad: zone %s: %w", name, err)
		}
		if len(res.Entries) == 1 {
			dn = res.Entries[0].DN
			break
		}
	}
	if dn == "" {
		return "", 0, fmt.Errorf("%w: %s", ErrNoZone, name)
	}
	recs, _, err := d.node("DC=@," + dn)
	if err != nil {
		return "", 0, err
	}
	for _, r := range recs {
		if r.typ == typeSOA && len(r.data) >= 4 {
			serial = binary.BigEndian.Uint32(r.data[:4])
		}
	}
	return dn, serial, nil
}

// ReverseZone finds the reverse zone of an IPv4 address: the longest
// in-addr.arpa zone that holds it, and the address's name in it.
func (d *Directory) ReverseZone(ip netip.Addr) (zone, node string, err error) {
	if !ip.Is4() {
		return "", "", fmt.Errorf("ad: %s: only IPv4 has reverse zones here", ip)
	}
	o := ip.As4()
	labels := []string{fmt.Sprint(o[3]), fmt.Sprint(o[2]), fmt.Sprint(o[1]), fmt.Sprint(o[0])}
	for i := 1; i < 4; i++ {
		z := strings.Join(labels[i:], ".") + ".in-addr.arpa"
		if _, _, err := d.Zone(z); err == nil {
			return z, strings.Join(labels[:i], "."), nil
		} else if !errors.Is(err, ErrNoZone) {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("%w: no reverse zone for %s", ErrNoZone, ip)
}

// SetRecord makes rec the only record of its type at name in zone, and
// reports whether anything changed. A record of the type with other data
// is someone else's, and is replaced only when owned (Gaia recorded the
// name as its own).
func (d *Directory) SetRecord(zone, name string, rec Record, owned bool) (bool, error) {
	zdn, serial, err := d.Zone(zone)
	if err != nil {
		return false, err
	}
	raw, err := encodeRecord(rec, serial)
	if err != nil {
		return false, err
	}
	dn := fmt.Sprintf("DC=%s,%s", ldap.EscapeDN(strings.ToLower(name)), zdn)
	recs, tomb, err := d.node(dn)
	if errors.Is(err, errNoNode) {
		add := ldap.NewAddRequest(dn, nil)
		add.Attribute("objectClass", []string{"top", "dnsNode"})
		add.Attribute("dnsRecord", []string{string(raw)})
		if err := d.conn.Add(add); err != nil {
			return false, fmt.Errorf("ad: add %s.%s: %w", name, zone, err)
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	keep := []string{}
	same := false
	for _, r := range recs {
		if tomb || r.typ == 0 {
			continue
		}
		if r.typ != rec.Type {
			keep = append(keep, string(r.raw))
			continue
		}
		if r.sameAs(rec) {
			same = true
			continue
		}
		if !owned {
			return false, fmt.Errorf("%w: %s.%s has %s", ErrTaken, name, zone, r.String())
		}
	}
	if same && !tomb && countType(recs, rec.Type) == 1 {
		return false, nil
	}
	mod := ldap.NewModifyRequest(dn, nil)
	mod.Replace("dnsRecord", append(keep, string(raw)))
	if tomb {
		mod.Replace("dNSTombstoned", []string{"FALSE"})
	}
	if err := d.conn.Modify(mod); err != nil {
		return false, fmt.Errorf("ad: set %s.%s: %w", name, zone, err)
	}
	return true, nil
}

// DeleteRecord removes the records of rec's type and data at name, and
// the name when nothing is left. It reports whether there was one.
func (d *Directory) DeleteRecord(zone, name string, rec Record) (bool, error) {
	zdn, _, err := d.Zone(zone)
	if errors.Is(err, ErrNoZone) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	dn := fmt.Sprintf("DC=%s,%s", ldap.EscapeDN(strings.ToLower(name)), zdn)
	recs, tomb, err := d.node(dn)
	if errors.Is(err, errNoNode) || tomb {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var keep []string
	found := false
	for _, r := range recs {
		if r.typ == rec.Type && r.sameAs(rec) {
			found = true
			continue
		}
		if r.typ != 0 {
			keep = append(keep, string(r.raw))
		}
	}
	if !found {
		return false, nil
	}
	if len(keep) == 0 {
		if err := d.conn.Del(ldap.NewDelRequest(dn, nil)); err != nil {
			return false, fmt.Errorf("ad: delete %s.%s: %w", name, zone, err)
		}
		return true, nil
	}
	mod := ldap.NewModifyRequest(dn, nil)
	mod.Replace("dnsRecord", keep)
	if err := d.conn.Modify(mod); err != nil {
		return false, fmt.Errorf("ad: delete %s.%s: %w", name, zone, err)
	}
	return true, nil
}

// Records returns the live records at name in zone.
func (d *Directory) Records(zone, name string) ([]Record, error) {
	zdn, _, err := d.Zone(zone)
	if err != nil {
		return nil, err
	}
	recs, tomb, err := d.node(fmt.Sprintf("DC=%s,%s", ldap.EscapeDN(strings.ToLower(name)), zdn))
	if errors.Is(err, errNoNode) || tomb {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, r := range recs {
		if rec, ok := r.record(); ok {
			out = append(out, rec)
		}
	}
	return out, nil
}

func countType(recs []rawRecord, t uint16) int {
	n := 0
	for _, r := range recs {
		if r.typ == t {
			n++
		}
	}
	return n
}

var errNoNode = errors.New("no node")

// node reads a dnsNode's records and whether it is tombstoned.
func (d *Directory) node(dn string) ([]rawRecord, bool, error) {
	res, err := d.conn.Search(ldap.NewSearchRequest(dn, ldap.ScopeBaseObject, ldap.NeverDerefAliases, 0, 0, false,
		"(objectClass=dnsNode)", []string{"dnsRecord", "dNSTombstoned"}, nil))
	if ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject) || (err == nil && len(res.Entries) == 0) {
		return nil, false, errNoNode
	}
	if err != nil {
		return nil, false, fmt.Errorf("ad: %s: %w", dn, err)
	}
	e := res.Entries[0]
	var out []rawRecord
	for _, v := range e.GetRawAttributeValues("dnsRecord") {
		r, err := decodeRecord(v)
		if err != nil {
			return nil, false, fmt.Errorf("ad: %s: %w", dn, err)
		}
		out = append(out, r)
	}
	return out, strings.EqualFold(e.GetAttributeValue("dNSTombstoned"), "TRUE"), nil
}

// rawRecord is a dnsRecord value: a 24-byte header, then the data.
type rawRecord struct {
	typ  uint16
	ttl  uint32
	data []byte
	raw  []byte
}

const (
	recordVersion = 5
	rankZone      = 0xF0
)

func decodeRecord(b []byte) (rawRecord, error) {
	if len(b) < 24 {
		return rawRecord{}, fmt.Errorf("dnsRecord of %d bytes", len(b))
	}
	n := int(binary.LittleEndian.Uint16(b[0:2]))
	if len(b) < 24+n {
		return rawRecord{}, fmt.Errorf("dnsRecord: %d data bytes, %d there", n, len(b)-24)
	}
	return rawRecord{typ: binary.LittleEndian.Uint16(b[2:4]), ttl: binary.BigEndian.Uint32(b[12:16]), data: b[24 : 24+n], raw: b}, nil
}

func encodeRecord(r Record, serial uint32) ([]byte, error) {
	var data []byte
	switch r.Type {
	case TypeA:
		ip, err := netip.ParseAddr(r.Data)
		if err != nil || !ip.Is4() {
			return nil, fmt.Errorf("ad: A record: %q is not an IPv4 address", r.Data)
		}
		a := ip.As4()
		data = a[:]
	case TypePTR:
		var err error
		if data, err = countName(r.Data); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("ad: record type %d", r.Type)
	}
	b := make([]byte, 24, 24+len(data))
	binary.LittleEndian.PutUint16(b[0:2], uint16(len(data)))
	binary.LittleEndian.PutUint16(b[2:4], r.Type)
	b[4], b[5] = recordVersion, rankZone
	binary.LittleEndian.PutUint32(b[8:12], serial)
	binary.BigEndian.PutUint32(b[12:16], r.TTL)
	// Reserved and the time stamp stay zero: a static record.
	return append(b, data...), nil
}

// countName is DNS_COUNT_NAME: the length of the raw name, the number of
// labels, then each label with its length, then a zero.
func countName(name string) ([]byte, error) {
	name = strings.TrimSuffix(name, ".")
	labels := strings.Split(name, ".")
	raw := []byte{}
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return nil, fmt.Errorf("ad: name %q", name)
		}
		raw = append(raw, byte(len(l)))
		raw = append(raw, l...)
	}
	raw = append(raw, 0)
	if len(raw) > 255 {
		return nil, fmt.Errorf("ad: name %q is too long", name)
	}
	return append([]byte{byte(len(raw)), byte(len(labels))}, raw...), nil
}

func parseCountName(b []byte) (string, bool) {
	if len(b) < 2 {
		return "", false
	}
	n, count := int(b[0]), int(b[1])
	raw := b[2:]
	if len(raw) < n {
		return "", false
	}
	var labels []string
	for i := 0; i < count; i++ {
		if len(raw) == 0 || int(raw[0])+1 > len(raw) {
			return "", false
		}
		labels = append(labels, string(raw[1:1+raw[0]]))
		raw = raw[1+raw[0]:]
	}
	return strings.Join(labels, "."), true
}

func (r rawRecord) record() (Record, bool) {
	switch r.typ {
	case TypeA:
		if len(r.data) != 4 {
			return Record{}, false
		}
		return Record{Type: TypeA, TTL: r.ttl, Data: netip.AddrFrom4([4]byte(r.data)).String()}, true
	case TypePTR:
		name, ok := parseCountName(r.data)
		return Record{Type: TypePTR, TTL: r.ttl, Data: name}, ok
	}
	return Record{}, false
}

// sameAs compares the data; the TTL may differ.
func (r rawRecord) sameAs(rec Record) bool {
	got, ok := r.record()
	return ok && got.Type == rec.Type && strings.EqualFold(strings.TrimSuffix(got.Data, "."), strings.TrimSuffix(rec.Data, "."))
}

func (r rawRecord) String() string {
	if rec, ok := r.record(); ok {
		return fmt.Sprintf("%s %s", typeName(rec.Type), rec.Data)
	}
	return fmt.Sprintf("a type %d record", r.typ)
}

func typeName(t uint16) string {
	if i := slices.Index([]uint16{TypeA, TypePTR}, t); i >= 0 {
		return []string{"A", "PTR"}[i]
	}
	return fmt.Sprint(t)
}
