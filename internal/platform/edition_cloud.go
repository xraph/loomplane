//go:build cloud

package platform

import (
	"github.com/xraph/dispatch/engine"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/billing"
	"github.com/xraph/loomplane/internal/config"
)

// editionExtensions are the extensions only this edition adds: billing.
func editionExtensions(cfg config.Config, jobs func() *engine.Engine) []forge.Extension {
	return []forge.Extension{billing.New(cfg, jobs)}
}

// editionQueues are the Dispatch queues only this edition works.
func editionQueues() []string { return []string{billing.Queue} }
