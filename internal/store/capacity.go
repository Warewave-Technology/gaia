package store

import (
	"context"
	"database/sql"
	"time"
)

// CapacityFact is what the proof capacity (internal/capacity, ADR-007 C4) needs to know of a VM with a policy.
type CapacityFact struct {
	VMID          int64
	VM            string
	PolicyID      int64
	Policy        string
	CheckTemplate string
	OSTag         string // the value of the VM's os: tag (os:windows, os:linux); "" without one
	TestIntervalS int64
	MaxProofAgeS  int64
	ProofAt       *float64 // unix: the newest passing test finished (the status table)
	Rank          int
	LastQueued    string // the newest restore test job's created_at; ""
	QueuedJob     int64
	QueuedAt      string
	RunningSince  string
	Durations     []float64 // its last 10 tests that ran, newest first (seconds)
}

// CapacityFacts reads every VM with a policy, in one query, and the durations of each one's last 10 tests that ran.
func (d *DB) CapacityFacts(ctx context.Context) ([]CapacityFact, error) {
	fs, err := rowsOf(ctx, d.db, func(s interface{ Scan(...any) error }) (CapacityFact, error) {
		var f CapacityFact
		var last, qat, run, osTag sql.NullString
		var qjob sql.NullInt64
		err := s.Scan(&f.VMID, &f.VM, &f.PolicyID, &f.Policy, &f.CheckTemplate, &f.TestIntervalS, &f.MaxProofAgeS, &f.ProofAt, &f.Rank,
			&last, &qjob, &qat, &run, &osTag)
		f.LastQueued, f.QueuedJob, f.QueuedAt, f.RunningSince, f.OSTag = last.String, qjob.Int64, qat.String, run.String, osTag.String
		return f, err
	}, `SELECT v.id, v.name, p.id, p.name, p.check_template, p.test_interval_s, p.max_proof_age_s, s.proven_finished_at, COALESCE(s.rank, 1),
		(SELECT j.created_at FROM jobs j WHERE j.vm_id = v.id AND j.kind = 'restore_test' ORDER BY j.id DESC LIMIT 1),
		(SELECT j.id FROM jobs j WHERE j.vm_id = v.id AND j.kind = 'restore_test' AND j.state IN ('queued', 'running') AND j.state = 'queued' ORDER BY j.id LIMIT 1),
		(SELECT j.created_at FROM jobs j WHERE j.vm_id = v.id AND j.kind = 'restore_test' AND j.state IN ('queued', 'running') AND j.state = 'queued' ORDER BY j.id LIMIT 1),
		(SELECT j.started_at FROM jobs j WHERE j.vm_id = v.id AND j.kind = 'restore_test' AND j.state IN ('queued', 'running') AND j.state = 'running' ORDER BY j.id LIMIT 1),
		(SELECT t.value FROM vm_tags t WHERE t.vm_id = v.id AND t.key = 'os' ORDER BY t.value LIMIT 1)
		FROM vms v JOIN policies p ON p.id = v.policy_id LEFT JOIN vm_status s ON s.vm_id = v.id ORDER BY v.id`)
	if err != nil {
		return nil, err
	}
	st, err := d.db.PrepareContext(ctx, `SELECT started_at, finished_at FROM restore_tests WHERE vm_id = ? AND verdict IN ('OK', 'WARNING', 'CRITICAL')
		AND finished_at != '' ORDER BY id DESC LIMIT 10`)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	for i := range fs {
		rows, err := st.QueryContext(ctx, fs[i].VMID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var a, b string
			if err := rows.Scan(&a, &b); err != nil {
				rows.Close()
				return nil, err
			}
			ta, err1 := time.Parse(time.RFC3339Nano, a)
			tb, err2 := time.Parse(time.RFC3339Nano, b)
			if err1 == nil && err2 == nil && !tb.Before(ta) {
				fs[i].Durations = append(fs[i].Durations, tb.Sub(ta).Seconds())
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return fs, nil
}

// QueuedTests lists the queued restore tests in the order they are started (ADR-007 C4): retests of CRITICAL VMs,
// then VMs never proven, then by when their proof expires, then by policy name, then by when they were queued.
func (d *DB) QueuedTests(ctx context.Context) ([]Job, error) {
	return rowsOf(ctx, d.db, scanJob, `SELECT j.id, j.kind, j.vm_id, j.state, j.reason, j.error, j.created_at, j.started_at, j.finished_at
		FROM jobs j JOIN vms v ON v.id = j.vm_id LEFT JOIN policies p ON p.id = v.policy_id LEFT JOIN vm_status s ON s.vm_id = v.id
		WHERE j.state IN ('queued', 'running') AND j.state = 'queued' AND j.kind = 'restore_test'
		ORDER BY COALESCE(s.rank, 1) = 0 DESC, s.proven_test_id IS NULL DESC, COALESCE(s.proven_finished_at + s.max_proof_age_s, 0),
			COALESCE(p.name, ''), j.id`)
}
