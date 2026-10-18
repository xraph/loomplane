package recovery

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// TargetBackend owns database operations on one explicitly connected provider.
// Native runtime ownership and recipe validation remain in that provider.
type TargetBackend interface {
	Backend
	RestoreBackend
}

// Targets selects the recorded provider for every recovery operation. It never
// substitutes another connected target when that provider is unavailable.
type Targets map[string]TargetBackend

func (targets Targets) selectTarget(p security.Principal, binding *planning.Binding) (TargetBackend, error) {
	if binding == nil || p.OrgID == "" || binding.OrgID != p.OrgID || binding.Subject != planning.ResourceSubject {
		return nil, security.ErrForbidden
	}
	var cfg planning.ResourceConfig
	if json.Unmarshal(binding.Config, &cfg) != nil || cfg.Kind == "" || cfg.Catalog == nil || cfg.Provider != binding.Provider || cfg.Target == "" || cfg.Target != binding.ConnectionID {
		return nil, fmt.Errorf("%w: database binding no longer matches its recorded provider and connection", ErrInvalid)
	}
	backend := targets[binding.Provider]
	if backend == nil {
		return nil, fmt.Errorf("%w: database target has no recovery adapter", ErrInvalid)
	}
	return backend, nil
}
func (targets Targets) BackupDatabase(ctx context.Context, p security.Principal, b *planning.Binding, files *backupfiles.Store, key string) (backupfiles.Object, error) {
	backend, err := targets.selectTarget(p, b)
	if err != nil {
		return backupfiles.Object{}, err
	}
	return backend.BackupDatabase(ctx, p, b, files, key)
}
func (targets Targets) RestoreDatabase(ctx context.Context, p security.Principal, b *planning.Binding, files *backupfiles.Store, object backupfiles.Object, operation string) (string, error) {
	backend, err := targets.selectTarget(p, b)
	if err != nil {
		return "", err
	}
	return backend.RestoreDatabase(ctx, p, b, files, object, operation)
}
func (targets Targets) DeleteRecovery(ctx context.Context, p security.Principal, b *planning.Binding, operation string) error {
	backend, err := targets.selectTarget(p, b)
	if err != nil {
		return err
	}
	return backend.DeleteRecovery(ctx, p, b, operation)
}
