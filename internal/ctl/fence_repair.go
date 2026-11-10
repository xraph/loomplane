package ctl

import (
	"errors"

	"github.com/xraph/forge/cli"
)

func (a *App) fenceRepairCommand() cli.Command {
	return infraCommand("fence-repair", "fence-repair <connection-id> [--yes --review digest]",
		"Review a server's stuck fence journal, then clear its dirty mark with --yes", a.fenceRepair,
		infraYesFlag(), cloudFlag("review", "Exact review digest for --yes"))
}

// fenceRepair always shows the current review. With --yes it repairs, but
// only when --review is that review, so you clear the journal you read.
func (a *App) fenceRepair(ctx cli.CommandContext) error {
	if err := infraRequired(ctx, 1); err != nil {
		return err
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	review, err := c.ReviewFenceRepair(ctxOf(ctx), ctx.Arg(0))
	if err != nil {
		return err
	}
	if printErr := a.json(review); printErr != nil {
		return printErr
	}
	if !ctx.Bool("yes") {
		return nil
	}
	if ctx.String("review") == "" || ctx.String("review") != review.Review {
		return errors.New("the fence journal changed or no --review was given; review again")
	}
	repaired, err := c.RepairFence(ctxOf(ctx), ctx.Arg(0), review.Review)
	if err != nil {
		return err
	}
	return a.json(repaired)
}
