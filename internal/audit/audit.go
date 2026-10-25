// Package audit writes Gaia's audit log (design 17.4): who did what, from
// which session and address, to which object, and how it ended. The log
// is append-only; the application's database role may not change it.
package audit

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Entry is one action.
type Entry struct {
	Actor     string // a username, gaia, or source/<name>
	ActorID   string
	SessionID string
	Address   string
	Action    string // settings.import, settings.write, change.approve…
	Object    string // Kind/name, a change or run ID
	Result    string // ok when empty, else denied or failed
	Detail    any
}

// Write adds an entry, in the caller's transaction when q is one.
func Write(ctx context.Context, q *db.Queries, e Entry) error {
	var detail []byte
	if e.Detail != nil {
		var err error
		if detail, err = json.Marshal(e.Detail); err != nil {
			return err
		}
	}
	if e.Result == "" {
		e.Result = "ok"
	}
	_, err := q.InsertAudit(ctx, db.InsertAuditParams{
		Actor: e.Actor, ActorID: text(e.ActorID), SessionID: text(e.SessionID), Address: text(e.Address),
		Action: e.Action, Object: text(e.Object), Result: e.Result, Detail: detail,
	})
	return err
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
