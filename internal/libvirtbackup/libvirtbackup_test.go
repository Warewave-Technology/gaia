package libvirtbackup

import (
	"strings"
	"testing"
)

func TestParseServer(t *testing.T) {
	for in, want := range map[string]string{
		"tcp:127.0.0.1:10809": `<server transport='tcp' name='127.0.0.1' port='10809'/>`,
		"unix:/run/x.sock":    `<server transport='unix' socket='/run/x.sock'/>`,
	} {
		s, err := ParseServer(in)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.xml()
		if err != nil || got != want {
			t.Fatalf("%s: %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "tcp", "udp:x:1", "unix:"} {
		if _, err := ParseServer(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if _, err := (Server{"tcp", "localhost"}).xml(); err == nil {
		t.Fatal("tcp without port accepted")
	}
}

func TestDiskTargets(t *testing.T) {
	d, err := diskTargets(`<domain><devices>
		<disk type='file' device='disk'><target dev='vda' bus='virtio'/></disk>
		<disk type='file' device='disk'><target dev='vdb' bus='virtio'/><readonly/></disk>
		<interface type='network'/></devices></domain>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(d) != 2 || d[0].Target != "vda" || d[1].Target != "vdb" {
		t.Fatalf("%+v", d)
	}
}

func TestXMLEscape(t *testing.T) {
	if got := xmlEscape(`a'b"<c>&`); strings.ContainsAny(got, `'"<>`) {
		t.Fatalf("unescaped: %s", got)
	}
}
