package kubernetes

import (
	"context"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/owned"
	nativecache "github.com/xraph/loomplane/internal/clouds/redis"
)

var _ owned.DatabaseBackend = (*Runtime)(nil)

// Kubernetes qualifies the PostgreSQL native engine, and Redis and Valkey
// as another kind's dependency without backups. These wrappers preserve
// PostgreSQL's pre-access validation and enforced recovery gate.
func (r *Runtime) ConfigureDatabase(ctx context.Context, iid id.ID, labels map[string]string, password string) error {
	if labels[nativecache.LabelEngine] != "" {
		return r.configureCache(ctx, iid, labels, password)
	}
	return r.ConfigurePostgres(ctx, iid, labels, password)
}
func (r *Runtime) BackupDatabase(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, key string) (backupfiles.Object, error) {
	if labels[nativecache.LabelEngine] != "" {
		return backupfiles.Object{}, ErrCacheBackupsUnavailable
	}
	return r.BackupPostgres(ctx, iid, labels, files, key)
}
func (r *Runtime) RestoreDatabase(ctx context.Context, iid id.ID, labels map[string]string, files *backupfiles.Store, object backupfiles.Object) error {
	if labels[nativecache.LabelEngine] != "" {
		return ErrCacheBackupsUnavailable
	}
	return r.RestorePostgres(ctx, iid, labels, files, object)
}
func (r *Runtime) DatabaseProbe(ctx context.Context, iid id.ID, labels map[string]string) error {
	if labels[nativecache.LabelEngine] != "" {
		return ErrCacheBackupsUnavailable
	}
	return r.RestoreProbe(ctx, iid, labels)
}
