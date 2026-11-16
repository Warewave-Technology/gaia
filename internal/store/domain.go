package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	nameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	vmNameRe = regexp.MustCompile(`^[^/\x00-\x1f]{1,255}$`)
	targetRe = regexp.MustCompile(`^[a-z][a-z0-9]{1,15}$`)
)

// ValidName reports whether n is a valid name for a host, repository or policy.
func ValidName(n string) bool { return nameRe.MatchString(n) }

func checkName(kind, n string) error {
	if !nameRe.MatchString(n) {
		return fmt.Errorf("%w: a %s name is 1-128 of A-Z a-z 0-9 . _ - and starts with a letter or digit", ErrInvalid, kind)
	}
	return nil
}

func notFound(kind string, id int64, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s %d", ErrNotFound, kind, id)
	}
	return err
}

// querier is a *sql.DB or, inside a transaction, its *sql.Tx (with one connection, querying the DB from inside a
// transaction would wait for itself).
type querier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

// rowsOf runs a query and scans every row.
func rowsOf[T any](ctx context.Context, db querier, scan func(interface{ Scan(...any) error }) (T, error), q string, args ...any) ([]T, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// --- hosts ---

// Host is a protected KVM/libvirt host, reached over SSH (ADR-007 §3).
type Host struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Address    string `json:"address"`
	SSHPort    int    `json:"ssh_port"`
	SSHUser    string `json:"ssh_user"`
	SSHHostKey string `json:"ssh_host_key"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// HostInput creates a host.
type HostInput struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	SSHPort int    `json:"ssh_port"`
	SSHUser string `json:"ssh_user"`
}

// HostPatch changes a host; nil fields stay.
type HostPatch struct {
	Address *string `json:"address,omitempty"`
	SSHPort *int    `json:"ssh_port,omitempty"`
	SSHUser *string `json:"ssh_user,omitempty"`
}

const hostCols = `id, name, address, ssh_port, ssh_user, ssh_host_key, created_at, updated_at`

func scanHost(s interface{ Scan(...any) error }) (Host, error) {
	var h Host
	err := s.Scan(&h.ID, &h.Name, &h.Address, &h.SSHPort, &h.SSHUser, &h.SSHHostKey, &h.CreatedAt, &h.UpdatedAt)
	return h, err
}

func checkHost(address string, port int, user string) error {
	switch {
	case address == "" || strings.ContainsAny(address, " /\\@") || len(address) > 253:
		return fmt.Errorf("%w: address %q", ErrInvalid, address)
	case port < 1 || port > 65535:
		return fmt.Errorf("%w: ssh_port %d", ErrInvalid, port)
	case !usernameRe.MatchString(user) && user != "root":
		return fmt.Errorf("%w: ssh_user %q", ErrInvalid, user)
	}
	return nil
}

// CreateHost adds a host.
func (d *DB) CreateHost(ctx context.Context, in HostInput) (Host, error) {
	if in.SSHPort == 0 {
		in.SSHPort = 22
	}
	if err := checkName("host", in.Name); err != nil {
		return Host{}, err
	}
	if err := checkHost(in.Address, in.SSHPort, in.SSHUser); err != nil {
		return Host{}, err
	}
	now := d.ts()
	r, err := d.db.ExecContext(ctx, `INSERT INTO hosts (name, address, ssh_port, ssh_user, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		in.Name, in.Address, in.SSHPort, in.SSHUser, now, now)
	if err != nil {
		return Host{}, classify(err)
	}
	id, err := r.LastInsertId()
	if err != nil {
		return Host{}, err
	}
	return d.Host(ctx, id)
}

// Host returns a host.
func (d *DB) Host(ctx context.Context, id int64) (Host, error) {
	h, err := scanHost(d.db.QueryRowContext(ctx, `SELECT `+hostCols+` FROM hosts WHERE id = ?`, id))
	return h, notFound("host", id, err)
}

// Hosts lists hosts.
func (d *DB) Hosts(ctx context.Context) ([]Host, error) {
	return rowsOf(ctx, d.db, scanHost, `SELECT `+hostCols+` FROM hosts ORDER BY id`)
}

// UpdateHost applies p.
func (d *DB) UpdateHost(ctx context.Context, id int64, p HostPatch) (Host, error) {
	h, err := d.Host(ctx, id)
	if err != nil {
		return h, err
	}
	if p.Address != nil {
		h.Address = *p.Address
	}
	if p.SSHPort != nil {
		h.SSHPort = *p.SSHPort
	}
	if p.SSHUser != nil {
		h.SSHUser = *p.SSHUser
	}
	if err := checkHost(h.Address, h.SSHPort, h.SSHUser); err != nil {
		return h, err
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE hosts SET address = ?, ssh_port = ?, ssh_user = ?, updated_at = ? WHERE id = ?`,
		h.Address, h.SSHPort, h.SSHUser, d.ts(), id); err != nil {
		return h, classify(err)
	}
	return d.Host(ctx, id)
}

// DeleteHost removes a host that has no VMs.
func (d *DB) DeleteHost(ctx context.Context, id int64) error {
	return d.deleteRow(ctx, "hosts", "host", id)
}

func (d *DB) deleteRow(ctx context.Context, table, kind string, id int64) error {
	r, err := d.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE id = ?`, id)
	if err != nil {
		return classify(err)
	}
	if n, err := r.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("%w: %s %d", ErrNotFound, kind, id)
	}
	return nil
}

// --- repositories ---

// Repository is a Kopia repository (ADR-007 §4).
type Repository struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Location  string `json:"location"`
	Splitter  string `json:"splitter"`
	Hash      string `json:"hash"`
	CreatedAt string `json:"created_at"`
}

const repoCols = `id, name, kind, location, splitter, hash, created_at`

func scanRepo(s interface{ Scan(...any) error }) (Repository, error) {
	var r Repository
	err := s.Scan(&r.ID, &r.Name, &r.Kind, &r.Location, &r.Splitter, &r.Hash, &r.CreatedAt)
	return r, err
}

// CreateRepository records a repository that was created.
func (d *DB) CreateRepository(ctx context.Context, r Repository) (Repository, error) {
	if err := checkName("repository", r.Name); err != nil {
		return r, err
	}
	res, err := d.db.ExecContext(ctx, `INSERT INTO repositories (name, kind, location, splitter, hash, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		r.Name, r.Kind, r.Location, r.Splitter, r.Hash, d.ts())
	if err != nil {
		return r, classify(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return r, err
	}
	return d.Repository(ctx, id)
}

// Repository returns a repository.
func (d *DB) Repository(ctx context.Context, id int64) (Repository, error) {
	r, err := scanRepo(d.db.QueryRowContext(ctx, `SELECT `+repoCols+` FROM repositories WHERE id = ?`, id))
	return r, notFound("repository", id, err)
}

// RepositoryByName returns a repository by name.
func (d *DB) RepositoryByName(ctx context.Context, name string) (Repository, error) {
	r, err := scanRepo(d.db.QueryRowContext(ctx, `SELECT `+repoCols+` FROM repositories WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("%w: repository %q", ErrNotFound, name)
	}
	return r, err
}

// Repositories lists repositories.
func (d *DB) Repositories(ctx context.Context) ([]Repository, error) {
	return rowsOf(ctx, d.db, scanRepo, `SELECT `+repoCols+` FROM repositories ORDER BY id`)
}

// --- policies ---

// Policy says how often a VM is backed up and tested, what is kept, and what counts as healthy (ADR-007).
type Policy struct {
	ID              int64           `json:"id"`
	Name            string          `json:"name"`
	RepositoryID    int64           `json:"repository_id"`
	BackupIntervalS int64           `json:"backup_interval_s"`
	TestIntervalS   int64           `json:"test_interval_s"`
	KeepLast        int             `json:"keep_last"`
	KeepDaily       int             `json:"keep_daily"`
	KeepWeekly      int             `json:"keep_weekly"`
	RPOTargetS      int64           `json:"rpo_target_s"`
	RTOTargetS      int64           `json:"rto_target_s"`
	MaxProofAgeS    int64           `json:"max_proof_age_s"`
	CheckTemplate   string          `json:"check_template"`
	Checks          json.RawMessage `json:"checks"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
}

// PolicyInput creates a policy; PolicyPatch fields are the same, all optional.
type PolicyInput struct {
	Name            string          `json:"name"`
	RepositoryID    int64           `json:"repository_id"`
	BackupIntervalS int64           `json:"backup_interval_s"`
	TestIntervalS   int64           `json:"test_interval_s"`
	KeepLast        int             `json:"keep_last"`
	KeepDaily       int             `json:"keep_daily"`
	KeepWeekly      int             `json:"keep_weekly"`
	RPOTargetS      int64           `json:"rpo_target_s"`
	RTOTargetS      int64           `json:"rto_target_s"`
	MaxProofAgeS    int64           `json:"max_proof_age_s"`
	CheckTemplate   string          `json:"check_template"`
	Checks          json.RawMessage `json:"checks,omitempty"`
}

// PolicyPatch changes a policy; nil fields stay.
type PolicyPatch struct {
	RepositoryID    *int64          `json:"repository_id,omitempty"`
	BackupIntervalS *int64          `json:"backup_interval_s,omitempty"`
	TestIntervalS   *int64          `json:"test_interval_s,omitempty"`
	KeepLast        *int            `json:"keep_last,omitempty"`
	KeepDaily       *int            `json:"keep_daily,omitempty"`
	KeepWeekly      *int            `json:"keep_weekly,omitempty"`
	RPOTargetS      *int64          `json:"rpo_target_s,omitempty"`
	RTOTargetS      *int64          `json:"rto_target_s,omitempty"`
	MaxProofAgeS    *int64          `json:"max_proof_age_s,omitempty"`
	CheckTemplate   *string         `json:"check_template,omitempty"`
	Checks          json.RawMessage `json:"checks,omitempty"`
}

const policyCols = `id, name, repository_id, backup_interval_s, test_interval_s, keep_last, keep_daily, keep_weekly,
	rpo_target_s, rto_target_s, max_proof_age_s, check_template, checks, created_at, updated_at`

func scanPolicy(s interface{ Scan(...any) error }) (Policy, error) {
	var p Policy
	var checks string
	err := s.Scan(&p.ID, &p.Name, &p.RepositoryID, &p.BackupIntervalS, &p.TestIntervalS, &p.KeepLast, &p.KeepDaily, &p.KeepWeekly,
		&p.RPOTargetS, &p.RTOTargetS, &p.MaxProofAgeS, &p.CheckTemplate, &checks, &p.CreatedAt, &p.UpdatedAt)
	p.Checks = json.RawMessage(checks)
	return p, err
}

// CheckTemplates are the verification templates a policy may name (internal/verify).
var CheckTemplates = []string{"linux-basic", "linux-boot", "windows-basic"}

func checkPolicy(p Policy) error {
	if p.CheckTemplate == "" || !contains(CheckTemplates, p.CheckTemplate) {
		return fmt.Errorf("%w: check_template %q (one of %s)", ErrInvalid, p.CheckTemplate, strings.Join(CheckTemplates, ", "))
	}
	var checks []map[string]any
	if err := json.Unmarshal(p.Checks, &checks); err != nil {
		return fmt.Errorf("%w: checks: want a JSON array of check objects: %v", ErrInvalid, err)
	}
	if p.MaxProofAgeS < p.TestIntervalS {
		return fmt.Errorf("%w: max_proof_age_s (%d) is shorter than test_interval_s (%d): the proof would always be too old",
			ErrInvalid, p.MaxProofAgeS, p.TestIntervalS)
	}
	return nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// CreatePolicy adds a policy.
func (d *DB) CreatePolicy(ctx context.Context, in PolicyInput) (Policy, error) {
	if err := checkName("policy", in.Name); err != nil {
		return Policy{}, err
	}
	if len(in.Checks) == 0 {
		in.Checks = json.RawMessage("[]")
	}
	p := Policy{Name: in.Name, RepositoryID: in.RepositoryID, BackupIntervalS: in.BackupIntervalS, TestIntervalS: in.TestIntervalS,
		KeepLast: in.KeepLast, KeepDaily: in.KeepDaily, KeepWeekly: in.KeepWeekly, RPOTargetS: in.RPOTargetS, RTOTargetS: in.RTOTargetS,
		MaxProofAgeS: in.MaxProofAgeS, CheckTemplate: in.CheckTemplate, Checks: in.Checks}
	if err := checkPolicy(p); err != nil {
		return p, err
	}
	now := d.ts()
	r, err := d.db.ExecContext(ctx, `INSERT INTO policies (name, repository_id, backup_interval_s, test_interval_s, keep_last, keep_daily,
		keep_weekly, rpo_target_s, rto_target_s, max_proof_age_s, check_template, checks, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, p.Name, p.RepositoryID, p.BackupIntervalS, p.TestIntervalS, p.KeepLast,
		p.KeepDaily, p.KeepWeekly, p.RPOTargetS, p.RTOTargetS, p.MaxProofAgeS, p.CheckTemplate, string(p.Checks), now, now)
	if err != nil {
		return p, classify(err)
	}
	id, err := r.LastInsertId()
	if err != nil {
		return p, err
	}
	return d.Policy(ctx, id)
}

// Policy returns a policy.
func (d *DB) Policy(ctx context.Context, id int64) (Policy, error) {
	p, err := scanPolicy(d.db.QueryRowContext(ctx, `SELECT `+policyCols+` FROM policies WHERE id = ?`, id))
	return p, notFound("policy", id, err)
}

// Policies lists policies.
func (d *DB) Policies(ctx context.Context) ([]Policy, error) {
	return rowsOf(ctx, d.db, scanPolicy, `SELECT `+policyCols+` FROM policies ORDER BY id`)
}

// UpdatePolicy applies x.
func (d *DB) UpdatePolicy(ctx context.Context, id int64, x PolicyPatch) (Policy, error) {
	p, err := d.Policy(ctx, id)
	if err != nil {
		return p, err
	}
	set64 := func(dst *int64, v *int64) {
		if v != nil {
			*dst = *v
		}
	}
	setInt := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
		}
	}
	set64(&p.RepositoryID, x.RepositoryID)
	set64(&p.BackupIntervalS, x.BackupIntervalS)
	set64(&p.TestIntervalS, x.TestIntervalS)
	setInt(&p.KeepLast, x.KeepLast)
	setInt(&p.KeepDaily, x.KeepDaily)
	setInt(&p.KeepWeekly, x.KeepWeekly)
	set64(&p.RPOTargetS, x.RPOTargetS)
	set64(&p.RTOTargetS, x.RTOTargetS)
	set64(&p.MaxProofAgeS, x.MaxProofAgeS)
	if x.CheckTemplate != nil {
		p.CheckTemplate = *x.CheckTemplate
	}
	if len(x.Checks) > 0 {
		p.Checks = x.Checks
	}
	if err := checkPolicy(p); err != nil {
		return p, err
	}
	_, err = d.db.ExecContext(ctx, `UPDATE policies SET repository_id = ?, backup_interval_s = ?, test_interval_s = ?, keep_last = ?,
		keep_daily = ?, keep_weekly = ?, rpo_target_s = ?, rto_target_s = ?, max_proof_age_s = ?, check_template = ?, checks = ?,
		updated_at = ? WHERE id = ?`, p.RepositoryID, p.BackupIntervalS, p.TestIntervalS, p.KeepLast, p.KeepDaily, p.KeepWeekly,
		p.RPOTargetS, p.RTOTargetS, p.MaxProofAgeS, p.CheckTemplate, string(p.Checks), d.ts(), id)
	if err != nil {
		return p, classify(err)
	}
	return d.Policy(ctx, id)
}

// DeletePolicy removes a policy no VM uses.
func (d *DB) DeletePolicy(ctx context.Context, id int64) error {
	return d.deleteRow(ctx, "policies", "policy", id)
}

// --- VMs and disks ---

// VM is a protected virtual machine.
type VM struct {
	ID        int64  `json:"id"`
	HostID    int64  `json:"host_id"`
	Name      string `json:"name"`
	UUID      string `json:"uuid"`
	PolicyID  *int64 `json:"policy_id"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// VMInput registers a VM.
type VMInput struct {
	HostID   int64  `json:"host_id"`
	Name     string `json:"name"`
	UUID     string `json:"uuid"`
	PolicyID *int64 `json:"policy_id"`
}

// VMPatch changes a VM's policy (policy_id 0 removes it) or UUID; nil fields stay.
type VMPatch struct {
	PolicyID *int64  `json:"policy_id,omitempty"`
	UUID     *string `json:"uuid,omitempty"`
}

const vmCols = `id, host_id, name, uuid, policy_id, created_at, updated_at`

func scanVM(s interface{ Scan(...any) error }) (VM, error) {
	var v VM
	var pid sql.NullInt64
	err := s.Scan(&v.ID, &v.HostID, &v.Name, &v.UUID, &pid, &v.CreatedAt, &v.UpdatedAt)
	if pid.Valid {
		v.PolicyID = &pid.Int64
	}
	return v, err
}

// CreateVM registers a VM on a host.
func (d *DB) CreateVM(ctx context.Context, actor string, in VMInput) (VM, error) {
	if !vmNameRe.MatchString(in.Name) {
		return VM{}, fmt.Errorf("%w: VM name %q", ErrInvalid, in.Name)
	}
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		now := d.ts()
		r, err := tx.ExecContext(ctx, `INSERT INTO vms (host_id, name, uuid, policy_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			in.HostID, in.Name, in.UUID, in.PolicyID, now, now)
		if err != nil {
			return err
		}
		if id, err = r.LastInsertId(); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "vm", id, "", "registered", "registered", actor)
	})
	if err != nil {
		return VM{}, err
	}
	return d.VM(ctx, id)
}

// VM returns a VM.
func (d *DB) VM(ctx context.Context, id int64) (VM, error) {
	v, err := scanVM(d.db.QueryRowContext(ctx, `SELECT `+vmCols+` FROM vms WHERE id = ?`, id))
	return v, notFound("vm", id, err)
}

// VMs lists VMs, of one host if hostID > 0.
func (d *DB) VMs(ctx context.Context, hostID int64) ([]VM, error) {
	if hostID > 0 {
		return rowsOf(ctx, d.db, scanVM, `SELECT `+vmCols+` FROM vms WHERE host_id = ? ORDER BY id`, hostID)
	}
	return rowsOf(ctx, d.db, scanVM, `SELECT `+vmCols+` FROM vms ORDER BY id`)
}

// UpdateVM applies p.
func (d *DB) UpdateVM(ctx context.Context, id int64, p VMPatch) (VM, error) {
	v, err := d.VM(ctx, id)
	if err != nil {
		return v, err
	}
	if p.PolicyID != nil {
		v.PolicyID = p.PolicyID
		if *p.PolicyID == 0 {
			v.PolicyID = nil
		}
	}
	if p.UUID != nil {
		v.UUID = *p.UUID
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE vms SET policy_id = ?, uuid = ?, updated_at = ? WHERE id = ?`, v.PolicyID, v.UUID, d.ts(), id); err != nil {
		return v, classify(err)
	}
	return d.VM(ctx, id)
}

// DeleteVM removes a VM that has no points (its disks go with it).
func (d *DB) DeleteVM(ctx context.Context, id int64) error { return d.deleteRow(ctx, "vms", "vm", id) }

// Disk is one disk of a VM.
type Disk struct {
	ID        int64  `json:"id"`
	VMID      int64  `json:"vm_id"`
	Target    string `json:"target"`
	SizeBytes int64  `json:"size_bytes"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// DiskInput adds a disk to a VM.
type DiskInput struct {
	Target    string `json:"target"`
	SizeBytes int64  `json:"size_bytes"`
}

func scanDisk(s interface{ Scan(...any) error }) (Disk, error) {
	var k Disk
	err := s.Scan(&k.ID, &k.VMID, &k.Target, &k.SizeBytes, &k.CreatedAt, &k.UpdatedAt)
	return k, err
}

// CreateDisk adds a disk.
func (d *DB) CreateDisk(ctx context.Context, vmID int64, in DiskInput) (Disk, error) {
	if !targetRe.MatchString(in.Target) {
		return Disk{}, fmt.Errorf("%w: disk target %q (e.g. vda, sdb)", ErrInvalid, in.Target)
	}
	now := d.ts()
	r, err := d.db.ExecContext(ctx, `INSERT INTO disks (vm_id, target, size_bytes, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		vmID, in.Target, in.SizeBytes, now, now)
	if err != nil {
		return Disk{}, classify(err)
	}
	id, err := r.LastInsertId()
	if err != nil {
		return Disk{}, err
	}
	k, err := scanDisk(d.db.QueryRowContext(ctx, `SELECT id, vm_id, target, size_bytes, created_at, updated_at FROM disks WHERE id = ?`, id))
	return k, err
}

// Disks lists a VM's disks.
func (d *DB) Disks(ctx context.Context, vmID int64) ([]Disk, error) {
	return rowsOf(ctx, d.db, scanDisk, `SELECT id, vm_id, target, size_bytes, created_at, updated_at FROM disks WHERE vm_id = ? ORDER BY target`, vmID)
}

// --- jobs ---

// Job is work Gaia does (T15, T16 run them).
type Job struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	VMID       *int64 `json:"vm_id"`
	State      string `json:"state"`
	Reason     string `json:"reason"`
	Error      string `json:"error"`
	CreatedAt  string `json:"created_at"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
}

const jobCols = `id, kind, vm_id, state, reason, error, created_at, started_at, finished_at`

func scanJob(s interface{ Scan(...any) error }) (Job, error) {
	var j Job
	var vm sql.NullInt64
	err := s.Scan(&j.ID, &j.Kind, &vm, &j.State, &j.Reason, &j.Error, &j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	if vm.Valid {
		j.VMID = &vm.Int64
	}
	return j, err
}

// jobTransitions are the allowed state changes of a job.
var jobTransitions = map[string][]string{
	"queued":  {"running", "cancelled"},
	"running": {"ok", "failed", "cancelled"},
}

// CreateJob queues a job.
func (d *DB) CreateJob(ctx context.Context, actor, kind string, vmID *int64, reason string) (Job, error) {
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `INSERT INTO jobs (kind, vm_id, state, reason, created_at) VALUES (?, ?, 'queued', ?, ?)`, kind, vmID, reason, d.ts())
		if err != nil {
			return err
		}
		if id, err = r.LastInsertId(); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "job", id, "", "queued", reason, actor)
	})
	if err != nil {
		return Job{}, err
	}
	return d.Job(ctx, id)
}

// SetJobState moves a job to state to, recording why.
func (d *DB) SetJobState(ctx context.Context, actor string, id int64, to, reason, errText string) (Job, error) {
	err := d.tx(ctx, func(tx *sql.Tx) error {
		j, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, id))
		if err != nil {
			return notFound("job", id, err)
		}
		if !contains(jobTransitions[j.State], to) {
			return fmt.Errorf("%w: job %d is %s and cannot become %s", ErrConflict, id, j.State, to)
		}
		now := d.ts()
		switch to {
		case "running":
			_, err = tx.ExecContext(ctx, `UPDATE jobs SET state = ?, started_at = ? WHERE id = ?`, to, now, id)
		default:
			_, err = tx.ExecContext(ctx, `UPDATE jobs SET state = ?, finished_at = ?, error = ? WHERE id = ?`, to, now, errText, id)
		}
		if err != nil {
			return err
		}
		return d.recordState(ctx, tx, "job", id, j.State, to, reason, actor)
	})
	if err != nil {
		return Job{}, err
	}
	return d.Job(ctx, id)
}

// Job returns a job.
func (d *DB) Job(ctx context.Context, id int64) (Job, error) {
	j, err := scanJob(d.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, id))
	return j, notFound("job", id, err)
}

// Jobs lists jobs newest first, of one VM if vmID > 0.
func (d *DB) Jobs(ctx context.Context, vmID int64, limit int) ([]Job, error) {
	if vmID > 0 {
		return rowsOf(ctx, d.db, scanJob, `SELECT `+jobCols+` FROM jobs WHERE vm_id = ? ORDER BY id DESC LIMIT ?`, vmID, limit)
	}
	return rowsOf(ctx, d.db, scanJob, `SELECT `+jobCols+` FROM jobs ORDER BY id DESC LIMIT ?`, limit)
}

// --- points, restore tests, evidence ---

// Point is a backup point of a VM: one checkpoint on the host, one diskstore point per disk (point_disks).
type Point struct {
	ID           int64  `json:"id"`
	VMID         int64  `json:"vm_id"`
	JobID        int64  `json:"job_id"`
	RepositoryID int64  `json:"repository_id"`
	Kind         string `json:"kind"`
	TakenAt      string `json:"taken_at"`
	CheckState   string `json:"check_state"` // unchecked, ok, failed (post-backup check, or a repository verification)
	Bytes        int64  `json:"bytes"`
	State        string `json:"state"` // present, pruned
	Checkpoint   string `json:"checkpoint"`
	ParentID     *int64 `json:"parent_id"`
	Reason       string `json:"reason"`
}

const pointCols = `id, vm_id, job_id, repository_id, kind, taken_at, check_state, bytes, state, checkpoint, parent_id, reason`

func scanPoint(s interface{ Scan(...any) error }) (Point, error) {
	var p Point
	var parent sql.NullInt64
	err := s.Scan(&p.ID, &p.VMID, &p.JobID, &p.RepositoryID, &p.Kind, &p.TakenAt, &p.CheckState, &p.Bytes, &p.State, &p.Checkpoint, &parent, &p.Reason)
	if parent.Valid {
		p.ParentID = &parent.Int64
	}
	return p, err
}

// CreatePoint records a point; its post-backup check starts unchecked.
func (d *DB) CreatePoint(ctx context.Context, actor string, p Point) (Point, error) {
	err := d.tx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `INSERT INTO points (vm_id, job_id, repository_id, kind, taken_at, check_state, bytes)
			VALUES (?, ?, ?, ?, ?, 'unchecked', ?)`, p.VMID, p.JobID, p.RepositoryID, p.Kind, p.TakenAt, p.Bytes)
		if err != nil {
			return err
		}
		if p.ID, err = r.LastInsertId(); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "point", p.ID, "", "unchecked", fmt.Sprintf("%s point of job %d", p.Kind, p.JobID), actor)
	})
	if err != nil {
		return Point{}, err
	}
	return d.Point(ctx, p.ID)
}

// Point returns a point.
func (d *DB) Point(ctx context.Context, id int64) (Point, error) {
	p, err := scanPoint(d.db.QueryRowContext(ctx, `SELECT `+pointCols+` FROM points WHERE id = ?`, id))
	return p, notFound("point", id, err)
}

// Points lists points newest first, of one VM if vmID > 0.
func (d *DB) Points(ctx context.Context, vmID int64, limit int) ([]Point, error) {
	if vmID > 0 {
		return rowsOf(ctx, d.db, scanPoint, `SELECT `+pointCols+` FROM points WHERE vm_id = ? ORDER BY id DESC LIMIT ?`, vmID, limit)
	}
	return rowsOf(ctx, d.db, scanPoint, `SELECT `+pointCols+` FROM points ORDER BY id DESC LIMIT ?`, limit)
}

// RestoreTest is one restore test of a point.
type RestoreTest struct {
	ID         int64    `json:"id"`
	VMID       int64    `json:"vm_id"`
	PointID    int64    `json:"point_id"`
	JobID      int64    `json:"job_id"`
	Verdict    string   `json:"verdict"`
	ProvenRTOS *float64 `json:"proven_rto_s"`
	StartedAt  string   `json:"started_at"`
	FinishedAt string   `json:"finished_at"`
}

const testCols = `id, vm_id, point_id, job_id, verdict, proven_rto_s, started_at, finished_at`

func scanTest(s interface{ Scan(...any) error }) (RestoreTest, error) {
	var t RestoreTest
	var rto sql.NullFloat64
	err := s.Scan(&t.ID, &t.VMID, &t.PointID, &t.JobID, &t.Verdict, &rto, &t.StartedAt, &t.FinishedAt)
	if rto.Valid {
		t.ProvenRTOS = &rto.Float64
	}
	return t, err
}

// CreateRestoreTest records a test that started; its verdict is UNKNOWN until it finishes.
func (d *DB) CreateRestoreTest(ctx context.Context, actor string, t RestoreTest) (RestoreTest, error) {
	err := d.tx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `INSERT INTO restore_tests (vm_id, point_id, job_id, verdict, started_at) VALUES (?, ?, ?, 'UNKNOWN', ?)`,
			t.VMID, t.PointID, t.JobID, d.ts())
		if err != nil {
			return err
		}
		if t.ID, err = r.LastInsertId(); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "restore_test", t.ID, "", "UNKNOWN", fmt.Sprintf("test of point %d started", t.PointID), actor)
	})
	if err != nil {
		return RestoreTest{}, err
	}
	return d.RestoreTest(ctx, t.ID)
}

// RestoreTest returns a test.
func (d *DB) RestoreTest(ctx context.Context, id int64) (RestoreTest, error) {
	t, err := scanTest(d.db.QueryRowContext(ctx, `SELECT `+testCols+` FROM restore_tests WHERE id = ?`, id))
	return t, notFound("restore test", id, err)
}

// RestoreTests lists tests newest first, of one VM if vmID > 0.
func (d *DB) RestoreTests(ctx context.Context, vmID int64, limit int) ([]RestoreTest, error) {
	if vmID > 0 {
		return rowsOf(ctx, d.db, scanTest, `SELECT `+testCols+` FROM restore_tests WHERE vm_id = ? ORDER BY id DESC LIMIT ?`, vmID, limit)
	}
	return rowsOf(ctx, d.db, scanTest, `SELECT `+testCols+` FROM restore_tests ORDER BY id DESC LIMIT ?`, limit)
}

// EvidenceBundle is a sealed evidence bundle in the installation's ledger (ADR-006).
type EvidenceBundle struct {
	ID           int64  `json:"id"`
	BundleID     string `json:"bundle_id"`
	TestID       *int64 `json:"test_id"`
	VMID         *int64 `json:"vm_id"`
	LedgerSeq    int    `json:"ledger_seq"`
	BundleSHA256 string `json:"bundle_sha256"`
	Verdict      string `json:"verdict"`
	CreatedAt    string `json:"created_at"`
}

const evCols = `id, bundle_id, test_id, vm_id, ledger_seq, bundle_sha256, verdict, created_at`

func scanEvidence(s interface{ Scan(...any) error }) (EvidenceBundle, error) {
	var e EvidenceBundle
	var tid, vid sql.NullInt64
	err := s.Scan(&e.ID, &e.BundleID, &tid, &vid, &e.LedgerSeq, &e.BundleSHA256, &e.Verdict, &e.CreatedAt)
	if tid.Valid {
		e.TestID = &tid.Int64
	}
	if vid.Valid {
		e.VMID = &vid.Int64
	}
	return e, err
}

// AddEvidence records a bundle that was sealed and appended to the ledger.
func (d *DB) AddEvidence(ctx context.Context, e EvidenceBundle) (EvidenceBundle, error) {
	r, err := d.db.ExecContext(ctx, `INSERT INTO evidence_bundles (bundle_id, test_id, vm_id, ledger_seq, bundle_sha256, verdict, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, e.BundleID, e.TestID, e.VMID, e.LedgerSeq, e.BundleSHA256, e.Verdict, d.ts())
	if err != nil {
		return e, classify(err)
	}
	id, err := r.LastInsertId()
	if err != nil {
		return e, err
	}
	return d.Evidence(ctx, id)
}

// Evidence returns a bundle record.
func (d *DB) Evidence(ctx context.Context, id int64) (EvidenceBundle, error) {
	e, err := scanEvidence(d.db.QueryRowContext(ctx, `SELECT `+evCols+` FROM evidence_bundles WHERE id = ?`, id))
	return e, notFound("evidence bundle", id, err)
}

// EvidenceBundles lists bundle records newest first, of one VM if vmID > 0.
func (d *DB) EvidenceBundles(ctx context.Context, vmID int64, limit int) ([]EvidenceBundle, error) {
	if vmID > 0 {
		return rowsOf(ctx, d.db, scanEvidence, `SELECT `+evCols+` FROM evidence_bundles WHERE vm_id = ? ORDER BY id DESC LIMIT ?`, vmID, limit)
	}
	return rowsOf(ctx, d.db, scanEvidence, `SELECT `+evCols+` FROM evidence_bundles ORDER BY id DESC LIMIT ?`, limit)
}

// --- state backups (schema 2) ---

// StateBackup is one backup of Gaia's own state into a repository.
type StateBackup struct {
	ID           int64  `json:"id"`
	RepositoryID int64  `json:"repository_id"`
	State        string `json:"state"`
	Reason       string `json:"reason"`
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at"`
	Error        string `json:"error"`
	SnapshotID   string `json:"snapshot_id"`
	Bytes        int64  `json:"bytes"`
	SHA256       string `json:"sha256"`
}

const sbCols = `id, repository_id, state, reason, started_at, finished_at, error, snapshot_id, bytes, sha256`

func scanSB(s interface{ Scan(...any) error }) (StateBackup, error) {
	var b StateBackup
	err := s.Scan(&b.ID, &b.RepositoryID, &b.State, &b.Reason, &b.StartedAt, &b.FinishedAt, &b.Error, &b.SnapshotID, &b.Bytes, &b.SHA256)
	return b, err
}

// StartStateBackup records a state backup that is starting.
func (d *DB) StartStateBackup(ctx context.Context, actor string, repoID int64, reason string) (StateBackup, error) {
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `INSERT INTO state_backups (repository_id, state, reason, started_at) VALUES (?, 'running', ?, ?)`, repoID, reason, d.ts())
		if err != nil {
			return err
		}
		if id, err = r.LastInsertId(); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "state_backup", id, "", "running", reason, actor)
	})
	if err != nil {
		return StateBackup{}, err
	}
	return d.StateBackup(ctx, id)
}

// FinishStateBackup records how a state backup ended (ok when errText is empty).
func (d *DB) FinishStateBackup(ctx context.Context, actor string, id int64, snapshotID string, bytes int64, sum, errText string) (StateBackup, error) {
	to := "ok"
	if errText != "" {
		to = "failed"
	}
	err := d.tx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `UPDATE state_backups SET state = ?, finished_at = ?, error = ?, snapshot_id = ?, bytes = ?, sha256 = ?
			WHERE id = ? AND state = 'running'`, to, d.ts(), errText, snapshotID, bytes, sum, id)
		if err != nil {
			return err
		}
		if n, err := r.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return fmt.Errorf("%w: state backup %d is not running", ErrConflict, id)
		}
		reason := "stored as snapshot " + snapshotID
		if errText != "" {
			reason = errText
		}
		return d.recordState(ctx, tx, "state_backup", id, "running", to, reason, actor)
	})
	if err != nil {
		return StateBackup{}, err
	}
	return d.StateBackup(ctx, id)
}

// StateBackup returns a state backup record.
func (d *DB) StateBackup(ctx context.Context, id int64) (StateBackup, error) {
	b, err := scanSB(d.db.QueryRowContext(ctx, `SELECT `+sbCols+` FROM state_backups WHERE id = ?`, id))
	return b, notFound("state backup", id, err)
}

// StateBackups lists state backups newest first.
func (d *DB) StateBackups(ctx context.Context, limit int) ([]StateBackup, error) {
	return rowsOf(ctx, d.db, scanSB, `SELECT `+sbCols+` FROM state_backups ORDER BY id DESC LIMIT ?`, limit)
}
