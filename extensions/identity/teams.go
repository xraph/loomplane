package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/pglock"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/teams"
)

// TeamList is the body of GET /api/v1/teams: every team in the workspace,
// with its members and grants.
type TeamList struct {
	Teams []teams.Team `json:"teams"`
}

// ForgeEnvelope tells the generated client the list wraps Teams.
func (TeamList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "teams"}
}

// syncLockPoolSize is the sync lock pool's size. It matches the
// authorizer's bound on syncs that hold a lock at once, so a lock never
// waits for a connection of its own.
const syncLockPoolSize = 4

// wireTeams builds the teams service and hands the authorizer what Sync
// needs: the people's team grants and the per-person lock.
//
// The lock has a pool of its own. A sync holds the person's advisory lock
// on one connection while it writes Warden's rows through another, so on
// one shared pool a burst of syncs could take every connection with locks
// and leave none for the writes. Here the writes always have the main
// pool, whatever database.pool_size is. Nothing here talks to the
// database: the pool connects on first use.
func (e *Extension) wireTeams() error {
	e.teams = teams.NewService(teams.Deps{
		DB:      e.pg,
		Authz:   e.authz,
		Members: e.membership,
		Audit:   teamAuditor{chronicle: e.audit, appID: e.cfg.Identity.AppID, log: e.Logger()},
		Log: func(msg string, err error) {
			e.Logger().Error(msg, forge.F("error", err.Error()))
		},
	})
	e.authz.SetTeamGrants(e.teams.Grants)
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	e.lockPG = pgdriver.New()
	if err := e.lockPG.Open(ctx, e.cfg.Database.URL, driver.WithPoolSize(syncLockPoolSize)); err != nil {
		return fmt.Errorf("open the sync lock pool: %w", err)
	}
	e.authz.SetSyncLocks(security.LockFunc(pglock.For(e.lockPG)), security.TryLockFunc(pglock.TryFor(e.lockPG)))
	return nil
}

// teamRoutes serves Loomplane's teams. Everyone in the workspace reads
// them; changing one takes member:manage, which the service checks, along
// with the grant ceiling and the refusal of anything but a person.
func (e *Extension) teamRoutes(v1 forge.Router) error {
	type route struct {
		method, path string
		handler      forge.Handler
		opts         []forge.RouteOption
	}
	routes := []route{
		{http.MethodGet, "/teams", e.listTeams, []forge.RouteOption{
			forge.WithName("teams.list"), forge.WithSummary("The workspace's teams, with their members and grants"),
			forge.WithResponseSchema(http.StatusOK, "Teams", TeamList{}),
		}},
		{http.MethodGet, "/teams/:id", e.getTeam, []forge.RouteOption{
			forge.WithName("teams.get"), forge.WithSummary("One team"),
			forge.WithResponseSchema(http.StatusOK, "The team", teams.Team{}),
		}},
		{http.MethodPost, "/teams", e.createTeam, []forge.RouteOption{
			forge.WithName("teams.create"), forge.WithSummary("Create a team"),
			forge.WithRequestSchema(teams.CreateTeamInput{}),
			forge.WithResponseSchema(http.StatusCreated, "The new team", teams.Team{}),
		}},
		{http.MethodPatch, "/teams/:id", e.updateTeam, []forge.RouteOption{
			forge.WithName("teams.update"), forge.WithSummary("Rename a team or change its description"),
			forge.WithRequestSchema(teams.UpdateTeamInput{}),
			forge.WithResponseSchema(http.StatusOK, "The team", teams.Team{}),
		}},
		{http.MethodDelete, "/teams/:id", e.deleteTeam, []forge.RouteOption{
			forge.WithName("teams.delete"),
			forge.WithSummary("Delete a team. Its members lose the roles it gave them, and projects it was responsible for have none."),
			forge.WithNoContentResponse(),
			forge.WithInvalidates("Team[]"),
		}},
		{http.MethodPut, "/teams/:id/members/:user_id", e.addTeamMember, []forge.RouteOption{
			forge.WithName("teams.addMember"), forge.WithSummary("Put a workspace member in the team"),
			forge.WithResponseSchema(http.StatusOK, "The team", teams.Team{}),
		}},
		{http.MethodDelete, "/teams/:id/members/:user_id", e.removeTeamMember, []forge.RouteOption{
			forge.WithName("teams.removeMember"), forge.WithSummary("Take a person out of the team"),
			forge.WithResponseSchema(http.StatusOK, "The team", teams.Team{}),
		}},
		{http.MethodPut, "/teams/:id/grants", e.setTeamGrant, []forge.RouteOption{
			forge.WithName("teams.setGrant"),
			forge.WithSummary("Give the team a role on a project, or on the whole workspace without a project. Never above your own role."),
			forge.WithRequestSchema(teams.SetGrantInput{}),
			forge.WithResponseSchema(http.StatusOK, "The team", teams.Team{}),
		}},
		{http.MethodDelete, "/teams/:id/grants/:grant_id", e.removeTeamGrant, []forge.RouteOption{
			forge.WithName("teams.removeGrant"), forge.WithSummary("Take one of the team's roles away from its members"),
			forge.WithResponseSchema(http.StatusOK, "The team", teams.Team{}),
		}},
	}
	for _, rt := range routes {
		opts := append([]forge.RouteOption{forge.WithTags("Teams")}, rt.opts...)
		var err error
		switch rt.method {
		case http.MethodGet:
			err = v1.GET(rt.path, rt.handler, opts...)
		case http.MethodPost:
			err = v1.POST(rt.path, rt.handler, opts...)
		case http.MethodPatch:
			err = v1.PATCH(rt.path, rt.handler, opts...)
		case http.MethodPut:
			err = v1.PUT(rt.path, rt.handler, opts...)
		case http.MethodDelete:
			err = v1.DELETE(rt.path, rt.handler, opts...)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *Extension) listTeams(c forge.Context) error {
	p, err := principalOf(c)
	if err != nil {
		return err
	}
	list, err := e.teams.List(c.Request().Context(), p)
	if err != nil {
		return teamError(err)
	}
	return c.JSON(http.StatusOK, TeamList{Teams: list.Teams})
}

func (e *Extension) getTeam(c forge.Context) error {
	p, err := principalOf(c)
	if err != nil {
		return err
	}
	return teamReply(c, http.StatusOK)(e.teams.Get(c.Request().Context(), p, c.Param("id")))
}

func (e *Extension) createTeam(c forge.Context) error {
	p, err := principalOf(c)
	if err != nil {
		return err
	}
	var in teams.CreateTeamInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be a JSON team")
	}
	return teamReply(c, http.StatusCreated)(e.teams.Create(c.Request().Context(), p, in))
}

func (e *Extension) updateTeam(c forge.Context) error {
	p, err := principalOf(c)
	if err != nil {
		return err
	}
	var in teams.UpdateTeamInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be a JSON team")
	}
	return teamReply(c, http.StatusOK)(e.teams.Update(c.Request().Context(), p, c.Param("id"), in))
}

func (e *Extension) deleteTeam(c forge.Context) error {
	p, err := principalOf(c)
	if err != nil {
		return err
	}
	if err = e.teams.Delete(c.Request().Context(), p, c.Param("id")); err != nil {
		return teamError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (e *Extension) addTeamMember(c forge.Context) error {
	p, err := principalOf(c)
	if err != nil {
		return err
	}
	return teamReply(c, http.StatusOK)(e.teams.AddMember(c.Request().Context(), p, c.Param("id"), c.Param("user_id")))
}

func (e *Extension) removeTeamMember(c forge.Context) error {
	p, err := principalOf(c)
	if err != nil {
		return err
	}
	return teamReply(c, http.StatusOK)(e.teams.RemoveMember(c.Request().Context(), p, c.Param("id"), c.Param("user_id")))
}

func (e *Extension) setTeamGrant(c forge.Context) error {
	p, err := principalOf(c)
	if err != nil {
		return err
	}
	var in teams.SetGrantInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be a JSON grant")
	}
	return teamReply(c, http.StatusOK)(e.teams.SetGrant(c.Request().Context(), p, c.Param("id"), in))
}

func (e *Extension) removeTeamGrant(c forge.Context) error {
	p, err := principalOf(c)
	if err != nil {
		return err
	}
	return teamReply(c, http.StatusOK)(e.teams.RemoveGrant(c.Request().Context(), p, c.Param("id"), c.Param("grant_id")))
}

// teamReply answers with the team, or with the error's status.
func teamReply(c forge.Context, status int) func(*teams.Team, error) error {
	return func(t *teams.Team, err error) error {
		if err != nil {
			return teamError(err)
		}
		return c.JSON(status, t)
	}
}

// teamError maps a teams error to its HTTP response. Anything it doesn't
// know stays as it is, an internal error whose text the caller never sees.
func teamError(err error) error {
	switch {
	case errors.Is(err, teams.ErrNotFound), errors.Is(err, teams.ErrGrantNotFound), errors.Is(err, teams.ErrMemberNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, teams.ErrSlugTaken):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, teams.ErrInvalid), errors.Is(err, teams.ErrNotMember):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}
	if status := security.StatusFor(err); status != http.StatusInternalServerError {
		return forge.NewHTTPError(status, err.Error())
	}
	return err
}

// teamAuditor records team changes in Chronicle, in the organization's
// stream, the way an invitation is recorded: the team is the resource, the
// caller the user, and the member or grant goes in as meta. Deleting a
// team is a warning.
type teamAuditor struct {
	chronicle *chronicle.Chronicle
	appID     string
	log       forge.Logger
}

func (a teamAuditor) Record(ctx context.Context, ev teams.AuditEvent) {
	b := a.chronicle.Info(ctx, ev.Action, "team", ev.TeamID)
	if ev.Action == "team.delete" {
		b = a.chronicle.Warning(ctx, ev.Action, "team", ev.TeamID)
	}
	b = b.Category("members").AppID(a.appID).TenantID(ev.Principal.OrgID).UserID(ev.Principal.UserID).Outcome("success")
	for k, v := range ev.Attributes {
		b = b.Meta(k, v)
	}
	if err := b.Record(); err != nil {
		a.log.Error("record audit event", forge.F("action", ev.Action), forge.F("team_id", ev.TeamID), forge.F("error", err.Error()))
	}
}

// authsomeTeamsPath is authsome's team routes, inside its router.
var authsomeTeamsPath = regexp.MustCompile(`^` + authsomeAPIPath + `/orgs/[^/]+/teams(/.*)?$`)

// refuseAuthsomeTeams answers authsome's own team routes with a 404, on
// any method. A workspace has one kind of team, Loomplane's, and a team
// made through authsome would be one Loomplane ignores.
func refuseAuthsomeTeams(next forge.Handler) forge.Handler {
	return func(c forge.Context) error {
		if isAuthsomeTeamsPath(c.Request().URL) {
			return forge.NewHTTPError(http.StatusNotFound, "Loomplane's teams are at /api/v1/teams")
		}
		return next(c)
	}
}

// isAuthsomeTeamsPath reports whether any spelling of the request's path
// names authsome's team routes. bunrouter routes on the raw path when the
// request has one, so the raw and escaped paths are checked as they are:
// cleaning would fold "teams/%2e%2e" into the org, and decoding would
// split an org ID holding "%2F". The decoded path, and its cleaned form,
// are checked too, for "/%74eams", "//teams" and "/./teams".
func isAuthsomeTeamsPath(u *url.URL) bool {
	for _, p := range []string{u.RawPath, u.EscapedPath(), u.Path, path.Clean(u.Path)} {
		if p != "" && authsomeTeamsPath.MatchString(p) {
			return true
		}
	}
	return false
}
