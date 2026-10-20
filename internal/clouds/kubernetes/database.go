package kubernetes

import (
	"context"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/owned"
)

var _ owned.DatabaseBackend = (*Runtime)(nil)

// Kubernetes currently qualifies only the PostgreSQL native engine. These
// wrappers preserve its pre-access validation and enforced recovery gate.
func (r *Runtime) ConfigureDatabase(ctx context.Context, iid id.ID, labels map[string]string, password string) error {
	return r.ConfigurePostgres(ctx, iid, labels, password)
}
func (r *Runtime) BackupDatabase(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, key string) (backupfiles.Object, error) {
	return r.BackupPostgres(ctx, iid, labels, files, key)
}
func (r *Runtime) RestoreDatabase(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, object backupfiles.Object) error {
	return r.RestorePostgres(ctx, iid, labels, files, object)
}
func (r *Runtime) DatabaseProbe(ctx context.Context, iid id.ID, labels map[string]string) error {
	return r.RestoreProbe(ctx, iid, labels)
}
