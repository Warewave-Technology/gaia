package catalog

import (
	"strings"
	"testing"
)

func TestLoadSplitLeavesGitSettingsOut(t *testing.T) {
	size := func(cpu string) []byte {
		return []byte("apiVersion: gaia/v1alpha1\nkind: Size\nmetadata: { name: small }\nspec: { cpu: " + cpu + ", memoryGiB: 2, diskGiB: 20 }\n")
	}
	baseline := []byte("apiVersion: gaia/v1alpha1\nkind: Baseline\nmetadata: { name: linux-base }\nspec: { os: linux, playbook: b.yml }\n")
	cat, err := LoadSplit("c1", []File{
		{Path: "platform/sizes.yaml", Data: size("2")},
		{Path: "platform/baseline.yaml", Data: baseline},
		{Path: SettingsPath(KindSize, "small", false), Data: size("4")},
	})
	if err != nil {
		t.Fatal(err)
	}
	o, ok := cat.Get(KindSize, "small")
	if !ok || o.Spec.(*SizeSpec).CPU != 4 || o.Source.File != "settings:Size/small.yaml" {
		t.Errorf("small = %+v", o)
	}
	if _, ok := cat.Get(KindBaseline, "linux-base"); !ok {
		t.Error("a Git kind was left out")
	}
	if len(cat.Ignored) != 1 || cat.Ignored[0].Key.String() != "Size/small" || cat.Ignored[0].Source.File != "platform/sizes.yaml" {
		t.Errorf("ignored = %+v", cat.Ignored)
	}

	_, err = LoadSplit("c1", []File{{Path: SettingsPath(KindBaseline, "linux-base", false), Data: baseline}})
	if err == nil || !strings.Contains(err.Error(), "Baseline is not a setting") {
		t.Errorf("a Git kind in the settings: %v", err)
	}
	if _, err := Load("c1", []File{{Path: "platform/sizes.yaml", Data: size("2")}, {Path: "platform/more.yaml", Data: size("4")}}); err == nil {
		t.Error("Load no longer finds duplicates")
	}
}

func TestSettingsPaths(t *testing.T) {
	p := SettingsPath(KindNetwork, "lab-poc", false)
	if p != "settings:Network/lab-poc.yaml" || !FromSettings(p) || SettingsFileKind(p) != KindNetwork {
		t.Errorf("%s", p)
	}
	if SettingsPath(KindNetwork, "x", true) != "settings:Network/x.patch.yaml" || FromSettings("platform/x.yaml") {
		t.Error("patch paths")
	}
	if IsSetting(KindAnsibleRole) || IsSetting(KindServer) || !IsSetting(KindAuthConfig) || len(SettingKinds()) != 13 {
		t.Error("kinds")
	}
}
