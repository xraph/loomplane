package ctl

import (
	"errors"
	"fmt"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
)

func (a *App) nativeRetentionCommand() cli.Command {
	return cli.NewCommand("native-retention", "Retain tested native backups and delete eligible independent points", nil,
		cli.WithSubcommand(cli.NewCommand("preview", "Preview native retention without deleting backups", a.previewNativeRetention, cli.WithFlags(append([]cli.Flag{jsonFlag}, retentionSettingFlags()...)...))),
		cli.WithSubcommand(cli.NewCommand("delete", "Permanently delete an eligible native recovery point", a.retireNativePoint, cli.WithUsage("backups native-retention delete <source-id> <point-id> --yes [--older-than-days 30] [--keep-last 7] [--request-id ...] [--wait]"), cli.WithFlags(append(retirementRequestFlags(), retentionSettingFlags()...)...))),
		cli.WithSubcommand(cli.NewCommand("show", "Read native deletion status", a.showNativeRetirement, cli.WithFlags(recoveryFlags()...))),
		cli.WithSubcommand(cli.NewCommand("retry", "Resume the original native deletion", a.retryNativeRetirement, cli.WithFlags(append(recoveryFlags(), cli.NewBoolFlag("yes", "y", "Confirm the original native deletion", false))...))),
		cli.WithSubcommand(cli.NewCommand("history", "Read committed native deletion history", a.nativeRetirementHistory, cli.WithFlags(retirementPageFlags()...))))
}
func (a *App) previewNativeRetention(ctx cli.CommandContext) error {
	if ctx.Arg(0) == "" {
		return errors.New("give a retained native source ID")
	}
	settings, err := retentionSettings(ctx)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	preview, err := c.NativeRetentionPreview(ctxOf(ctx), ctx.Arg(0), settings.OlderThanDays, settings.KeepLast)
	if err != nil {
		return err
	}
	return a.json(preview)
}
func (a *App) retireNativePoint(ctx cli.CommandContext) error {
	if !ctx.Bool("yes") {
		return errors.New("pass --yes to permanently delete this eligible native backup")
	}
	if ctx.Arg(0) == "" || ctx.Arg(1) == "" {
		return errors.New("give the retained native source and recovery point IDs")
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
	r, err := c.RetireNativePoint(ctxOf(ctx), ctx.Arg(0), ctx.Arg(1), client.NativeRetirementRequest{RequestID: key, Policy: client.RetentionSettings{OlderThanDays: settings.OlderThanDays, KeepLast: settings.KeepLast}, AcknowledgeDeletion: true})
	if err != nil {
		return fmt.Errorf("native deletion request %s: %w", key, err)
	}
	return a.nativeRetirementResult(ctx, c, r)
}
func (a *App) showNativeRetirement(ctx cli.CommandContext) error {
	if ctx.Arg(0) == "" {
		return errors.New("give a native deletion ID")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	r, err := c.NativeRetirement(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	return a.nativeRetirementResult(ctx, c, r)
}
func (a *App) retryNativeRetirement(ctx cli.CommandContext) error {
	if !ctx.Bool("yes") {
		return errors.New("pass --yes to resume the original native deletion")
	}
	if ctx.Arg(0) == "" {
		return errors.New("give a native deletion ID")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	r, err := c.RetryNativeRetirement(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	return a.nativeRetirementResult(ctx, c, r)
}
func (a *App) nativeRetirementResult(ctx cli.CommandContext, c *client.Client, r *client.NativeRetirement) error {
	bounded, cancel := recoveryContext(ctx)
	defer cancel()
	if ctx.Bool("wait") {
		result, err := c.WaitNativeRetirement(bounded, r.ID)
		if err != nil {
			return a.recoveryFollowError(ctx, r, "native deletion", r.ID, err)
		}
		r = result
	}
	return a.recoveryResult(ctx, r, r.ID, r.Status, r.Error)
}
func (a *App) nativeRetirementHistory(ctx cli.CommandContext) error {
	if ctx.Arg(0) == "" {
		return errors.New("give a native deletion ID")
	}
	in, err := retirementPageInput(ctx)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	page, err := c.NativeRetirementHistory(ctxOf(ctx), ctx.Arg(0), in)
	if err != nil {
		return err
	}
	return a.json(page)
}
