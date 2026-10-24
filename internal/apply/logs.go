package apply

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Warewave-Technology/gaia/internal/catalog"
)

// archiveLogs stores the run's events, every output line and Ansible
// event, as JSON lines in the Platform's run log bucket. The database
// keeps them 90 days; the archive keeps them without a lock.
func (r *runner) archiveLogs(ctx context.Context) error {
	name := r.g.Platform().Spec.(*catalog.PlatformSpec).RunLogs
	if name == "" {
		return nil
	}
	evs, err := r.d.Store.Q().RunEvents(ctx, r.run)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range evs {
		line := map[string]any{"seq": e.Seq, "stage": e.Stage, "type": e.Event, "time": e.CreatedAt.Time}
		if e.HostUid.Valid {
			line["host"] = e.HostUid.String
		}
		if e.Task.Valid {
			line["task"] = e.Task.String
		}
		if e.Stdout.Valid {
			line["stdout"] = e.Stdout.String
		}
		if len(e.Data) > 0 {
			line["data"] = json.RawMessage(e.Data)
		}
		if err := enc.Encode(line); err != nil {
			return err
		}
	}
	p := r.provider(name)
	creds, err := r.d.Secrets.Read(ctx, p.Credentials)
	if err != nil {
		return err
	}
	a, err := r.d.Archive(p.Endpoint, p.Bucket, creds["access_key"], creds["secret_key"])
	if err != nil {
		return err
	}
	t := r.started
	_, err = a.Put(ctx, fmt.Sprintf("logs/%04d/%02d/%s.jsonl", t.Year(), t.Month(), r.run), buf.Bytes(), time.Time{})
	return err
}
