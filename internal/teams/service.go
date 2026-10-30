package teams

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

const (
	maxNameRunes        = 80
	maxDescriptionRunes = 500
	// drainBatch is how many queued people one Drain syncs at most.
	drainBatch = 500
)

// teamRoles are the roles a team can carry. Never the owner's, and never
// the automation's.
var teamRoles = map[string]bool{security.RoleViewer: true, security.RoleDeveloper: true, security.RoleAdmin: true}

// Service manages teams. Every method that takes a Principal checks it
// first: member:read to read, member:manage to change.
type Service struct {
	db      *pgdriver.PgDB
	authz   Authorizer
	members security.MembershipFunc
	audit   Auditor
	log     func(msg string, err error)
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	log := d.Log
	if log == nil {
		log = func(string, error) {}
	}
	return &Service{db: d.DB, authz: d.Authz, members: d.Members, audit: d.Audit, log: log}
}

// List is every team in the caller's workspace, with members and grants.
func (s *Service) List(ctx context.Context, p security.Principal) (*TeamList, error) {
	if err := s.authz.Require(ctx, p, security.MemberRead, ""); err != nil {
		return nil, err
	}
	teams, err := loadTeams(ctx, s.db, p.OrgID, "")
	if err != nil {
		return nil, err
	}
	return &TeamList{Teams: teams}, nil
}

// Get is one team of the caller's workspace.
func (s *Service) Get(ctx context.Context, p security.Principal, id string) (*Team, error) {
	if err := s.authz.Require(ctx, p, security.MemberRead, ""); err != nil {
		return nil, err
	}
	return s.load(ctx, p.OrgID, id)
}

func (s *Service) load(ctx context.Context, orgID, id string) (*Team, error) {
	if id == "" {
		return nil, ErrNotFound
	}
	teams, err := loadTeams(ctx, s.db, orgID, id)
	if err != nil {
		return nil, err
	}
	if len(teams) == 0 {
		return nil, ErrNotFound
	}
	return &teams[0], nil
}

// Create makes a team in the caller's workspace.
func (s *Service) Create(ctx context.Context, p security.Principal, in CreateTeamInput) (*Team, error) {
	if err := s.authz.Require(ctx, p, security.MemberManage, ""); err != nil {
		return nil, err
	}
	name, slug, err := cleanName(in.Name)
	if err != nil {
		return nil, err
	}
	description, err := cleanDescription(in.Description)
	if err != nil {
		return nil, err
	}
	id, err := typeid.Generate("team")
	if err != nil {
		return nil, fmt.Errorf("generate team id: %w", err)
	}
	t := &Team{ID: id.String(), OrgID: p.OrgID, Name: name, Slug: slug, Description: description, Members: []Member{}, Grants: []Grant{}}
	if insertErr := insertTeam(ctx, s.db, t, p.UserID); insertErr != nil {
		return nil, insertErr
	}
	s.record(ctx, p, "team.create", t.ID, nil)
	return t, nil
}

// Update changes a team's name or description. A new name re-derives the
// slug. Rights don't change, so nobody is synced.
func (s *Service) Update(ctx context.Context, p security.Principal, id string, in UpdateTeamInput) (*Team, error) {
	if err := s.authz.Require(ctx, p, security.MemberManage, ""); err != nil {
		return nil, err
	}
	var name, slug, description *string
	var fields []string
	if in.Name != nil {
		n, sl, err := cleanName(*in.Name)
		if err != nil {
			return nil, err
		}
		name, slug = &n, &sl
		fields = append(fields, "name")
	}
	if in.Description != nil {
		d, err := cleanDescription(*in.Description)
		if err != nil {
			return nil, err
		}
		description = &d
		fields = append(fields, "description")
	}
	if len(fields) == 0 {
		return s.load(ctx, p.OrgID, id)
	}
	found, err := updateTeam(ctx, s.db, p.OrgID, id, name, slug, description)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNotFound
	}
	s.record(ctx, p, "team.update", id, map[string]any{"fields": fields})
	return s.load(ctx, p.OrgID, id)
}

// Delete removes a team, its memberships and grants, and clears it as any
// project's responsible team. Its members are queued before it goes, and
// synced once it has.
func (s *Service) Delete(ctx context.Context, p security.Principal, id string) error {
	if err := s.authz.Require(ctx, p, security.MemberManage, ""); err != nil {
		return err
	}
	err := s.change(ctx, p.OrgID, id, func(tx driver.Tx) ([]queued, error) {
		q, err := queueTeam(ctx, tx, p.OrgID, id)
		if err != nil {
			return nil, err
		}
		return q, deleteTeam(ctx, tx, p.OrgID, id)
	})
	if err != nil {
		return err
	}
	s.record(ctx, p, "team.delete", id, nil)
	return nil
}

// AddMember puts a workspace member in the team. Only people join teams:
// an empty ID, or one holding ":" (Loomplane automation's
// "loomplane:<org>", the agent's "agent:<org>"), is refused before any
// lookup.
func (s *Service) AddMember(ctx context.Context, p security.Principal, teamID, userID string) (*Team, error) {
	if userID == "" || strings.Contains(userID, ":") {
		return nil, fmt.Errorf("%w: only a person can join a team", ErrInvalid)
	}
	if err := s.authz.Require(ctx, p, security.MemberManage, ""); err != nil {
		return nil, err
	}
	if _, found, err := s.members(ctx, p.OrgID, userID); err != nil {
		return nil, fmt.Errorf("look up membership: %w", err)
	} else if !found {
		return nil, ErrNotMember
	}
	err := s.change(ctx, p.OrgID, teamID, func(tx driver.Tx) ([]queued, error) {
		// The insert takes the member ID from authsome_members, so a
		// person who left after the lookup above is still refused.
		added, err := addMember(ctx, tx, p.OrgID, teamID, userID, p.UserID)
		if err != nil {
			return nil, err
		}
		if !added {
			return nil, ErrNotMember
		}
		return queuePerson(ctx, tx, p.OrgID, userID)
	})
	if err != nil {
		return nil, err
	}
	s.record(ctx, p, "team.member.add", teamID, map[string]any{"user_id": userID})
	return s.load(ctx, p.OrgID, teamID)
}

// RemoveMember takes a person out of the team. Removing someone who isn't
// in it changes nothing.
func (s *Service) RemoveMember(ctx context.Context, p security.Principal, teamID, userID string) (*Team, error) {
	if err := s.authz.Require(ctx, p, security.MemberManage, ""); err != nil {
		return nil, err
	}
	removed := false
	err := s.change(ctx, p.OrgID, teamID, func(tx driver.Tx) ([]queued, error) {
		var err error
		if removed, err = removeMember(ctx, tx, teamID, userID); err != nil || !removed {
			return nil, err
		}
		return queuePerson(ctx, tx, p.OrgID, userID)
	})
	if err != nil {
		return nil, err
	}
	if removed {
		s.record(ctx, p, "team.member.remove", teamID, map[string]any{"user_id": userID})
	}
	return s.load(ctx, p.OrgID, teamID)
}

// SetGrant gives the team a role on a project of the caller's workspace,
// or on the whole workspace. The caller can't grant a role above their own
// member role (security.MayGrant).
func (s *Service) SetGrant(ctx context.Context, p security.Principal, teamID string, in SetGrantInput) (*Team, error) {
	if !teamRoles[in.Role] {
		return nil, fmt.Errorf("%w: a team's role is viewer, developer or admin", ErrInvalid)
	}
	if err := s.authz.Require(ctx, p, security.MemberManage, ""); err != nil {
		return nil, err
	}
	callerRole, found, err := s.members(ctx, p.OrgID, p.UserID)
	if err != nil {
		return nil, fmt.Errorf("look up membership: %w", err)
	}
	granted, err := security.MemberRoleFor(in.Role)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if !found || !security.MayGrant(callerRole, granted) {
		return nil, fmt.Errorf("%w: you can't grant a role above your own", security.ErrForbidden)
	}
	id, err := typeid.Generate("tgrant")
	if err != nil {
		return nil, fmt.Errorf("generate grant id: %w", err)
	}
	grantID := ""
	err = s.change(ctx, p.OrgID, teamID, func(tx driver.Tx) ([]queued, error) {
		var upsertErr error
		if grantID, upsertErr = upsertGrant(ctx, tx, p.OrgID, teamID, id.String(), in.Role, in.ProjectID, p.UserID); upsertErr != nil {
			return nil, upsertErr
		}
		return queueTeam(ctx, tx, p.OrgID, teamID)
	})
	if err != nil {
		return nil, err
	}
	s.record(ctx, p, "team.grant.set", teamID, map[string]any{"grant_id": grantID, "role": in.Role, "project_id": in.ProjectID})
	return s.load(ctx, p.OrgID, teamID)
}

// RemoveGrant takes one of the team's grants away from all its members.
func (s *Service) RemoveGrant(ctx context.Context, p security.Principal, teamID, grantID string) (*Team, error) {
	if err := s.authz.Require(ctx, p, security.MemberManage, ""); err != nil {
		return nil, err
	}
	var gone Grant
	err := s.change(ctx, p.OrgID, teamID, func(tx driver.Tx) ([]queued, error) {
		var err error
		if gone, err = deleteGrant(ctx, tx, p.OrgID, teamID, grantID); err != nil {
			return nil, err
		}
		return queueTeam(ctx, tx, p.OrgID, teamID)
	})
	if err != nil {
		return nil, err
	}
	s.record(ctx, p, "team.grant.remove", teamID, map[string]any{"grant_id": grantID, "role": gone.Role, "project_id": gone.ProjectID})
	return s.load(ctx, p.OrgID, teamID)
}

// Grants is every role the person holds through a team, as a current
// member, with ProjectID "" for the whole workspace. It's the
// security.TeamGrants Sync reads while holding the person's lock, so it
// only reads and never syncs.
func (s *Service) Grants(ctx context.Context, orgID, userID string) ([]security.ScopedRole, error) {
	return grantsOf(ctx, s.db, orgID, userID)
}

// Leave takes the person out of every team in the organization. It's the
// security.Leaver authsome's member removal runs before it revokes the
// person's roles, so it neither queues nor syncs: the removal's own
// revokes, before and after authsome's delete, take everything away.
func (s *Service) Leave(ctx context.Context, orgID, userID string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM loomplane_team_members WHERE org_id = $1 AND user_id = $2`, orgID, userID); err != nil {
		return fmt.Errorf("leave teams: %w", err)
	}
	return nil
}

// Drain syncs up to drainBatch queued people, oldest first: what a failed
// or interrupted sync left behind. It's the teams.sync job's body. It never
// waits on a person's lock (TrySync): a person whose sync is running
// elsewhere stays queued for the next run. A failed sync is logged and its
// row stays. Drain fails only when it can't read the queue.
func (s *Service) Drain(ctx context.Context) error {
	rows, err := due(ctx, s.db, drainBatch)
	if err != nil {
		return err
	}
	// due has read and closed its rows: no connection is held from here.
	for _, row := range rows {
		if ctx.Err() != nil {
			return nil
		}
		ran, err := s.authz.TrySync(ctx, row.orgID, row.userID)
		if err != nil {
			s.log("sync roles from the queue", fmt.Errorf("org %s user %s: %w", row.orgID, row.userID, err))
			continue
		}
		if ran {
			s.dequeue(ctx, row)
		}
	}
	return nil
}

// Reap is a projects.Reaper: before a project is deleted (and its grants
// with it), it queues the members of every team with a grant on it, and
// returns what syncs them once the deletion has happened. An environment's
// deletion changes no grant, so it returns nil.
func (s *Service) Reap(ctx context.Context, orgID, projectID, environmentID string) func(context.Context) {
	if environmentID != "" || projectID == "" {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.log("queue the members of a deleted project's teams", fmt.Errorf("org %s project %s: %w", orgID, projectID, err))
		return nil
	}
	defer rollback(tx)
	rows, err := queueProject(ctx, tx, orgID, projectID)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		s.log("queue the members of a deleted project's teams", fmt.Errorf("org %s project %s: %w", orgID, projectID, err))
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	return func(ctx context.Context) { s.syncAll(ctx, rows) }
}

// change runs fn in a transaction that holds the team's row lock, and
// once it has committed, syncs everyone fn queued. A team that isn't in
// orgID is ErrNotFound.
//
// The syncs run after the commit, with no connection held: Sync waits for
// a person's lock, which holds a connection of its own, and needs another.
func (s *Service) change(ctx context.Context, orgID, teamID string, fn func(tx driver.Tx) ([]queued, error)) error {
	if teamID == "" {
		return ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("change team: %w", err)
	}
	defer rollback(tx)
	if lockErr := lockTeam(ctx, tx, orgID, teamID); lockErr != nil {
		return lockErr
	}
	rows, err := fn(tx)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("change team: %w", err)
	}
	s.syncAll(ctx, rows)
	return nil
}

// syncAll syncs each queued person and takes their row off the queue. A
// failed sync is logged and its row stays for the job. Callers hold no
// connection.
func (s *Service) syncAll(ctx context.Context, rows []queued) {
	for _, row := range rows {
		if err := s.authz.Sync(ctx, row.orgID, row.userID); err != nil {
			s.log("sync roles after a team change", fmt.Errorf("org %s user %s: %w", row.orgID, row.userID, err))
			continue
		}
		s.dequeue(ctx, row)
	}
}

func (s *Service) dequeue(ctx context.Context, row queued) {
	if err := dequeue(ctx, s.db, row); err != nil {
		s.log("take a synced person off the queue", fmt.Errorf("org %s user %s: %w", row.orgID, row.userID, err))
	}
}

func (s *Service) record(ctx context.Context, p security.Principal, action, teamID string, attrs map[string]any) {
	if s.audit == nil {
		return
	}
	s.audit.Record(ctx, AuditEvent{Action: action, TeamID: teamID, Principal: p, Attributes: attrs})
}

// cleanName trims a team name, checks its length and derives its slug.
func cleanName(raw string) (name, slug string, err error) {
	name = strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameRunes {
		return "", "", fmt.Errorf("%w: a team's name is 1 to %d characters", ErrInvalid, maxNameRunes)
	}
	if slug = projects.Slugify(name); slug == "" {
		return "", "", fmt.Errorf("%w: a team's name needs a letter or a digit", ErrInvalid)
	}
	return name, slug, nil
}

func cleanDescription(raw string) (string, error) {
	d := strings.TrimSpace(raw)
	if utf8.RuneCountInString(d) > maxDescriptionRunes {
		return "", fmt.Errorf("%w: a team's description is at most %d characters", ErrInvalid, maxDescriptionRunes)
	}
	return d, nil
}
