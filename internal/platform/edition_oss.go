//go:build !cloud

package platform

import (
	"github.com/xraph/dispatch/engine"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/config"
)

// editionExtensions are the extensions only this edition adds. The public
// build has none.
func editionExtensions(config.Config, func() *engine.Engine) []forge.Extension { return nil }

// editionQueues are the Dispatch queues only this edition works.
func editionQueues() []string { return nil }
