package ctl

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/pkg/client"
)

func retentionPolicyWriteFlags() []cli.Flag {
	return []cli.Flag{jsonFlag, cli.NewBoolFlag("yes", "y", "Confirm this automatic retention policy change", false)}
}
func (a *App) retentionSetCommand() cli.Command {
	flags := append(retentionPolicyWriteFlags(), retentionSettingFlags()...)
	flags = append(flags, cli.NewStringFlag("revision", "", "Observed policy revision, or 0 to create", "0"))
	return cli.NewCommand("retention-set", "Enable automatic deletion under an explicit age and count policy", a.setRetentionPolicy, cli.WithUsage("backups retention-set <backup-id> --yes [--revision 0] [--older-than-days 30] [--keep-last 7] [--json]"), cli.WithFlags(flags...))
}
func retentionPolicyWriteCheck(ctx cli.CommandContext) error {
	if !ctx.Bool("yes") {
		return errors.New("pass --yes to change automatic backup deletion")
	}
	if ctx.Arg(0) == "" {
		return errors.New("give a backup ID from the source history")
	}
	return nil
}
func (a *App) showRetentionPolicy(ctx cli.CommandContext) error {
	if ctx.Arg(0) == "" {
		return errors.New("give a backup ID from the source history")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	state, err := c.BackupRetentionPolicy(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	return a.json(state)
}
func (a *App) setRetentionPolicy(ctx cli.CommandContext) error {
	if err := retentionPolicyWriteCheck(ctx); err != nil {
		return err
	}
	revision, err := strconv.ParseInt(ctx.String("revision"), 10, 64)
	if err != nil || revision < 0 || revision >= 2147483647 {
		return errors.New("revision must be the observed policy revision, or 0 to create")
	}
	settings, err := retentionSettings(ctx)
	if err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	saved, err := c.SetBackupRetentionPolicy(ctxOf(ctx), ctx.Arg(0), client.RetentionPolicyInput{Revision: revision, Enabled: true, Policy: client.RetentionSettings{OlderThanDays: settings.OlderThanDays, KeepLast: settings.KeepLast}})
	if err != nil {
		return fmt.Errorf("policy save failed; inspect backups retention-policy %s before retrying: %w", ctx.Arg(0), err)
	}
	if !ctx.Bool("json") {
		a.printf("Automatic deletion enabled. Eligible backups may now be deleted after a retained recovery point is checked.\n")
	}
	return a.json(saved)
}
func (a *App) pauseRetentionPolicy(ctx cli.CommandContext) error {
	if err := retentionPolicyWriteCheck(ctx); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	state, err := c.BackupRetentionPolicy(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	if !state.Configured {
		return a.json(state)
	}
	if state.Policy == nil || state.Policy.Revision < 1 {
		return errors.New("policy response is incomplete; inspect it before retrying")
	}
	saved, err := c.SetBackupRetentionPolicy(ctxOf(ctx), ctx.Arg(0), client.RetentionPolicyInput{Revision: state.Policy.Revision, Enabled: false, Policy: state.Policy.Policy})
	if err != nil {
		return fmt.Errorf("policy pause failed; inspect backups retention-policy %s before retrying: %w", ctx.Arg(0), err)
	}
	if !ctx.Bool("json") {
		a.printf("Automatic selection paused. Previously accepted deletions may still finish.\n")
	}
	return a.json(saved)
}
func (a *App) retentionPolicyHistory(ctx cli.CommandContext) error {
	if ctx.Arg(0) == "" {
		return errors.New("give a backup ID from the source history")
	}
	in, err := retirementPageInput(ctx)
	if err != nil {
		return err
	}
	if checkErr := (recovery.RetentionPolicyHistoryInput{Before: in.Before, Limit: in.Limit}).Validate(); checkErr != nil {
		return errors.New("before must be a canonical positive integer cursor from the preceding response")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	page, err := c.BackupRetentionPolicyHistory(ctxOf(ctx), ctx.Arg(0), in)
	if err != nil {
		return err
	}
	return a.json(page)
}
