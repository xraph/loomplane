package deployments

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/enginebackup"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recovery"
	"github.com/xraph/loomplane/internal/security"
)

func recoverySource(b *planning.Binding, d recovery.Descriptor) enginebackup.Identity {
	return enginebackup.Identity{Org: b.OrgID, Project: b.ProjectID, Environment: b.EnvironmentID, Resource: b.Name, Instance: b.ExternalID, Kind: d.Engine, Revision: d.Revision, Image: d.Image, Architecture: d.Architecture, Format: d.Format}
}

// recoveryRun renders from newly generated copy material, never source secrets.
func (e *OwnedTarget) recoveryRun(ctx context.Context, b *planning.Binding, cfg *planning.ResourceConfig, d recovery.Descriptor, name string) (ComponentRun, string, error) {
	run := ComponentRun{OrgID: b.OrgID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID, Resource: name, Kind: cfg.Kind, Provider: cfg.Provider, Component: d.Component.Component, TargetSettings: maps.Clone(cfg.TargetSettings), Recovery: true, Env: map[string]string{}, Secrets: map[string][]byte{}}
	for _, secret := range d.Kind.Secrets {
		var value []byte
		var err error
		if secret.Generator != "" {
			generator, ok := e.hooks(cfg.Kind).(StructuredSecretGenerator)
			if !ok {
				return run, "", errors.New("recovery secret generator unavailable")
			}
			request := SecretGeneration{OrgID: b.OrgID, EnvironmentID: b.EnvironmentID, Resource: name, Kind: cfg.Kind, Secret: secret, Secrets: run.Secrets}
			value, err = generator.GenerateSecret(ctx, request)
			if err == nil {
				err = generator.ValidateSecret(ctx, request, value)
			}
		} else {
			value, err = generateSecret(secret)
		}
		if err != nil {
			return run, "", err
		}
		run.Secrets[secret.Name] = value
	}
	reader := rand.Text() + rand.Text()
	if cfg.Kind == "duckdb" {
		reader = string(run.Secrets["token"])
	}
	run.Secrets["recovery-reader"] = []byte(reader)
	for _, env := range run.Component.Env {
		switch {
		case env.Secret != "":
			run.Env[env.Name] = string(run.Secrets[env.Secret])
		case env.From != nil || env.Template != "" || env.Requires != "":
			return run, "", errors.New("recovery component has unsupported environment inputs")
		default:
			run.Env[env.Name] = env.Value
		}
	}
	return run, reader, nil
}

func (e *OwnedTarget) restoreComponent(ctx context.Context, p security.Principal, b *planning.Binding, cfg *planning.ResourceConfig, d recovery.Descriptor, files *backupfiles.Store, object backupfiles.Object, operation string) (_ string, err error) {
	if cfg.Provider != "server" {
		return "", recovery.ErrNotOnKubernetes
	}
	if d.Component.Resource != b.Name {
		return "", errors.New("recovery requires a directly bound service")
	}
	name, ref, err := recoveryIdentity(b, operation)
	if err != nil {
		return "", err
	}
	if _, lookupErr := e.Ctrl.Instances.GetBySlug(ctx, name); !errors.Is(lookupErr, ctrlplane.ErrNotFound) {
		if lookupErr != nil {
			return "", lookupErr
		}
		return "", errors.New("recovery instance already exists")
	}
	// Verify the entire captured object before generating credentials or creating storage.
	verified, err := enginebackup.Verified(ctx, files, object, recoverySource(b, d))
	if err != nil {
		return "", err
	}
	if closeErr := closeVerified(verified); closeErr != nil {
		return "", closeErr
	}
	backend, err := e.registeredBackend(e.Register(b.OrgID, b.ConnectionID))
	if err != nil {
		return "", err
	}
	native, ok := backend.(owned.DatabaseBackend)
	if !ok {
		return "", errors.New("target has no recovery adapter")
	}
	run, reader, err := e.recoveryRun(ctx, b, cfg, d, name)
	if err != nil {
		return "", err
	}
	rendered, err := e.renderFiles(ctx, d.Kind, run)
	if err != nil {
		return "", err
	}
	settings, err := json.Marshal(cfg.TargetSettings)
	if err != nil {
		return "", err
	}
	source, err := json.Marshal(recoverySource(b, d))
	if err != nil {
		return "", err
	}
	labels := map[string]string{owned.LabelOrg: b.OrgID, owned.LabelProject: b.ProjectID, owned.LabelEnvironment: b.EnvironmentID, owned.LabelConnection: b.ConnectionID, owned.LabelService: name, owned.LabelRuntime: ref, owned.LabelRuntimeFiles: "true", owned.LabelPublic: "false", owned.LabelSettings: string(settings), owned.LabelArchitecture: d.Architecture, owned.LabelImage: d.Image, owned.LabelRevision: operation, server.LabelRecovery: operation, enginebackup.LabelSource: string(source)}
	component := *d.Component
	component.Resource = name
	spec := cpprovider.ServiceSpec{Name: mainService, Image: d.Image, Env: map[string]string{}}
	componentSpec(&spec, labels, &component, cfg.Provider)
	for i := range spec.Volumes {
		spec.Volumes[i].SizeMB = 0
	}
	values := map[string]string{"LOOMPLANE_APP_PASSWORD": reader}
	for _, env := range run.Component.Env {
		if env.Secret != "" {
			values[env.Name] = run.Env[env.Name]
		} else {
			spec.Env[env.Name] = run.Env[env.Name]
		}
	}
	if cfg.Kind == "falkordb" {
		spec.Args = append([]string(nil), spec.Args...)
		script := strings.Replace(spec.Args[0], "save 60 1000", `save ""`, 1)
		script = strings.Replace(script, "user default on #${hash} ~* &* +@all", "user default on #${hash} ~* &* +@all\nuser app on #${reader} ~* &* +ping +echo +quit +hello +graph.ro_query +graph.list", 1)
		script = strings.Replace(script, "umask 077", `reader=$(printf '%s' "$LOOMPLANE_APP_PASSWORD" | sha256sum); reader=${reader%% *}`+"\numask 077", 1)
		spec.Args[0] = script
	}
	if cfg.Kind == "duckdb" {
		spec.Args = []string{"serve-readonly"}
	}
	if checkErr := e.validateTarget(ctx, labels); checkErr != nil {
		return "", checkErr
	}
	runtimeValues, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	if checkErr := e.Secrets.Put(ctx, ref, runtimeValues); checkErr != nil {
		return "", checkErr
	}
	created := false
	defer func() {
		if err == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if created {
			err = errors.Join(err, e.DeleteRecovery(cleanup, p, b, operation))
			return
		}
		err = errors.Join(err, e.Secrets.Delete(cleanup, ref+runtimeFilesSuffix), e.deleteRuntime(cleanup, b.OrgID, b.EnvironmentID, name, ref))
	}()
	if checkErr := e.keepRuntimeFiles(ctx, ref, rendered); checkErr != nil {
		return "", checkErr
	}
	inst, err := e.Ctrl.Instances.Create(ctx, instance.CreateRequest{Name: name, ProviderName: e.Register(b.OrgID, b.ConnectionID), Services: []cpprovider.ServiceSpec{spec}, Labels: labels})
	// A failed create may still leave an instance discoverable by its slug.
	created = true
	if err != nil {
		return "", err
	}
	if checkErr := native.RestoreDatabase(ctx, inst.ID, labels, files, object); checkErr != nil {
		return "", checkErr
	}
	if checkErr := native.ConfigureDatabase(ctx, inst.ID, labels, reader); checkErr != nil {
		return "", checkErr
	}
	if checkErr := native.DatabaseProbe(ctx, inst.ID, labels); checkErr != nil {
		return "", checkErr
	}
	return inst.ID.String(), nil
}

func closeVerified(file *os.File) error { return errors.Join(file.Close(), os.Remove(file.Name())) }
