package owned

import (
	"context"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/backupfiles"
)

// DatabaseBackend is the native lifecycle consumed by database workflows.
// Each target validates its qualified engine, identity and recovery isolation.
type DatabaseBackend interface {
	Backend
	ConfigureDatabase(context.Context, id.ID, map[string]string, string) error
	BackupDatabase(context.Context, id.ID, map[string]string, *backupfiles.Store, string) (backupfiles.Object, error)
	RestoreDatabase(context.Context, id.ID, map[string]string, *backupfiles.Store, backupfiles.Object) error
	DatabaseProbe(context.Context, id.ID, map[string]string) error
	PurgeRecovery(context.Context, id.ID, map[string]string) error
}
