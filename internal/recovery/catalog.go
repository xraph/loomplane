package recovery

import (
	"fmt"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

// refusalError is a reason a resource has no backups or restores. It is an
// invalid request, and says so in its own words.
type refusalError struct{ msg string }

func (r *refusalError) Error() string { return r.msg }
func (r *refusalError) Unwrap() error { return ErrInvalid }

// namedRefusalError is a refusal worded for one resource. It matches the
// sentinel it came from.
type namedRefusalError struct {
	msg  string
	kind error
}

func (r *namedRefusalError) Error() string   { return r.msg }
func (r *namedRefusalError) Unwrap() []error { return []error{r.kind, ErrInvalid} }

// What a resource that has no backups or restores can be told.
var (
	// ErrNothingToRestore is for stateless components and composition
	// parents.
	ErrNothingToRestore error = &refusalError{"it keeps no data: it isn't backed up and there is nothing to restore"}
	// ErrRebuildable is for components with a volume that is rebuilt from
	// its source.
	ErrRebuildable error = &refusalError{"rebuilt from its source, not backed up"}
	// ErrNotManaged is for external dependencies.
	ErrNotManaged error = &refusalError{"Not managed by Loomplane: no backups or restores"}
)

// familyOf is how a kind finds the engine family whose recovery it shares.
// Tests swap it to register kinds without touching the default registry.
var familyOf = recipes.FamilyOf

// Protection is what recovery does for a resource's data. A database with
// an adapter on its target is native, and a database without one is "".
// Components and composition parents keep nothing or are rebuilt. An
// external dependency has no deployment, so it arrives as a config whose
// Provider is planning.DependencyExternal.
func Protection(cfg *planning.ResourceConfig) recipes.Protection {
	switch {
	case cfg.Provider == planning.DependencyExternal:
		return recipes.ProtectionExternal
	case cfg.Component != nil:
		if cfg.Component.Protection == recipes.ProtectionRebuildable {
			return recipes.ProtectionRebuildable
		}
		return recipes.ProtectionNone
	case cfg.Composition != nil:
		return recipes.ProtectionNone
	}
	family := familyOf(cfg.Kind)
	if cfg.Catalog != nil && family != "" && cfg.Catalog.Engine() == family && (cfg.Provider == "server" || cfg.Provider == "kubernetes" && family == "postgres") {
		return recipes.ProtectionNative
	}
	return ""
}

func supportsRecovery(cfg *planning.ResourceConfig) bool {
	return Protection(cfg) == recipes.ProtectionNative
}

// refuseRecovery says why the named resource has no database recovery, or
// returns nil when it has.
func refuseRecovery(name string, cfg *planning.ResourceConfig) error {
	switch Protection(cfg) {
	case recipes.ProtectionNative:
		return nil
	case recipes.ProtectionNone:
		return &namedRefusalError{name + " keeps no data: it isn't backed up and there is nothing to restore", ErrNothingToRestore}
	case recipes.ProtectionRebuildable:
		return &namedRefusalError{name + " is rebuilt from its source, not backed up", ErrRebuildable}
	case recipes.ProtectionExternal:
		return ErrNotManaged
	}
	return fmt.Errorf("%w: the resource has no supported database backup adapter", ErrInvalid)
}
