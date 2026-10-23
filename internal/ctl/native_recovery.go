package ctl

import (
	"fmt"
	"strconv"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
)

func nativeRecoveryFlags() []cli.Flag {
	return []cli.Flag{jsonFlag, cli.NewStringFlag("before", "", "Previous response's next_before", ""), cli.NewStringFlag("limit", "", "Page size, from 1 to 100", "50")}
}
func nativeRecoveryScopeFlags() []cli.Flag {
	return append(nativeRecoveryFlags(), cli.NewStringFlag("environment-id", "", "Limit to an environment ID", ""), cli.NewStringFlag("resource", "", "Limit to a retained resource name", ""))
}

func (a *App) nativeRecoveryCommands() []cli.Command {
	return []cli.Command{
		cli.NewCommand("native-sources", "List captured cloud database histories", a.nativeSources, cli.WithUsage("backups native-sources <project> [--environment-id ID] [--resource NAME] [--before ...] [--limit 50] [--json]"), cli.WithFlags(nativeRecoveryScopeFlags()...)),
		cli.NewCommand("native-inventory", "Read saved native snapshot observations", a.nativeInventory, cli.WithUsage("backups native-inventory <source> [--before ...] [--limit 50] [--json]"), cli.WithFlags(nativeRecoveryFlags()...)),
		cli.NewCommand("native-refresh", "Refresh snapshots from the original cloud account", a.nativeRefresh, cli.WithUsage("backups native-refresh <source> [--json]"), cli.WithFlag(jsonFlag)),
	}
}
func nativeRecoveryLimit(ctx cli.CommandContext) (int, error) {
	limit, err := strconv.Atoi(ctx.String("limit"))
	if err != nil || limit < 1 || limit > 100 {
		return 0, fmt.Errorf("limit must be from 1 to 100")
	}
	return limit, nil
}
func (a *App) nativeSources(ctx cli.CommandContext) error {
	limit, err := nativeRecoveryLimit(ctx)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	project, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	page, err := c.NativeRecoverySourcesPage(ctxOf(ctx), project.ID, client.NativeRecoveryPage{EnvironmentID: ctx.String("environment-id"), Resource: ctx.String("resource"), Before: ctx.String("before"), Limit: limit})
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(page)
	}
	rows := [][]string{}
	for _, source := range page.Sources {
		rows = append(rows, []string{source.ID, source.Name, source.Provider, source.Region, source.PhysicalID, source.State, strconv.FormatBool(source.Complete)})
	}
	a.table([]string{"SOURCE", "RESOURCE", "PROVIDER", "REGION", "PHYSICAL ID", "STATE", "COMPLETE"}, rows)
	if page.NextBefore != "" {
		a.printf("Next page: --before %s\n", page.NextBefore)
	}
	return nil
}
func (a *App) nativeInventory(ctx cli.CommandContext) error {
	limit, err := nativeRecoveryLimit(ctx)
	if err != nil {
		return err
	}
	id := ctx.Arg(0)
	if id == "" {
		return fmt.Errorf("give a source ID")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	page, err := c.NativeRecoveryInventory(ctxOf(ctx), id, ctx.String("before"), limit)
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(page)
	}
	a.printf("Observation: %s; complete: %t. Snapshot restore is not verified by inventory.\n", page.Source.State, page.Source.Complete)
	rows := [][]string{}
	for _, point := range page.Points {
		expiry := "not reported"
		if point.ExpiresAt != nil {
			expiry = point.ExpiresAt.UTC().Format("2006-01-02 15:04Z")
		}
		rows = append(rows, []string{point.ID, point.Name, point.Kind, point.Status, point.CreatedAt.Format("2006-01-02 15:04Z"), expiry, strconv.FormatBool(point.Present)})
	}
	a.table([]string{"ID", "SNAPSHOT", "KIND", "STATUS", "CREATED", "PROVIDER EXPIRY", "LAST SEEN PRESENT"}, rows)
	if page.NextBefore != "" {
		a.printf("Next page: --before %s\n", page.NextBefore)
	}
	return nil
}
func (a *App) nativeRefresh(ctx cli.CommandContext) error {
	id := ctx.Arg(0)
	if id == "" {
		return fmt.Errorf("give a source ID")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	page, err := c.RefreshNativeRecovery(ctxOf(ctx), id)
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(page)
	}
	a.printf("Refreshed %s: complete %t. Saved observations are available with backups native-inventory %s.\n", page.Source.Name, page.Source.Complete, id)
	return nil
}
