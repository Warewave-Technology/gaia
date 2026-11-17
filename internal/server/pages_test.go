package server

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/store"
)

var pagesOut = flag.String("pages", "", "write the role × page matrix (markdown) to this file")

// pageAccess is web/src/app/access.json: the screens' own table of who sees what.
type pageAccess struct {
	Abilities map[string][]string `json:"abilities"`
	Pages     []struct {
		Path     string              `json:"path"`
		Name     string              `json:"name"`
		Requires *string             `json:"requires"`
		Reads    []string            `json:"reads"`
		Actions  map[string][]string `json:"actions"`
	} `json:"pages"`
}

// Every screen, every role (T20 gate c): a role the screen is for gets every read it makes; a role it is not for is
// refused at least one of them by the API, not only hidden by the UI; an action is allowed exactly for the roles its
// ability names; without a session every read is 401.
func TestPageRoles(t *testing.T) {
	b, err := os.ReadFile("../../web/src/app/access.json")
	if err != nil {
		t.Fatal(err)
	}
	var acc pageAccess
	if err := json.Unmarshal(b, &acc); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	f.seed()
	path := func(ep string) (string, string) {
		method, p, _ := strings.Cut(ep, " ")
		p = strings.ReplaceAll(f.path(route{path: p}), "{name}", "result.json")
		return method, p
	}
	roles := []string{"", store.RoleAuditor, store.RoleOperator, store.RoleAdmin}
	var md strings.Builder
	md.WriteString("| Screen | Endpoint | anonymous | auditor | operator | admin |\n|---|---|---|---|---|---|\n")
	for _, pg := range acc.Pages {
		for _, role := range roles {
			sees := role != "" && (pg.Requires == nil || slices.Contains(acc.Abilities[*pg.Requires], role))
			refused := 0
			for _, ep := range pg.Reads {
				m, p := path(ep)
				switch st := f.do(f.tokens[role], m, p, "", nil); {
				case role == "" && st != http.StatusUnauthorized:
					t.Errorf("%s: %s without a session: %d", pg.Name, ep, st)
				case sees && st != http.StatusOK:
					t.Errorf("%s as %s: %s: %d", pg.Name, role, ep, st)
				case st == http.StatusForbidden:
					refused++
				}
			}
			if role != "" && !sees && refused == 0 {
				t.Errorf("%s is not for %s, but the API gives it every read", pg.Name, role)
			}
			for ability, eps := range pg.Actions {
				for _, ep := range eps {
					m, p := path(ep)
					st := f.do(f.tokens[role], m, p, "", nil)
					allowed := slices.Contains(acc.Abilities[ability], role)
					if role == "" && st != http.StatusUnauthorized || role != "" && !allowed && st != http.StatusForbidden ||
						allowed && (st == http.StatusUnauthorized || st == http.StatusForbidden) {
						t.Errorf("%s as %q: %s (%s): %d", pg.Name, role, ep, ability, st)
					}
				}
			}
		}
		for _, ep := range pg.Reads {
			md.WriteString(pageRow(f, pg.Name+" `"+pg.Path+"`", ep, roles, path))
		}
		for ability, eps := range pg.Actions {
			for _, ep := range eps {
				md.WriteString(pageRow(f, fmt.Sprintf("%s `%s` (%s)", pg.Name, pg.Path, ability), ep, roles, path))
			}
		}
	}
	if *pagesOut != "" {
		if err := os.WriteFile(*pagesOut, []byte(md.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("%d screens × %d roles", len(acc.Pages), len(roles))
}

func pageRow(f *fixture, screen, ep string, roles []string, path func(string) (string, string)) string {
	row := fmt.Sprintf("| %s | `%s` |", screen, ep)
	for _, role := range roles {
		m, p := path(ep)
		row += fmt.Sprintf(" %d |", f.do(f.tokens[role], m, p, "", nil))
	}
	return row + "\n"
}
