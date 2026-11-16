package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/diskstore"
	"github.com/Warewave-Technology/gaia/internal/hostconn"
	"github.com/Warewave-Technology/gaia/internal/status"

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
	// The passphrase of the recovery kit: a new repository means a new kit, which must open it (ADR-007 B5).
	KitPassphrase string `json:"kit_passphrase"`
}

// Health is the unauthenticated liveness answer.
type Health struct {
	Status string `json:"status"`
}

// TrustRequest pins a host's SSH key: the fingerprint the operator confirmed (ssh-keygen -lf on the host).
type TrustRequest struct {
	Fingerprint string `json:"fingerprint"`
}

// HostCheck is what Gaia sees on a host over its SSH connection.
type HostCheck struct {
	Libvirt string   `json:"libvirt"`
	QEMU    string   `json:"qemu"`
	Domains []string `json:"domains"`
	Seconds float64  `json:"seconds"`
}

// VerifyRequest starts a repository verification.
type VerifyRequest struct {
	Mode string `json:"mode"` // sampled or full
}

// MaintenanceRequest runs a repository's maintenance now.
type MaintenanceRequest struct {
	Force bool `json:"force"` // full maintenance even if not due
}

// VMStatus is a VM's status with everything behind it (ADR-007 "The status model").
type VMStatus struct {
	VMID      int64  `json:"vm_id"`
	VM        string `json:"vm"`
	ChangedAt string `json:"changed_at"` // when the status last changed
	status.Result
}

// vmStatus computes a VM's status now, stores it (a change is recorded) and returns it.
func (s *Server) vmStatus(ctx context.Context, vm store.VM) (VMStatus, error) {
	r, err := s.refreshStatus(ctx, vm.ID)
	if err != nil {
		return VMStatus{}, err
	}
	st, err := s.db.VMStatuses(ctx, vm.ID)
	if err != nil {
		return VMStatus{}, err
	}
	out := VMStatus{VMID: vm.ID, VM: vm.Name, Result: r}
	if len(st) == 1 {
		out.ChangedAt = st[0].ChangedAt
	}
	return out, nil
}

// SystemInfo describes the installation.
type SystemInfo struct {
	Version          string                   `json:"version"`
	SchemaVersion    int                      `json:"schema_version"`
	Migrations       []store.AppliedMigration `json:"migrations"`
	EvidenceKeyID    string                   `json:"evidence_key_id"` // the evidence trust anchor (ADR-006)
	SSHPublicKey     string                   `json:"ssh_public_key"`  // Gaia's key, for a host's authorized_keys
	SSHAuthorizedKey string                   `json:"ssh_authorized_keys_line"`
	StartedAt        string                   `json:"started_at"`
	StateBackupAt    string                   `json:"state_backup_at"`
	StateBackupRepo  string                   `json:"state_backup_repository"`
	RecoveryKit      KitSummary               `json:"recovery_kit"`
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
		{method: "GET", path: "/api/v1/status", summary: "Every VM's status, computed now", roles: anyRole, out: []VMStatus{},
			h: func(c *call) (int, any, error) {
				vms, err := s.db.VMs(c.r.Context(), 0)
				if err != nil {
					return 0, nil, err
				}
				out := []VMStatus{}
				for _, vm := range vms {
					v, err := s.vmStatus(c.r.Context(), vm)
					if err != nil {
						return 0, nil, fmt.Errorf("VM %s: %w", vm.Name, err)
					}
					out = append(out, v)
				}
				return http.StatusOK, out, nil
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
		{method: "POST", path: "/api/v1/hosts/{id}/scan", summary: "Read the host's SSH key without trusting it", roles: writers, out: hostconn.Scanned{},
			h: func(c *call) (int, any, error) {
				h, err := s.hostOf(c)
				if err != nil {
					return 0, nil, err
				}
				sc, err := hostconn.Scan(c.r.Context(), h.Address, h.SSHPort)
				if err != nil {
					return 0, nil, fmt.Errorf("%w: %v", errHost, err)
				}
				c.detail = "scanned " + sc.Fingerprint
				return http.StatusOK, sc, nil
			}},
		{method: "POST", path: "/api/v1/hosts/{id}/trust", summary: "Pin the host's SSH key if it has the fingerprint given", roles: writers,
			in: TrustRequest{}, out: store.Host{}, example: `{"fingerprint":"SHA256:none"}`,
			h: func(c *call) (int, any, error) {
				h, err := s.hostOf(c)
				if err != nil {
					return 0, nil, err
				}
				var in TrustRequest
				if err := c.decode(&in); err != nil {
					return 0, nil, err
				}
				sc, err := hostconn.Scan(c.r.Context(), h.Address, h.SSHPort)
				if err != nil {
					return 0, nil, fmt.Errorf("%w: %v", errHost, err)
				}
				if sc.Fingerprint != strings.TrimSpace(in.Fingerprint) {
					return 0, nil, fmt.Errorf("%w: the host's key is %s, not %s: not trusted", store.ErrConflict, sc.Fingerprint, in.Fingerprint)
				}
				c.detail = "trusted " + sc.Fingerprint
				return one(s.db.PinHostKey(c.r.Context(), c.user.Username, h.ID, sc.Key, sc.Fingerprint))
			}},
		{method: "POST", path: "/api/v1/hosts/{id}/check", summary: "Connect over SSH and list the host's libvirt domains", roles: writers, out: HostCheck{},
			h: func(c *call) (int, any, error) {
				h, err := s.hostOf(c)
				if err != nil {
					return 0, nil, err
				}
				t := time.Now()
				hc, err := hostconn.Dial(c.r.Context(), hostTarget(h), s.sshKey)
				if err != nil {
					return 0, nil, fmt.Errorf("%w: %v", errHost, err)
				}
				defer hc.Close()
				lv, err := hc.Libvirt()
				if err != nil {
					return 0, nil, fmt.Errorf("%w: %v", errHost, err)
				}
				defer lv.Close()
				var out HostCheck
				if out.Libvirt, out.QEMU, err = lv.Versions(); err != nil {
					return 0, nil, fmt.Errorf("%w: %v", errHost, err)
				}
				if out.Domains, err = lv.Domains(); err != nil {
					return 0, nil, fmt.Errorf("%w: %v", errHost, err)
				}
				out.Seconds = time.Since(t).Seconds()
				c.detail = fmt.Sprintf("libvirt %s, %d domains", out.Libvirt, len(out.Domains))
				return http.StatusOK, out, nil
			}},

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
		{method: "POST", path: "/api/v1/vms/{id}/backups", summary: "Queue a backup of the VM now", roles: writers, out: store.Job{}, status: http.StatusAccepted,
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				vm, err := s.db.VM(c.r.Context(), id)
				if err != nil {
					return 0, nil, err
				}
				if vm.PolicyID == nil {
					return 0, nil, fmt.Errorf("%w: VM %s has no policy (it says which repository to use)", store.ErrConflict, vm.Name)
				}
				if j, open, err := s.db.OpenJob(c.r.Context(), id, "backup"); err != nil {
					return 0, nil, err
				} else if open {
					return 0, nil, fmt.Errorf("%w: VM %s already has backup job %d (%s)", store.ErrConflict, vm.Name, j.ID, j.State)
				}
				j, err := s.db.CreateJob(c.r.Context(), c.user.Username, "backup", &id, "requested by "+c.user.Username)
				if err != nil {
					return 0, nil, err
				}
				c.detail = fmt.Sprintf("queued backup job %d", j.ID)
				return http.StatusAccepted, j, nil
			}},
		{method: "GET", path: "/api/v1/vms/{id}/status", summary: "A VM's status, computed now, with its reasons", roles: anyRole, out: VMStatus{},
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				vm, err := s.db.VM(c.r.Context(), id)
				if err != nil {
					return 0, nil, err
				}
				return one(s.vmStatus(c.r.Context(), vm))
			}},
		{method: "POST", path: "/api/v1/vms/{id}/tests", summary: "Queue a restore test of the VM's newest checked point now", roles: writers,
			out: store.Job{}, status: http.StatusAccepted,
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				vm, err := s.db.VM(c.r.Context(), id)
				if err != nil {
					return 0, nil, err
				}
				if vm.PolicyID == nil {
					return 0, nil, fmt.Errorf("%w: VM %s has no policy (it says which checks to run)", store.ErrConflict, vm.Name)
				}
				if j, open, err := s.db.OpenJob(c.r.Context(), id, "restore_test"); err != nil {
					return 0, nil, err
				} else if open {
					return 0, nil, fmt.Errorf("%w: VM %s already has restore test job %d (%s)", store.ErrConflict, vm.Name, j.ID, j.State)
				}
				if _, err := s.db.LatestPoint(c.r.Context(), id); err != nil {
					return 0, nil, fmt.Errorf("%w: VM %s has no checked point to test", store.ErrConflict, vm.Name)
				}
				j, err := s.db.CreateJob(c.r.Context(), c.user.Username, "restore_test", &id, "requested by "+c.user.Username)
				if err != nil {
					return 0, nil, err
				}
				c.detail = fmt.Sprintf("queued restore test job %d", j.ID)
				return http.StatusAccepted, j, nil
			}},
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
		{method: "POST", path: "/api/v1/repositories/{id}/verify", summary: "Start a verification of the repository (sampled or full)", roles: writers,
			in: VerifyRequest{}, out: store.Verification{}, status: http.StatusAccepted, example: `{"mode":"sampled"}`,
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				var in VerifyRequest
				if err := c.decode(&in); err != nil {
					return 0, nil, err
				}
				r, err := s.db.Repository(c.r.Context(), id)
				if err != nil {
					return 0, nil, err
				}
				v, err := s.StartVerification(context.WithoutCancel(c.r.Context()), c.user.Username, r, in.Mode, "requested by "+c.user.Username)
				if err != nil {
					return 0, nil, err
				}
				c.detail = fmt.Sprintf("verification %d (%s)", v.ID, v.Mode)
				return http.StatusAccepted, v, nil
			}},
		{method: "POST", path: "/api/v1/repositories/{id}/maintenance", summary: "Run the repository's maintenance now (Gaia's GC, then Kopia's)", roles: adminOnly,
			in: MaintenanceRequest{}, out: diskstore.MaintenanceResult{}, example: `{"force":false}`,
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				var in MaintenanceRequest
				if err := c.decode(&in); err != nil {
					return 0, nil, err
				}
				r, err := s.db.Repository(c.r.Context(), id)
				if err != nil {
					return 0, nil, err
				}
				lock := s.repoLocks.get(r.ID)
				lock.Lock()
				defer lock.Unlock()
				st, err := s.OpenRepository(c.r.Context(), r)
				if err != nil {
					return 0, nil, err
				}
				defer st.Close(c.r.Context())
				m, err := st.Maintain(c.r.Context(), in.Force, s.maintenanceSafety)
				if err != nil {
					return 0, nil, err
				}
				c.detail = "maintenance " + m.Mode
				return http.StatusOK, m, nil
			}},
		{method: "GET", path: "/api/v1/verifications", summary: "Repository verifications, newest first", roles: anyRole, out: []store.Verification{},
			query: append([]param{{"repository_id", "integer", "only this repository's"}}, limit...),
			h: func(c *call) (int, any, error) {
				rid, err := c.intQuery("repository_id", 0, 0)
				if err != nil {
					return 0, nil, err
				}
				n, err := c.intQuery("limit", 100, 1000)
				if err != nil {
					return 0, nil, err
				}
				return list(s.db.Verifications(c.r.Context(), rid, int(n)))
			}},
		{method: "GET", path: "/api/v1/verifications/{id}", summary: "One verification, with the points it found damaged", roles: anyRole, out: store.Verification{},
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				return one(s.db.Verification(c.r.Context(), id))
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
		{method: "GET", path: "/api/v1/jobs/{id}", summary: "One job, with its checkpoint and what it did", roles: anyRole, out: store.JobInfo{},
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				return one(s.db.JobInfo(c.r.Context(), id))
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
		{method: "GET", path: "/api/v1/points/{id}/disks", summary: "A point's disks", roles: anyRole, out: []store.PointDisk{},
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				if _, err := s.db.Point(c.r.Context(), id); err != nil {
					return 0, nil, err
				}
				return list(s.db.PointDisks(c.r.Context(), id))
			}},
		{method: "POST", path: "/api/v1/points/{id}/restore-check", summary: "Restore the point's disks to scratch files, hash them, remove them", roles: writers,
			out: []RestoredDisk{},
			h: func(c *call) (int, any, error) {
				id, err := c.id("id")
				if err != nil {
					return 0, nil, err
				}
				p, err := s.db.Point(c.r.Context(), id)
				if err != nil {
					return 0, nil, err
				}
				if p.State != "present" {
					return 0, nil, fmt.Errorf("%w: point %d is %s", store.ErrConflict, id, p.State)
				}
				out, err := s.restoreCheck(c.r.Context(), p)
				if err != nil {
					return 0, nil, err
				}
				c.detail = fmt.Sprintf("restored and hashed %d disks", len(out))
				return http.StatusOK, out, nil
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

		// --- recovery kit (ADR-007 B5) ---
		{method: "GET", path: "/api/v1/recovery-kit", summary: "Whether the recovery kit can rebuild this server, and every kit written", roles: anyRole,
			out: KitInfo{}, h: s.kitInfo},
		{method: "GET", path: "/api/v1/recovery-kit/file", summary: "The newest recovery kit (age, encrypted with its passphrase)", roles: adminOnly,
			auditRead: true, h: s.kitFile},
		{method: "POST", path: "/api/v1/recovery-kit", summary: "Write a new recovery kit (also to change its passphrase)", roles: adminOnly,
			in: KitPassphrase{}, out: store.RecoveryKit{}, status: http.StatusCreated, example: `{"passphrase":"not the passphrase"}`, h: s.regenerateKit},
		{method: "POST", path: "/api/v1/recovery-kit/confirm", summary: "Record that a copy of the newest kit, opened with its passphrase, is stored off the server",
			roles: adminOnly, in: KitConfirm{}, out: store.RecoveryKit{}, example: `{"generation":99,"sha256":"00"}`, h: s.confirmKit},
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
// hostOf loads the host named by the path's {id}.
func (s *Server) hostOf(c *call) (store.Host, error) {
	id, err := c.id("id")
	if err != nil {
		return store.Host{}, err
	}
	return s.db.Host(c.r.Context(), id)
}

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
