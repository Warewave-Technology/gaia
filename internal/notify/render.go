package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

var titles = map[string]string{
	"change.pending":    "onay bekliyor",
	"change.approved":   "onaylandı",
	"change.rejected":   "reddedildi",
	"change.applied":    "uygulandı",
	"change.failed":     "başarısız",
	"drift.found":       "sapma bulundu",
	"approval.expiring": "onayın süresi doluyor",
	"access.privileged": "ayrıcalıklı gruba AD'den yeni üye",
}

// render is a notification as a title and plain text lines.
func render(ev Event) (string, string) {
	what := titles[ev.Trigger]
	if what == "" {
		what = ev.Trigger
	}
	title := fmt.Sprintf("%s: değişiklik %s", ev.Contract, what)
	var b strings.Builder
	if ev.Trigger == "access.privileged" {
		title = fmt.Sprintf("%s: %s", ev.Group, what)
		fmt.Fprintf(&b, "Kişi: %s\nTakım: %s\nGrup: %s\n%s", ev.Person, ev.Team, ev.Group, ev.Comment)
		return title, strings.TrimRight(b.String(), "\n")
	}
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	line("Değişiklik", ev.Change)
	if ev.Source == "revert" {
		line("Kaynak", "geri alma")
	}
	line("İsteyen", strings.Join(ev.RequestedBy, ", "))
	line("Plan", fmt.Sprintf("%d oluşturulacak, %d kalacak, %d silinecek", ev.Summary.Create, ev.Summary.Keep, ev.Summary.Delete))
	for _, a := range ev.Approvers {
		line("Onaylayabilecekler", fmt.Sprintf("%s (%s): %s", a.Name, a.Why, strings.Join(a.Candidates, ", ")))
	}
	line("Onaylayan", strings.Join(ev.ApprovedBy, ", "))
	if ev.DecidedBy != "" {
		line("Karar", ev.DecidedBy+": "+ev.Comment)
	}
	line("Run", ev.Run)
	line("Hata", ev.Error)
	line("Bağlantı", ev.URL)
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
		card["actions"] = []any{map[string]string{"type": "Action.OpenUrl", "title": "Gaia'da aç", "url": ev.URL}}
	}
	b, _ := json.Marshal(map[string]any{
		"type":        "message",
		"attachments": []any{map[string]any{"contentType": "application/vnd.microsoft.card.adaptive", "content": card}},
	})
	return b
}
