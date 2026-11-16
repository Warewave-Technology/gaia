package server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Warewave-Technology/gaia/internal/store"
)

// Role sets.
var (
	public     []string // nil: no login needed
	anyRole    = []string{store.RoleAdmin, store.RoleOperator, store.RoleAuditor}
	writers    = []string{store.RoleAdmin, store.RoleOperator}
	adminOnly  = []string{store.RoleAdmin}
	adminAudit = []string{store.RoleAdmin, store.RoleAuditor}
)

// param documents a query parameter.
type param struct{ name, typ, doc string }

// route is one API operation. in and out are zero values of the request and response types (nil: none); for the
// OpenAPI document and the role × endpoint test. example is a request body that the test sends.
type route struct {
	method, path, summary string
	roles                 []string
	in, out               any
	status                int // success status; 0 = 200
	query                 []param
	auditRead             bool // a read worth auditing (an export)
	example               string
	h                     func(c *call) (int, any, error)
}

// audited: every change, every login and logout, and audited reads.
func (rt *route) audited() bool { return rt.method != http.MethodGet || rt.auditRead }

// LoginRequest, LoginResponse etc. are the API's own request and response types (the store's types are used directly
// for resources).
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginResponse carries the bearer token.
type LoginResponse struct {
	Token     string     `json:"token"`
	ExpiresAt string     `json:"expires_at"`
	User      store.User `json:"user"`
}

// PasswordChange changes one's own password.
type PasswordChange struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// UserCreate creates a user.
type UserCreate struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	Password string `json:"password"`
}

// RepositoryCreate creates a Kopia repository.
type RepositoryCreate struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`     // filesystem (s3: not yet)
	Location string `json:"location"` // an absolute path for filesystem
}

// Health is the unauthenticated liveness answer.
type Health struct {
	Status string `json:"status"`
}

// SystemInfo describes the installation.
type SystemInfo struct {
	Version         string                   `json:"version"`
	SchemaVersion   int                      `json:"schema_version"`
	Migrations      []store.AppliedMigration `json:"migrations"`
	EvidenceKeyID   string                   `json:"evidence_key_id"` // the evidence trust anchor (ADR-006)
	StartedAt       string                   `json:"started_at"`
	StateBackupAt   string                   `json:"state_backup_at"`
	StateBackupRepo string                   `json:"state_backup_repository"`
}

func list[T any](v []T, err error) (int, any, error) {
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, v, nil
}

func one[T any](v T, err error) (int, any, error) {
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, v, nil
}

func created[T any](c *call, what string, v T, id int64, err error) (int, any, error) {
	if err != nil {
		return 0, nil, err
	}
	c.target = fmt.Sprintf("%s/%d", c.r.URL.Path, id)
	c.detail = "created " + what
	return http.StatusCreated, v, nil
}

func (s *Server) table() []route {
	const (
		host   = `{"name":"kvm1","address":"kvm1.lab","ssh_user":"gaia"}`
		vm     = `{"host_id":1,"name":"web1","uuid":""}`
		policy = `{"name":"gold","repository_id":1,"backup_interval_s":900,"test_interval_s":86400,"keep_last":8,"keep_daily":7,"keep_weekly":4,"rpo_target_s":3600,"rto_target_s":1800,"max_proof_age_s":172800,"check_template":"linux-basic"}`
	)
	limit := []param{{"limit", "integer", "at most this many (default 100, at most 1000)"}}
	vmFilter := append([]param{{"vm_id", "integer", "only this VM's"}}, limit...)
	return []route{
		// --- session ---
		{method: "POST", path: "/api/v1/login", summary: "Log in; returns a bearer token", roles: public, in: LoginRequest{}, out: LoginResponse{},
			example: `{"username":"nobody","password":"not a valid password"}`, h: s.login},
		{method: "POST", path: "/api/v1/logout", summary: "End this session", roles: anyRole, status: http.StatusNoContent,
			h: func(c *call) (int, any, error) {
				c.detail = "logged out"
				return http.StatusNoContent, nil, s.db.DeleteSession(c.r.Context(), c.session.ID)
			}},
		{method: "GET", path: "/api/v1/me", summary: "The logged-in user", roles: anyRole, out: store.User{},
			h: func(c *call) (int, any, error) { return http.StatusOK, c.user, nil }},
		{method: "PUT", path: "/api/v1/me/password", summary: "Change one's own password (ends one's sessions)", roles: anyRole,
			in: PasswordChange{}, status: http.StatusNoContent, example: `{"current_password":"wrong","new_password":"short"}`, h: s.changePassword},
		{method: "GET", path: "/api/v1/health", summary: "Liveness: the server answers and its database is readable", roles: public, out: Health{},
			h: func(c *call) (int, any, error) {
				if err := s.db.SQL().PingContext(c.r.Context()); err != nil {
					return 0, nil, fmt.Errorf("database: %w", err)
				}
				return http.StatusOK, Health{Status: "OK"}, nil
			}},
		{method: "GET", path: "/api/v1/openapi.json", summary: "This API's OpenAPI 3.1 description", roles: public, out: map[string]any{},
			h: func(c *call) (int, any, error) {
				c.raw = true
				c.w.Header().Set("Content-Type", "application/json")
				c.w.WriteHeader(http.StatusOK)
				_, err := c.w.Write(s.openapi)
				return http.StatusOK, nil, err
			}},
		{method: "GET", path: "/api/v1/system", summary: "Version, schema and the evidence trust anchor", roles: anyRole, out: SystemInfo{}, h: s.systemInfo},

		// --- users ---
		{method: "GET", path: "/api/v1/users", summary: "List users", roles: adminAudit, out: []store.User{},
			h: func(c *call) (int, any, error) { return list(s.db.Users(c.r.Context())) }},
		{method: "POST", path: "/api/v1/users", summary: "Create a user", roles: adminOnly, in: UserCreate{}, out: store.User{}, status: http.StatusCreated,
			example: `{"username":"matrix-user","role":"auditor","password":"a long enough password"}`,
			h: func(c *call) (int, any, error) {
				var in UserCreate
				if err := c.decode(&in); err != nil {
					return 0, nil, err
				}
				u, err := s.db.CreateUser(c.r.Context(), c.user.Username, in.Username, in.Role, in.Password)
				return created(c, "user "+in.Username+" ("+in.Role+")", u, u.ID, err)
			}},
		{method: "GET", path: "/api/v1/users/{id}", summary: "One user", roles: adminAudit, out: store.User{},
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				return one(s.db.User(c.r.Context(), id))
			}},
		{method: "PATCH", path: "/api/v1/users/{id}", summary: "Change a user's role, password or disabled flag", roles: adminOnly,
			in: store.UserPatch{}, out: store.User{}, example: `{}`,
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				var p store.UserPatch
				if err := c.decode(&p); err != nil {
					return 0, nil, err
				}
				c.detail = describeUserPatch(p)
				return one(s.db.UpdateUser(c.r.Context(), c.user.Username, id, p))
			}},

		// --- hosts ---
		{method: "GET", path: "/api/v1/hosts", summary: "List hosts", roles: anyRole, out: []store.Host{},
			h: func(c *call) (int, any, error) { return list(s.db.Hosts(c.r.Context())) }},
		{method: "POST", path: "/api/v1/hosts", summary: "Add a host", roles: writers, in: store.HostInput{}, out: store.Host{}, status: http.StatusCreated, example: host,
			h: func(c *call) (int, any, error) {
				var in store.HostInput
				if err := c.decode(&in); err != nil {
					return 0, nil, err
				}
				h, err := s.db.CreateHost(c.r.Context(), in)
				return created(c, "host "+in.Name, h, h.ID, err)
			}},
		{method: "GET", path: "/api/v1/hosts/{id}", summary: "One host", roles: anyRole, out: store.Host{},
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				return one(s.db.Host(c.r.Context(), id))
			}},
		{method: "PATCH", path: "/api/v1/hosts/{id}", summary: "Change a host", roles: writers, in: store.HostPatch{}, out: store.Host{}, example: `{}`,
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				var p store.HostPatch
				if err := c.decode(&p); err != nil {
					return 0, nil, err
				}
				c.detail = "changed"
				return one(s.db.UpdateHost(c.r.Context(), id, p))
			}},
		{method: "DELETE", path: "/api/v1/hosts/{id}", summary: "Remove a host without VMs", roles: writers, status: http.StatusNoContent,
			h: func(c *call) (int, any, error) { return s.remove(c, "host", s.db.DeleteHost) }},

		// --- VMs and disks ---
		{method: "GET", path: "/api/v1/vms", summary: "List VMs", roles: anyRole, out: []store.VM{},
			query: []param{{"host_id", "integer", "only this host's"}},
			h: func(c *call) (int, any, error) {
				hid, err := c.intQuery("host_id", 0, 0)
				if err != nil {
					return 0, nil, err
				}
				return list(s.db.VMs(c.r.Context(), hid))
			}},
		{method: "POST", path: "/api/v1/vms", summary: "Register a VM", roles: writers, in: store.VMInput{}, out: store.VM{}, status: http.StatusCreated, example: vm,
			h: func(c *call) (int, any, error) {
				var in store.VMInput
				if err := c.decode(&in); err != nil {
					return 0, nil, err
				}
				v, err := s.db.CreateVM(c.r.Context(), c.user.Username, in)
				return created(c, "VM "+in.Name, v, v.ID, err)
			}},
		{method: "GET", path: "/api/v1/vms/{id}", summary: "One VM", roles: anyRole, out: store.VM{},
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				return one(s.db.VM(c.r.Context(), id))
			}},
		{method: "PATCH", path: "/api/v1/vms/{id}", summary: "Assign a policy (policy_id 0 removes it) or set the UUID", roles: writers,
			in: store.VMPatch{}, out: store.VM{}, example: `{}`,
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				var p store.VMPatch
				if err := c.decode(&p); err != nil {
					return 0, nil, err
				}
				if p.PolicyID != nil {
					c.detail = fmt.Sprintf("policy_id %d", *p.PolicyID)
				}
				return one(s.db.UpdateVM(c.r.Context(), id, p))
			}},
		{method: "DELETE", path: "/api/v1/vms/{id}", summary: "Remove a VM without points", roles: writers, status: http.StatusNoContent,
			h: func(c *call) (int, any, error) { return s.remove(c, "VM", s.db.DeleteVM) }},
		{method: "GET", path: "/api/v1/vms/{id}/disks", summary: "A VM's disks", roles: anyRole, out: []store.Disk{},
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				if _, err := s.db.VM(c.r.Context(), id); err != nil {
					return 0, nil, err
				}
				return list(s.db.Disks(c.r.Context(), id))
			}},
		{method: "POST", path: "/api/v1/vms/{id}/disks", summary: "Add a disk to a VM", roles: writers, in: store.DiskInput{}, out: store.Disk{},
			status: http.StatusCreated, example: `{"target":"vdz","size_bytes":1073741824}`,
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				var in store.DiskInput
				if err := c.decode(&in); err != nil {
					return 0, nil, err
				}
				if _, err := s.db.VM(c.r.Context(), id); err != nil {
					return 0, nil, err
				}
				k, err := s.db.CreateDisk(c.r.Context(), id, in)
				return created(c, "disk "+in.Target, k, k.ID, err)
			}},

		// --- repositories and policies ---
		{method: "GET", path: "/api/v1/repositories", summary: "List repositories", roles: anyRole, out: []store.Repository{},
			h: func(c *call) (int, any, error) { return list(s.db.Repositories(c.r.Context())) }},
		{method: "POST", path: "/api/v1/repositories", summary: "Create a Kopia repository (ADR-004 defaults)", roles: adminOnly,
			in: RepositoryCreate{}, out: store.Repository{}, status: http.StatusCreated, example: `{"name":"matrix","kind":"s3","location":"s3://x"}`,
			h: s.createRepository},
		{method: "GET", path: "/api/v1/repositories/{id}", summary: "One repository", roles: anyRole, out: store.Repository{},
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				return one(s.db.Repository(c.r.Context(), id))
			}},
		{method: "GET", path: "/api/v1/policies", summary: "List policies", roles: anyRole, out: []store.Policy{},
			h: func(c *call) (int, any, error) { return list(s.db.Policies(c.r.Context())) }},
		{method: "POST", path: "/api/v1/policies", summary: "Create a policy", roles: writers, in: store.PolicyInput{}, out: store.Policy{},
			status: http.StatusCreated, example: policy,
			h: func(c *call) (int, any, error) {
				var in store.PolicyInput
				if err := c.decode(&in); err != nil {
					return 0, nil, err
				}
				p, err := s.db.CreatePolicy(c.r.Context(), in)
				return created(c, "policy "+in.Name, p, p.ID, err)
			}},
		{method: "GET", path: "/api/v1/policies/{id}", summary: "One policy", roles: anyRole, out: store.Policy{},
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				return one(s.db.Policy(c.r.Context(), id))
			}},
		{method: "PATCH", path: "/api/v1/policies/{id}", summary: "Change a policy", roles: writers, in: store.PolicyPatch{}, out: store.Policy{}, example: `{}`,
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				var p store.PolicyPatch
				if err := c.decode(&p); err != nil {
					return 0, nil, err
				}
				c.detail = "changed"
				return one(s.db.UpdatePolicy(c.r.Context(), id, p))
			}},
		{method: "DELETE", path: "/api/v1/policies/{id}", summary: "Remove a policy no VM uses", roles: writers, status: http.StatusNoContent,
			h: func(c *call) (int, any, error) { return s.remove(c, "policy", s.db.DeletePolicy) }},

		// --- what workers record (T15, T16): read-only here ---
		{method: "GET", path: "/api/v1/jobs", summary: "List jobs, newest first", roles: anyRole, out: []store.Job{}, query: vmFilter,
			h: func(c *call) (int, any, error) {
				vmID, n, err := c.filter()
				if err != nil {
					return 0, nil, err
				}
				return list(s.db.Jobs(c.r.Context(), vmID, n))
			}},
		{method: "GET", path: "/api/v1/jobs/{id}", summary: "One job", roles: anyRole, out: store.Job{},
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				return one(s.db.Job(c.r.Context(), id))
			}},
		{method: "GET", path: "/api/v1/points", summary: "List backup points, newest first", roles: anyRole, out: []store.Point{}, query: vmFilter,
			h: func(c *call) (int, any, error) {
				vmID, n, err := c.filter()
				if err != nil {
					return 0, nil, err
				}
				return list(s.db.Points(c.r.Context(), vmID, n))
			}},
		{method: "GET", path: "/api/v1/points/{id}", summary: "One backup point", roles: anyRole, out: store.Point{},
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				return one(s.db.Point(c.r.Context(), id))
			}},
		{method: "GET", path: "/api/v1/restore-tests", summary: "List restore tests, newest first", roles: anyRole, out: []store.RestoreTest{}, query: vmFilter,
			h: func(c *call) (int, any, error) {
				vmID, n, err := c.filter()
				if err != nil {
					return 0, nil, err
				}
				return list(s.db.RestoreTests(c.r.Context(), vmID, n))
			}},
		{method: "GET", path: "/api/v1/restore-tests/{id}", summary: "One restore test", roles: anyRole, out: store.RestoreTest{},
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				return one(s.db.RestoreTest(c.r.Context(), id))
			}},
		{method: "GET", path: "/api/v1/evidence", summary: "List evidence bundles, newest first", roles: anyRole, out: []store.EvidenceBundle{}, query: vmFilter,
			h: func(c *call) (int, any, error) {
				vmID, n, err := c.filter()
				if err != nil {
					return 0, nil, err
				}
				return list(s.db.EvidenceBundles(c.r.Context(), vmID, n))
			}},
		{method: "GET", path: "/api/v1/evidence/{id}", summary: "One evidence bundle", roles: anyRole, out: store.EvidenceBundle{},
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				return one(s.db.Evidence(c.r.Context(), id))
			}},
		{method: "GET", path: "/api/v1/evidence/{id}/export", summary: "Export a bundle with the ledger and the trust anchor (tar.gz, ADR-006)",
			roles: adminAudit, auditRead: true, h: s.exportEvidence},
		{method: "GET", path: "/api/v1/state-changes", summary: "Recorded state changes, oldest first", roles: anyRole, out: []store.StateChange{},
			query: append([]param{{"entity", "string", "e.g. job, point, restore_test, vm, user"}, {"entity_id", "integer", "with entity"},
				{"after", "integer", "after this seq"}}, limit...),
			h: func(c *call) (int, any, error) {
				eid, err := c.intQuery("entity_id", 0, 0)
				if err != nil {
					return 0, nil, err
				}
				after, err := c.intQuery("after", 0, 0)
				if err != nil {
					return 0, nil, err
				}
				n, err := c.intQuery("limit", 100, 1000)
				if err != nil {
					return 0, nil, err
				}
				return list(s.db.StateChanges(c.r.Context(), c.r.URL.Query().Get("entity"), eid, after, int(n)))
			}},

		// --- audit and state backups ---
		{method: "GET", path: "/api/v1/audit", summary: "The audit log, oldest first", roles: adminAudit, out: []store.AuditEntry{},
			query: append([]param{{"after", "integer", "after this seq"}}, limit...),
			h: func(c *call) (int, any, error) {
				after, err := c.intQuery("after", 0, 0)
				if err != nil {
					return 0, nil, err
				}
				n, err := c.intQuery("limit", 100, 1000)
				if err != nil {
					return 0, nil, err
				}
				return list(s.db.Audit(c.r.Context(), after, int(n)))
			}},
		{method: "GET", path: "/api/v1/audit/verify", summary: "Recompute the audit log's hash chain", roles: adminAudit, out: store.AuditCheck{},
			h: func(c *call) (int, any, error) { return one(s.db.VerifyAudit(c.r.Context())) }},
		{method: "GET", path: "/api/v1/state-backups", summary: "Backups of Gaia's own state, newest first", roles: anyRole, out: []store.StateBackup{}, query: limit,
			h: func(c *call) (int, any, error) {
				n, err := c.intQuery("limit", 100, 1000)
				if err != nil {
					return 0, nil, err
				}
				return list(s.db.StateBackups(c.r.Context(), int(n)))
			}},
		{method: "POST", path: "/api/v1/state-backups", summary: "Back up Gaia's state into its repository now", roles: adminOnly, out: store.StateBackup{},
			status: http.StatusCreated,
			h: func(c *call) (int, any, error) {
				b, err := s.BackupState(c.r.Context(), c.user.Username, "requested by "+c.user.Username)
				if err != nil && b.ID == 0 {
					return 0, nil, err
				}
				c.detail = fmt.Sprintf("state backup %d: %s", b.ID, b.State)
				status := http.StatusCreated
				if b.State != "ok" {
					status = http.StatusInternalServerError
				}
				return status, b, nil
			}},
	}
}

// filter reads ?vm_id and ?limit.
func (c *call) filter() (int64, int, error) {
	vmID, err := c.intQuery("vm_id", 0, 0)
	if err != nil {
		return 0, 0, err
	}
	n, err := c.intQuery("limit", 100, 1000)
	return vmID, int(n), err
}

// remove deletes the record named by the path's {id}.
func (s *Server) remove(c *call, what string, del func(context.Context, int64) error) (int, any, error) {
	id, err := c.id("id")
	if err != nil {
		return 0, nil, err
	}
	if err := del(c.r.Context(), id); err != nil {
		return 0, nil, err
	}
	c.detail = "removed " + what
	return http.StatusNoContent, nil, nil
}

func describeUserPatch(p store.UserPatch) string {
	d := ""
	if p.Role != nil {
		d += " role=" + *p.Role
	}
	if p.Disabled != nil {
		d += fmt.Sprintf(" disabled=%v", *p.Disabled)
	}
	if p.Password != nil {
		d += " password changed"
	}
	if d == "" {
		return "no change"
	}
	return d[1:]
}
