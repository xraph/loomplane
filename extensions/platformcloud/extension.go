// Package platformcloud is the Forge extension for Loomplane Cloud's control
// plane: the platform owner's console for regions, hosts, tenants and
// settings, a workspace's own view of its use, and the jobs that keep it all
// in order (projections, host checks, cleanup, metering, reclaim and
// restore).
//
// The planning extension builds the one platformcloud.Service its planner
// and its platform deploy target share, and provides it; this extension
// gives that same service its credentials, its organization list, the
// instance control its suspensions and cleanups act through, and the
// metering gate and meter.
package platformcloud

import (
	"context"
	"fmt"

	"github.com/xraph/chronicle"
	"github.com/xraph/dispatch/cron"
	"github.com/xraph/dispatch/engine"
	dispatchext "github.com/xraph/dispatch/extension"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	"github.com/xraph/loomplane/extensions/identity"
	planningext "github.com/xraph/loomplane/extensions/planning"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/instance"
	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/platformcloud"
	"github.com/xraph/loomplane/internal/security"
)

// Name is the extension's name.
const Name = "loomplane-platform-cloud"

// Extension serves the platform Cloud API and schedules its jobs.
type Extension struct {
	*forge.BaseExtension
	cfg       config.Config
	engineOf  func() *engine.Engine
	container forge.Container
	svc       *platformcloud.Service
	instances *instances
}

// New returns the extension. engineOf returns the dispatch extension's
// engine, which runs the jobs.
func New(cfg config.Config, engineOf func() *engine.Engine) *Extension {
	return &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Loomplane Cloud control plane"),
		cfg:           cfg,
		engineOf:      engineOf,
	}
}

// Dependencies implements forge.Extension. Identity guards the routes and
// audits; planning provides the shared service and the platform deploy
// target; dispatch runs the jobs.
func (e *Extension) Dependencies() []string {
	return []string{identity.Name, planningext.Name, dispatchext.ExtensionName}
}

// Register configures the shared service, registers the jobs and mounts the
// routes.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	c := fapp.Container()
	e.container = c
	db, err := forge.InjectType[*grove.DB](c)
	if err != nil {
		return fmt.Errorf("platform cloud needs the grove extension: %w", err)
	}
	guard, err := forge.InjectType[identity.Guard](c)
	if err != nil {
		return fmt.Errorf("platform cloud needs the identity extension: %w", err)
	}
	platformUser, err := forge.InjectType[identity.PlatformUser](c)
	if err != nil {
		return fmt.Errorf("platform cloud needs the identity extension: %w", err)
	}
	authz, err := forge.InjectType[*security.Authorizer](c)
	if err != nil {
		return fmt.Errorf("platform cloud needs the identity extension: %w", err)
	}
	audit, err := forge.InjectType[*chronicle.Chronicle](c)
	if err != nil {
		return fmt.Errorf("platform cloud needs the audit trail: %w", err)
	}
	svc, err := forge.InjectType[*platformcloud.Service](c)
	if err != nil {
		return fmt.Errorf("platform cloud needs the planning extension's platform Cloud service: %w", err)
	}
	target, err := forge.InjectType[*deployments.PlatformTarget](c)
	if err != nil {
		return fmt.Errorf("platform cloud needs the planning extension's platform target: %w", err)
	}
	if e.engineOf() == nil {
		return fmt.Errorf("platform cloud needs the dispatch extension to have registered")
	}
	pg := pgdriver.Unwrap(db)

	// Host client keys and the registry and DNS credentials share the
	// connection credentials' key but live under a Vault app of their own,
	// as the platform's sign-in apps do.
	key, err := security.DeriveKey(e.cfg.SecretKey, connections.VaultKeyLabel)
	if err != nil {
		return err
	}
	enc, err := crypto.NewEncryptor(key)
	if err != nil {
		return fmt.Errorf("vault encryptor: %w", err)
	}
	secrets := connections.NewVaultAppSecretsFor(
		secret.NewService(vaultpg.New(db), enc, secret.WithAppID(connections.PlatformCloudVaultAppID)), connections.PlatformCloudVaultAppID)
	orgs := orgLister{pg: pg, appID: e.cfg.Identity.AppID}
	e.instances = newInstances(svc, target, pg)
	e.svc = svc.WithSecrets(secrets).WithOrgs(orgs.ids).WithOrgExists(orgs.exists).WithInstances(e.instances)

	for _, j := range jobs(svc) {
		j.register(e.engineOf())
	}
	r := &routes{
		svc: svc, settings: instance.New(pg), mover: e.instances, authz: authz, isPlatform: platformUser,
		audit:    chronicleAuditor{chronicle: audit, appID: e.cfg.Identity.AppID, log: e.Logger()},
		orgNames: orgs.names,
	}
	return r.mount(fapp.Router(), guard)
}

// Start gives the service the metering gate and meter, the one place they
// are chosen, and schedules the jobs, which run on the cron leader only. An
// edition that bills provides a metering.Gate and metering.Meter; one that
// doesn't lets everything run and records nothing.
func (e *Extension) Start(ctx context.Context) error {
	gate, meter := metering.AllowAll, metering.Discard
	if forge.HasType[metering.Gate](e.container) {
		g, err := forge.InjectType[metering.Gate](e.container)
		if err != nil {
			return err
		}
		gate = g
	}
	if forge.HasType[metering.Meter](e.container) {
		m, err := forge.InjectType[metering.Meter](e.container)
		if err != nil {
			return err
		}
		meter = m
	}
	e.svc.WithGate(gate).WithMeter(meter)
	for _, j := range jobs(e.svc) {
		if err := j.schedule(ctx, e.engineOf()); err != nil {
			return fmt.Errorf("schedule %s: %w", j.cron, err)
		}
	}
	e.MarkStarted()
	return nil
}

// Stop implements forge.Extension. Nothing runs in the background: every
// start and move waits for its effect.
func (e *Extension) Stop(context.Context) error {
	e.MarkStopped()
	return nil
}

// cronJob is one of the platform Cloud's scheduled jobs.
type cronJob struct {
	cron, every, name string
	run               func(ctx context.Context) error
}

// tick is the payload of every platform Cloud job: each run covers
// everything.
type tick struct{}

func (j cronJob) register(eng *engine.Engine) {
	run := j.run
	engine.Register(eng, jobDefinition(j.name, func(ctx context.Context, _ tick) error { return run(ctx) }))
}

func (j cronJob) schedule(ctx context.Context, eng *engine.Engine) error {
	return engine.RegisterCron(ctx, eng, &cron.Definition[tick]{Name: j.cron, Schedule: j.every, JobName: j.name, Queue: jobQueue})
}

// orgLister lists the organizations of Loomplane's authsome app.
type orgLister struct {
	pg    *pgdriver.PgDB
	appID string
}

func (o orgLister) ids(ctx context.Context) ([]string, error) {
	rows, err := o.pg.Query(ctx, `SELECT id FROM authsome_organizations WHERE app_id=$1 ORDER BY id`, o.appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if checkErr := rows.Scan(&id); checkErr != nil {
			return nil, checkErr
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// exists reports whether the organization is still one of the app's.
func (o orgLister) exists(ctx context.Context, orgID string) (bool, error) {
	var on bool
	err := o.pg.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authsome_organizations WHERE id=$1 AND app_id=$2)`, orgID, o.appID).Scan(&on)
	return on, err
}

func (o orgLister) names(ctx context.Context) (map[string]string, error) {
	rows, err := o.pg.Query(ctx, `SELECT id,name FROM authsome_organizations WHERE app_id=$1`, o.appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if checkErr := rows.Scan(&id, &name); checkErr != nil {
			return nil, checkErr
		}
		out[id] = name
	}
	return out, rows.Err()
}

// chronicleAuditor records the platform owner's changes in Chronicle, at
// the app level like the platform's sign-in apps: the actor, what changed
// and how, never a credential.
type chronicleAuditor struct {
	chronicle *chronicle.Chronicle
	appID     string
	log       forge.Logger
}

func (a chronicleAuditor) Record(ctx context.Context, action, userID, resource, resourceID, outcome string, meta map[string]string) {
	b := a.chronicle.Warning(ctx, action, resource, resourceID).
		Category("platform_cloud").AppID(a.appID).UserID(userID).Outcome(outcome)
	for k, v := range meta {
		b = b.Meta(k, v)
	}
	if err := b.Record(); err != nil {
		a.log.Error("record audit event", forge.F("action", action), forge.F("error", err.Error()))
	}
}
