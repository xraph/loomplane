package deployments

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// BackupDatabase resolves the recorded instance through the selected connection
// again; a backup cannot follow a binding that moved to another tenant or target.
func (e *OwnedTarget) BackupDatabase(ctx context.Context, p security.Principal, b *planning.Binding, files *backupfiles.Store, key string) (backupfiles.Object, error) {
	ctx = infrastructure.WithPrincipal(ctx, p)
	if b == nil || b.OrgID != p.OrgID || b.Subject != planning.ResourceSubject {
		return backupfiles.Object{}, errors.New("database is outside the requested scope")
	}
	var cfg planning.ResourceConfig
	if err := json.Unmarshal(b.Config, &cfg); err != nil {
		return backupfiles.Object{}, err
	}
	if err := e.checkDatabaseBinding(b, &cfg); err != nil {
		return backupfiles.Object{}, err
	}
	if _, err := databaseService(&cfg); err != nil {
		return backupfiles.Object{}, err
	}
	iid, err := id.Parse(b.ExternalID)
	if err != nil {
		return backupfiles.Object{}, err
	}
	inst, err := e.Ctrl.Instances.Get(ctx, iid)
	if err != nil {
		return backupfiles.Object{}, err
	}
	if inst.TenantID != b.OrgID || inst.Labels[owned.LabelProject] != b.ProjectID || inst.Labels[owned.LabelEnvironment] != b.EnvironmentID || inst.Labels[owned.LabelConnection] != b.ConnectionID || inst.Labels[owned.LabelService] != b.Name || inst.Labels[server.LabelRecovery] != "" {
		return backupfiles.Object{}, errors.New("database instance no longer matches its binding")
	}
	if len(inst.Services) != 1 || inst.Services[0].Image != cfg.Catalog.Image {
		return backupfiles.Object{}, errors.New("database image changed since qualification")
	}
	backend, err := e.registeredBackend(e.Register(b.OrgID, b.ConnectionID))
	if err != nil {
		return backupfiles.Object{}, err
	}
	native, ok := backend.(owned.DatabaseBackend)
	if !ok {
		return backupfiles.Object{}, errors.New("database target does not support backups")
	}
	return native.BackupDatabase(ctx, iid, inst.Labels, files, key)
}
