package ctl

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/client"
)

func recoveryFlags() []cli.Flag {
	return []cli.Flag{jsonFlag, cli.NewBoolFlag("wait", "w", "Wait for a terminal result", false), timeoutFlag("Maximum wait time", 45*time.Minute)}
}

func recoveryContext(ctx cli.CommandContext) (context.Context, context.CancelFunc) {
	if !ctx.Bool("wait") {
		return ctxOf(ctx), func() {}
	}
	return context.WithTimeout(ctxOf(ctx), time.Duration(ctx.Duration("timeout")))
}

func (a *App) backupResult(ctx cli.CommandContext, c *client.Client, b *client.ServiceBackup) error {
	bounded, cancel := recoveryContext(ctx)
	defer cancel()
	if ctx.Bool("wait") {
		id := b.ID
		result, err := c.WaitServiceBackup(bounded, id)
		if err != nil {
			return a.recoveryFollowError(ctx, b, "backup", id, err)
		}
		b = result
	}
	return a.recoveryResult(ctx, b, b.ID, b.Status, b.Error)
}

func (a *App) restoreResult(ctx cli.CommandContext, c *client.Client, r *client.ServiceRestore) error {
	bounded, cancel := recoveryContext(ctx)
	defer cancel()
	if ctx.Bool("wait") {
		id := r.ID
		result, err := c.WaitServiceRestore(bounded, id)
		if err != nil {
			return a.recoveryFollowError(ctx, r, "restore", id, err)
		}
		r = result
	}
	return a.recoveryResult(ctx, r, r.ID, r.Status, r.Error)
}

func (a *App) recoveryFollowError(ctx cli.CommandContext, record any, kind, id string, cause error) error {
	if ctx.Bool("json") {
		cause = errors.Join(cause, a.json(record))
	}
	return fmt.Errorf("follow %s %s with loomctl %ss show: %w", kind, id, kind, cause)
}

func (a *App) recoveryResult(ctx cli.CommandContext, record any, id, status, detail string) error {
	if err := a.json(record); err != nil {
		return err
	}
	if ctx.Bool("wait") && status != "succeeded" && status != "deleted" {
		return fmt.Errorf("recovery operation %s %s: %s", id, status, detail)
	}
	return nil
}

func (a *App) backupPolicies(ctx cli.CommandContext) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	p, err := a.project(ctx, c)
	if err != nil {
		return err
	}
	policies, err := c.BackupPolicies(ctxOf(ctx), p.ID)
	if err != nil {
		return err
	}
	if ctx.Bool("json") {
		return a.json(policies)
	}
	rows := make([][]string, 0, len(policies))
	for _, policy := range policies {
		rows = append(rows, []string{policy.Resource, policy.Environment, fmt.Sprint(policy.EveryHours), fmt.Sprint(policy.Enabled), policy.NextAt.Format(time.RFC3339), policy.LastError})
	}
	a.table([]string{"RESOURCE", "ENVIRONMENT", "EVERY HOURS", "ENABLED", "NEXT", "ERROR"}, rows)
	return nil
}
