package store

import (
	"context"
	"time"
)

// Timeline is what happened to a VM since a moment: its points (taken since), its restore tests (finished since)
// and its jobs that failed (finished since), each oldest first. For the UI's history strip (T20).
type Timeline struct {
	Points     []Point       `json:"points"`
	Tests      []RestoreTest `json:"tests"`
	FailedJobs []Job         `json:"failed_jobs"`
}

// VMTimeline returns the VM's timeline since a moment. Times are compared as RFC 3339 text at second resolution,
// so a row up to a second before since may be included; callers filter on the parsed time.
func (d *DB) VMTimeline(ctx context.Context, vmID int64, since time.Time) (Timeline, error) {
	from := since.UTC().Add(-time.Second).Format("2006-01-02T15:04:05Z")
	var tl Timeline
	var err error
	if tl.Points, err = rowsOf(ctx, d.db, scanPoint, `SELECT `+pointCols+` FROM points WHERE vm_id = ? AND taken_at >= ? ORDER BY taken_at, id`, vmID, from); err != nil {
		return tl, err
	}
	if tl.Tests, err = rowsOf(ctx, d.db, scanTest, `SELECT `+testCols+` FROM restore_tests WHERE vm_id = ? AND finished_at >= ? ORDER BY finished_at, id`, vmID, from); err != nil {
		return tl, err
	}
	tl.FailedJobs, err = rowsOf(ctx, d.db, scanJob, `SELECT `+jobCols+` FROM jobs WHERE vm_id = ? AND state = 'failed' AND finished_at >= ? ORDER BY finished_at, id`, vmID, from)
	return tl, err
}
