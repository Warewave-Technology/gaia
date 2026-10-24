package zabbix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fake is the part of the Zabbix API the client uses, in memory.
type fake struct {
	mu         sync.Mutex
	groups     map[string]string // name -> id
	templates  map[string]string
	hosts      map[string]map[string]any
	next       int
	calls      []string
	users      map[string]string // username -> id
	userGroups map[string]map[string]any
}

func newFake() *fake {
	return &fake{groups: map[string]string{"Gaia": "1"}, templates: map[string]string{"ICMP Ping": "10564"}, hosts: map[string]map[string]any{}, next: 100,
		users: map[string]string{"ayse": "7"}, userGroups: map[string]map[string]any{}}
}

func (f *fake) id() string { f.next++; return strconv.Itoa(f.next) }

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		ID     int             `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if r.Header.Get("Authorization") != "Bearer tok" {
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "error": map[string]any{"code": -32602, "message": "Invalid params.", "data": "Not authorized."}, "id": req.ID})
		return
	}
	f.calls = append(f.calls, req.Method)
	var p map[string]any
	_ = json.Unmarshal(req.Params, &p)
	var result any
	switch req.Method {
	case "hostgroup.get":
		out := []map[string]string{}
		for _, n := range p["filter"].(map[string]any)["name"].([]any) {
			if id, ok := f.groups[n.(string)]; ok {
				out = append(out, map[string]string{"groupid": id, "name": n.(string)})
			}
		}
		result = out
	case "hostgroup.create":
		id := f.id()
		f.groups[p["name"].(string)] = id
		result = map[string]any{"groupids": []string{id}}
	case "template.get":
		out := []map[string]string{}
		for _, n := range p["filter"].(map[string]any)["host"].([]any) {
			if id, ok := f.templates[n.(string)]; ok {
				out = append(out, map[string]string{"templateid": id, "host": n.(string)})
			}
		}
		result = out
	case "host.get":
		out := []map[string]any{}
		for id, h := range f.hosts {
			if ids, ok := p["hostids"].([]any); ok && !slices.Contains(ids, any(id)) {
				continue
			}
			if tags, ok := p["tags"].([]any); ok {
				want := tags[0].(map[string]any)
				if !hasTag(h, want["tag"].(string), want["value"].(string)) {
					continue
				}
			}
			out = append(out, h)
		}
		result = out
	case "host.create":
		id := f.id()
		f.hosts[id] = record(id, p, []any{map[string]any{"interfaceid": f.id(), "ip": p["interfaces"].([]any)[0].(map[string]any)["ip"], "main": "1", "type": "1"}})
		result = map[string]any{"hostids": []string{id}}
	case "host.update":
		id := p["hostid"].(string)
		f.hosts[id] = record(id, p, f.hosts[id]["interfaces"].([]any))
		result = map[string]any{"hostids": []string{id}}
	case "hostinterface.update":
		for _, h := range f.hosts {
			for _, i := range h["interfaces"].([]any) {
				if i.(map[string]any)["interfaceid"] == p["interfaceid"] {
					i.(map[string]any)["ip"] = p["ip"]
				}
			}
		}
		result = map[string]any{}
	case "user.get":
		out := []map[string]string{}
		for _, n := range p["filter"].(map[string]any)["username"].([]any) {
			if id, ok := f.users[n.(string)]; ok {
				out = append(out, map[string]string{"userid": id, "username": n.(string)})
			}
		}
		result = out
	case "usergroup.get":
		out := []map[string]any{}
		for _, n := range p["filter"].(map[string]any)["name"].([]any) {
			if g, ok := f.userGroups[n.(string)]; ok {
				out = append(out, map[string]any{"usrgrpid": g["usrgrpid"]})
			}
		}
		result = out
	case "usergroup.create":
		p["usrgrpid"] = f.id()
		f.userGroups[p["name"].(string)] = p
		result = map[string]any{"usrgrpids": []any{p["usrgrpid"]}}
	case "usergroup.update":
		for n, g := range f.userGroups {
			if g["usrgrpid"] == p["usrgrpid"] {
				p["name"] = n
				f.userGroups[n] = p
			}
		}
		result = map[string]any{"usrgrpids": []any{p["usrgrpid"]}}
	case "host.delete":
		var ids []string
		_ = json.Unmarshal(req.Params, &ids)
		for _, id := range ids {
			delete(f.hosts, id)
		}
		result = map[string]any{"hostids": ids}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": result, "id": req.ID})
}

func record(id string, p map[string]any, ifaces []any) map[string]any {
	return map[string]any{
		"hostid": id, "host": p["host"], "monitored_by": p["monitored_by"], "proxyid": "0",
		"hostgroups": p["groups"], "parentTemplates": p["templates"], "tags": p["tags"], "interfaces": ifaces,
	}
}

func hasTag(h map[string]any, tag, value string) bool {
	for _, t := range h["tags"].([]any) {
		m := t.(map[string]any)
		if m["tag"] == tag && m["value"] == value {
			return true
		}
	}
	return false
}

func host() Host {
	return Host{
		UID: "uid1", Hostname: "web01", IP: "10.0.0.5",
		Groups: []string{"Gaia", "Gaia/env/test"}, Templates: []string{"ICMP Ping"},
		Tags: []Tag{{Tag: "env", Value: "test"}, {Tag: "contract", Value: "web"}},
	}
}

func TestRegisterCreatesThenKeepsThenUpdates(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := New(srv.URL, "tok")
	ctx := context.Background()

	r, err := c.Register(ctx, host(), "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Action != "create" || f.groups["Gaia/env/test"] == "" {
		t.Fatalf("result %+v, groups %v", r, f.groups)
	}
	h := f.hosts[r.HostID]
	if h["host"] != "web01" || !hasTag(h, UIDTag, "uid1") || !hasTag(h, "env", "test") {
		t.Errorf("host = %v", h)
	}

	again, err := c.Register(ctx, host(), r.HostID)
	if err != nil || again.Action != "same" || again.HostID != r.HostID {
		t.Errorf("second register: %+v, %v", again, err)
	}
	if slices.Contains(f.calls[len(f.calls)-2:], "host.update") {
		t.Errorf("an unchanged host was updated: %v", f.calls)
	}

	// Found by its UID tag when the recorded ID is unknown; the address
	// and tags follow the contract.
	changed := host()
	changed.IP = "10.0.0.6"
	changed.Tags = changed.Tags[:1]
	up, err := c.Register(ctx, changed, "")
	if err != nil || up.Action != "update" || up.HostID != r.HostID {
		t.Fatalf("update: %+v, %v", up, err)
	}
	h = f.hosts[r.HostID]
	if hasTag(h, "contract", "web") || h["interfaces"].([]any)[0].(map[string]any)["ip"] != "10.0.0.6" {
		t.Errorf("after update: %v", h)
	}
	if len(f.hosts) != 1 {
		t.Errorf("%d hosts", len(f.hosts))
	}

	ok, err := c.Delete(ctx, "uid1", r.HostID)
	if err != nil || !ok || len(f.hosts) != 0 {
		t.Errorf("delete: %v %v, %d hosts", ok, err, len(f.hosts))
	}
	if ok, err := c.Delete(ctx, "uid1", r.HostID); err != nil || ok {
		t.Errorf("second delete: %v %v", ok, err)
	}
}

func TestAHostWithTheSameNameButAnotherUIDIsNotMine(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := New(srv.URL, "tok")
	ctx := context.Background()
	old, err := c.Register(ctx, host(), "")
	if err != nil {
		t.Fatal(err)
	}
	// The recorded ID points at a host of another UID: it is not touched.
	other := host()
	other.UID = "uid2"
	if ok, err := c.Delete(ctx, "uid2", old.HostID); err != nil || ok {
		t.Errorf("deleted another UID's host: %v %v", ok, err)
	}
	if len(f.hosts) != 1 {
		t.Errorf("%d hosts", len(f.hosts))
	}
}

func TestErrors(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	ctx := context.Background()
	if _, err := New(srv.URL, "wrong").Register(ctx, host(), ""); err == nil || err.Error() != "zabbix hostgroup.get: Invalid params. Not authorized." {
		t.Errorf("err = %v", err)
	}
	h := host()
	h.Templates = []string{"Nope"}
	if _, err := New(srv.URL, "tok").Register(ctx, h, ""); err == nil || err.Error() != "zabbix: no template Nope" {
		t.Errorf("err = %v", err)
	}
}

// TestLab runs against a real Zabbix when $GAIA_ZABBIX_URL and
// $GAIA_ZABBIX_TOKEN are set.
func TestLab(t *testing.T) {
	url, tok := os.Getenv("GAIA_ZABBIX_URL"), os.Getenv("GAIA_ZABBIX_TOKEN")
	if url == "" || tok == "" {
		t.Skip("GAIA_ZABBIX_URL and GAIA_ZABBIX_TOKEN not set")
	}
	c := New(url, tok)
	ctx := context.Background()
	h := host()
	h.UID, h.Hostname, h.Groups = "gaia-test-uid", "gaia-zabbix-test", []string{"Gaia/test"}
	r, err := c.Register(ctx, h, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = c.Delete(ctx, h.UID, r.HostID) })
	again, err := c.Register(ctx, h, r.HostID)
	if err != nil || again.Action != "same" {
		t.Errorf("second register: %+v, %v", again, err)
	}
	h.IP = "10.0.0.6"
	if up, err := c.Register(ctx, h, r.HostID); err != nil || up.Action != "update" {
		t.Errorf("update: %+v, %v", up, err)
	}
	if ok, err := c.Delete(ctx, h.UID, r.HostID); err != nil || !ok {
		t.Errorf("delete: %v %v", ok, err)
	}
}

func TestUserGroupsAreExactlyGaias(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := New(srv.URL, "tok")
	ctx := context.Background()
	missing, err := c.UserGroup(ctx, "Gaia/team-web", map[string]int{"Gaia/poc-web": PermissionReadWrite}, []string{"ayse", "can"})
	if err != nil || strings.Join(missing, ",") != "can" {
		t.Fatalf("create: %v %v", missing, err)
	}
	g := f.userGroups["Gaia/team-web"]
	rights, _ := json.Marshal(g["hostgroup_rights"])
	users, _ := json.Marshal(g["users"])
	if string(rights) != `[{"id":"`+f.groups["Gaia/poc-web"]+`","permission":3}]` || string(users) != `[{"userid":"7"}]` {
		t.Errorf("group = %s %s", rights, users)
	}
	// Updated in place, emptied when the team has nothing left.
	if _, err := c.UserGroup(ctx, "Gaia/team-web", map[string]int{}, nil); err != nil {
		t.Fatal(err)
	}
	if g := f.userGroups["Gaia/team-web"]; len(g["hostgroup_rights"].([]any)) != 0 || len(g["users"].([]any)) != 0 || len(f.userGroups) != 1 {
		t.Errorf("after emptying: %+v", f.userGroups)
	}
	if _, err := c.UserGroup(ctx, "Zabbix administrators", nil, nil); err == nil {
		t.Error("changed a group that is not Gaia's")
	}
}

// TestLabUserGroups runs against a real Zabbix (make dev-zabbix, or the
// lab): a Gaia user group with rights and an existing user.
func TestLabUserGroups(t *testing.T) {
	url, tok := os.Getenv("GAIA_ZABBIX_URL"), os.Getenv("GAIA_ZABBIX_TOKEN")
	if url == "" || tok == "" {
		t.Skip("GAIA_ZABBIX_URL and GAIA_ZABBIX_TOKEN not set")
	}
	c := New(url, tok)
	ctx := context.Background()
	name := "Gaia/test-team"
	t.Cleanup(func() {
		var g []struct {
			ID string `json:"usrgrpid"`
		}
		_ = c.call(ctx, "usergroup.get", map[string]any{"output": []string{"usrgrpid"}, "filter": map[string]any{"name": []string{name}}}, &g)
		if len(g) > 0 {
			_ = c.call(ctx, "usergroup.delete", []string{g[0].ID}, nil)
		}
	})
	missing, err := c.UserGroup(ctx, name, map[string]int{"Gaia/contract/test": PermissionReadWrite, "Gaia/contract/other": PermissionRead}, []string{"Admin", "nobody-here"})
	if err != nil || strings.Join(missing, ",") != "nobody-here" {
		t.Fatalf("create: %v %v", missing, err)
	}
	var got []struct {
		Rights []struct {
			ID         string `json:"id"`
			Permission string `json:"permission"`
		} `json:"hostgroup_rights"`
		Users []struct {
			Username string `json:"username"`
		} `json:"users"`
	}
	if err := c.call(ctx, "usergroup.get", map[string]any{"output": "extend", "selectHostGroupRights": "extend", "selectUsers": []string{"username"}, "filter": map[string]any{"name": []string{name}}}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Rights) != 2 || len(got[0].Users) != 1 || got[0].Users[0].Username != "Admin" {
		t.Fatalf("group = %+v", got)
	}
	// Everything taken back: the group stays, empty.
	if _, err := c.UserGroup(ctx, name, nil, nil); err != nil {
		t.Fatal(err)
	}
	_ = c.call(ctx, "usergroup.get", map[string]any{"output": "extend", "selectHostGroupRights": "extend", "selectUsers": []string{"username"}, "filter": map[string]any{"name": []string{name}}}, &got)
	if len(got) != 1 || len(got[0].Rights) != 0 || len(got[0].Users) != 0 {
		t.Errorf("emptied group = %+v", got)
	}
}
