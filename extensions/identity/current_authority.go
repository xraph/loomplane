package identity

import (
	"fmt"

	"github.com/xraph/warden"
	"github.com/xraph/warden/store"

	"github.com/xraph/loomplane/internal/currentauthority"
)

// initWarden owns the fixed built-in engine construction. Registration remains
// SQL-free, and the coordinator stays unavailable until principal and domain
// qualification can be installed. All consumers retain this same engine.
func (e *Extension) initWarden(ordinary store.Store) (store.Store, error) {
	e.authority = &currentauthority.Coordinator{}
	routed := currentauthority.NewStoreRouter(e.authority, ordinary)
	engine, err := warden.NewEngine(
		warden.WithStore(routed),
		warden.WithCache(currentauthority.NewFreshCache(e.authority, warden.NewMemoryCache())),
		warden.WithLogger(e.Logger()),
	)
	if err != nil {
		return nil, fmt.Errorf("warden: %w", err)
	}
	e.warden = engine
	return routed, nil
}
