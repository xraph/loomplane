//go:build cloud

// Package billing is Loomplane Cloud's billing extension. It exists only in
// binaries built with -tags cloud; the public build never links it.
package billing

import (
	"github.com/xraph/dispatch/engine"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/extensions/notifications"
	"github.com/xraph/loomplane/internal/config"
)

// Name is the extension's name.
const Name = "loomplane-billing"

// Queue is the Dispatch queue billing's jobs run on.
const Queue = "billing"

// Extension is Loomplane Cloud's billing.
type Extension struct {
	*forge.BaseExtension
	cfg  config.Config
	jobs func() *engine.Engine
}

// New returns the extension. jobs returns the dispatch extension's engine.
func New(cfg config.Config, jobs func() *engine.Engine) *Extension {
	return &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Loomplane Cloud billing"),
		cfg:           cfg,
		jobs:          jobs,
	}
}

// Dependencies implements forge.Extension. Identity owns the accounts that
// get billed; notifications sends what billing has to say.
func (e *Extension) Dependencies() []string {
	return []string{identity.Name, notifications.Name}
}
