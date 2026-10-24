package cel

import (
	"strings"
	"testing"
)

func TestNaming(t *testing.T) {
	p, err := Naming(`site.name + env.substring(0, 1) + app.substring(0, 3) + pad(seq, 2)`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.EvalString(NamingVars("poc-web", map[string]string{"env": "test", "app": "poc"}, "lab", 7))
	if err != nil || got != "labtpoc07" {
		t.Errorf("= %q, %v; want labtpoc07", got, err)
	}

	p, _ = Naming(`contract + "-" + pad(seq, 3)`)
	if got, _ := p.EvalString(NamingVars("poc-web", nil, "lab", 12)); got != "poc-web-012" {
		t.Errorf("= %q", got)
	}
}

func TestCompileErrors(t *testing.T) {
	for src, want := range map[string]string{
		"":                      "empty expression",
		"site.name + nosuchvar": "undeclared reference",
		"seq + 1":               "returns int, want string",
		`pad("a", 2)`:           "no matching overload",
		"site.name +":           "column 12",
	} {
		if _, err := Naming(src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Naming(%q) = %v, want %q", src, err, want)
		}
	}
}

func TestEvalErrors(t *testing.T) {
	p, _ := Naming(`app.substring(0, 3)`)
	if _, err := p.EvalString(NamingVars("x", map[string]string{"app": "a"}, "lab", 1)); err == nil {
		t.Error("substring past the end: no error")
	}
	p, _ = Naming(`pad(seq, 99)`)
	if _, err := p.EvalString(NamingVars("x", nil, "lab", 1)); err == nil || !strings.Contains(err.Error(), "width") {
		t.Errorf("pad width: %v", err)
	}
}

func TestRule(t *testing.T) {
	p, err := Rule(`params.heap_mib * 4 <= size.memoryGiB * 1024 * 3 && labels.env != "prod"`)
	if err != nil {
		t.Fatal(err)
	}
	vars := func(heap int) map[string]any {
		return map[string]any{
			"params":  map[string]any{"heap_mib": heap},
			"size":    map[string]any{"cpu": 2, "memoryGiB": 4, "diskGiB": 20},
			"image":   map[string]any{"name": "rocky9", "os": "linux"},
			"network": map[string]any{"name": "lab-app", "site": "lab"},
			"labels":  map[string]string{"env": "test"},
		}
	}
	if ok, err := p.EvalBool(vars(3072)); !ok || err != nil {
		t.Errorf("3072 MiB on 4 GiB = %v, %v; want true", ok, err)
	}
	if ok, _ := p.EvalBool(vars(3073)); ok {
		t.Error("3073 MiB on 4 GiB = true; want false")
	}

	// Comparisons across int and double work; arithmetic does not mix them.
	if p, err := Rule(`params.heap_mib <= 0.75 * double(size.memoryGiB) * 1024.0`); err != nil {
		t.Error(err)
	} else if ok, _ := p.EvalBool(vars(3072)); !ok {
		t.Error("cross-type comparison: false")
	}
	if _, err := Rule(`size.cpu`); err != nil {
		t.Errorf("dyn result should compile (checked at eval): %v", err)
	}
	if _, err := Rule(`"x"`); err == nil {
		t.Error("string rule compiled")
	}
}

func TestSubnetValues(t *testing.T) {
	vars := map[string]any{"name": "ank-prod-app", "subnet": map[string]any{
		"cidr": "10.20.1.0/24", "site": "ank", "vlan": "120", "tags": []string{"gaia-managed"}, "labels": map[string]string{"site": "ank"},
	}}
	sel, err := SubnetRule(`subnet.site == "ank" && "gaia-managed" in subnet.tags`)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := sel.EvalBool(vars); err != nil || !ok {
		t.Errorf("select = %v, %v", ok, err)
	}
	m, err := SubnetValue(`{"portgroup": "dvPG-VLAN" + subnet.vlan, "vlan": int(subnet.vlan)}`)
	if err != nil {
		t.Fatal(err)
	}
	v, err := m.EvalValue(vars)
	got, _ := v.(map[string]any)
	if err != nil || got["portgroup"] != "dvPG-VLAN120" || got["vlan"] != float64(120) {
		t.Errorf("attachment = %#v, %v", v, err)
	}
	l, _ := SubnetValue(`[subnet.labels.site, name]`)
	if v, err := l.EvalValue(vars); err != nil || len(v.([]any)) != 2 || v.([]any)[1] != "ank-prod-app" {
		t.Errorf("list = %#v, %v", v, err)
	}
}
