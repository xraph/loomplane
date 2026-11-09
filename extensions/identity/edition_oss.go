//go:build !cloud

package identity

import (
	authsome "github.com/xraph/authsome"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
)

// editionState is what only this edition builds. The public build has
// nothing.
type editionState struct{}

// editionOptions are the authsome options only this edition adds. The public
// build has none.
func (e *Extension) editionOptions(*grove.DB) ([]authsome.Option, error) { return nil, nil }

// editionProvide puts what only this edition builds into DI.
func (e *Extension) editionProvide(c forge.Container) error { return e.edition.provide(c) }

// provide has nothing to put in DI: the public build builds nothing of its
// own.
func (editionState) provide(forge.Container) error { return nil }
