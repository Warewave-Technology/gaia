package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/repo"
)

func TestOperationsSchedulesAndAccess(t *testing.T) {
	e := setup(t)
	e.git.Write("platform/teams.yaml", `apiVersion: gaia/v1alpha1
kind: Team
metadata: { name: team-platform }
spec: {}
---
apiVersion: gaia/v1alpha1
kind: Group
metadata: { name: web-admins }
spec: { owner: team-platform, teams: [team-platform] }
`)
	e.git.Write("contracts/web.yaml", `apiVersion: gaia/v1alpha1
kind: Server
metadata: { name: web, labels: { app: webx, env: test, owner: team-platform } }
spec:
  replicas: 0
  image: rocky9
  size: small
  network: lab-poc
  baseline: linux-base
  access:
    - { group: web-admins, sudo: true }
    - { team: team-platform, sudo: [/usr/bin/systemctl restart nginx] }
`)
	e.git.Write("contracts/operations/weekly.yaml", `apiVersion: gaia/v1alpha1
kind: OperationSchedule
metadata: { name: weekly-reboot }
spec:
  recurrence: "FREQ=WEEKLY;BYDAY=SA"
  start: "03:00"
  duration: 2h
  approval: once
  output: contracts/operations
  template:
    action: reboot
    selector: 'labels.env == "test"'
`)
	op, err := catalog.Render(catalog.KindOperation, "weekly-reboot-20261031", nil, &catalog.OperationSpec{
		Action: "reboot", Selector: `labels.env == "test"`, Window: &catalog.Window{Start: "2026-10-31T03:00:00Z", End: "2026-10-31T05:00:00Z"},
	}, "OperationSchedule/weekly-reboot", "2026-10-31T03:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	e.git.Write("contracts/operations/weekly-reboot-20261031.yaml", string(op))
	e.git.Commit("operations and access")
	cat := &CatalogSource{Repos: e.repos, Platform: repo.Rev{Name: "platform", URL: e.repo, Paths: []string{"platform", "contracts"}}}
	if err := cat.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{Listen: "127.0.0.1:0", Catalog: cat}, e.store)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	var ops []OperationState
	if code := get(t, hs.URL+"/api/v1/operations", &ops); code != 200 || len(ops) != 1 {
		t.Fatalf("operations: %d %+v", code, ops)
	}
	if o := ops[0]; o.Status != "" || o.Action != "reboot" || o.Window == nil || o.Window.End != "2026-10-31T05:00:00Z" || o.Schedule != "OperationSchedule/weekly-reboot" {
		t.Errorf("operation = %+v", o)
	}
	var scheds []ScheduleInfo
	if code := get(t, hs.URL+"/api/v1/schedules", &scheds); code != 200 || len(scheds) != 1 {
		t.Fatalf("schedules: %d %+v", code, scheds)
	}
	if s := scheds[0]; s.Action != "reboot" || s.Recurrence != "FREQ=WEEKLY;BYDAY=SA" || strings.Join(s.Operations, ",") != "weekly-reboot-20261031" {
		t.Errorf("schedule = %+v", s)
	}
	var acc []AccessEntry
	if code := get(t, hs.URL+"/api/v1/access", &acc); code != 200 || len(acc) != 2 {
		t.Fatalf("access: %d %+v", code, acc)
	}
	if acc[0].Group != "web-admins" || !acc[0].Sudo || acc[1].Team != "team-platform" || acc[1].Sudo || strings.Join(acc[1].Commands, ",") != "/usr/bin/systemctl restart nginx" {
		t.Errorf("access = %+v", acc)
	}
}
