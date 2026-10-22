package ctl

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
)

func cloudFlag(name, description string) cli.Flag {
	return cli.NewStringFlag(name, "", description, "")
}

// cloudTags splits a comma-separated flag; the server validates each tag.
func cloudTags(value string) []string {
	tags := []string{}
	for _, tag := range strings.Split(value, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}
func cloudRevisionFlag() cli.Flag {
	return cloudFlag("revision", "Expected target, Cloud or assignment revision")
}
func cloudExecutionFlags() []cli.Flag {
	return append(recoveryFlags(), cli.NewBoolFlag("yes", "y", "Apply the displayed preview", false), cloudFlag("request-id", "Stable idempotency key to reuse after a lost response"))
}
func (a *App) cloudsCommand() cli.Command {
	return cli.NewCommand("clouds", "List the project's Clouds", a.listClouds, cli.WithUsage("clouds <project>"), cli.WithFlag(jsonFlag),
		cli.WithSubcommand(cli.NewCommand("create", "Create a Cloud", a.createCloud, cli.WithUsage("clouds create <project> --name production"), cli.WithFlags(jsonFlag, cloudFlag("name", "Cloud name")))),
		cli.WithSubcommand(cli.NewCommand("show", "Show a Cloud", a.showCloud, cli.WithUsage("clouds show <project> <cloud-id>"), cli.WithFlag(jsonFlag))),
		cli.WithSubcommand(cli.NewCommand("update", "Rename a Cloud at its expected revision", a.updateCloud, cli.WithUsage("clouds update <project> <cloud-id> --name name --revision 1"), cli.WithFlags(jsonFlag, cloudFlag("name", "Cloud name"), cloudRevisionFlag()))),
		cli.WithSubcommand(cli.NewCommand("delete", "Delete an unassigned Cloud", a.deleteCloud, cli.WithUsage("clouds delete <project> <cloud-id>"), cli.WithFlag(jsonFlag))),
		cli.WithSubcommand(cli.NewCommand("assignment", "Show an environment's Cloud and assignment generation", a.cloudAssignment, cli.WithUsage("clouds assignment <project> <environment-id>"), cli.WithFlag(jsonFlag))),
		cli.WithSubcommand(cli.NewCommand("assign", "Review an environment assignment, then apply with --yes", a.assignCloud, cli.WithUsage("clouds assign <project> <environment-id> --cloud cloud-id --revision 0 [--yes --wait]"), cli.WithFlags(append(cloudExecutionFlags(), cloudFlag("cloud", "Cloud ID; empty to unassign"), cloudRevisionFlag())...))),
		cli.WithSubcommand(cli.NewCommand("switch-default", "Review a change to a Cloud's default provider and account, then apply with --yes", a.switchCloudDefault, cli.WithUsage("clouds switch-default <project> <cloud-id> --provider vercel --connection conn-id --revision 3 [--clear] [--yes --wait]"), cli.WithFlags(append(cloudExecutionFlags(), cloudFlag("provider", "Destination provider"), cloudFlag("connection", "Destination connection ID"), cloudFlag("region", "Destination region"), cli.NewBoolFlag("clear", "", "Remove the default", false), cloudRevisionFlag())...))))
}
func (a *App) cloudTargetsCommand() cli.Command {
	return cli.NewCommand("cloud-targets", "List the targets in a Cloud", a.listCloudTargets, cli.WithUsage("cloud-targets <project> <cloud-id>"), cli.WithFlag(jsonFlag),
		cli.WithSubcommand(cli.NewCommand("show", "Show a target's desired placement", a.showCloudTarget, cli.WithUsage("cloud-targets show <project> <cloud-id> <target-id>"), cli.WithFlag(jsonFlag))),
		cli.WithSubcommand(cli.NewCommand("create", "Create a named deployment target", a.createCloudTarget, cli.WithUsage("cloud-targets create <project> <cloud-id> --name frontend --provider vercel --connection conn-id"), cli.WithFlags(jsonFlag, cloudFlag("name", "Immutable lowercase target slug"), cloudFlag("display-name", "Display label"), cloudFlag("provider", "Provider name"), cloudFlag("connection", "Verified connection ID"), cloudFlag("region", "Deployment region"), cloudFlag("tags", "Comma-separated tags, such as eu,gpu")))),
		cli.WithSubcommand(cli.NewCommand("update", "Change a target's display name", a.updateCloudTarget, cli.WithUsage("cloud-targets update <project> <cloud-id> <target-id> --display-name Frontend --revision 1 [--tags eu,gpu | --clear-tags]"), cli.WithFlags(jsonFlag, cloudFlag("display-name", "Display label"), cloudFlag("tags", "Replace the tags, comma-separated"), cli.NewBoolFlag("clear-tags", "", "Remove every tag", false), cloudRevisionFlag()))),
		cli.WithSubcommand(cli.NewCommand("delete", "Delete an unused target", a.deleteCloudTarget, cli.WithUsage("cloud-targets delete <project> <cloud-id> <target-id>"), cli.WithFlag(jsonFlag))),
		cli.WithSubcommand(cli.NewCommand("switch", "Review an account or provider switch, then apply with --yes", a.switchCloudTarget, cli.WithUsage("cloud-targets switch <project> <cloud-id> <target-id> --provider vercel --connection conn-id --revision 1 [--yes --wait]"), cli.WithFlags(append(cloudExecutionFlags(), cloudFlag("provider", "Destination provider"), cloudFlag("connection", "Destination connection ID"), cloudFlag("region", "Destination region"), cloudRevisionFlag())...))))
}
func (a *App) cloudSyncsCommand() cli.Command {
	return cli.NewCommand("cloud-syncs", "List persisted Cloud operations", a.listCloudSyncs, cli.WithUsage("cloud-syncs <project> [--before cursor]"), cli.WithFlags(jsonFlag, cloudFlag("before", "Next cursor from a previous page")),
		cli.WithSubcommand(cli.NewCommand("show", "Show persisted deployment and routing progress", a.showCloudSync, cli.WithUsage("cloud-syncs show <project> <sync-id> [--wait]"), cli.WithFlags(recoveryFlags()...))),
		cli.WithSubcommand(cli.NewCommand("wait", "Wait within a bounded timeout", a.waitCloudSync, cli.WithUsage("cloud-syncs wait <project> <sync-id> [--timeout 45m]"), cli.WithFlags(recoveryFlags()...))),
		cli.WithSubcommand(cli.NewCommand("retry", "Retry failed or pending cleanup work", a.retryCloudSync, cli.WithUsage("cloud-syncs retry <project> <sync-id> [--wait]"), cli.WithFlags(recoveryFlags()...))),
		cli.WithSubcommand(cli.NewCommand("revert", "Review compensation, then apply with --yes", a.revertCloudSync, cli.WithUsage("cloud-syncs revert <project> <sync-id> [--yes --wait]"), cli.WithFlags(cloudExecutionFlags()...))),
		cli.WithSubcommand(cli.NewCommand("routing", "Record completed external routing for one environment", a.completeCloudRouting, cli.WithUsage("cloud-syncs routing <project> <sync-id> --environment env-id --evidence 'DNS verified' [--wait]"), cli.WithFlags(append(recoveryFlags(), cloudFlag("environment", "Environment ID"), cloudFlag("evidence", "How you verified external routing"))...))),
		cli.WithSubcommand(cli.NewCommand("apply", "Accept a saved preview and revision", a.applyCloudPreview, cli.WithUsage("cloud-syncs apply <project> --preview preview-id --revision digest --request-id stable-key [--yes --wait]"), cli.WithFlags(append(cloudExecutionFlags(), cloudFlag("preview", "Immutable preview ID"), cloudRevisionFlag())...))))
}
func (a *App) cloudClient(ctx cli.CommandContext, args int) (*client.Client, *client.Project, error) {
	for i := 0; i < args; i++ {
		if ctx.Arg(i) == "" {
			return nil, nil, errors.New("give the project and required resource IDs")
		}
	}
	c, err := a.client()
	if err != nil {
		return nil, nil, err
	}
	p, err := a.project(ctx, c)
	return c, p, err
}
func cloudRevision(ctx cli.CommandContext) (int64, error) {
	value, err := strconv.ParseInt(ctx.String("revision"), 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("give --revision with the current nonnegative revision")
	}
	return value, nil
}
func (a *App) listClouds(ctx cli.CommandContext) error {
	c, p, err := a.cloudClient(ctx, 1)
	if err != nil {
		return err
	}
	v, err := c.Clouds(ctxOf(ctx), p.ID)
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) createCloud(ctx cli.CommandContext) error {
	c, p, err := a.cloudClient(ctx, 1)
	if err != nil {
		return err
	}
	v, err := c.CreateCloud(ctxOf(ctx), p.ID, client.CloudCreateInput{Name: ctx.String("name")})
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) showCloud(ctx cli.CommandContext) error {
	c, p, err := a.cloudClient(ctx, 2)
	if err != nil {
		return err
	}
	v, err := c.Cloud(ctxOf(ctx), p.ID, ctx.Arg(1))
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) updateCloud(ctx cli.CommandContext) error {
	revision, err := cloudRevision(ctx)
	if err != nil {
		return err
	}
	c, p, err := a.cloudClient(ctx, 2)
	if err != nil {
		return err
	}
	v, err := c.UpdateCloud(ctxOf(ctx), p.ID, ctx.Arg(1), client.CloudUpdateInput{Name: ctx.String("name"), Revision: revision})
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) deleteCloud(ctx cli.CommandContext) error {
	c, p, err := a.cloudClient(ctx, 2)
	if err != nil {
		return err
	}
	return c.DeleteCloud(ctxOf(ctx), p.ID, ctx.Arg(1))
}
func (a *App) listCloudTargets(ctx cli.CommandContext) error {
	c, p, err := a.cloudClient(ctx, 2)
	if err != nil {
		return err
	}
	v, err := c.CloudTargets(ctxOf(ctx), p.ID, ctx.Arg(1))
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) showCloudTarget(ctx cli.CommandContext) error {
	c, p, err := a.cloudClient(ctx, 3)
	if err != nil {
		return err
	}
	v, err := c.CloudTarget(ctxOf(ctx), p.ID, ctx.Arg(1), ctx.Arg(2))
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) createCloudTarget(ctx cli.CommandContext) error {
	c, p, err := a.cloudClient(ctx, 2)
	if err != nil {
		return err
	}
	v, err := c.CreateCloudTarget(ctxOf(ctx), p.ID, ctx.Arg(1), client.CloudCreateTargetInput{Name: ctx.String("name"), DisplayName: ctx.String("display-name"), Provider: ctx.String("provider"), ConnectionID: ctx.String("connection"), Region: ctx.String("region"), Tags: cloudTags(ctx.String("tags"))})
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) updateCloudTarget(ctx cli.CommandContext) error {
	revision, err := cloudRevision(ctx)
	if err != nil {
		return err
	}
	c, p, err := a.cloudClient(ctx, 3)
	if err != nil {
		return err
	}
	in := client.CloudUpdateTargetInput{DisplayName: ctx.String("display-name"), Revision: revision}
	if tags := cloudTags(ctx.String("tags")); len(tags) > 0 || ctx.Bool("clear-tags") {
		if len(tags) > 0 && ctx.Bool("clear-tags") {
			return fmt.Errorf("use --tags or --clear-tags, not both")
		}
		in.Tags = &tags
	}
	v, err := c.UpdateCloudTarget(ctxOf(ctx), p.ID, ctx.Arg(1), ctx.Arg(2), in)
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) deleteCloudTarget(ctx cli.CommandContext) error {
	c, p, err := a.cloudClient(ctx, 3)
	if err != nil {
		return err
	}
	return c.DeleteCloudTarget(ctxOf(ctx), p.ID, ctx.Arg(1), ctx.Arg(2))
}
func (a *App) cloudAssignment(ctx cli.CommandContext) error {
	c, p, err := a.cloudClient(ctx, 2)
	if err != nil {
		return err
	}
	v, err := c.EnvironmentCloud(ctxOf(ctx), p.ID, ctx.Arg(1))
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) assignCloud(ctx cli.CommandContext) error {
	revision, err := cloudRevision(ctx)
	if err != nil {
		return err
	}
	c, p, err := a.cloudClient(ctx, 2)
	if err != nil {
		return err
	}
	preview, err := c.PreviewEnvironmentCloud(ctxOf(ctx), p.ID, ctx.Arg(1), client.EnvironmentCloudInput{CloudID: ctx.String("cloud"), Revision: revision})
	if err != nil {
		return err
	}
	return a.acceptCloudPreview(ctx, c, p.ID, preview, false)
}
func (a *App) switchCloudTarget(ctx cli.CommandContext) error {
	revision, err := cloudRevision(ctx)
	if err != nil {
		return err
	}
	c, p, err := a.cloudClient(ctx, 3)
	if err != nil {
		return err
	}
	preview, err := c.PreviewCloudChange(ctxOf(ctx), p.ID, client.CloudChangeInput{CloudID: ctx.Arg(1), TargetID: ctx.Arg(2), Provider: ctx.String("provider"), ConnectionID: ctx.String("connection"), Region: ctx.String("region"), Revision: revision})
	if err != nil {
		return err
	}
	return a.acceptCloudPreview(ctx, c, p.ID, preview, false)
}
func (a *App) switchCloudDefault(ctx cli.CommandContext) error {
	revision, err := cloudRevision(ctx)
	if err != nil {
		return err
	}
	in := client.CloudChangeInput{CloudID: ctx.Arg(1), Default: true, Provider: ctx.String("provider"), ConnectionID: ctx.String("connection"), Region: ctx.String("region"), Revision: revision}
	if ctx.Bool("clear") == (in.Provider != "") {
		return fmt.Errorf("name the new --provider and --connection, or pass --clear")
	}
	c, p, err := a.cloudClient(ctx, 2)
	if err != nil {
		return err
	}
	preview, err := c.PreviewCloudChange(ctxOf(ctx), p.ID, in)
	if err != nil {
		return err
	}
	return a.acceptCloudPreview(ctx, c, p.ID, preview, false)
}
func (a *App) listCloudSyncs(ctx cli.CommandContext) error {
	c, p, err := a.cloudClient(ctx, 1)
	if err != nil {
		return err
	}
	v, err := c.CloudSyncs(ctxOf(ctx), p.ID, ctx.String("before"), 30)
	if err != nil {
		return err
	}
	return a.json(v)
}
func (a *App) showCloudSync(ctx cli.CommandContext) error { return a.getCloudSync(ctx, false) }
func (a *App) waitCloudSync(ctx cli.CommandContext) error { return a.getCloudSync(ctx, true) }
func (a *App) getCloudSync(ctx cli.CommandContext, forceWait bool) error {
	c, p, err := a.cloudClient(ctx, 2)
	if err != nil {
		return err
	}
	if forceWait || ctx.Bool("wait") {
		return a.cloudSyncResult(ctx, c, p.ID, &client.CloudSync{ID: ctx.Arg(1)}, true)
	}
	v, err := c.CloudSync(ctxOf(ctx), p.ID, ctx.Arg(1))
	if err != nil {
		return err
	}
	return a.cloudSyncResult(ctx, c, p.ID, v, forceWait)
}
func (a *App) retryCloudSync(ctx cli.CommandContext) error {
	c, p, err := a.cloudClient(ctx, 2)
	if err != nil {
		return err
	}
	v, err := c.RetryCloudSync(ctxOf(ctx), p.ID, ctx.Arg(1))
	if err != nil {
		return err
	}
	return a.cloudSyncResult(ctx, c, p.ID, v, false)
}
func (a *App) revertCloudSync(ctx cli.CommandContext) error {
	c, p, err := a.cloudClient(ctx, 2)
	if err != nil {
		return err
	}
	v, err := c.PreviewCloudRevert(ctxOf(ctx), p.ID, ctx.Arg(1))
	if err != nil {
		return err
	}
	return a.acceptCloudPreview(ctx, c, p.ID, v, true)
}
func (a *App) completeCloudRouting(ctx cli.CommandContext) error {
	c, p, err := a.cloudClient(ctx, 2)
	if err != nil {
		return err
	}
	v, err := c.CompleteCloudRouting(ctxOf(ctx), p.ID, ctx.Arg(1), client.CompleteCloudRoutingInput{EnvironmentID: ctx.String("environment"), Evidence: ctx.String("evidence")})
	if err != nil {
		return err
	}
	return a.cloudSyncResult(ctx, c, p.ID, v, false)
}
func (a *App) applyCloudPreview(ctx cli.CommandContext) error {
	c, p, err := a.cloudClient(ctx, 1)
	if err != nil {
		return err
	}
	preview, err := c.CloudPreview(ctxOf(ctx), p.ID, ctx.String("preview"))
	if err != nil {
		return err
	}
	if ctx.String("revision") != preview.Revision {
		return errors.New("preview revision differs; review it again")
	}
	return a.acceptCloudPreview(ctx, c, p.ID, preview, preview.Reverts != "")
}
func (a *App) acceptCloudPreview(ctx cli.CommandContext, c *client.Client, project string, preview *client.CloudPreview, revert bool) error {
	if err := a.json(preview); err != nil {
		return err
	}
	if len(preview.Blockers) > 0 {
		return errors.New("cloud change is blocked; resolve the displayed blockers")
	}
	if !ctx.Bool("yes") {
		return nil
	}
	key := requestID(ctx)
	in := client.ApplyCloudInput{PreviewID: preview.ID, Revision: preview.Revision, IdempotencyKey: key}
	var result *client.CloudSync
	var err error
	if revert {
		result, err = c.ApplyCloudRevert(ctxOf(ctx), project, in)
	} else {
		result, err = c.ApplyCloudChange(ctxOf(ctx), project, in)
	}
	if err != nil {
		return fmt.Errorf("cloud request %s: %w", key, err)
	}
	return a.cloudSyncResult(ctx, c, project, result, false)
}
func (a *App) cloudSyncResult(ctx cli.CommandContext, c *client.Client, project string, sync *client.CloudSync, forceWait bool) error {
	follow := forceWait || ctx.Bool("wait")
	if follow {
		bounded, cancel := context.WithTimeout(ctxOf(ctx), time.Duration(ctx.Duration("timeout")))
		defer cancel()
		latest, err := c.WaitCloudSync(bounded, project, sync.ID)
		if latest != nil {
			sync = latest
		}
		if err != nil {
			return errors.Join(fmt.Errorf("follow Cloud sync %s with cloud-syncs show %s %s: %w", sync.ID, project, sync.ID, err), a.json(sync))
		}
	}
	if err := a.json(sync); err != nil {
		return err
	}
	if follow && sync.Status != "succeeded" && sync.Status != "reverted" {
		return fmt.Errorf("cloud sync %s is %s: %s", sync.ID, sync.Status, sync.Error)
	}
	return nil
}
