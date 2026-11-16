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

func TestParseMachine(t *testing.T) {
	x := `<domain type='kvm'><name>vm</name><memory unit='KiB'>1048576</memory><vcpu placement='static'>2</vcpu>
<os><type arch='x86_64' machine='pc-q35-7.2'>hvm</type><boot dev='hd'/></os>
<devices>
<disk type='file' device='cdrom'><target dev='sda' bus='sata'/><readonly/></disk>
<disk type='file' device='disk'><source file='/a.qcow2'/><target dev='vda' bus='virtio'/></disk>
<interface type='network'><mac address='52:54:00:16:00:01'/><source network='n'/><model type='virtio'/></interface>
</devices></domain>`
	m, err := parseMachine(x)
	want := Machine{Arch: "x86_64", Firmware: "bios", MemoryMiB: 1024, VCPUs: 2, DiskBus: "virtio", NICModel: "virtio", MAC: "52:54:00:16:00:01"}
	if err != nil || m != want {
		t.Fatalf("%+v %v", m, err)
	}
	efi := strings.Replace(x, "<os>", "<os firmware='efi'>", 1)
	efi = strings.Replace(efi, "<memory unit='KiB'>1048576</memory>", "<memory unit='GiB'>2</memory>", 1)
	if m, _ := parseMachine(efi); m.Firmware != "efi" || m.MemoryMiB != 2048 {
		t.Fatalf("efi: %+v", m)
	}
	noNIC := x[:strings.Index(x, "<interface")] + "</devices></domain>"
	if m, _ := parseMachine(noNIC); m.MAC != "" || m.NICModel != "" {
		t.Fatalf("no interface: %+v", m)
	}
}
