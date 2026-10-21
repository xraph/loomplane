package ctl

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
)

func retirementRequestFlags() []cli.Flag {
	return append(recoveryFlags(), cli.NewBoolFlag("yes", "y", "Confirm permanent deletion of the selected backup", false), cli.NewStringFlag("request-id", "", "Stable key to reuse after a lost response", ""))
}

func retirementPageFlags() []cli.Flag {
	return []cli.Flag{jsonFlag, cli.NewStringFlag("before", "", "Continue from the response cursor", ""), cli.NewStringFlag("limit", "", "Maximum records, from 1 to 100", "50")}
}

func (a *App) retireCommand() cli.Command {
	flags := append(retirementRequestFlags(), retentionSettingFlags()...)
	return cli.NewCommand("retire", "Permanently delete an eligible backup while keeping a tested recovery point", a.retireBackup, cli.WithUsage("backups retire <backup-id> --yes [--older-than-days 30] [--keep-last 7] [--request-id ...] [--wait] [--json]"), cli.WithFlags(flags...))
}

func (a *App) retirementsCommand() cli.Command {
	return cli.NewCommand("retirements", "List backup retirement records", a.retirements, cli.WithUsage("retirements <project> [--before ...] [--limit 50] [--json]"), cli.WithFlags(retirementPageFlags()...),
		cli.WithSubcommand(cli.NewCommand("show", "Show retirement status", a.showRetirement, cli.WithUsage("retirements show <retirement-id> [--wait] [--json]"), cli.WithFlags(recoveryFlags()...))),
		cli.WithSubcommand(cli.NewCommand("history", "Read committed retirement history", a.retirementHistory, cli.WithUsage("retirements history <retirement-id> [--before ...] [--limit 50] [--json]"), cli.WithFlags(retirementPageFlags()...))),
		cli.WithSubcommand(cli.NewCommand("retry", "Authorize another bounded set of retirement attempts", a.retryRetirement, cli.WithUsage("retirements retry <retirement-id> --yes [--request-id ...] [--wait] [--json]"), cli.WithFlags(retirementRequestFlags()...))))
}

func retirementPageInput(ctx cli.CommandContext) (client.RetirementPageInput, error) {
	limit, err := strconv.Atoi(ctx.String("limit"))
	if err != nil || limit < 1 || limit > 100 {
		return client.RetirementPageInput{}, errors.New("limit must be an integer from 1 to 100")
	}
	return client.RetirementPageInput{Before: ctx.String("before"), Limit: limit}, nil
}

func (a *App) retireBackup(ctx cli.CommandContext) error {
	if !ctx.Bool("yes") {
		return errors.New("pass --yes to permanently delete this eligible backup")
	}
	if ctx.Arg(0) == "" {
		return errors.New("give a backup ID")
	}
	settings, err := retentionSettings(ctx)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	key := requestID(ctx)
	if !ctx.Bool("json") {
		a.printf("Request ID: %s\n", key)
	}
	r, err := c.RetireServiceBackup(ctxOf(ctx), ctx.Arg(0), client.RetirementRequest{RequestID: key, Policy: client.RetentionSettings{OlderThanDays: settings.OlderThanDays, KeepLast: settings.KeepLast}})
	if err != nil {
		return fmt.Errorf("retirement request %s: %w", key, err)
	}
	return a.retirementResult(ctx, c, r)
}

func (a *App) retryRetirement(ctx cli.CommandContext) error {
	if !ctx.Bool("yes") {
		return errors.New("pass --yes to authorize another attempt to delete this backup")
	}
	if ctx.Arg(0) == "" {
		return errors.New("give a retirement ID")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	key := requestID(ctx)
	if !ctx.Bool("json") {
		a.printf("Request ID: %s\n", key)
	}
	r, err := c.RetryServiceRetirement(ctxOf(ctx), ctx.Arg(0), key)
	if err != nil {
		return fmt.Errorf("retirement retry request %s: %w", key, err)
	}
	return a.retirementResult(ctx, c, r)
}

func (a *App) showRetirement(ctx cli.CommandContext) error {
	if ctx.Arg(0) == "" {
		return errors.New("give a retirement ID")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	r, err := c.ServiceRetirement(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	return a.retirementResult(ctx, c, r)
}

func (a *App) retirementResult(ctx cli.CommandContext, c *client.Client, r *client.ServiceRetirement) error {
	bounded, cancel := recoveryContext(ctx)
	defer cancel()
	if ctx.Bool("wait") {
		result, err := c.WaitServiceRetirement(bounded, r.ID)
		if err != nil {
			return a.recoveryFollowError(ctx, r, "retirement", r.ID, err)
		}
		r = result
	}
	return a.recoveryResult(ctx, r, r.ID, r.Status, r.Error)
}

func (a *App) retirements(ctx cli.CommandContext) error {
	in, err := retirementPageInput(ctx)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	page, err := c.ServiceRetirements(ctxOf(ctx), p.ID, in)
	if err != nil {
		return err
	}
	return a.json(page)
}

func (a *App) retirementHistory(ctx cli.CommandContext) error {
	if ctx.Arg(0) == "" {
		return errors.New("give a retirement ID")
	}
	in, err := retirementPageInput(ctx)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	page, err := c.ServiceRetirementHistory(ctxOf(ctx), ctx.Arg(0), in)
	if err != nil {
		return err
	}
	return a.json(page)
}
