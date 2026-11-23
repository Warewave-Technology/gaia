package migrate

import (
	"os"
	"strings"
	"testing"
)

// testdata/firstboot-smbus-loop.txt is the first-boot log of T11's Windows Server 2022 converted with virtio-win 0.1.302
// as it is, after 480 s in a sandbox (lab/lab-vm/t28-firstboot.sh run as-is, T28): 17 boots, smbus failing on each.
func TestDriverInstallLoopsRealLog(t *testing.T) {
	b, err := os.ReadFile("testdata/firstboot-smbus-loop.txt")
	if err != nil {
		t.Fatal(err)
	}
	got := DriverInstallLoops(string(b))
	if len(got) != 1 || got[0].Driver != "smbus" || got[0].Failures != 17 || got[0].Boots != 17 ||
		!strings.Contains(got[0].Why, "root certificate which is not trusted") {
		t.Fatalf("loops %+v", got)
	}
	text := DriverLoopText(got)
	for _, want := range []string{"Windows restarts on every boot", "virtio-win's smbus driver", "17 of 17 boots", `exclude_drivers ["smbus"]`} {
		if !strings.Contains(text, want) {
			t.Errorf("text %q lacks %q", text, want)
		}
	}
}

func TestDriverInstallLoops(t *testing.T) {
	boot := func(lines ...string) string {
		return "starting firstboot service\r\nrunning \"C:\\Program Files\\Guestfs\\Firstboot\\scripts\\2000-0001-pnputil-install-drivers.bat\"\r\n" +
			strings.Join(lines, "\r\n") + "\r\n"
	}
	fail := func(d, why string) string {
		return "Installing: " + d + ".inf.\r\nAdding the driver package failed : " + why + "\r\nFailed to install " + d + ".inf."
	}
	ok := func(d string) string { return "Installing: " + d + ".inf.\r\nSuccessfully installed " + d + ".inf." }
	retry := "Script finished with exit code 249, retrying on next boot"
	for _, tc := range []struct {
		name string
		log  string
		want []string
	}{
		{"one boot only", boot(fail("smbus", "x"), retry), nil},
		{"two boots, both retried", boot(ok("balloon"), fail("smbus", "x"), retry) + boot(fail("smbus", "x"), retry), []string{"smbus"}},
		{"two drivers", boot(fail("viomem", "y"), fail("smbus", "x"), retry) + boot(fail("viomem", "y"), fail("smbus", "x"), retry), []string{"smbus", "viomem"}},
		{"failed, then went in", boot(fail("smbus", "x"), retry) + boot(fail("smbus", "x"), retry) + boot(ok("smbus"), "Script finished with exit code 0"), nil},
		{"failed without a retry", boot(fail("smbus", "x"), "Script finished with exit code 1") + boot(fail("smbus", "x"), "Script finished with exit code 1"), nil},
		{"no first-boot service", "Installing: smbus.inf.\r\nFailed to install smbus.inf.\r\n" + retry, nil},
	} {
		var names []string
		for _, l := range DriverInstallLoops(tc.log) {
			names = append(names, l.Driver)
		}
		if strings.Join(names, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: %v, want %v", tc.name, names, tc.want)
		}
	}
}
