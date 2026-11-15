package nbd

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
)

// fakeServer speaks just enough of the server side to test the client's protocol handling
// without qemu: replies are built by hand, so the tests can also send what qemu never would.
type fakeServer struct {
	data     []byte
	contexts map[string]uint32                                            // the meta contexts the server knows
	extents  map[uint32][][2]uint32                                       // per context: (length, flags) descriptors, from offset 0
	read     func(w io.Writer, cookie, off uint64, n uint32, data []byte) // replies to NBD_CMD_READ
}

func be(vs ...any) []byte {
	var b bytes.Buffer
	for _, v := range vs {
		binary.Write(&b, binary.BigEndian, v)
	}
	return b.Bytes()
}

func chunkHdr(flags, typ uint16, cookie uint64, n int) []byte {
	return be(uint32(magicStructured), flags, typ, cookie, uint32(n))
}

// dataChunks replies with one OFFSET_DATA chunk per half, second half first (servers may reorder).
func dataChunks(w io.Writer, cookie, off uint64, n uint32, data []byte) {
	h := n / 2
	w.Write(append(chunkHdr(0, replyOffsetData, cookie, 8+int(n-h)), append(be(off+uint64(h)), data[off+uint64(h):off+uint64(n)]...)...))
	w.Write(append(chunkHdr(replyFlagDone, replyOffsetData, cookie, 8+int(h)), append(be(off), data[off:off+uint64(h)]...)...))
}

func (s *fakeServer) serve(t *testing.T, conn net.Conn) {
	defer conn.Close()
	conn.Write(be(uint64(magicNBD), uint64(magicOpt), uint16(flagFixedNewstyle|flagNoZeroes)))
	var clientFlags uint32
	if binary.Read(conn, binary.BigEndian, &clientFlags) != nil {
		return
	}
	optReply := func(opt, typ uint32, data []byte) {
		conn.Write(append(be(uint64(magicOptReply), opt, typ, uint32(len(data))), data...))
	}
	for {
		var h struct {
			Magic    uint64
			Opt, Len uint32
		}
		if binary.Read(conn, binary.BigEndian, &h) != nil {
			return
		}
		data := make([]byte, h.Len)
		io.ReadFull(conn, data)
		switch h.Opt {
		case optStructuredRepl:
			optReply(h.Opt, repAck, nil)
		case optSetMetaContext:
			r := bytes.NewReader(data)
			var n uint32
			binary.Read(r, binary.BigEndian, &n)
			r.Seek(int64(n), io.SeekCurrent)
			binary.Read(r, binary.BigEndian, &n)
			for range n {
				var l uint32
				binary.Read(r, binary.BigEndian, &l)
				q := make([]byte, l)
				r.Read(q)
				if id, ok := s.contexts[string(q)]; ok {
					optReply(h.Opt, repMetaContext, append(be(id), q...))
				}
			}
			optReply(h.Opt, repAck, nil)
		case optGo:
			optReply(h.Opt, repInfo, be(uint16(infoExport), uint64(len(s.data)), uint16(transmitReadOnly|1)))
			optReply(h.Opt, repInfo, be(uint16(infoBlockSize), uint32(1), uint32(4096), uint32(64<<10)))
			optReply(h.Opt, repAck, nil)
			s.transmit(t, conn)
			return
		default:
			optReply(h.Opt, repErrBit|1, []byte("unsupported"))
		}
	}
}

func (s *fakeServer) transmit(t *testing.T, conn net.Conn) {
	for {
		var r struct {
			Magic       uint32
			Flags, Type uint16
			Cookie, Off uint64
			Len         uint32
		}
		if binary.Read(conn, binary.BigEndian, &r) != nil {
			return
		}
		if r.Magic != magicRequest {
			t.Errorf("request magic %#x", r.Magic)
			return
		}
		switch r.Type {
		case cmdRead:
			s.read(conn, r.Cookie, r.Off, r.Len, s.data)
		case cmdBlockStatus:
			for id, ds := range s.extents {
				var p []byte
				pos := uint64(0)
				for _, d := range ds {
					if pos+uint64(d[0]) > r.Off { // descriptors from the requested offset on
						l := d[0]
						if pos < r.Off {
							l -= uint32(r.Off - pos)
						}
						p = append(p, be(l, d[1])...)
					}
					pos += uint64(d[0])
				}
				conn.Write(append(chunkHdr(0, replyBlockStatus, r.Cookie, 4+len(p)), append(be(id), p...)...))
			}
			conn.Write(chunkHdr(replyFlagDone, replyNone, r.Cookie, 0))
		case cmdDisc:
			return
		}
	}
}

func dialFake(t *testing.T, s *fakeServer, opt Options) (*Client, error) {
	t.Helper()
	a, b := net.Pipe()
	go s.serve(t, b)
	c, err := NewClient(context.Background(), a, opt)
	if err != nil {
		a.Close()
		return nil, err
	}
	t.Cleanup(func() { c.Close() })
	return c, nil
}

func pattern(n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = byte(i*7 + i/251)
	}
	return p
}

func TestFakeReadReorderedChunksAndSplit(t *testing.T) {
	s := &fakeServer{data: pattern(300 << 10), read: dataChunks}
	c, err := dialFake(t, s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Size() != 300<<10 || !c.ReadOnly() || c.MaxPayload() != 64<<10 {
		t.Fatalf("size %d read-only %v max payload %d", c.Size(), c.ReadOnly(), c.MaxPayload())
	}
	got := make([]byte, 200<<10) // more than the advertised 64 KiB maximum: split into 4 requests
	if _, err := c.ReadAt(got, 1000); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, s.data[1000:1000+200<<10]) {
		t.Fatal("data mismatch")
	}
	if n, cmds := c.Stats(); n != 200<<10 || cmds != 4 {
		t.Fatalf("stats %d bytes, %d reads", n, cmds)
	}
	if _, err := c.ReadAt(make([]byte, 10), int64(len(s.data))-5); err == nil {
		t.Fatal("read past the end succeeded")
	}
}

func TestFakeReadHoleIsZeroFilled(t *testing.T) {
	s := &fakeServer{data: pattern(8192), read: func(w io.Writer, cookie, off uint64, n uint32, data []byte) {
		w.Write(append(chunkHdr(0, replyOffsetHole, cookie, 12), be(off, n/2)...))
		w.Write(append(chunkHdr(replyFlagDone, replyOffsetData, cookie, 8+int(n-n/2)), append(be(off+uint64(n/2)), data[off+uint64(n/2):off+uint64(n)]...)...))
	}}
	c, err := dialFake(t, s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := bytes.Repeat([]byte{0xff}, 4096)
	if _, err := c.ReadAt(got, 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[:2048], make([]byte, 2048)) || !bytes.Equal(got[2048:], s.data[2048:4096]) {
		t.Fatal("hole not zero-filled or data wrong")
	}
	if n, _ := c.Stats(); n != 2048 {
		t.Fatalf("read bytes %d, want 2048 (holes carry no payload)", n)
	}
}

func TestFakeReadRejectsBadReplies(t *testing.T) {
	cases := map[string]struct {
		read func(w io.Writer, cookie, off uint64, n uint32, data []byte)
		want string
	}{
		"gap": {func(w io.Writer, cookie, off uint64, n uint32, data []byte) {
			w.Write(append(chunkHdr(replyFlagDone, replyOffsetData, cookie, 8+int(n/2)), append(be(off), data[off:off+uint64(n/2)]...)...))
		}, "cover"},
		"outside": {func(w io.Writer, cookie, off uint64, n uint32, data []byte) {
			w.Write(append(chunkHdr(replyFlagDone, replyOffsetData, cookie, 8+int(n)), append(be(off+1), data[off:off+uint64(n)]...)...))
		}, "outside request"},
		"error chunk": {func(w io.Writer, cookie, off uint64, n uint32, data []byte) {
			msg := "disk on fire"
			w.Write(append(chunkHdr(replyFlagDone, replyError, cookie, 6+len(msg)), append(be(uint32(5), uint16(len(msg))), msg...)...))
		}, "disk on fire"},
		"wrong cookie": {func(w io.Writer, cookie, off uint64, n uint32, data []byte) {
			dataChunks(w, cookie+1, off, n, data)
		}, "cookie"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, err := dialFake(t, &fakeServer{data: pattern(4096), read: tc.read}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.ReadAt(make([]byte, 4096), 0)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want %q", err, tc.want)
			}
		})
	}
}

func TestFakeMetaContexts(t *testing.T) {
	bitmap := DirtyBitmapPrefix + "b0"
	s := &fakeServer{
		data:     pattern(1 << 20),
		contexts: map[string]uint32{ContextBaseAllocation: 1, bitmap: 7},
		extents: map[uint32][][2]uint32{
			1: {{256 << 10, 0}, {768 << 10, StateHole | StateZero}},
			7: {{64 << 10, 0}, {64 << 10, StateDirty}, {64 << 10, StateDirty}, {832 << 10, 0}},
		},
		read: dataChunks,
	}
	c, err := dialFake(t, s, Options{MetaContexts: []string{ContextBaseAllocation, bitmap}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Extents(bitmap, 32<<10, 512<<10)
	if err != nil {
		t.Fatal(err)
	}
	// Clipped to the range, adjacent dirty extents merged.
	want := []Extent{{32 << 10, 32 << 10, 0}, {64 << 10, 128 << 10, StateDirty}, {192 << 10, 352 << 10, 0}}
	if !equalExtents(got, want) {
		t.Fatalf("dirty extents %v, want %v", got, want)
	}
	got, err = c.Extents(ContextBaseAllocation, 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !equalExtents(got, []Extent{{0, 256 << 10, 0}, {256 << 10, 768 << 10, StateHole | StateZero}}) {
		t.Fatalf("allocation extents %v", got)
	}
	if _, err := c.Extents("qemu:dirty-bitmap:other", 0, 4096); err == nil {
		t.Fatal("extents for a context that was not negotiated")
	}
}

// A missing dirty bitmap must fail the handshake, not read as "nothing changed".
func TestFakeUnknownContextFails(t *testing.T) {
	s := &fakeServer{data: pattern(4096), contexts: map[string]uint32{ContextBaseAllocation: 1}, read: dataChunks}
	_, err := dialFake(t, s, Options{MetaContexts: []string{ContextBaseAllocation, DirtyBitmapPrefix + "missing"}})
	if err == nil || !strings.Contains(err.Error(), "did not accept") {
		t.Fatalf("error %v", err)
	}
}

func equalExtents(a, b []Extent) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
