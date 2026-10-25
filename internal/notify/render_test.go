package notify

import (
	"strings"
	"testing"
)

func TestRenderSettingsChange(t *testing.T) {
	title, body := render(Event{Trigger: "settings.pending", Change: "set-1", Status: "small: 3 vCPU", RequestedBy: []string{"mehmet"},
		Comment: "more room", Summary: Summary{Keep: 2}, URL: "https://gaia/changes/settings/set-1"})
	if title != "Settings change awaits approval: small: 3 vCPU" {
		t.Errorf("title = %q", title)
	}
	for _, want := range []string{"Proposal: set-1", "Requested by: mehmet", "Why: more room", "Contracts whose plan changes: 2", "Link: https://gaia/changes/settings/set-1"} {
		if !strings.Contains(body, want) {
			t.Errorf("%q not in %q", want, body)
		}
	}
	_, body = render(Event{Trigger: "settings.rejected", Change: "set-1", Status: "x", RequestedBy: []string{"mehmet"}, DecidedBy: "ayse", Comment: "no"})
	if !strings.Contains(body, "Decision: ayse: no") || strings.Contains(body, "Why") {
		t.Errorf("rejected = %q", body)
	}
	_, body = render(Event{Trigger: "settings.applied", Change: "set-1", Status: "x", Label: "v4"})
	if !strings.Contains(body, "Settings version: v4") {
		t.Errorf("applied = %q", body)
	}
}
