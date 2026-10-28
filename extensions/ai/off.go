package ai

import (
	"context"
	"errors"

	"github.com/xraph/dispatch/engine"
	dispatchext "github.com/xraph/dispatch/extension"
	"github.com/xraph/dispatch/job"
	"github.com/xraph/forge"
)

// OffName is the name of the extension the platform registers when AI is
// off.
const OffName = "loomplane-ai-off"

// Off handles what the AI section left in Dispatch when it was on. Its
// cron entries persist in the database and keep firing, and a job nothing
// handles fails every time. So with AI off, the AI section's jobs are
// registered with handlers that do nothing, and their entries fire
// harmlessly until AI is on again.
//
// It deletes nothing, so it never depends on the database at start or on
// the order replicas start in during a rollout: a replica with AI on
// still finds its schedule. It registers no route, hook or setting.
type Off struct {
	*forge.BaseExtension
	engineOf func() *engine.Engine
}

// NewOff returns the extension. engineOf returns the dispatch extension's
// engine.
func NewOff(engineOf func() *engine.Engine) *Off {
	return &Off{
		BaseExtension: forge.NewBaseExtension(OffName, "0.1.0", "Quiets the AI section's schedules while AI is off"),
		engineOf:      engineOf,
	}
}

// Dependencies implements forge.Extension.
func (e *Off) Dependencies() []string { return []string{dispatchext.ExtensionName} }

// Register registers the AI section's jobs with handlers that do nothing.
// Dispatch only works its queue once every extension has registered, so
// no job arrives before its handler.
func (e *Off) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	eng := e.engineOf()
	if eng == nil {
		return errors.New("the AI-off extension needs the dispatch extension to have registered")
	}
	RegisterOffJobs(eng)
	return nil
}

// Start has nothing to start.
func (e *Off) Start(context.Context) error {
	e.MarkStarted()
	return nil
}

// RegisterOffJobs registers each of the AI section's scheduled jobs with a
// handler that does nothing, for while AI is off.
func RegisterOffJobs(eng *engine.Engine) {
	engine.Register(eng, job.NewDefinition(JobReconcileModels, func(context.Context, reconcilePayload) error {
		return nil
	}, job.WithMaxRetries(0)))
}
