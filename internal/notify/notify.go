// Package notify tells people about changes (design 7.6): the change
// engine emits events, notify works out who subscribed to them, records
// one notification per recipient and queues its delivery. Deliveries are
// retried; the notifications table says what was sent.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Event is something that happened to a change.
type Event struct {
	Trigger     string    `json:"trigger"`
	Change      string    `json:"change"`
	Contract    string    `json:"contract"`
	Status      string    `json:"status"`
	PlanHash    string    `json:"planHash,omitempty"`
	Label       string    `json:"label,omitempty"`
	Source      string    `json:"source,omitempty"`
	RequestedBy []string  `json:"requestedBy,omitempty"`
	ApprovedBy  []string  `json:"approvedBy,omitempty"`
	DecidedBy   string    `json:"decidedBy,omitempty"`
	Comment     string    `json:"comment,omitempty"`
	Run         string    `json:"run,omitempty"`
	Error       string    `json:"error,omitempty"`
	Summary     Summary   `json:"summary"`
	At          time.Time `json:"at"`
	// Approvers are the teams whose members may approve a pending change;
	// their channels hear about it too.
	Approvers []Approver `json:"approvers,omitempty"`
	// URL is the change's page.
	URL string `json:"url,omitempty"`

	// Events that are not a change's: Key tells them apart, Teams are
	// told besides the owner's, and Group, Team and Person say who got
	// access where.
	Key    string   `json:"key,omitempty"`
	Teams  []string `json:"teams,omitempty"`
	Group  string   `json:"group,omitempty"`
	Team   string   `json:"team,omitempty"`
	Person string   `json:"person,omitempty"`
}

// Approver is one approval a pending change needs.
type Approver struct {
	Name       string   `json:"name"`
	Teams      []string `json:"teams"`
	Candidates []string `json:"candidates"`
	Why        string   `json:"why"`
}

// Summary counts what the change's plan does to servers.
type Summary struct {
	Create int `json:"create"`
	Keep   int `json:"keep"`
	Delete int `json:"delete"`
}

// Channel is a provider as a notification needs it: no secret, only
// where to find it.
type Channel struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Endpoint    string   `json:"endpoint,omitempty"`
	From        string   `json:"from,omitempty"`
	Credentials string   `json:"credentials,omitempty"`
	SecretsAddr string   `json:"secretsAddr,omitempty"` // the OpenBao the credentials are in
	To          []string `json:"to,omitempty"`
}

// Message is what a notification carries.
type Message struct {
	Event   Event   `json:"event"`
	Channel Channel `json:"channel"`
}

// Notifier records and queues notifications.
type Notifier struct {
	Store *store.Store
	Queue *river.Client[pgx.Tx]
	Log   *slog.Logger
}

func (n *Notifier) log() *slog.Logger {
	if n.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return n.Log
}

// Emit sends ev to its subscribers: the channels of the contract's owner
// team and, for a pending change, of the teams that may approve it; and
// the people who subscribed to the trigger and may see the contract. An
// event reaches each recipient once, however often it is emitted.
func (n *Notifier) Emit(ctx context.Context, g *catalog.Graph, ev Event) error {
	if n == nil || g == nil {
		return nil
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	if a := g.AuthConfig(); a != nil && ev.URL == "" && ev.Change != "" {
		ev.URL = a.URL + "/changes/" + ev.Change
	}
	key := ev.Change + "/" + ev.Trigger
	if ev.Key != "" {
		key = ev.Key
	}
	var errs []error
	send := func(recipient string, ch Channel) {
		if err := n.record(ctx, key, recipient, ev, ch); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", recipient, err))
		}
	}

	owner := ""
	if o, ok := g.Catalog.Get(catalog.KindServer, ev.Contract); ok {
		owner = o.Metadata.Labels["owner"]
	}
	teams := append([]string{owner}, ev.Teams...)
	if ev.Trigger == "change.pending" {
		for _, a := range ev.Approvers {
			teams = append(teams, a.Teams...)
		}
	}
	sort.Strings(teams)
	for _, team := range slices.Compact(teams) {
		t := g.Team(team)
		if t == nil {
			continue
		}
		for i, sub := range t.Notifications {
			if !slices.Contains(sub.Triggers, ev.Trigger) {
				continue
			}
			ch, err := channel(g, sub.Provider)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			ch.To = sub.To
			send(fmt.Sprintf("team/%s/%d", team, i), ch)
		}
	}

	if p := g.Platform(); p != nil {
		if pn := p.Spec.(*catalog.PlatformSpec).Notifications; pn != nil && pn.Email != "" {
			if err := n.people(ctx, g, ev, pn.Email, owner, send); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// people sends ev to the people who subscribed to its trigger and see the
// contract in full: its owners, operators and platform admins.
func (n *Notifier) people(ctx context.Context, g *catalog.Graph, ev Event, email, owner string, send func(string, Channel)) error {
	q := n.Store.Q()
	subs, err := q.Subscribers(ctx, ev.Trigger)
	if err != nil {
		return err
	}
	for _, u := range subs {
		teams, err := q.Teams(ctx, u.ID)
		if err != nil {
			return err
		}
		roles := g.RolesOf(teams)
		if (owner == "" || !slices.Contains(teams, owner)) && !slices.Contains(roles, "operator") && !slices.Contains(roles, "platform-admin") {
			continue
		}
		ch, err := channel(g, email)
		if err != nil {
			return err
		}
		ch.To = []string{u.Email.String}
		send("user/"+u.ID, ch)
	}
	return nil
}

func channel(g *catalog.Graph, name string) (Channel, error) {
	o, ok := g.Catalog.Get(catalog.KindProvider, name)
	if !ok {
		return Channel{}, fmt.Errorf("no Provider %q", name)
	}
	ps := o.Spec.(*catalog.ProviderSpec)
	ch := Channel{Name: name, Type: ps.Type, Endpoint: ps.Endpoint, From: ps.From, Credentials: ps.Credentials}
	if ch.Credentials != "" {
		if p := g.Platform(); p != nil {
			if b, ok := g.Catalog.Get(catalog.KindProvider, p.Spec.(*catalog.PlatformSpec).Secrets); ok {
				ch.SecretsAddr = b.Spec.(*catalog.ProviderSpec).Endpoint
			}
		}
	}
	return ch, nil
}

// record inserts a notification and queues its delivery, both or neither.
func (n *Notifier) record(ctx context.Context, key, recipient string, ev Event, ch Channel) error {
	msg, err := json.Marshal(Message{Event: ev, Channel: ch})
	if err != nil {
		return err
	}
	return n.Store.TxFunc(ctx, func(tx pgx.Tx) error {
		id, err := db.New(tx).InsertNotification(ctx, db.InsertNotificationParams{
			EventKey: key, Trigger: ev.Trigger, Channel: ch.Name, Recipient: recipient,
			Contract: text(ev.Contract), ChangeID: text(ev.Change), Message: msg,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // sent before
		}
		if err != nil {
			return err
		}
		if _, err := n.Queue.InsertTx(ctx, tx, SendArgs{ID: id}, nil); err != nil {
			return err
		}
		n.log().Info("notification", "id", id, "trigger", ev.Trigger, "change", ev.Change, "channel", ch.Name, "to", recipient)
		return nil
	})
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
