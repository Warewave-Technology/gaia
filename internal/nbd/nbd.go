// Package nbd is a minimal, pure-Go NBD client (ADR-004 §6): fixed newstyle
// handshake, structured replies, NBD_OPT_GO, meta contexts, NBD_CMD_READ and
// NBD_CMD_BLOCK_STATUS. It reads disk exports such as libvirt's pull-mode
// backup (qemu's NBD server), including the "qemu:dirty-bitmap:*" context.
//
// It is read-only on purpose: it has no write, trim or flush command.
// A Client sends one request at a time; open several Clients for parallel reads.
//
// Protocol reference: https://github.com/NetworkBlockDevice/nbd/blob/master/doc/proto.md
package nbd

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
	"time"
)

// Meta context names.
const (
	// ContextBaseAllocation reports holes (StateHole) and zero areas (StateZero).
	ContextBaseAllocation = "base:allocation"
	// DirtyBitmapPrefix + bitmap name: extents with flag StateDirty changed since the bitmap started.
	DirtyBitmapPrefix = "qemu:dirty-bitmap:"
)

// Extent flags. Their meaning depends on the meta context.
const (
	StateHole  uint32 = 1 << 0 // base:allocation: not allocated
	StateZero  uint32 = 1 << 1 // base:allocation: reads as zeroes
	StateDirty uint32 = 1 << 0 // qemu:dirty-bitmap:*: changed
)

const (
	magicNBD        = 0x4e42444d41474943 // "NBDMAGIC"
	magicOpt        = 0x49484156454f5054 // "IHAVEOPT"
	magicOldstyle   = 0x0000420281861253
	magicOptReply   = 0x0003e889045565a9
	magicRequest    = 0x25609513
	magicSimple     = 0x67446698
	magicStructured = 0x668e33ef

	flagFixedNewstyle = 1 << 0
	flagNoZeroes      = 1 << 1

	optGo             = 7
	optStructuredRepl = 8
	optSetMetaContext = 10

	repAck         = 1
	repInfo        = 3
	repMetaContext = 4
	repErrBit      = 1 << 31

	infoExport    = 0
	infoBlockSize = 3

	cmdRead        = 0
	cmdDisc        = 2
	cmdBlockStatus = 7

	replyFlagDone     = 1 << 0
	replyNone         = 0
	replyOffsetData   = 1
	replyOffsetHole   = 2
	replyBlockStatus  = 5
	replyErrBit       = 1 << 15
	replyError        = replyErrBit | 1
	replyErrorOffset  = replyErrBit | 2
	transmitReadOnly  = 1 << 1
	defaultMaxPayload = 32 << 20 // the protocol's recommended limit when the server does not say
	maxBlockStatusLen = 1 << 31  // per request; the length field is 32 bits
	maxOptReplyLen    = 1 << 20  // bounds what a confused server can make us allocate
)

// Options select the export and the meta contexts to negotiate.
type Options struct {
	Export       string   // export name; qemu-nbd's default is ""
	MetaContexts []string // e.g. ContextBaseAllocation, DirtyBitmapPrefix+"backup-vda"
}

// Extent is one run of bytes with the same flags.
type Extent struct {
	Offset uint64 `json:"offset"`
	Length uint64 `json:"length"`
	Flags  uint32 `json:"flags"`
}

// Client is a connected NBD client in the transmission phase.
type Client struct {
	mu         sync.Mutex
	conn       net.Conn
	size       uint64
	flags      uint16
	minBlock   uint32
	maxPayload uint32
	contexts   map[string]uint32
	cookie     uint64
	readBytes  uint64 // payload bytes received for NBD_CMD_READ
	readCmds   uint64
	broken     error // set after a transmission error: the reply stream is out of sync
}

// Dial connects to network ("unix" or "tcp") addr and negotiates opt.
func Dial(ctx context.Context, network, addr string, opt Options) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	c, err := NewClient(ctx, conn, opt)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

// NewClient runs the handshake on conn. On error the caller closes conn.
func NewClient(ctx context.Context, conn net.Conn, opt Options) (*Client, error) {
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
		defer conn.SetDeadline(time.Time{})
	}
	c := &Client{conn: conn, contexts: map[string]uint32{}, minBlock: 1, maxPayload: defaultMaxPayload}
	if err := c.handshake(opt); err != nil {
		return nil, fmt.Errorf("nbd handshake: %w", err)
	}
	return c, nil
}

// Size is the export size in bytes.
func (c *Client) Size() uint64 { return c.size }

// ReadOnly reports whether the server marked the export read-only.
func (c *Client) ReadOnly() bool { return c.flags&transmitReadOnly != 0 }

// MaxPayload is the largest single NBD_CMD_READ this client sends.
func (c *Client) MaxPayload() uint32 { return c.maxPayload }

// HasContext reports whether the server accepted a meta context.
func (c *Client) HasContext(name string) bool { _, ok := c.contexts[name]; return ok }

// Stats returns the payload bytes and the number of NBD_CMD_READ requests so far.
func (c *Client) Stats() (readBytes, readCmds uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readBytes, c.readCmds
}

// Close sends NBD_CMD_DISC and closes the connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var werr error
	if c.broken == nil {
		c.cookie++
		werr = c.writeRequest(0, cmdDisc, c.cookie, 0, 0)
	}
	c.broken = net.ErrClosed
	return errors.Join(werr, c.conn.Close())
}

// ReadAt reads len(p) bytes at off. Holes reported by the server are zero-filled.
func (c *Client) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || uint64(off)+uint64(len(p)) > c.size {
		return 0, fmt.Errorf("nbd read [%d, +%d) outside export of %d bytes", off, len(p), c.size)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken != nil {
		return 0, fmt.Errorf("nbd connection unusable: %w", c.broken)
	}
	for done := 0; done < len(p); {
		n := min(len(p)-done, int(c.maxPayload))
		if err := c.read(p[done:done+n], uint64(off)+uint64(done)); err != nil {
			c.broken = err
			return done, err
		}
		done += n
	}
	return len(p), nil
}

// Extents returns the extents of meta context name that cover [off, off+length),
// clipped to that range, in order and without gaps.
func (c *Client) Extents(name string, off, length uint64) ([]Extent, error) {
	id, ok := c.contexts[name]
	if !ok {
		return nil, fmt.Errorf("meta context %q was not negotiated", name)
	}
	if off+length > c.size {
		return nil, fmt.Errorf("nbd block status [%d, +%d) outside export of %d bytes", off, length, c.size)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken != nil {
		return nil, fmt.Errorf("nbd connection unusable: %w", c.broken)
	}
	var out []Extent
	for pos, end := off, off+length; pos < end; {
		n := min(end-pos, maxBlockStatusLen)
		got, err := c.blockStatus(pos, uint32(n))
		if err != nil {
			c.broken = err
			return nil, err
		}
		ext := got[id]
		if len(ext) == 0 {
			return nil, fmt.Errorf("server sent no extents for %q at %d", name, pos)
		}
		for _, e := range ext {
			if pos >= end {
				break
			}
			e.Offset = pos
			e.Length = min(e.Length, end-pos)
			if n := len(out); n > 0 && out[n-1].Flags == e.Flags {
				out[n-1].Length += e.Length
			} else {
				out = append(out, e)
			}
			pos += e.Length
		}
	}
	return out, nil
}

// --- handshake ---

func (c *Client) handshake(opt Options) error {
	var hdr struct {
		Magic, Opt uint64
		Flags      uint16
	}
	if err := binary.Read(c.conn, binary.BigEndian, &hdr.Magic); err != nil {
		return err
	}
	if hdr.Magic != magicNBD {
		return fmt.Errorf("bad server magic %#x", hdr.Magic)
	}
	if err := binary.Read(c.conn, binary.BigEndian, &hdr.Opt); err != nil {
		return err
	}
	if hdr.Opt == magicOldstyle {
		return errors.New("oldstyle negotiation is not supported")
	}
	if hdr.Opt != magicOpt {
		return fmt.Errorf("bad option magic %#x", hdr.Opt)
	}
	if err := binary.Read(c.conn, binary.BigEndian, &hdr.Flags); err != nil {
		return err
	}
	if hdr.Flags&flagFixedNewstyle == 0 {
		return errors.New("server does not support fixed newstyle negotiation")
	}
	clientFlags := uint32(flagFixedNewstyle)
	if hdr.Flags&flagNoZeroes != 0 {
		clientFlags |= flagNoZeroes
	}
	if err := binary.Write(c.conn, binary.BigEndian, clientFlags); err != nil {
		return err
	}

	// Structured replies are required: block status and sparse reads need them.
	if err := c.sendOpt(optStructuredRepl, nil); err != nil {
		return err
	}
	typ, data, err := c.readOptReply(optStructuredRepl)
	if err != nil {
		return err
	}
	if typ != repAck {
		return optError("NBD_OPT_STRUCTURED_REPLY", typ, data)
	}

	if len(opt.MetaContexts) > 0 {
		if err := c.setMetaContexts(opt); err != nil {
			return err
		}
	}
	return c.optGo(opt.Export)
}

func (c *Client) setMetaContexts(opt Options) error {
	var b bytes.Buffer
	putString32(&b, opt.Export)
	binary.Write(&b, binary.BigEndian, uint32(len(opt.MetaContexts)))
	for _, q := range opt.MetaContexts {
		putString32(&b, q)
	}
	if err := c.sendOpt(optSetMetaContext, b.Bytes()); err != nil {
		return err
	}
	for {
		typ, data, err := c.readOptReply(optSetMetaContext)
		if err != nil {
			return err
		}
		switch typ {
		case repMetaContext:
			if len(data) < 4 {
				return errors.New("short NBD_REP_META_CONTEXT")
			}
			c.contexts[string(data[4:])] = binary.BigEndian.Uint32(data)
			continue
		case repAck:
		default:
			return optError("NBD_OPT_SET_META_CONTEXT", typ, data)
		}
		break
	}
	// A server leaves out contexts it does not know; a missing dirty bitmap must not look like "nothing changed".
	for _, q := range opt.MetaContexts {
		if _, ok := c.contexts[q]; !ok {
			return fmt.Errorf("server did not accept meta context %q", q)
		}
	}
	return nil
}

func (c *Client) optGo(export string) error {
	var b bytes.Buffer
	putString32(&b, export)
	binary.Write(&b, binary.BigEndian, uint16(1))
	binary.Write(&b, binary.BigEndian, uint16(infoBlockSize))
	if err := c.sendOpt(optGo, b.Bytes()); err != nil {
		return err
	}
	gotExport := false
	for {
		typ, data, err := c.readOptReply(optGo)
		if err != nil {
			return err
		}
		switch typ {
		case repInfo:
			if len(data) < 2 {
				return errors.New("short NBD_REP_INFO")
			}
			switch binary.BigEndian.Uint16(data) {
			case infoExport:
				if len(data) != 12 {
					return errors.New("bad NBD_INFO_EXPORT length")
				}
				c.size = binary.BigEndian.Uint64(data[2:])
				c.flags = binary.BigEndian.Uint16(data[10:])
				gotExport = true
			case infoBlockSize:
				if len(data) != 14 {
					return errors.New("bad NBD_INFO_BLOCK_SIZE length")
				}
				c.minBlock = max(binary.BigEndian.Uint32(data[2:]), 1)
				if m := binary.BigEndian.Uint32(data[10:]); m > 0 {
					c.maxPayload = min(m, defaultMaxPayload)
				}
			}
		case repAck:
			if !gotExport {
				return errors.New("NBD_OPT_GO finished without NBD_INFO_EXPORT")
			}
			return nil
		default:
			return optError("NBD_OPT_GO", typ, data)
		}
	}
}

func (c *Client) sendOpt(opt uint32, data []byte) error {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint64(magicOpt))
	binary.Write(&b, binary.BigEndian, opt)
	binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.Write(data)
	_, err := c.conn.Write(b.Bytes())
	return err
}

func (c *Client) readOptReply(opt uint32) (uint32, []byte, error) {
	var h struct {
		Magic     uint64
		Opt, Type uint32
		Len       uint32
	}
	if err := binary.Read(c.conn, binary.BigEndian, &h); err != nil {
		return 0, nil, err
	}
	if h.Magic != magicOptReply {
		return 0, nil, fmt.Errorf("bad option reply magic %#x", h.Magic)
	}
	if h.Opt != opt {
		return 0, nil, fmt.Errorf("reply for option %d, expected %d", h.Opt, opt)
	}
	if h.Len > maxOptReplyLen {
		return 0, nil, fmt.Errorf("option reply of %d bytes", h.Len)
	}
	data := make([]byte, h.Len)
	if _, err := io.ReadFull(c.conn, data); err != nil {
		return 0, nil, err
	}
	return h.Type, data, nil
}

func optError(name string, typ uint32, data []byte) error {
	if typ&repErrBit != 0 {
		return fmt.Errorf("%s refused (reply %d): %s", name, typ&^repErrBit, data)
	}
	return fmt.Errorf("%s: unexpected reply type %d", name, typ)
}

func putString32(b *bytes.Buffer, s string) {
	binary.Write(b, binary.BigEndian, uint32(len(s)))
	b.WriteString(s)
}

// --- transmission ---

func (c *Client) writeRequest(flags, typ uint16, cookie, off uint64, length uint32) error {
	var b [28]byte
	binary.BigEndian.PutUint32(b[0:], magicRequest)
	binary.BigEndian.PutUint16(b[4:], flags)
	binary.BigEndian.PutUint16(b[6:], typ)
	binary.BigEndian.PutUint64(b[8:], cookie)
	binary.BigEndian.PutUint64(b[16:], off)
	binary.BigEndian.PutUint32(b[24:], length)
	_, err := c.conn.Write(b[:])
	return err
}

type chunk struct {
	flags, typ uint16
	payload    []byte
}

// readChunk reads one reply for cookie. A simple reply (allowed for errors) becomes an error.
func (c *Client) readChunk(cookie uint64, maxLen uint32) (chunk, error) {
	var magic uint32
	if err := binary.Read(c.conn, binary.BigEndian, &magic); err != nil {
		return chunk{}, err
	}
	switch magic {
	case magicSimple:
		var h struct {
			Errno  uint32
			Cookie uint64
		}
		if err := binary.Read(c.conn, binary.BigEndian, &h); err != nil {
			return chunk{}, err
		}
		if h.Cookie != cookie {
			return chunk{}, fmt.Errorf("reply for cookie %d, expected %d", h.Cookie, cookie)
		}
		if h.Errno == 0 {
			return chunk{}, errors.New("simple reply without error after structured replies were negotiated")
		}
		return chunk{}, fmt.Errorf("server error %d", h.Errno)
	case magicStructured:
	default:
		return chunk{}, fmt.Errorf("bad reply magic %#x", magic)
	}
	var h struct {
		Flags, Type uint16
		Cookie      uint64
		Len         uint32
	}
	if err := binary.Read(c.conn, binary.BigEndian, &h); err != nil {
		return chunk{}, err
	}
	if h.Cookie != cookie {
		return chunk{}, fmt.Errorf("reply for cookie %d, expected %d", h.Cookie, cookie)
	}
	if h.Len > maxLen {
		return chunk{}, fmt.Errorf("reply chunk of %d bytes, limit %d", h.Len, maxLen)
	}
	p := make([]byte, h.Len)
	if _, err := io.ReadFull(c.conn, p); err != nil {
		return chunk{}, err
	}
	if h.Type&replyErrBit != 0 {
		return chunk{}, chunkError(h.Type, p)
	}
	return chunk{flags: h.Flags, typ: h.Type, payload: p}, nil
}

func chunkError(typ uint16, p []byte) error {
	if (typ == replyError || typ == replyErrorOffset) && len(p) >= 6 {
		errno := binary.BigEndian.Uint32(p)
		n := int(binary.BigEndian.Uint16(p[4:]))
		msg := p[6:min(6+n, len(p))]
		if typ == replyErrorOffset && len(p) >= 6+n+8 {
			return fmt.Errorf("server error %d at offset %d: %s", errno, binary.BigEndian.Uint64(p[6+n:]), msg)
		}
		return fmt.Errorf("server error %d: %s", errno, msg)
	}
	return fmt.Errorf("server error chunk type %#x", typ)
}

// read fills p from off with one NBD_CMD_READ; c.mu is held.
func (c *Client) read(p []byte, off uint64) error {
	c.cookie++
	if err := c.writeRequest(0, cmdRead, c.cookie, off, uint32(len(p))); err != nil {
		return err
	}
	c.readCmds++
	var covered [][2]uint64 // [start, end) within p
	for {
		ch, err := c.readChunk(c.cookie, uint32(len(p))+8)
		if err != nil {
			return fmt.Errorf("nbd read at %d: %w", off, err)
		}
		switch ch.typ {
		case replyOffsetData:
			if len(ch.payload) < 8 {
				return errors.New("short NBD_REPLY_TYPE_OFFSET_DATA")
			}
			start, data := binary.BigEndian.Uint64(ch.payload), ch.payload[8:]
			if start < off || start+uint64(len(data)) > off+uint64(len(p)) {
				return fmt.Errorf("data chunk [%d, +%d) outside request [%d, +%d)", start, len(data), off, len(p))
			}
			copy(p[start-off:], data)
			c.readBytes += uint64(len(data))
			covered = append(covered, [2]uint64{start - off, start - off + uint64(len(data))})
		case replyOffsetHole:
			if len(ch.payload) != 12 {
				return errors.New("bad NBD_REPLY_TYPE_OFFSET_HOLE")
			}
			start, n := binary.BigEndian.Uint64(ch.payload), uint64(binary.BigEndian.Uint32(ch.payload[8:]))
			if start < off || start+n > off+uint64(len(p)) {
				return fmt.Errorf("hole chunk [%d, +%d) outside request [%d, +%d)", start, n, off, len(p))
			}
			clear(p[start-off : start-off+n])
			covered = append(covered, [2]uint64{start - off, start - off + n})
		case replyNone:
		default:
			return fmt.Errorf("unexpected reply chunk type %d for read", ch.typ)
		}
		if ch.flags&replyFlagDone != 0 {
			break
		}
	}
	// Chunks may come in any order but must cover the request exactly once.
	slices.SortFunc(covered, func(a, b [2]uint64) int { return cmp.Compare(a[0], b[0]) })
	var pos uint64
	for _, r := range covered {
		if r[0] != pos {
			return fmt.Errorf("read at %d: chunks leave a gap or overlap at +%d", off, pos)
		}
		pos = r[1]
	}
	if pos != uint64(len(p)) {
		return fmt.Errorf("read at %d: chunks cover %d of %d bytes", off, pos, len(p))
	}
	return nil
}

// blockStatus sends one NBD_CMD_BLOCK_STATUS; c.mu is held. Result: context id -> extents from off.
func (c *Client) blockStatus(off uint64, length uint32) (map[uint32][]Extent, error) {
	c.cookie++
	if err := c.writeRequest(0, cmdBlockStatus, c.cookie, off, length); err != nil {
		return nil, err
	}
	out := map[uint32][]Extent{}
	for {
		ch, err := c.readChunk(c.cookie, 1<<24)
		if err != nil {
			return nil, fmt.Errorf("nbd block status at %d: %w", off, err)
		}
		switch ch.typ {
		case replyBlockStatus:
			p := ch.payload
			if len(p) < 4+8 || (len(p)-4)%8 != 0 {
				return nil, errors.New("bad NBD_REPLY_TYPE_BLOCK_STATUS length")
			}
			id, pos := binary.BigEndian.Uint32(p), off
			for d := p[4:]; len(d) > 0; d = d[8:] {
				n := uint64(binary.BigEndian.Uint32(d))
				if n == 0 {
					return nil, errors.New("zero-length extent")
				}
				out[id] = append(out[id], Extent{Offset: pos, Length: n, Flags: binary.BigEndian.Uint32(d[4:])})
				pos += n
			}
		case replyNone:
		default:
			return nil, fmt.Errorf("unexpected reply chunk type %d for block status", ch.typ)
		}
		if ch.flags&replyFlagDone != 0 {
			return out, nil
		}
	}
}
