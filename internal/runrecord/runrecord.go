package runrecord

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Record is the body of a run record (design 10.1). Its JSON, as
// encoding/json writes it, is what is hashed; fields keep their order.
type Record struct {
	Run                  string            `json:"run"`
	RequestedBy          string            `json:"requestedBy"`
	ApprovedBy           []string          `json:"approvedBy"`
	Source               Source            `json:"source"`
	CatalogCommit        string            `json:"catalogCommit"`
	ConfigCommit         string            `json:"configCommit,omitempty"`
	ExecutionEnvironment string            `json:"executionEnvironment,omitempty"`
	Contracts            []string          `json:"contracts"`
	PlanHash             string            `json:"planHash"`
	ExecutedAs           map[string]string `json:"executedAs"`
	StartedAt            time.Time         `json:"startedAt"`
	FinishedAt           time.Time         `json:"finishedAt"`
	Result               string            `json:"result"` // succeeded | failed
	Error                string            `json:"error,omitempty"`
	Changes              []Change          `json:"changes"`
}

// Source is where the contracts came from.
type Source struct {
	Repo   string `json:"repo"`
	Commit string `json:"commit"`
	Signed bool   `json:"signed"`
}

// Change is what one stage did to one server.
type Change struct {
	Contract string `json:"contract"`
	UID      string `json:"uid,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Stage    string `json:"stage"` // plan | provision | monitoring | configure | decommission
	Action   string `json:"action"`
	Detail   string `json:"detail,omitempty"`
}

// Hash links a body to the record before it: sha256 of the previous hash,
// a newline and the body.
func Hash(prev string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(prev))
	h.Write([]byte{'\n'})
	h.Write(body)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Append writes the record at the end of the chain. Records are appended
// one at a time: the table is locked against other writers.
func Append(ctx context.Context, s *store.Store, r Record) (db.RunRecord, error) {
	body, err := json.Marshal(r)
	if err != nil {
		return db.RunRecord{}, err
	}
	var out db.RunRecord
	err = s.Tx(ctx, func(q *db.Queries) error {
		if err := q.LockRunRecords(ctx); err != nil {
			return err
		}
		prev := ""
		last, err := q.LastRunRecord(ctx)
		switch {
		case err == nil:
			prev = last.RecordHash
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		out, err = q.InsertRunRecord(ctx, db.InsertRunRecordParams{RunID: r.Run, Body: string(body), PrevHash: prev, RecordHash: Hash(prev, body)})
		return err
	})
	return out, err
}

// Broken is a record that does not fit the chain.
type Broken struct {
	Seq    int64
	Run    string
	Reason string
}

func (b *Broken) Error() string {
	return fmt.Sprintf("record %d (%s): %s", b.Seq, b.Run, b.Reason)
}

// Verify walks the chain from the first record and returns how many
// records it checked. A record whose hash does not match its body, or
// that does not point at the record before it, is a *Broken error.
func Verify(ctx context.Context, q *db.Queries) (int, error) {
	const page = 500
	n, prev, after := 0, "", int64(0)
	for {
		recs, err := q.RunRecords(ctx, db.RunRecordsParams{Seq: after, Limit: page})
		if err != nil {
			return n, err
		}
		for _, r := range recs {
			if r.PrevHash != prev {
				return n, &Broken{Seq: r.Seq, Run: r.RunID, Reason: fmt.Sprintf("points at %s, the record before is %s", short(r.PrevHash), short(prev))}
			}
			if h := Hash(r.PrevHash, []byte(r.Body)); h != r.RecordHash {
				return n, &Broken{Seq: r.Seq, Run: r.RunID, Reason: "the body does not match its hash"}
			}
			var body Record
			if err := json.Unmarshal([]byte(r.Body), &body); err != nil || body.Run != r.RunID {
				return n, &Broken{Seq: r.Seq, Run: r.RunID, Reason: "the body is not this run's record"}
			}
			prev, after = r.RecordHash, r.Seq
			n++
		}
		if len(recs) < page {
			return n, nil
		}
	}
}

func short(h string) string {
	if h == "" {
		return "(none)"
	}
	if len(h) > 19 {
		return h[:19]
	}
	return h
}

// Archived is a record as it is stored in the archive: the body and the
// chain around it.
type Archived struct {
	Seq        int64           `json:"seq"`
	PrevHash   string          `json:"prevHash"`
	RecordHash string          `json:"recordHash"`
	Body       json.RawMessage `json:"body"`
}

// Key is where a record is stored in the archive.
func Key(r db.RunRecord) string {
	t := r.CreatedAt.Time.UTC()
	return fmt.Sprintf("records/%04d/%02d/%s.json", t.Year(), t.Month(), r.RunID)
}

// Archive stores records where nobody can change them before retention
// ends.
type Archive interface {
	Put(ctx context.Context, key string, data []byte, retainUntil time.Time) (string, error)
}

// Store puts the record in the archive and notes where on the record.
func Store(ctx context.Context, s *store.Store, a Archive, r db.RunRecord, retention time.Duration) (string, error) {
	// Compact, so that the body keeps the exact bytes that were hashed and
	// the copy can be checked on its own.
	data, err := json.Marshal(Archived{Seq: r.Seq, PrevHash: r.PrevHash, RecordHash: r.RecordHash, Body: json.RawMessage(r.Body)})
	if err != nil {
		return "", err
	}
	loc, err := a.Put(ctx, Key(r), append(data, '\n'), time.Now().Add(retention))
	if err != nil {
		return "", err
	}
	return loc, s.Q().SetRunRecordArchived(ctx, db.SetRunRecordArchivedParams{RunID: r.RunID, Archived: pgtype.Text{String: loc, Valid: true}})
}
