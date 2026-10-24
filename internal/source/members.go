package source

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Warewave-Technology/gaia/internal/ad"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/notify"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Membership is what one membership sync of a directory changed.
type Membership struct {
	Provider string   `json:"provider"`
	Joined   []string `json:"joined,omitempty"` // team/role: username
	Left     []string `json:"left,omitempty"`
	Disabled []string `json:"disabled,omitempty"` // accounts closed in the directory or gone from it
	Enabled  []string `json:"enabled,omitempty"`
}

// Memberships brings the people of every team with an AD group into the
// database (design 14.2): accounts by objectGUID, members of the team's
// group and of its approvers group, nested groups opened when the team's
// TeamSource says so. An account closed or deleted in AD is closed in
// Gaia and its sessions end.
func (s *Syncer) Memberships(ctx context.Context, g *catalog.Graph) ([]Membership, error) {
	byProvider := map[string][]*catalog.Object{}
	for _, o := range g.Catalog.List(catalog.KindTeam) {
		if t := o.Spec.(*catalog.TeamSpec); t.ADGroup != nil && t.ADGroup.Provider != "" {
			byProvider[t.ADGroup.Provider] = append(byProvider[t.ADGroup.Provider], o)
		}
	}
	providers := make([]string, 0, len(byProvider))
	for p := range byProvider {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	var out []Membership
	var errs []error
	for _, p := range providers {
		m, err := s.members(ctx, g, p, byProvider[p])
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
		}
		out = append(out, m)
		if serr := s.save(ctx, Report{Source: "Directory/" + p, Status: status(err), Error: errText(err),
			Added: m.Joined, Removed: m.Left, Changed: append(append([]string{}, m.Disabled...), m.Enabled...)}); serr != nil {
			errs = append(errs, serr)
		}
	}
	return out, errors.Join(errs...)
}

func status(err error) string {
	if err != nil {
		return "failed"
	}
	return "synced"
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Syncer) members(ctx context.Context, g *catalog.Graph, provider string, teams []*catalog.Object) (Membership, error) {
	rep := Membership{Provider: provider}
	dir, err := s.directory(ctx, g, provider)
	if err != nil {
		return rep, err
	}
	defer func() { _ = dir.Close() }()
	q := s.Store.Q()
	source := "ad:" + provider
	// The first sync of a directory brings everyone; that is not news.
	known0, err := q.DirectoryUsers(ctx, provider)
	if err != nil {
		return rep, err
	}
	first := len(known0) == 0
	ids := map[string]string{} // GUID → user id
	names := map[string]string{}
	seen := map[string]bool{}

	account := func(u ad.User) (string, error) {
		if id, ok := ids[u.GUID]; ok {
			return id, nil
		}
		id, err := s.account(ctx, provider, u, &rep)
		if err != nil {
			return "", err
		}
		ids[u.GUID], names[id], seen[u.GUID] = id, u.Username, true
		return id, nil
	}
	var kept []string
	for _, o := range teams {
		t := o.Spec.(*catalog.TeamSpec)
		recursive := true
		if src := o.Metadata.Annotations[catalog.AnnotationGeneratedBy]; strings.HasPrefix(src, catalog.KindTeamSource+"/") {
			if ts, ok := g.Catalog.Get(catalog.KindTeamSource, strings.TrimPrefix(src, catalog.KindTeamSource+"/")); ok {
				recursive = ts.Spec.(*catalog.TeamSourceSpec).Membership.Recursive
			}
		}
		groups := map[string]string{"member": t.ADGroup.DN}
		if t.ApproversGroup != nil {
			groups["approver"] = t.ApproversGroup.DN
		}
		kept = append(kept, o.Metadata.Name)
		for role, dn := range groups {
			people, err := dir.Members(dn, recursive)
			if err != nil {
				return rep, err
			}
			var userIDs []string
			for _, u := range people {
				id, err := account(u)
				if err != nil {
					return rep, err
				}
				if !u.Disabled {
					userIDs = append(userIDs, id)
				}
			}
			before, err := q.MembershipUsers(ctx, db.MembershipUsersParams{Team: o.Metadata.Name, Source: source, Role: role})
			if err != nil {
				return rep, err
			}
			for _, id := range userIDs {
				if !slices.Contains(before, id) {
					rep.Joined = append(rep.Joined, fmt.Sprintf("%s/%s: %s", o.Metadata.Name, role, names[id]))
					if role == "member" && !first {
						s.privileged(ctx, g, o.Metadata.Name, names[id], id)
					}
				}
			}
			for _, id := range before {
				if !slices.Contains(userIDs, id) {
					rep.Left = append(rep.Left, fmt.Sprintf("%s/%s: %s", o.Metadata.Name, role, s.name(ctx, id)))
				}
			}
			if userIDs == nil {
				userIDs = []string{}
			}
			if err := q.SetMemberships(ctx, db.SetMembershipsParams{Team: o.Metadata.Name, Source: source, Role: role, Users: userIDs}); err != nil {
				return rep, err
			}
		}
		if t.ApproversGroup == nil {
			if err := q.SetMemberships(ctx, db.SetMembershipsParams{Team: o.Metadata.Name, Source: source, Role: "approver", Users: []string{}}); err != nil {
				return rep, err
			}
		}
	}
	if err := q.DropSourceTeams(ctx, db.DropSourceTeamsParams{Source: source, Teams: kept}); err != nil {
		return rep, err
	}
	// Accounts in no team any more: still in AD, or gone from it.
	known, err := q.DirectoryUsers(ctx, provider)
	if err != nil {
		return rep, err
	}
	for _, u := range known {
		if seen[u.Subject.String] {
			continue
		}
		a, err := dir.UserByGUID(u.Subject.String)
		switch {
		case errors.Is(err, ad.ErrNoAccount):
			a = ad.User{GUID: u.Subject.String, Username: u.DirectoryName.String, DisplayName: u.DisplayName.String, Mail: u.Email.String, Disabled: true}
		case err != nil:
			return rep, err
		}
		if _, err := s.account(ctx, provider, a, &rep); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

func (s *Syncer) name(ctx context.Context, id string) string {
	if u, err := s.Store.Q().User(ctx, id); err == nil {
		return u.Username
	}
	return id
}

// account creates or updates the Gaia account of a directory account; a
// closed one loses its sessions.
func (s *Syncer) account(ctx context.Context, provider string, u ad.User, rep *Membership) (string, error) {
	q := s.Store.Q()
	cur, err := q.DirectoryUser(ctx, db.DirectoryUserParams{Provider: provider, Subject: text(u.GUID)})
	if errors.Is(err, pgx.ErrNoRows) {
		name := u.Username
		if _, err := q.UserByName(ctx, name); err == nil {
			// The name is someone else's (a local account): keep both.
			name = name + "." + provider
		}
		id := store.NewUserID()
		by := pgtype.Text{}
		if u.Disabled {
			by = text("directory")
		}
		err := q.InsertDirectoryUser(ctx, db.InsertDirectoryUserParams{
			ID: id, Username: name, DisplayName: text(u.DisplayName), Provider: provider, Subject: text(u.GUID),
			DirectoryName: text(u.Username), Email: text(u.Mail), Enabled: !u.Disabled, DisabledBy: by,
		})
		return id, err
	}
	if err != nil {
		return "", err
	}
	err = q.UpdateDirectoryUser(ctx, db.UpdateDirectoryUserParams{ID: cur.ID, DisplayName: text(u.DisplayName), DirectoryName: text(u.Username), Email: text(u.Mail), Active: !u.Disabled})
	if err != nil {
		return "", err
	}
	switch {
	case cur.Enabled && u.Disabled:
		rep.Disabled = append(rep.Disabled, cur.Username)
		if err := q.RevokeUserSessions(ctx, cur.ID); err != nil {
			return "", err
		}
	case !cur.Enabled && !u.Disabled && cur.DisabledBy.String == "directory":
		rep.Enabled = append(rep.Enabled, cur.Username)
	}
	return cur.ID, nil
}

// privileged tells the owners of privileged groups, and the platform
// admins, that AD added someone to one of their teams (design 14.3):
// Gaia distributes the access, since AD decides who is in a team, but
// makes it seen.
func (s *Syncer) privileged(ctx context.Context, g *catalog.Graph, team, person, userID string) {
	if s.Notify == nil {
		return
	}
	var admins []string
	if pol := g.Policy(); pol != nil {
		admins = pol.Spec.(*catalog.PolicySpec).GaiaRoles["platform-admin"]
	}
	for _, o := range g.Catalog.List(catalog.KindGroup) {
		teams, privileged := g.GroupTeams(o.Metadata.Name)
		if !privileged || !slices.Contains(teams, team) {
			continue
		}
		gs := o.Spec.(*catalog.GroupSpec)
		ev := notify.Event{
			Trigger: "access.privileged", Key: fmt.Sprintf("access/%s/%s/%s/%s", o.Metadata.Name, team, userID, time.Now().UTC().Format(time.RFC3339)),
			Teams: append([]string{gs.Owner}, admins...), Group: o.Metadata.Name, Team: team, Person: person,
			Comment: "AD'de " + team + " takımına eklendi; Gaia erişimi dağıtıyor.",
		}
		if err := s.Notify.Emit(ctx, g, ev); err != nil {
			s.log().Warn("notification", "group", o.Metadata.Name, "err", err)
		}
	}
}
