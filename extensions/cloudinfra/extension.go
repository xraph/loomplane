// Package cloudinfra registers organization server inventory and durable operations.
package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	troveext "github.com/xraph/trove/extension"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	connectionsext "github.com/xraph/loomplane/extensions/connections"
	fleetext "github.com/xraph/loomplane/extensions/fleets"
	"github.com/xraph/loomplane/extensions/identity"
	notificationsext "github.com/xraph/loomplane/extensions/notifications"
	projectsext "github.com/xraph/loomplane/extensions/projects"
	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/currentauthority"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/pglock"
	"github.com/xraph/loomplane/internal/security"
)

const Name = "loomplane-cloud-infrastructure"

// newService builds the service with the operator's capacity gates taken
// from config, so a gate can't be left at its zero value by accident.
func newService(cfg config.CloudsConfig, d cloudinfra.Deps) *cloudinfra.Service {
	d.ClusterCapacity = cfg.ClusterCapacity
	d.ManagedClusters = cfg.ManagedClusters
	d.EnableAutomaticCapacity = cfg.AutomaticCapacity.Enabled
	return cloudinfra.NewService(d)
}

// registrationSweepEvery spaces renewal sweeps; each one reads a domain
// back from the registrar at most daily.
const registrationSweepEvery = time.Hour

type Extension struct {
	*forge.BaseExtension
	cfg        config.Config
	workflows  *orchestration.Deployments
	streams    server.NodeStreams
	svc        *cloudinfra.Service
	cancel     context.CancelFunc
	done       chan struct{}
	namespaces namespaceShutdown
}

// New returns the extension. streams reaches joined nodes, so a server whose
// connection is a node's is inspected through its streams.
func New(cfg config.Config, workflows *orchestration.Deployments, streams server.NodeStreams) *Extension {
	return &Extension{BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Cloud server infrastructure"), cfg: cfg, workflows: workflows, streams: streams}
}
func (*Extension) Dependencies() []string {
	return []string{fleetext.Name, troveext.ExtensionName, connectionsext.Name, identity.Name, projectsext.Name, notificationsext.DeliveryName}
}
func (e *Extension) Register(app forge.App) error {
	if err := e.BaseExtension.Register(app); err != nil {
		return err
	}
	c := app.Container()
	db, err := forge.InjectType[*grove.DB](c)
	if err != nil {
		return err
	}
	authz, err := forge.InjectType[*security.Authorizer](c)
	if err != nil {
		return err
	}
	guard, err := forge.InjectType[identity.Guard](c)
	if err != nil {
		return err
	}
	creds, err := forge.InjectType[*connections.Service](c)
	if err != nil {
		return err
	}
	audit, err := forge.InjectType[*chronicle.Chronicle](c)
	if err != nil {
		return err
	}
	notices, err := forge.InjectType[*notifications.Service](c)
	if err != nil {
		return err
	}
	key, err := security.DeriveKey(e.cfg.SecretKey, connections.VaultKeyLabel)
	if err != nil {
		return err
	}
	enc, err := crypto.NewEncryptor(key)
	if err != nil {
		return err
	}
	secrets := connections.NewVaultSecrets(secret.NewService(vaultpg.New(db), enc, secret.WithAppID(connections.VaultAppID)))
	pg := pgdriver.Unwrap(db)
	store := cloudinfra.NewPgStore(pg)
	instances, namespaces, namespaceErr := fleetNamespaceDependencies(c, store, e.cfg.Identity.AppID)
	if namespaceErr != nil {
		return namespaceErr
	}
	e.namespaces = namespaces
	// Server proxies order certificates from the configured directory too.
	issuer := server.ACMEIssuer{Directory: e.cfg.Clouds.ACMEDirectory}
	if e.cfg.Clouds.ACMERootsFile != "" {
		if issuer.Roots, err = os.ReadFile(e.cfg.Clouds.ACMERootsFile); err != nil {
			return fmt.Errorf("read clouds.acme_roots_file: %w", err)
		}
	}
	if err = server.SetACMEIssuer(issuer); err != nil {
		return fmt.Errorf("clouds.acme_directory: %w", err)
	}
	endpoint := e.cfg.Clouds.EnrollmentURL
	if endpoint == "" {
		endpoint = e.cfg.PublicURL
	}
	principal, err := forge.InjectType[security.PrincipalVerifier](c)
	if err != nil {
		return err
	}
	proofKey, err := kubernetes.ReplicaProofKey(e.cfg.SecretKey)
	if err != nil {
		return err
	}
	e.svc = newService(e.cfg.Clouds, cloudinfra.Deps{InstanceAuthority: instances, InstanceNamespaces: namespaces, ReplicaVerifier: kubernetes.NewControllerStore(pg).WithReplicaProof(proofKey), Clusters: store, ClusterWorkflow: e.workflows, CapacityPrincipal: principal, CapacityDemandWakeup: e.workflows, Store: store, Clouds: cloudprofiles.NewPgStore(pg), Connections: creds, RuntimeConnections: connections.NewPgStore(pg), Secrets: secrets, Authz: authz, Runtime: cloudinfra.HostRuntime{Streams: e.streams}, NodeStreams: e.streams, Nodes: &nodes.Adopter{Connections: creds}, NodeConnections: creds, NodeAddresses: creds, Adapters: cloudinfra.BuiltinAdapters(cloudinfra.Endpoints{DigitalOcean: e.cfg.Clouds.DigitalOceanEndpoint, Vercel: e.cfg.Clouds.VercelEndpoint, Cloudflare: e.cfg.Clouds.CloudflareEndpoint, CloudflareRegistrarSandbox: e.cfg.Clouds.CloudflareRegistrarSandbox, Lock: pglock.For(pg)}), Workflow: e.workflows, Notices: notices, EnrollmentURL: endpoint, ACMEDirectory: e.cfg.Clouds.ACMEDirectory, Audit: projectsext.NewAuditor(audit, e.cfg.Identity.AppID, e.Logger()),
		Warn: func(msg string, err error) { e.Logger().Warn(msg, forge.F("error", err.Error())) }})
	creds.ManagedCredentials = e.svc
	files, filesErr := forge.InjectType[*backupfiles.Store](c)
	if filesErr != nil {
		return filesErr
	}
	e.svc.RegistryFiles = files
	e.workflows.BindInfrastructure(e.svc)
	e.workflows.BindClusters(e.svc)
	if err = forge.ProvideValue(c, e.svc); err != nil {
		return fmt.Errorf("provide cloud infrastructure: %w", err)
	}
	return e.routes(app.Router(), guard)
}
func (e *Extension) Start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.done = make(chan struct{})
	go func() {
		defer close(e.done)
		registryDone := make(chan struct{})
		go func() {
			defer close(registryDone)
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				if err := e.svc.RecoverManagedRegistries(ctx); err != nil && ctx.Err() == nil {
					e.Logger().Warn("Reconcile managed registries", forge.F("error", err.Error()))
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
		defer func() { <-registryDone }()
		// A node's address change that didn't finish is picked up here, on
		// its own loop: one node can take a while.
		addressesDone := make(chan struct{})
		go func() {
			defer close(addressesDone)
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				if err := e.svc.ReconcileNodeAddresses(ctx); err != nil && ctx.Err() == nil {
					e.Logger().Warn("Follow nodes to their addresses", forge.F("error", err.Error()))
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
		defer func() { <-addressesDone }()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		var swept time.Time
		for {
			if err := e.svc.RecoverEdges(ctx); err != nil && ctx.Err() == nil {
				e.Logger().Warn("Recover CDN operations", forge.F("error", err.Error()))
			}
			if err := e.svc.RecoverRoutes(ctx); err != nil && ctx.Err() == nil {
				e.Logger().Warn("Recover domain route operations", forge.F("error", err.Error()))
			}
			if err := e.svc.RecoverClusterOperations(ctx); err != nil && ctx.Err() == nil {
				e.Logger().Warn("Recover cluster operations", forge.F("error", err.Error()))
			}
			if err := e.svc.RecoverInfrastructure(ctx); err != nil && ctx.Err() == nil {
				e.Logger().Warn("Recover infrastructure operations", forge.F("error", err.Error()))
			}
			if err := e.workflows.RecoverCapacity(ctx); err != nil && ctx.Err() == nil {
				e.Logger().Warn("recover capacity workflow wakes", forge.F("error", err.Error()))
			}
			if err := e.svc.ReconcileExpansions(ctx); err != nil && ctx.Err() == nil {
				e.Logger().Warn("Recover capacity expansions", forge.F("error", err.Error()))
			}
			if err := e.svc.RecoverRegistrations(ctx); err != nil && ctx.Err() == nil {
				e.Logger().Warn("Recover domain registrations", forge.F("error", err.Error()))
			}
			// Each managed zone waiting on its TXT proof is looked at no more
			// than every two minutes, however often this runs, and each
			// verified zone's record once a day.
			if err := e.svc.CheckZoneProofs(ctx); err != nil && ctx.Err() == nil {
				e.Logger().Warn("Check domain zone proofs", forge.F("error", err.Error()))
			}
			if time.Since(swept) >= registrationSweepEvery {
				swept = time.Now()
				if err := e.svc.SweepRegistrations(ctx); err != nil && ctx.Err() == nil {
					e.Logger().Warn("Sweep domain registrations", forge.F("error", err.Error()))
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	e.MarkStarted()
	return nil
}

type namespaceShutdown interface {
	Shutdown(context.Context) error
}

func (e *Extension) Stop(ctx context.Context) error {
	if e.namespaces != nil {
		closing, cancel := context.WithCancel(context.Background())
		cancel()
		// Refuse new namespace work before workers begin draining. The final join
		// below collects retained cleanup errors after all owned work has settled.
		_ = e.namespaces.Shutdown(closing) //nolint:errcheck // The final drain below reports retained cleanup errors.
	}
	if e.cancel != nil {
		e.cancel()
	}
	// Existing audit or provider work may outlive the caller deadline. Keep
	// dependencies alive until the worker has actually finished.
	if e.done != nil {
		<-e.done
	}
	var drainErr error
	if e.namespaces != nil {
		// Forge continues dependency teardown after Stop errors. Preserve the main
		// pool until native operation and cleanup deadlines release all ownership.
		drainErr = e.namespaces.Shutdown(context.WithoutCancel(ctx))
	}
	if e.BaseExtension != nil {
		e.MarkStopped()
	}
	return errors.Join(ctx.Err(), drainErr)
}

// Identity provides the stable handle before startup installs current authority.
func fleetNamespaceDependencies(c forge.Container, store *cloudinfra.PgStore, appID string) (*fleets.Service, *cloudinfra.InstanceNamespaceTransactions, error) {
	authority, err := forge.InjectType[*currentauthority.FleetAuthority](c)
	if err != nil || authority == nil {
		return nil, nil, fmt.Errorf("cloud infrastructure needs Fleet authority: %w", currentauthority.ErrUnavailable)
	}
	instances, err := forge.InjectType[*fleets.Service](c)
	if err != nil || instances == nil || store == nil || appID == "" {
		return nil, nil, fmt.Errorf("cloud infrastructure needs Fleet namespace dependencies: %w", currentauthority.ErrUnavailable)
	}
	return instances, cloudinfra.NewInstanceNamespaceTransactions(authority, instances, store, appID), nil
}
