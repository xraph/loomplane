package ai

import (
	"context"
	"errors"
	"fmt"

	"github.com/xraph/dispatch"
	"github.com/xraph/dispatch/engine"
	dispatchext "github.com/xraph/dispatch/extension"
	"github.com/xraph/forge"
)

// OffName is the name of the extension the platform registers when AI is
// off.
const OffName = "loomplane-ai-off"

// Off removes what the AI section left in Dispatch when it was on. Its
// cron entries persist in the database, and with AI off nothing handles
// their jobs, so each would fail every minute. It registers no route,
// hook, setting or job.
type Off struct {
	*forge.BaseExtension
	engineOf func() *engine.Engine
}

// NewOff returns the extension. engineOf returns the dispatch extension's
// engine.
func NewOff(engineOf func() *engine.Engine) *Off {
	return &Off{
		BaseExtension: forge.NewBaseExtension(OffName, "0.1.0", "Removes the AI section's schedules while AI is off"),
		engineOf:      engineOf,
	}
}

// Dependencies implements forge.Extension.
func (e *Off) Dependencies() []string { return []string{dispatchext.ExtensionName} }

// Start removes the AI section's cron entries. The database is migrated
// by now.
func (e *Off) Start(ctx context.Context) error {
	if err := DropSchedules(ctx, e.engineOf()); err != nil {
		return fmt.Errorf("remove the AI schedules: %w", err)
	}
	e.MarkStarted()
	return nil
}

// DropSchedules deletes the cron entries of the AI section's jobs. Turning
// AI on again registers them afresh.
func DropSchedules(ctx context.Context, eng *engine.Engine) error {
	entries, err := eng.CronStore().ListCrons(ctx)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.JobName != JobReconcileModels {
			continue
		}
		// Another replica starting at the same time may have taken it.
		if err = eng.DeleteCron(ctx, entry.ID); err != nil && !errors.Is(err, dispatch.ErrCronNotFound) {
			return err
		}
	}
	return nil
}
