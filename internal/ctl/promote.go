package ctl

import (
	"errors"
	"fmt"
	"strings"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
)

func (a *App) promoteCommand() cli.Command {
	return cli.NewCommand("promote", "Plan an environment to run what another one runs, reusing its images where it can", a.promote,
		cli.WithUsage("promote <project> [--from staging] [--to production] [--scope web]"),
		cli.WithFlags(
			cli.NewStringFlag("from", "", "The environment to promote from", "staging"),
			cli.NewStringFlag("to", "", "The environment to promote to", "production"),
			cli.NewStringSliceFlag("scope", "", "A repository to promote, by alias; every one if none, and the rest stay as deployed", nil),
			cli.NewBoolFlag("no-wait", "", "Start planning and return", false), jsonFlag))
}

func (a *App) rollbackCommand() cli.Command {
	return cli.NewCommand("rollback", "Plan an environment back to its previous successful release; data-bearing resources stay as they are", a.rollback,
		cli.WithUsage(`rollback <project> --env production --reason "..." [--deployment dep_x]`),
		cli.WithFlags(
			cli.NewStringFlag("env", "e", "The environment", "production"),
			cli.NewStringFlag("reason", "", "Why, for the audit trail and whoever approves it", ""),
			cli.NewStringFlag("deployment", "", "The deployment to undo; it must be the environment's newest, which is the default", ""),
			cli.NewBoolFlag("no-wait", "", "Start planning and return", false), jsonFlag))
}

// promote plans the --to environment from what the --from environment
// runs. A person approves the plan, as with any other.
func (a *App) promote(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	plan, err := c.CreatePlan(ctxOf(ctx), p.ID, client.NewPlan{Environment: ctx.String("to"), From: ctx.String("from"), Scope: ctx.StringSlice("scope")})
	if err != nil {
		return err
	}
	return a.finishPlan(ctx, c, plan)
}

// rollback plans an environment back to its previous release, after
// saying which release it undoes and which it restores. A person
// approves the plan, as with any other.
func (a *App) rollback(ctx cli.CommandContext) error {
	reason := strings.TrimSpace(ctx.String("reason"))
	if reason == "" {
		return errors.New(`say why you're rolling back with --reason "..."; it goes in the audit trail and to whoever approves it`)
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	env, deployment := ctx.String("env"), ctx.String("deployment")
	target, err := c.RollbackTarget(ctxOf(ctx), p.ID, env, deployment)
	if err != nil {
		return err
	}
	if !ctx.Bool("json") {
		a.printf("Rolls %s back from %s to %s\n", target.Environment, target.Target.DeploymentID, target.Restore.DeploymentID)
		for _, cm := range target.Commits {
			if cm.From != cm.To {
				a.printf("  %s: %s to %s\n", cm.Alias, orDash(shortSHA(cm.From)), shortSHA(cm.To))
			}
		}
		a.printf("\n")
	}
	plan, err := c.CreateRollback(ctxOf(ctx), p.ID, client.NewRollback{Environment: env, Reason: reason, DeploymentID: deployment})
	if err != nil {
		return err
	}
	return a.finishPlan(ctx, c, plan)
}

// finishPlan waits for a plan unless --no-wait, and prints it.
func (a *App) finishPlan(ctx cli.CommandContext, c *client.Client, plan *client.Plan) error {
	var err error
	if !ctx.Bool("no-wait") {
		if plan, err = c.WaitPlan(ctxOf(ctx), plan.ID); err != nil {
			return err
		}
	}
	if ctx.Bool("json") {
		return a.json(plan)
	}
	a.printPlan(plan)
	return nil
}

// shortSHA is a commit at the seven characters Plan.Commits shows.
func shortSHA(sha string) string { return sha[:min(7, len(sha))] }

// kindLine says what a promotion or a rollback plan moves, or "" for a
// release.
func kindLine(p *client.Plan) string {
	switch {
	case p.Kind == "promotion":
		line := fmt.Sprintf("Promotes %s to %s", p.From, p.Environment)
		if commits := p.Commits(); commits != "" {
			line += ": " + commits
		}
		return line
	case p.Kind == "rollback" && p.Rollback != nil:
		line := fmt.Sprintf("Rollback of %s, restoring %s", p.Rollback.TargetDeploymentID, p.Rollback.RestoreDeploymentID)
		if p.Rollback.Trigger != "" && p.Rollback.Trigger != "manual" {
			line += ", started by " + client.AutomationName
		}
		return line + ": " + p.Rollback.Reason
	}
	return ""
}

// artifactLine says whether a service reuses an image or builds again.
func artifactLine(x client.ArtifactChoice) string {
	switch {
	case x.Reused && x.Reference == "" && x.Revision != "":
		return fmt.Sprintf("%s: returns to its earlier deployment %s", x.Service, x.Revision)
	case x.Reused && x.From != "":
		return fmt.Sprintf("%s: reuses %s's image", x.Service, x.From)
	case x.Reused:
		return x.Service + ": reuses an earlier image"
	case x.Reason != "":
		return fmt.Sprintf("%s: builds again (%s)", x.Service, x.Reason)
	}
	return x.Service + ": builds again"
}

// leftoverLine says what a rollback leaves as the release it undoes made
// it, and why.
func leftoverLine(l client.Leftover) string {
	by := map[string]string{
		"delete":  "created by the release",
		"create":  "removed by the release",
		"update":  "changed by the release",
		"replace": "replaced by the release",
	}[l.Op]
	if by == "" {
		by = "left by the release"
	}
	line := fmt.Sprintf("Left in place: %s (%s, %s)", l.Name, l.Subject, by)
	if l.Reason != "" {
		line += "\n  " + l.Reason
	}
	return line
}
