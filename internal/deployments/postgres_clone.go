package deployments

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

func recoveryIdentity(b *planning.Binding, operation string) (name, runtimeRef string, err error) {
	key, err := typeid.Parse(operation)
	if err != nil || key.Prefix() != "restore" {
		return "", "", errors.New("invalid recovery identity")
	}
	name = "restore-" + strings.TrimPrefix(operation, "restore_")
	return name, resourceKey(b.OrgID, b.EnvironmentID, name, "runtime_"+strings.TrimPrefix(operation, "restore_")), nil
}
func (e *OwnedTarget) RestoreDatabase(ctx context.Context, p security.Principal, b *planning.Binding, files *backupfiles.Store, object backupfiles.Object, operation string) (string, error) {
	ctx = infrastructure.WithPrincipal(ctx, p)
	if p.OrgID != b.OrgID || b.Subject != planning.ResourceSubject {
		return "", ctrlplane.ErrForbidden
	}
	var cfg planning.ResourceConfig
	if err := json.Unmarshal(b.Config, &cfg); err != nil {
		return "", err
	}
	if _, err := postgresService(&cfg); err != nil {
		return "", err
	}
	name, ref, err := recoveryIdentity(b, operation)
	if err != nil {
		return "", err
	}
	if _, lookupErr := e.Ctrl.Instances.GetBySlug(ctx, name); !errors.Is(lookupErr, ctrlplane.ErrNotFound) {
		if lookupErr != nil {
			return "", lookupErr
		}
		return "", errors.New("recovery instance already exists; remove it before requesting another restore")
	}
	settings, _ := json.Marshal(cfg.TargetSettings) //nolint:errcheck // string maps always encode
	labels := map[string]string{owned.LabelOrg: b.OrgID, owned.LabelProject: b.ProjectID, owned.LabelEnvironment: b.EnvironmentID, owned.LabelConnection: b.ConnectionID, owned.LabelService: name, owned.LabelRuntime: ref, owned.LabelPublic: "false", owned.LabelSettings: string(settings), owned.LabelArchitecture: cfg.Catalog.Architecture, owned.LabelRevision: operation, server.LabelDatabase: databaseName(b.Name), server.LabelRecovery: operation}
	if checkErr := e.validateTarget(ctx, labels); checkErr != nil {
		return "", checkErr
	}
	key := e.Register(b.OrgID, b.ConnectionID)
	backend, err := e.registeredBackend(key)
	if err != nil {
		return "", err
	}
	native, ok := backend.(*server.Runtime)
	if !ok {
		return "", errors.New("target has no recovery adapter")
	}
	password := rand.Text() + rand.Text()
	runtimeValues, _ := json.Marshal(map[string]string{"POSTGRES_PASSWORD": rand.Text() + rand.Text(), "LOOMPLANE_APP_PASSWORD": password}) //nolint:errcheck // string maps always encode
	if checkErr := e.Secrets.Put(ctx, ref, runtimeValues); checkErr != nil {
		return "", checkErr
	}
	sp := cpprovider.ServiceSpec{Name: mainService, Image: cfg.Catalog.Image, Env: map[string]string{"POSTGRES_DB": databaseName(b.Name)}, Ports: []cpprovider.PortSpec{{Container: 5432, Protocol: "tcp"}}, Volumes: []cpprovider.VolumeSpec{{Name: "data", Type: "persistent", MountPath: cfg.Catalog.MountPath}}, Resources: cpprovider.ResourceSpec{CPUMillis: 1000, MemoryMB: 512}}
	inst, err := e.Ctrl.Instances.Create(ctx, instance.CreateRequest{Name: name, ProviderName: key, Services: []cpprovider.ServiceSpec{sp}, Labels: labels})
	if err != nil {
		return "", err
	}
	if checkErr := native.ConfigurePostgres(ctx, inst.ID, labels, password); checkErr != nil {
		return inst.ID.String(), checkErr
	}
	if checkErr := native.RestorePostgres(ctx, inst.ID, labels, files, object); checkErr != nil {
		return inst.ID.String(), checkErr
	}
	return inst.ID.String(), native.RestoreProbe(ctx, inst.ID, labels)
}

// DeleteRecovery is confined to the named temporary copy, including its volume.
// The deterministic slug also finds a create that finished before a worker crash.
func (e *OwnedTarget) DeleteRecovery(ctx context.Context, p security.Principal, b *planning.Binding, operation string) error {
	ctx = infrastructure.WithPrincipal(ctx, p)
	if p.OrgID != b.OrgID {
		return ctrlplane.ErrForbidden
	}
	name, ref, err := recoveryIdentity(b, operation)
	if err != nil {
		return err
	}
	inst, err := e.Ctrl.Instances.GetBySlug(ctx, name)
	if errors.Is(err, ctrlplane.ErrNotFound) {
		return e.deleteRuntime(ctx, b.OrgID, b.EnvironmentID, name, ref)
	}
	if err != nil {
		return err
	}
	labels := inst.Labels
	if inst.TenantID != b.OrgID || labels[owned.LabelProject] != b.ProjectID || labels[owned.LabelEnvironment] != b.EnvironmentID || labels[owned.LabelConnection] != b.ConnectionID || labels[owned.LabelService] != name || labels[owned.LabelRuntime] != ref || labels[server.LabelRecovery] != operation || labels[owned.LabelPublic] != "false" {
		return ctrlplane.ErrForbidden
	}
	backend, err := e.registeredBackend(e.Register(b.OrgID, b.ConnectionID))
	if err != nil {
		return err
	}
	native, ok := backend.(*server.Runtime)
	if !ok {
		return errors.New("target has no recovery cleanup")
	}
	if checkErr := native.PurgeRecovery(ctx, inst.ID, labels); checkErr != nil {
		return checkErr
	}
	// Keep the instance row until every owned data volume has been removed.
	if checkErr := e.Ctrl.Instances.Delete(ctx, inst.ID); checkErr != nil {
		return checkErr
	}
	return e.deleteRuntime(ctx, b.OrgID, b.EnvironmentID, name, ref)
}
