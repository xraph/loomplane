package ctl

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
)

func (a *App) backupCommand() cli.Command {
	return cli.NewCommand("backups", "List a project's database recovery points", a.backups, cli.WithUsage("backups <project>"), cli.WithFlag(jsonFlag),
		cli.WithSubcommand(cli.NewCommand("create", "Queue a native database backup", a.createBackup, cli.WithUsage("backups create <project> --resource database [--env production] [--request-id ...] [--wait] [--json]"), cli.WithFlags(append(recoveryFlags(), cli.NewStringFlag("resource", "", "Deployed resource name", "database"), cli.NewStringFlag("env", "e", "Environment", "production"), cli.NewStringFlag("request-id", "", "Stable key to reuse after a lost response", ""))...))),
		cli.WithSubcommand(cli.NewCommand("show", "Show backup status and verification evidence", a.showBackup, cli.WithUsage("backups show <backup-id> [--wait]"), cli.WithFlags(recoveryFlags()...))),
		cli.WithSubcommand(cli.NewCommand("retry", "Retry a failed backup", a.retryBackup, cli.WithUsage("backups retry <backup-id> [--wait]"), cli.WithFlags(recoveryFlags()...))),
		cli.WithSubcommand(a.retireCommand()),
		cli.WithSubcommand(cli.NewCommand("retention", "Preview full-backup retention without deleting data", a.previewBackupRetention, cli.WithUsage("backups retention <backup-id> [--older-than-days 30] [--keep-last 7] [--json]"), cli.WithFlags(append([]cli.Flag{jsonFlag}, retentionSettingFlags()...)...))),
		cli.WithSubcommand(cli.NewCommand("policies", "List saved backup schedules", a.backupPolicies, cli.WithUsage("backups policies <project> [--json]"), cli.WithFlag(jsonFlag))),
		cli.WithSubcommand(cli.NewCommand("policy", "Set a full-backup schedule; all points are retained", a.backupPolicy, cli.WithUsage("backups policy <project> --resource database --every-hours 24 [--disable]"), cli.WithFlags(cli.NewStringFlag("resource", "", "Deployed resource name", "database"), cli.NewStringFlag("env", "e", "Environment", "production"), cli.NewStringFlag("every-hours", "", "Cadence, from 1 to 168 hours", "24"), cli.NewBoolFlag("disable", "", "Disable the schedule", false)))))
}
func (a *App) restoreCommand() cli.Command {
	return cli.NewCommand("restores", "List isolated recovery copies", a.restores, cli.WithUsage("restores <project>"), cli.WithFlag(jsonFlag),
		cli.WithSubcommand(cli.NewCommand("create", "Restore a backup into a private copy, removed after 24 hours", a.createRestore, cli.WithUsage("restores create <backup-id> [--request-id ...] [--wait] [--json]"), cli.WithFlags(append(recoveryFlags(), cli.NewStringFlag("request-id", "", "Stable key to reuse after a lost response", ""))...))),
		cli.WithSubcommand(cli.NewCommand("show", "Show restore status and SQL verification", a.showRestore, cli.WithUsage("restores show <restore-id> [--wait]"), cli.WithFlags(recoveryFlags()...))),
		cli.WithSubcommand(cli.NewCommand("delete", "Remove a temporary recovery copy and its volume", a.deleteRestore, cli.WithUsage("restores delete <restore-id> --yes"), cli.WithFlag(cli.NewBoolFlag("yes", "y", "Confirm deletion of the temporary copy", false)))))
}
func requestID(ctx cli.CommandContext) string {
	if value := ctx.String("request-id"); value != "" {
		return value
	}
	return rand.Text()
}
func (a *App) backups(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	list, err := c.ServiceBackups(ctxOf(ctx), p.ID)
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(list)
	}
	rows := [][]string{}
	for _, b := range list {
		verified := "not tested"
		if b.RestoredAt != nil {
			verified = b.RestoredAt.Format("2006-01-02 15:04Z")
		}
		rows = append(rows, []string{b.ID, b.Name, b.Status, strconv.FormatInt(b.Bytes, 10), verified})
	}
	a.table([]string{"ID", "RESOURCE", "STATUS", "BYTES", "RESTORE VERIFIED"}, rows)
	return nil
}
func (a *App) createBackup(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	key := requestID(ctx)
	if !ctx.Bool("json") {
		a.printf("Request ID: %s\n", key)
	}
	b, err := c.CreateServiceBackup(ctxOf(ctx), p.ID, client.BackupRequest{Environment: ctx.String("env"), Resource: ctx.String("resource"), RequestID: key})
	if err != nil {
		return fmt.Errorf("backup request %s: %w", key, err)
	}
	return a.backupResult(ctx, c, b)
}
func (a *App) showBackup(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	if ctx.Arg(0) == "" {
		return errors.New("give a backup ID")
	}
	b, err := c.ServiceBackup(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	return a.backupResult(ctx, c, b)
}
func (a *App) retryBackup(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	if ctx.Arg(0) == "" {
		return errors.New("give a backup ID")
	}
	b, err := c.RetryServiceBackup(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	return a.backupResult(ctx, c, b)
}
func (a *App) backupPolicy(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	hours, err := strconv.Atoi(ctx.String("every-hours"))
	if err != nil || hours < 1 || hours > 168 {
		return errors.New("every-hours must be an integer from 1 to 168")
	}
	policy, err := c.SetBackupPolicy(ctxOf(ctx), p.ID, client.BackupPolicyInput{Environment: ctx.String("env"), Resource: ctx.String("resource"), EveryHours: hours, Enabled: !ctx.Bool("disable")})
	if err != nil {
		return err
	}
	return a.json(policy)
}
func (a *App) restores(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	list, err := c.ServiceRestores(ctxOf(ctx), p.ID)
	if err != nil {
		return err
	}
	return a.json(list)
}
func (a *App) createRestore(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	if ctx.Arg(0) == "" {
		return errors.New("give a backup ID")
	}
	key := requestID(ctx)
	if !ctx.Bool("json") {
		a.printf("Request ID: %s\n", key)
	}
	r, err := c.RestoreServiceBackup(ctxOf(ctx), ctx.Arg(0), key)
	if err != nil {
		return fmt.Errorf("restore request %s: %w", key, err)
	}
	return a.restoreResult(ctx, c, r)
}
func (a *App) showRestore(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	if ctx.Arg(0) == "" {
		return errors.New("give a restore ID")
	}
	r, err := c.ServiceRestore(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	return a.restoreResult(ctx, c, r)
}
func (a *App) deleteRestore(ctx cli.CommandContext) error {
	if !ctx.Bool("yes") {
		return errors.New("pass --yes to remove this isolated copy and its volume")
	}
	if ctx.Arg(0) == "" {
		return errors.New("give a restore ID")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	if checkErr := c.DeleteServiceRestore(ctxOf(ctx), ctx.Arg(0)); checkErr != nil {
		return checkErr
	}
	a.printf("Recovery copy removed. The backup and verification record remain.\n")
	return nil
}
