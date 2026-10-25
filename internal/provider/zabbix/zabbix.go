package zabbix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// UIDTag is the tag that marks a host as Gaia's server (design 9.1).
const UIDTag = "gaia.uid"

// Client talks to the Zabbix API (7.0) with an API token.
type Client struct {
	endpoint, token string
	http            *http.Client
	id              atomic.Int64
}

// New returns a client for the api_jsonrpc.php endpoint.
func New(endpoint, token string) *Client {
	return &Client{endpoint: endpoint, token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

// Error is an error the API returned.
type Error struct {
	Method  string
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("zabbix %s: %s %s", e.Method, e.Message, e.Data)
}

func (c *Client) call(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params, "id": c.id.Add(1)})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json-rpc")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("zabbix: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("zabbix %s: HTTP %d", method, resp.StatusCode)
	}
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *Error          `json:"error"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("zabbix %s: %w", method, err)
	}
	if r.Error != nil {
		r.Error.Method = method
		return r.Error
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(r.Result, out)
}

// Tag is a Zabbix host tag.
type Tag struct {
	Tag   string `json:"tag"`
	Value string `json:"value"`
}

// Host is a server as Gaia registers it.
type Host struct {
	UID       string
	Hostname  string
	IP        string
	Groups    []string // created when missing
	Templates []string // technical names; must exist
	Tags      []Tag    // the UID tag is added
	Proxy     string   // optional
}

// Result is what Register did.
type Result struct {
	HostID string
	Action string // create | update | same
}

type hostRecord struct {
	HostID      string `json:"hostid"`
	Host        string `json:"host"`
	MonitoredBy string `json:"monitored_by"`
	ProxyID     string `json:"proxyid"`
	Groups      []struct {
		GroupID string `json:"groupid"`
	} `json:"hostgroups"`
	Templates []struct {
		TemplateID string `json:"templateid"`
	} `json:"parentTemplates"`
	Tags       []Tag `json:"tags"`
	Interfaces []struct {
		InterfaceID string `json:"interfaceid"`
		IP          string `json:"ip"`
		Main        string `json:"main"`
		Type        string `json:"type"`
	} `json:"interfaces"`
}

// Register creates or updates the host of h.UID: found by hostID when
// given, else by its UID tag. Hosts are never matched by name, since names
// are reused (design 9.1).
func (c *Client) Register(ctx context.Context, h Host, hostID string) (Result, error) {
	groups, err := c.groupIDs(ctx, h.Groups)
	if err != nil {
		return Result{}, err
	}
	templates, err := c.templateIDs(ctx, h.Templates)
	if err != nil {
		return Result{}, err
	}
	proxy := ""
	if h.Proxy != "" {
		if proxy, err = c.proxyID(ctx, h.Proxy); err != nil {
			return Result{}, err
		}
	}
	tags := append([]Tag{{Tag: UIDTag, Value: h.UID}}, h.Tags...)
	sort.Slice(tags[1:], func(i, j int) bool { return tags[1+i].Tag < tags[1+j].Tag })

	cur, err := c.find(ctx, h.UID, hostID)
	if err != nil {
		return Result{}, err
	}
	params := map[string]any{
		"host": h.Hostname, "name": h.Hostname,
		"groups": idList("groupid", groups), "templates": idList("templateid", templates), "tags": tags,
		"monitored_by": "0",
	}
	if proxy != "" {
		params["monitored_by"], params["proxyid"] = "1", proxy
	}
	if cur == nil {
		params["interfaces"] = []map[string]any{{"type": 1, "main": 1, "useip": 1, "ip": h.IP, "dns": "", "port": "10050"}}
		var out struct {
			HostIDs []string `json:"hostids"`
		}
		if err := c.call(ctx, "host.create", params, &out); err != nil {
			return Result{}, err
		}
		return Result{HostID: out.HostIDs[0], Action: "create"}, nil
	}

	changed := false
	if !same(cur, h, groups, templates, tags, proxy) {
		params["hostid"] = cur.HostID
		// Gaia owns the host: its templates replace the linked ones.
		if err := c.call(ctx, "host.update", params, nil); err != nil {
			return Result{}, err
		}
		changed = true
	}
	for _, i := range cur.Interfaces {
		if i.Main == "1" && i.Type == "1" && i.IP != h.IP {
			if err := c.call(ctx, "hostinterface.update", map[string]any{"interfaceid": i.InterfaceID, "ip": h.IP}, nil); err != nil {
				return Result{}, err
			}
			changed = true
		}
	}
	if changed {
		return Result{HostID: cur.HostID, Action: "update"}, nil
	}
	return Result{HostID: cur.HostID, Action: "same"}, nil
}

// Delete removes the host of uid, with its history (design 9.3). A host
// that is already gone is not an error.
func (c *Client) Delete(ctx context.Context, uid, hostID string) (bool, error) {
	cur, err := c.find(ctx, uid, hostID)
	if err != nil || cur == nil {
		return false, err
	}
	return true, c.call(ctx, "host.delete", []string{cur.HostID}, nil)
}

// HostState is a host as Zabbix has it, for drift (design 7.5).
type HostState struct {
	HostID    string
	Name      string
	Enabled   bool
	IP        string   // the main interface's
	Groups    []string // names
	Templates []string // technical names
}

// State reads the host of uid, found as Register finds it; nil when there
// is none.
func (c *Client) State(ctx context.Context, uid, hostID string) (*HostState, error) {
	cur, err := c.find(ctx, uid, hostID)
	if err != nil || cur == nil {
		return nil, err
	}
	var hosts []struct {
		HostID string `json:"hostid"`
		Host   string `json:"host"`
		Status string `json:"status"`
		Groups []struct {
			Name string `json:"name"`
		} `json:"hostgroups"`
		Templates []struct {
			Host string `json:"host"`
		} `json:"parentTemplates"`
	}
	if err := c.call(ctx, "host.get", map[string]any{"hostids": []string{cur.HostID}, "output": []string{"hostid", "host", "status"},
		"selectHostGroups": []string{"name"}, "selectParentTemplates": []string{"host"}}, &hosts); err != nil {
		return nil, err
	}
	if len(hosts) != 1 {
		return nil, nil
	}
	h := hosts[0]
	st := &HostState{HostID: h.HostID, Name: h.Host, Enabled: h.Status == "0"}
	for _, i := range cur.Interfaces {
		if i.Main == "1" {
			st.IP = i.IP
		}
	}
	for _, g := range h.Groups {
		st.Groups = append(st.Groups, g.Name)
	}
	for _, t := range h.Templates {
		st.Templates = append(st.Templates, t.Host)
	}
	sort.Strings(st.Groups)
	sort.Strings(st.Templates)
	return st, nil
}

// GaiaHosts are the hosts that carry a gaia.uid tag, by UID, with their
// names: for discovery, to find the ones whose server is gone.
func (c *Client) GaiaHosts(ctx context.Context) (map[string]string, error) {
	var hosts []struct {
		Host string `json:"host"`
		Tags []Tag  `json:"tags"`
	}
	if err := c.call(ctx, "host.get", map[string]any{"output": []string{"host"}, "selectTags": "extend",
		"tags": []map[string]any{{"tag": UIDTag, "operator": 4}}}, &hosts); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, h := range hosts {
		for _, t := range h.Tags {
			if t.Tag == UIDTag {
				out[t.Value] = h.Host
			}
		}
	}
	return out, nil
}

// SetEnabled switches a host's monitoring on or off: a stopped server
// waiting to be deleted is not monitored, and keeps its history
// (design 9.8).
func (c *Client) SetEnabled(ctx context.Context, hostID string, enabled bool) error {
	status := "1"
	if enabled {
		status = "0"
	}
	return c.call(ctx, "host.update", map[string]any{"hostid": hostID, "status": status}, nil)
}

func (c *Client) find(ctx context.Context, uid, hostID string) (*hostRecord, error) {
	params := map[string]any{
		"output": []string{"hostid", "host", "monitored_by", "proxyid"}, "selectHostGroups": []string{"groupid"},
		"selectParentTemplates": []string{"templateid"}, "selectTags": "extend",
		"selectInterfaces": []string{"interfaceid", "ip", "main", "type"},
	}
	if hostID != "" {
		params["hostids"] = []string{hostID}
	} else {
		params["tags"] = []map[string]any{{"tag": UIDTag, "value": uid, "operator": 1}}
	}
	var hosts []hostRecord
	if err := c.call(ctx, "host.get", params, &hosts); err != nil {
		return nil, err
	}
	// The recorded host must still carry the UID: a host ID can point at
	// another host after a restore.
	var mine []hostRecord
	for _, h := range hosts {
		if slices.Contains(h.Tags, Tag{Tag: UIDTag, Value: uid}) {
			mine = append(mine, h)
		}
	}
	switch {
	case len(mine) == 0 && hostID != "":
		return c.find(ctx, uid, "")
	case len(mine) == 0:
		return nil, nil
	case len(mine) > 1:
		return nil, fmt.Errorf("zabbix: %d hosts carry %s=%s", len(mine), UIDTag, uid)
	}
	return &mine[0], nil
}

func same(cur *hostRecord, h Host, groups, templates []string, tags []Tag, proxy string) bool {
	if cur.Host != h.Hostname {
		return false
	}
	if (proxy != "") != (cur.MonitoredBy == "1") || (proxy != "" && cur.ProxyID != proxy) {
		return false
	}
	var g, t []string
	for _, x := range cur.Groups {
		g = append(g, x.GroupID)
	}
	for _, x := range cur.Templates {
		t = append(t, x.TemplateID)
	}
	ct := slices.Clone(cur.Tags)
	sort.Slice(ct, func(i, j int) bool {
		return ct[i].Tag < ct[j].Tag || ct[i].Tag == ct[j].Tag && ct[i].Value < ct[j].Value
	})
	want := slices.Clone(tags)
	sort.Slice(want, func(i, j int) bool {
		return want[i].Tag < want[j].Tag || want[i].Tag == want[j].Tag && want[i].Value < want[j].Value
	})
	return sameSet(g, groups) && sameSet(t, templates) && slices.Equal(ct, want)
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	sort.Strings(a)
	sort.Strings(b)
	return slices.Equal(a, b)
}

func idList(key string, ids []string) []map[string]string {
	out := []map[string]string{}
	for _, id := range ids {
		out = append(out, map[string]string{key: id})
	}
	return out
}

// groupIDs returns the IDs of the named host groups, creating missing ones.
func (c *Client) groupIDs(ctx context.Context, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, errors.New("zabbix: a host needs at least one host group")
	}
	var found []struct {
		GroupID string `json:"groupid"`
		Name    string `json:"name"`
	}
	if err := c.call(ctx, "hostgroup.get", map[string]any{"output": []string{"groupid", "name"}, "filter": map[string]any{"name": names}}, &found); err != nil {
		return nil, err
	}
	byName := map[string]string{}
	for _, g := range found {
		byName[g.Name] = g.GroupID
	}
	var out []string
	for _, n := range names {
		id, ok := byName[n]
		if !ok {
			var created struct {
				GroupIDs []string `json:"groupids"`
			}
			if err := c.call(ctx, "hostgroup.create", map[string]any{"name": n}, &created); err != nil {
				return nil, err
			}
			id = created.GroupIDs[0]
		}
		out = append(out, id)
	}
	return out, nil
}

func (c *Client) templateIDs(ctx context.Context, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	var found []struct {
		TemplateID string `json:"templateid"`
		Host       string `json:"host"`
	}
	if err := c.call(ctx, "template.get", map[string]any{"output": []string{"templateid", "host"}, "filter": map[string]any{"host": names}}, &found); err != nil {
		return nil, err
	}
	byName := map[string]string{}
	for _, t := range found {
		byName[t.Host] = t.TemplateID
	}
	var out, missing []string
	for _, n := range names {
		if id, ok := byName[n]; ok {
			out = append(out, id)
		} else {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("zabbix: no template %s", strings.Join(missing, ", "))
	}
	return out, nil
}

func (c *Client) proxyID(ctx context.Context, name string) (string, error) {
	var found []struct {
		ProxyID string `json:"proxyid"`
	}
	if err := c.call(ctx, "proxy.get", map[string]any{"output": []string{"proxyid"}, "filter": map[string]any{"name": name}}, &found); err != nil {
		return "", err
	}
	if len(found) != 1 {
		return "", fmt.Errorf("zabbix: no proxy %s", name)
	}
	return found[0].ProxyID, nil
}

// Zabbix permissions of a user group on a host group.
const (
	PermissionRead      = 2
	PermissionReadWrite = 3
)

// UserGroupPrefix names the user groups Gaia manages; it leaves others
// alone.
const UserGroupPrefix = "Gaia/"

// UserGroup makes a Gaia user group exactly so (design 14.4): rights on
// host groups by name, and as members the Zabbix users of those names.
// Gaia does not create Zabbix users; it returns the names it found none
// for.
func (c *Client) UserGroup(ctx context.Context, name string, rights map[string]int, usernames []string) ([]string, error) {
	if !strings.HasPrefix(name, UserGroupPrefix) {
		return nil, fmt.Errorf("zabbix: %q is not a Gaia user group", name)
	}
	hostGroups := make([]string, 0, len(rights))
	for g := range rights {
		hostGroups = append(hostGroups, g)
	}
	sort.Strings(hostGroups)
	var rightList []map[string]any
	if len(hostGroups) > 0 {
		ids, err := c.groupIDs(ctx, hostGroups)
		if err != nil {
			return nil, err
		}
		for i, id := range ids {
			rightList = append(rightList, map[string]any{"id": id, "permission": rights[hostGroups[i]]})
		}
	}
	users := []map[string]string{}
	var missing []string
	if len(usernames) > 0 {
		var found []struct {
			UserID   string `json:"userid"`
			Username string `json:"username"`
		}
		if err := c.call(ctx, "user.get", map[string]any{"output": []string{"userid", "username"}, "filter": map[string]any{"username": usernames}}, &found); err != nil {
			return nil, err
		}
		byName := map[string]string{}
		for _, u := range found {
			byName[u.Username] = u.UserID
		}
		for _, n := range usernames {
			if id, ok := byName[n]; ok {
				users = append(users, map[string]string{"userid": id})
			} else {
				missing = append(missing, n)
			}
		}
	}
	if rightList == nil {
		rightList = []map[string]any{}
	}
	var existing []struct {
		ID string `json:"usrgrpid"`
	}
	if err := c.call(ctx, "usergroup.get", map[string]any{"output": []string{"usrgrpid"}, "filter": map[string]any{"name": []string{name}}}, &existing); err != nil {
		return nil, err
	}
	params := map[string]any{"hostgroup_rights": rightList, "users": users}
	if len(existing) == 0 {
		params["name"] = name
		return missing, c.call(ctx, "usergroup.create", params, nil)
	}
	params["usrgrpid"] = existing[0].ID
	return missing, c.call(ctx, "usergroup.update", params, nil)
}

// Maintenance puts hosts in a maintenance (with data collection) until
// until, for an Operation (design 6.4); it returns the maintenance's ID.
func (c *Client) Maintenance(ctx context.Context, name string, hostIDs []string, until time.Time) (string, error) {
	now := time.Now().Unix()
	end := until.Unix()
	if end <= now {
		end = now + 3600
	}
	hosts := []map[string]string{}
	for _, id := range hostIDs {
		hosts = append(hosts, map[string]string{"hostid": id})
	}
	var out struct {
		IDs []string `json:"maintenanceids"`
	}
	err := c.call(ctx, "maintenance.create", map[string]any{
		"name": name, "active_since": now, "active_till": end, "maintenance_type": 0, "hosts": hosts,
		"timeperiods": []map[string]any{{"timeperiod_type": 0, "start_date": now, "period": end - now}},
	}, &out)
	if err != nil {
		return "", err
	}
	if len(out.IDs) == 0 {
		return "", errors.New("zabbix: maintenance.create returned no ID")
	}
	return out.IDs[0], nil
}

// EndMaintenance deletes a maintenance.
func (c *Client) EndMaintenance(ctx context.Context, id string) error {
	return c.call(ctx, "maintenance.delete", []string{id}, nil)
}

// Check reads the API's version, which Zabbix answers without a token,
// and counts the hosts the token may see: the endpoint and the token
// both work.
func (c *Client) Check(ctx context.Context) (version string, hosts int, err error) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "apiinfo.version", "params": map[string]any{}, "id": c.id.Add(1)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json-rpc")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("zabbix: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var v struct {
		Result string `json:"result"`
		Error  *Error `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v); err != nil {
		return "", 0, fmt.Errorf("zabbix apiinfo.version: %w", err)
	}
	if v.Error != nil {
		v.Error.Method = "apiinfo.version"
		return "", 0, v.Error
	}
	var n string
	if err := c.call(ctx, "host.get", map[string]any{"countOutput": true}, &n); err != nil {
		return v.Result, 0, err
	}
	hosts, _ = strconv.Atoi(n)
	return v.Result, hosts, nil
}
