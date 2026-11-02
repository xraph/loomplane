package ctl

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
)

func (a *App) teamsCommand() cli.Command {
	return cli.NewCommand("teams", "List the workspace's teams, their members and roles", a.teams,
		cli.WithUsage("teams [--json]"), cli.WithFlag(jsonFlag),
		cli.WithSubcommand(cli.NewCommand("show", "Show a team: its members and grants", a.showTeam,
			cli.WithUsage("teams show <team> [--json]"), cli.WithFlag(jsonFlag))),
		cli.WithSubcommand(cli.NewCommand("create", "Create a team", a.createTeam,
			cli.WithUsage("teams create <name> [--description ...]"),
			cli.WithFlags(cli.NewStringFlag("description", "", "What the team is for; the agent reads it to pick a team", ""), jsonFlag))),
		cli.WithSubcommand(cli.NewCommand("delete", "Delete a team; its members lose the roles it gave", a.deleteTeam,
			cli.WithUsage("teams delete <team> --yes"),
			cli.WithFlag(cli.NewBoolFlag("yes", "y", "Confirm the deletion", false)))),
		cli.WithSubcommand(cli.NewCommand("add", "Put a workspace member in a team", a.addTeamMember,
			cli.WithUsage("teams add <team> <email>"))),
		cli.WithSubcommand(cli.NewCommand("remove", "Take a person out of a team", a.removeTeamMember,
			cli.WithUsage("teams remove <team> <email>"))),
		cli.WithSubcommand(cli.NewCommand("grant", "Give a team a role on a project, or on the whole workspace", a.grantTeam,
			cli.WithUsage("teams grant <team> <viewer|developer|admin> [--project <project>]"),
			cli.WithFlag(cli.NewStringFlag("project", "p", "A project, by slug, name or ID; the whole workspace if empty", "")))),
		cli.WithSubcommand(cli.NewCommand("revoke", "Take a team's role away on a project, or on the whole workspace", a.revokeTeam,
			cli.WithUsage("teams revoke <team> [--project <project>]"),
			cli.WithFlag(cli.NewStringFlag("project", "p", "A project, by slug, name or ID; the whole workspace if empty", "")))),
	)
}

// projectNames maps project IDs to slugs, for grant labels. A project it
// doesn't know keeps its ID.
func projectNames(ctx context.Context, c *client.Client, teams ...client.Team) (func(id string) string, error) {
	need := false
	for _, t := range teams {
		for _, g := range t.Grants {
			if g.ProjectID != "" {
				need = true
			}
		}
	}
	names := map[string]string{}
	if need {
		list, err := c.Projects(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range list {
			names[p.ID] = p.Slug
		}
	}
	return func(id string) string {
		if n, ok := names[id]; ok {
			return n
		}
		return id
	}, nil
}

func grantText(g client.TeamGrant, project func(string) string) string {
	if g.ProjectID == "" {
		return g.Role + " on the workspace"
	}
	return g.Role + " on " + project(g.ProjectID)
}

func grantsText(t client.Team, project func(string) string) string {
	parts := make([]string, 0, len(t.Grants))
	for _, g := range t.Grants {
		parts = append(parts, grantText(g, project))
	}
	return orDash(strings.Join(parts, ", "))
}

func (a *App) teams(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	list, err := c.Teams(ctxOf(ctx))
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		if list == nil {
			list = []client.Team{}
		}
		return a.json(list)
	}
	project, err := projectNames(ctxOf(ctx), c, list...)
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(list))
	for _, t := range list {
		rows = append(rows, []string{t.Name, t.Slug, fmt.Sprint(len(t.Members)), grantsText(t, project)})
	}
	a.table([]string{"NAME", "SLUG", "MEMBERS", "GRANTS"}, rows)
	return nil
}

func (a *App) team(ctx cli.CommandContext, c *client.Client) (*client.Team, error) {
	ref := ctx.Arg(0)
	if ref == "" {
		return nil, errors.New("name the team: its slug, name or ID")
	}
	return c.FindTeam(ctxOf(ctx), ref)
}

func (a *App) showTeam(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	t, err := a.team(ctx, c)
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(t)
	}
	project, err := projectNames(ctxOf(ctx), c, *t)
	if err != nil {
		return err
	}
	a.printf("%s (%s)\n", t.Name, t.Slug)
	if t.Description != "" {
		a.printf("%s\n", t.Description)
	}
	a.printf("\nGrants:\n")
	if len(t.Grants) == 0 {
		a.printf("  none\n")
	}
	for _, g := range t.Grants {
		a.printf("  %s\n", grantText(g, project))
	}
	a.printf("\nMembers:\n")
	if len(t.Members) == 0 {
		a.printf("  none\n")
	}
	for _, m := range t.Members {
		a.printf("  %s <%s>\n", orDash(m.Name), m.Email)
	}
	return nil
}

func (a *App) createTeam(ctx cli.CommandContext) error {
	name := strings.TrimSpace(strings.Join(ctx.Args(), " "))
	if name == "" {
		return errors.New("name the team")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	t, err := c.CreateTeam(ctxOf(ctx), name, ctx.String("description"))
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(t)
	}
	a.printf("Created %s (%s). Next: loomctl teams add %s <email>\n", t.Name, t.ID, t.Slug)
	return nil
}

func (a *App) deleteTeam(ctx cli.CommandContext) error {
	if !ctx.Bool("yes") {
		return errors.New("pass --yes to delete the team; its members lose the roles it gave")
	}
	if ctx.Arg(0) == "" {
		return errors.New("name the team: its slug, name or ID")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	t, err := a.team(ctx, c)
	if err != nil {
		return err
	}
	if derr := c.DeleteTeam(ctxOf(ctx), t.ID); derr != nil {
		return derr
	}
	a.printf("Deleted %s.\n", t.Name)
	return nil
}

// memberByEmail finds the workspace member with that email.
func memberByEmail(ctx context.Context, c *client.Client, email string) (*client.Member, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, errors.New("give the person's email")
	}
	members, err := c.Members(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		if strings.EqualFold(m.Email, email) {
			return &m, nil
		}
	}
	return nil, errors.New("nobody with that email is in this workspace")
}

func (a *App) addTeamMember(ctx cli.CommandContext) error {
	return a.changeTeamMember(ctx, true)
}

func (a *App) removeTeamMember(ctx cli.CommandContext) error {
	return a.changeTeamMember(ctx, false)
}

func (a *App) changeTeamMember(ctx cli.CommandContext, add bool) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	t, err := a.team(ctx, c)
	if err != nil {
		return err
	}
	m, err := memberByEmail(ctxOf(ctx), c, ctx.Arg(1))
	if err != nil {
		return err
	}
	if add {
		if _, err = c.AddTeamMember(ctxOf(ctx), t.ID, m.UserID); err != nil {
			return err
		}
		a.printf("Added %s to %s.\n", m.Email, t.Name)
		return nil
	}
	if _, err = c.RemoveTeamMember(ctxOf(ctx), t.ID, m.UserID); err != nil {
		return err
	}
	a.printf("Removed %s from %s.\n", m.Email, t.Name)
	return nil
}

// grantProject resolves --project to an ID; empty means the workspace.
func (a *App) grantProject(ctx cli.CommandContext, c *client.Client) (id, label string, err error) {
	ref := strings.TrimSpace(ctx.String("project"))
	if ref == "" {
		return "", "the workspace", nil
	}
	p, err := c.FindProject(ctxOf(ctx), ref)
	if err != nil {
		return "", "", err
	}
	return p.ID, p.Slug, nil
}

func (a *App) grantTeam(ctx cli.CommandContext) error {
	role := strings.ToLower(strings.TrimSpace(ctx.Arg(1)))
	switch role {
	case "viewer", "developer", "admin":
	default:
		return errors.New("give a role: viewer, developer or admin")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	t, err := a.team(ctx, c)
	if err != nil {
		return err
	}
	projectID, label, err := a.grantProject(ctx, c)
	if err != nil {
		return err
	}
	if _, err = c.SetTeamGrant(ctxOf(ctx), t.ID, role, projectID); err != nil {
		return err
	}
	a.printf("%s is now %s on %s.\n", t.Name, role, label)
	return nil
}

func (a *App) revokeTeam(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	t, err := a.team(ctx, c)
	if err != nil {
		return err
	}
	projectID, label, err := a.grantProject(ctx, c)
	if err != nil {
		return err
	}
	for _, g := range t.Grants {
		if g.ProjectID != projectID {
			continue
		}
		if _, err = c.RemoveTeamGrant(ctxOf(ctx), t.ID, g.ID); err != nil {
			return err
		}
		a.printf("%s no longer has a role on %s.\n", t.Name, label)
		return nil
	}
	return errors.New("the team has no grant there")
}

func (a *App) projectTeam(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	if ctx.Bool("none") {
		if ctx.Arg(1) != "" {
			return errors.New("give a team or --none, not both")
		}
		if _, err = c.SetProjectTeam(ctxOf(ctx), p.ID, nil); err != nil {
			return err
		}
		a.printf("%s has no responsible team now.\n", p.Name)
		return nil
	}
	if ctx.Arg(1) == "" {
		return errors.New("name the team, or pass --none to clear it")
	}
	t, err := c.FindTeam(ctxOf(ctx), ctx.Arg(1))
	if err != nil {
		return err
	}
	if _, err = c.SetProjectTeam(ctxOf(ctx), p.ID, &t.ID); err != nil {
		return err
	}
	a.printf("%s is now responsible for %s.\n", t.Name, p.Name)
	return nil
}
