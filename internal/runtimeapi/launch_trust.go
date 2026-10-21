package runtimeapi

import (
	"context"

	"github.com/xraph/loomplane/internal/access"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// PublicSigningKey exposes a copy of the key used by this live listener. Launch
// delivery never reloads private key files or assumes a stopped listener is ready.
func (r *Runtime) PublicSigningKey(ctx context.Context, scope domain.Scope) (access.SnapshotKey, error) {
	if r == nil {
		return access.SnapshotKey{}, access.ErrSigningKeys
	}
	if err := r.Health(ctx); err != nil {
		return access.SnapshotKey{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running || scope.OrgID != r.cfg.OrganizationID {
		return access.SnapshotKey{}, access.ErrSigningKeys
	}
	signer, err := r.keys.Signer(ctx, scope)
	if err != nil {
		return access.SnapshotKey{}, err
	}
	return signer.Key(), nil
}
