package teams

import (
	"context"
	"fmt"
	"strings"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/notifications"
)

// AdminsName is the team name a Route carries when the workspace admins
// hear about a request.
const AdminsName = "Workspace admins"

// Route says who hears about an agent request.
type Route struct {
	TeamID  string   // "" when the admins get it
	Team    string   // the team's name, or AdminsName
	Source  string   // "named", "project" or "admins"
	Reason  string   // the agent's reason; only when Source is "named"
	Note    string   // why a named or responsible team was passed over, e.g. "Payments has no members"
	Members []string // user IDs to notify, current members only, sorted
}

// routeTeam is a team with its current members.
type routeTeam struct {
	id, name string
	members  []string
}

// Resolve picks who hears about a request. named is a team slug or ID from
// the agent, reason its reason. The order is the named team, the project's
// responsible team, then the workspace admins; a named or responsible team
// with no current member is passed over and says so in Note. It reads
// through q, so the caller's transaction sees its own writes, and it never
// looks outside orgID. It errors only when the database does.
func (s *Service) Resolve(ctx context.Context, q notifications.Querier, orgID, projectID, named, reason string) (Route, error) {
	var notes []string
	addNote := func(n string) {
		if !contains(notes, n) {
			notes = append(notes, n)
		}
	}

	if named != "" {
		t, err := routeTeamBy(ctx, q, orgID, `(t.slug = $2 OR t.id = $2)`, named)
		if err != nil {
			return Route{}, err
		}
		switch {
		case t == nil:
			addNote(fmt.Sprintf("No team named %q in this workspace", named))
		case len(t.members) == 0:
			addNote(t.name + " has no members")
		default:
			return Route{TeamID: t.id, Team: t.name, Source: "named", Reason: reason, Note: strings.Join(notes, "; "), Members: t.members}, nil
		}
	}

	if projectID != "" {
		t, err := routeTeamBy(ctx, q, orgID,
			`t.id = (SELECT responsible_team_id FROM loomplane_projects WHERE id = $2 AND org_id = $1)`, projectID)
		if err != nil {
			return Route{}, err
		}
		switch {
		case t == nil:
		case len(t.members) == 0:
			addNote(t.name + " has no members")
		default:
			return Route{TeamID: t.id, Team: t.name, Source: "project", Note: strings.Join(notes, "; "), Members: t.members}, nil
		}
	}

	admins, err := notifications.Admins(ctx, q, orgID)
	if err != nil {
		return Route{}, err
	}
	people := make([]string, 0, len(admins))
	for _, u := range admins {
		if u != "" && !strings.Contains(u, ":") {
			people = append(people, u)
		}
	}
	return Route{Team: AdminsName, Source: "admins", Note: strings.Join(notes, "; "), Members: people}, nil
}

// IsMember reports whether userID is a current member of teamID in orgID.
func (s *Service) IsMember(ctx context.Context, q notifications.Querier, orgID, teamID, userID string) (bool, error) {
	rows, err := q.Query(ctx, `SELECT 1 FROM loomplane_team_members tm
		JOIN loomplane_teams t ON t.id = tm.team_id AND t.org_id = tm.org_id
		`+currentMember+`
		WHERE tm.org_id = $1 AND tm.team_id = $2 AND tm.user_id = $3
		LIMIT 1`, orgID, teamID, userID)
	if err != nil {
		return false, fmt.Errorf("check team member: %w", err)
	}
	defer func() { _ = rows.Close() }()
	found := rows.Next()
	return found, rows.Err()
}

// routeTeamBy finds the one team in orgID that cond (over alias t, with
// $1 the org and $2 arg) selects, with its current members. A nil team
// means none matched. Members are people only: an ID with a colon is
// Loomplane automation or the agent, never someone to notify.
func routeTeamBy(ctx context.Context, q notifications.Querier, orgID, cond, arg string) (*routeTeam, error) {
	var t *routeTeam
	rows, err := q.Query(ctx, `SELECT t.id, t.name, COALESCE(tm.user_id, '')
		FROM loomplane_teams t
		LEFT JOIN loomplane_team_members tm ON tm.team_id = t.id AND tm.org_id = t.org_id
			AND tm.user_id <> '' AND position(':' IN tm.user_id) = 0
			AND EXISTS (SELECT 1 FROM authsome_members am
				WHERE am.id = tm.member_id AND am.org_id = tm.org_id AND am.user_id = tm.user_id)
		WHERE t.org_id = $1 AND `+cond+`
		ORDER BY t.id, tm.user_id`, orgID, arg)
	err = scanRouteTeam(rows, err, &t)
	if err != nil {
		return nil, fmt.Errorf("resolve team: %w", err)
	}
	return t, nil
}

// scanRouteTeam reads the rows of one team. Two teams can match a named
// value only if one's slug is another's ID, which typeids prevent; the
// first by ID wins.
func scanRouteTeam(rows driver.Rows, err error, out **routeTeam) error {
	return each(rows, err, func(r driver.Rows) error {
		var id, name, user string
		if err := r.Scan(&id, &name, &user); err != nil {
			return err
		}
		if *out == nil {
			*out = &routeTeam{id: id, name: name, members: []string{}}
		}
		if (*out).id != id {
			return nil
		}
		if user != "" {
			(*out).members = append((*out).members, user)
		}
		return nil
	})
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
