package ctl

import (
	"errors"

	"github.com/xraph/forge/cli"
)

func (a *App) failedCleanupCommand() cli.Command {
	commands := make([]cli.Command, 0, 5)
	for _, name := range []string{"show", "preview", "apply", "retry", "cancel"} {
		commands = append(commands, infraCommand(name, "cleanup "+name+" <deployment-id>", "Review and recover failed deployment cleanup", func(ctx cli.CommandContext) error { return a.failedCleanup(ctx, name) }, infraYesFlag(), cloudFlag("review", "Exact review digest for apply"), cloudFlag("expected-review-id", "Accepted review ID to cancel")))
	}
	return infraGroup("cleanup", "cleanup <action> <deployment-id>", "Review failed deployment cleanup", func(ctx cli.CommandContext) error { return a.failedCleanup(ctx, "show") }, commands...)
}
func (a *App) failedCleanup(ctx cli.CommandContext, action string) error {
	if err := infraRequired(ctx, 1); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var out any
	id := ctx.Arg(0)
	switch action {
	case "show":
		out, err = c.FailedCleanup(ctxOf(ctx), id)
	case "preview":
		out, err = c.PreviewFailedCleanup(ctxOf(ctx), id)
	case "apply":
		if !ctx.Bool("yes") || ctx.String("review") == "" {
			return errors.New("give --yes and --review with the exact saved digest")
		}
		out, err = c.ApplyFailedCleanup(ctxOf(ctx), id, ctx.String("review"))
	case "retry":
		out, err = c.RetryFailedCleanup(ctxOf(ctx), id)
	case "cancel":
		if !ctx.Bool("yes") || ctx.String("expected-review-id") == "" {
			return errors.New("give --yes and --expected-review-id with the accepted review ID")
		}
		out, err = c.CancelFailedCleanup(ctxOf(ctx), id, ctx.String("expected-review-id"))
	}
	if err != nil {
		return err
	}
	return a.json(out)
}
