package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/mail"
	"slices"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Notifications are a person's own subscriptions (design 7.6): the
// triggers they want an email for. Local accounts set their address;
// others get the one their identity provider verified.
type Notifications struct {
	Email         string   `json:"email"`
	EmailEditable bool     `json:"emailEditable"`
	Triggers      []string `json:"triggers"`
	Available     []string `json:"available"`
}

func (a *Service) notifications(ctx context.Context, userID string) (Notifications, error) {
	q := a.store.Q()
	u, err := q.User(ctx, userID)
	if err != nil {
		return Notifications{}, err
	}
	t, err := q.Subscriptions(ctx, userID)
	if err != nil {
		return Notifications{}, err
	}
	if t == nil {
		t = []string{}
	}
	return Notifications{Email: u.Email.String, EmailEditable: u.Provider == "local", Triggers: t, Available: catalog.Triggers}, nil
}

func (a *Service) myNotifications(w http.ResponseWriter, r *http.Request) {
	n, err := a.notifications(r.Context(), From(r.Context()).UserID)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (a *Service) setMyNotifications(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := From(ctx)
	var b struct {
		Email    *string  `json:"email"`
		Triggers []string `json:"triggers"`
	}
	if !decode(w, r, &b) {
		return
	}
	for _, t := range b.Triggers {
		if !slices.Contains(catalog.Triggers, t) {
			problem(w, http.StatusBadRequest, fmt.Sprintf("%q is not a trigger", t))
			return
		}
	}
	cur, err := a.notifications(ctx, p.UserID)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	if b.Email != nil && *b.Email != cur.Email {
		if !cur.EmailEditable {
			problem(w, http.StatusBadRequest, "your address comes from your identity provider")
			return
		}
		if *b.Email != "" {
			if addr, err := mail.ParseAddress(*b.Email); err != nil || addr.Address != *b.Email {
				problem(w, http.StatusBadRequest, "not an email address")
				return
			}
		}
	}
	err = a.store.Tx(ctx, func(q *db.Queries) error {
		if b.Email != nil && cur.EmailEditable {
			if err := q.SetEmail(ctx, db.SetEmailParams{ID: p.UserID, Email: text(*b.Email)}); err != nil {
				return err
			}
		}
		if err := q.ClearSubscriptions(ctx, p.UserID); err != nil {
			return err
		}
		for _, t := range b.Triggers {
			if err := q.AddSubscription(ctx, db.AddSubscriptionParams{UserID: p.UserID, Trigger: t}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	a.myNotifications(w, r)
}
