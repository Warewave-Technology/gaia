package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

var titles = map[string]string{
	"change.pending":    "awaits approval",
	"change.approved":   "approved",
	"change.rejected":   "rejected",
	"change.applied":    "applied",
	"change.failed":     "failed",
	"drift.found":       "drift found",
	"approval.expiring": "approval expiring",
	"access.privileged": "new member of a privileged group from the directory",
	"settings.pending":  "awaits approval",
	"settings.applied":  "applied",
	"settings.rejected": "rejected",
}

// render is a notification as a title and plain text lines.
func render(ev Event) (string, string) {
	what := titles[ev.Trigger]
	if what == "" {
		what = ev.Trigger
	}
	title := fmt.Sprintf("%s: change %s", ev.Contract, what)
	var b strings.Builder
	if ev.Trigger == "drift.found" {
		title = fmt.Sprintf("%s: %s", ev.Contract, what)
		fmt.Fprintf(&b, "Area: %s\n%s", ev.Status, ev.Comment)
		if ev.Change != "" {
			fmt.Fprintf(&b, "\nFix: %s", ev.Change)
		}
		if ev.URL != "" {
			fmt.Fprintf(&b, "\nLink: %s", ev.URL)
		}
		return title, strings.TrimRight(b.String(), "\n")
	}
	if strings.HasPrefix(ev.Trigger, "settings.") {
		// A settings change: Status is its title, Summary its impact.
		title = fmt.Sprintf("Settings change %s: %s", what, ev.Status)
		fmt.Fprintf(&b, "Proposal: %s\nRequested by: %s\n", ev.Change, strings.Join(ev.RequestedBy, ", "))
		if ev.Comment != "" && ev.DecidedBy == "" {
			fmt.Fprintf(&b, "Why: %s\n", ev.Comment)
		}
		if ev.Summary.Keep > 0 {
			fmt.Fprintf(&b, "Contracts whose plan changes: %d\n", ev.Summary.Keep)
		}
		if ev.DecidedBy != "" {
			fmt.Fprintf(&b, "Decision: %s: %s\n", ev.DecidedBy, ev.Comment)
		}
		if ev.Label != "" {
			fmt.Fprintf(&b, "Settings version: %s\n", ev.Label)
		}
		if ev.URL != "" {
			fmt.Fprintf(&b, "Link: %s", ev.URL)
		}
		return title, strings.TrimRight(b.String(), "\n")
	}
	if ev.Trigger == "access.privileged" {
		title = fmt.Sprintf("%s: %s", ev.Group, what)
		fmt.Fprintf(&b, "Person: %s\nTeam: %s\nGroup: %s\n%s", ev.Person, ev.Team, ev.Group, ev.Comment)
		return title, strings.TrimRight(b.String(), "\n")
	}
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	line("Change", ev.Change)
	switch ev.Source {
	case "revert":
		line("Source", "revert")
	case "settings":
		line("Source", "settings change")
	}
	line("Requested by", strings.Join(ev.RequestedBy, ", "))
	line("Plan", fmt.Sprintf("%d to create, %d to keep, %d to delete", ev.Summary.Create, ev.Summary.Keep, ev.Summary.Delete))
	for _, a := range ev.Approvers {
		line("May approve", fmt.Sprintf("%s (%s): %s", a.Name, a.Why, strings.Join(a.Candidates, ", ")))
	}
	line("Approved by", strings.Join(ev.ApprovedBy, ", "))
	if ev.DecidedBy != "" {
		line("Decision", ev.DecidedBy+": "+ev.Comment)
	}
	line("Run", ev.Run)
	line("Error", ev.Error)
	line("Link", ev.URL)
	return title, strings.TrimRight(b.String(), "\n")
}

func slackBody(ev Event) []byte {
	title, body := render(ev)
	b, _ := json.Marshal(map[string]any{
		"text": title,
		"blocks": []any{
			map[string]any{"type": "section", "text": map[string]string{"type": "mrkdwn", "text": "*" + escapeSlack(title) + "*\n" + escapeSlack(body)}},
		},
	})
	return b
}

// escapeSlack keeps Slack from reading names as mentions or links.
func escapeSlack(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// teamsBody is an Adaptive Card message, as Teams workflows take it.
func teamsBody(ev Event) []byte {
	title, body := render(ev)
	var facts []map[string]string
	for _, l := range strings.Split(body, "\n") {
		if k, v, ok := strings.Cut(l, ": "); ok {
			facts = append(facts, map[string]string{"title": k, "value": v})
		}
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"body": []any{
			map[string]any{"type": "TextBlock", "text": title, "weight": "Bolder", "size": "Medium", "wrap": true},
			map[string]any{"type": "FactSet", "facts": facts},
		},
	}
	if ev.URL != "" {
		card["actions"] = []any{map[string]string{"type": "Action.OpenUrl", "title": "Open in Gaia", "url": ev.URL}}
	}
	b, _ := json.Marshal(map[string]any{
		"type":        "message",
		"attachments": []any{map[string]any{"contentType": "application/vnd.microsoft.card.adaptive", "content": card}},
	})
	return b
}
