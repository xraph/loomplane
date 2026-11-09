package currentauthority

import (
	"reflect"

	"github.com/xraph/warden"
	"github.com/xraph/warden/plugin"
)

// profile is private. A fixture may qualify only the transaction mechanism;
// installed authority additionally requires engine, connection and principal
// qualification. No public constructor installs either profile.
type profile struct {
	ordinary    *ordinaryProfile
	engine      *warden.Engine
	invalidator plugin.Plugin
}

// captureEngineProfile is called only at the owned fixed built-in startup seam.
// It cannot certify an arbitrary engine's evaluator or later Go-level mutation.
func captureEngineProfile(engine *warden.Engine, router *storeRouter) (*profile, error) {
	if engine == nil || engine.Store() != router || engine.Plugins() == nil {
		return nil, ErrUnavailable
	}
	plugins := engine.Plugins().Plugins()
	if len(plugins) != 1 || plugins[0].Name() != "warden-cache-invalidator" {
		return nil, ErrUnavailable
	}
	// The concrete type is private upstream. Exact identity is retained after
	// this fixed startup capture; name equality alone never validates later lists.
	if reflect.TypeOf(plugins[0]).String() != "*warden.cacheInvalidator" {
		return nil, ErrUnavailable
	}
	p := &profile{engine: engine, invalidator: plugins[0]}
	if err := p.verify(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *profile) verify() error {
	if p == nil {
		return ErrUnavailable
	}
	if p.engine == nil {
		return nil
	} // Private transaction-only fixtures.
	if !reflect.DeepEqual(p.engine.Config(), warden.DefaultConfig()) || p.engine.Plugins() == nil {
		return ErrUnavailable
	}
	plugins := p.engine.Plugins().Plugins()
	if len(plugins) != 1 || plugins[0] != p.invalidator {
		return ErrUnavailable
	}
	return nil
}
