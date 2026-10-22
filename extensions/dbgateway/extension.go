// Package dbgateway is the Forge extension for the database gateway's
// control plane: the endpoints each deployed gateway polls for its signed
// routing snapshot, reports to and asks for holds, and the project routes
// that list gateways and pause and resume their routes.
package dbgateway

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/extensions/identity"
	planningext "github.com/xraph/loomplane/extensions/planning"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/dbgateway/plane"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/security"
)

// Name is the extension's name.
const Name = "loomplane-dbgateway"

// Extension serves the gateway endpoints.
type Extension struct {
	*forge.BaseExtension
	cfg config.Config
	svc *dbgateway.Service
}

// New returns the extension.
func New(cfg config.Config) *Extension {
	return &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Database gateway control plane"),
		cfg:           cfg,
	}
}

// Dependencies implements forge.Extension. Planning owns the Vault service
// resource addresses are kept in; identity guards the project routes and
// keeps the audit trail.
func (e *Extension) Dependencies() []string {
	return []string{identity.Name, planningext.Name}
}

// Register builds the service and mounts the routes. Without a signing key
// the routes still mount, and answer 503.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	c := fapp.Container()
	db, err := forge.InjectType[*grove.DB](c)
	if err != nil {
		return fmt.Errorf("the database gateway needs the grove extension: %w", err)
	}
	secrets, err := forge.InjectType[*deployments.VaultResourceSecrets](c)
	if err != nil {
		return fmt.Errorf("the database gateway needs the planning extension's resource secrets: %w", err)
	}
	authz, err := forge.InjectType[*security.Authorizer](c)
	if err != nil {
		return fmt.Errorf("the database gateway needs the identity extension: %w", err)
	}
	guard, err := forge.InjectType[identity.Guard](c)
	if err != nil {
		return fmt.Errorf("the database gateway needs the identity extension: %w", err)
	}
	audit, err := forge.InjectType[*chronicle.Chronicle](c)
	if err != nil {
		return fmt.Errorf("the database gateway needs the audit trail: %w", err)
	}
	var key ed25519.PrivateKey
	var trust plane.TrustKey
	if path := e.cfg.DBGateway.SigningKeyFile; path != "" {
		if key, trust, err = dbgateway.LoadSigningKey(path); err != nil {
			return fmt.Errorf("dbgateway.signing_key_file: %w", err)
		}
	} else {
		e.Logger().Warn("dbgateway.signing_key_file isn't set: the database gateway endpoints answer 503")
	}
	e.svc = dbgateway.NewService(dbgateway.NewStore(pgdriver.Unwrap(db)), resourceURLs{secrets: secrets}, key, trust, time.Now)
	if key != nil {
		warnAboutKeyWindow(e.Logger(), e.svc.Ready(), trust, time.Now())
	}
	e.svc.Authz = authz
	e.svc.Audit = auditor{chronicle: audit, appID: e.cfg.Identity.AppID, log: e.Logger()}
	e.svc.OnSkip = func(gatewayID string, skipped []dbgateway.Skipped) {
		for _, s := range skipped {
			e.Logger().Warn("a database gateway route is left out of its snapshot",
				forge.F("gateway_id", gatewayID), forge.F("resource", s.Resource), forge.F("reason", s.Reason))
		}
	}
	// Deploying a gateway hands it the service's trust keys.
	if err := forge.ProvideValue(c, e.svc); err != nil {
		return fmt.Errorf("provide the database gateway service: %w", err)
	}
	if err := mount(fapp.Router(), &handlers{
		svc: e.svc, wait: pollWait, every: pollEvery, now: time.Now,
		warn: func(msg string, err error) { e.Logger().Warn(msg, forge.F("error", err.Error())) },
	}); err != nil {
		return err
	}
	return mountManagement(fapp.Router(), guard, e.svc)
}

// keyExpiryWarning is how soon before the signing key expires Loomplane
// starts warning about it.
const keyExpiryWarning = 30 * 24 * time.Hour

// warnAboutKeyWindow logs, at startup, a signing key that can't sign now,
// which leaves the gateway endpoints answering 503 and gateway deploys
// failing, or one that expires within 30 days. ready is the service's Ready.
func warnAboutKeyWindow(log forge.Logger, ready error, trust plane.TrustKey, now time.Time) {
	switch {
	case errors.Is(ready, dbgateway.ErrSigningKeyExpired):
		log.Error("the database gateway signing key in dbgateway.signing_key_file has expired: the database gateway endpoints answer 503 and deployments with a gateway fail until you replace it and restart",
			forge.F("not_after", trust.NotAfter.UTC().Format(time.RFC3339)))
	case errors.Is(ready, dbgateway.ErrSigningKeyNotYetValid):
		log.Error("the database gateway signing key in dbgateway.signing_key_file isn't valid yet: the database gateway endpoints answer 503 and deployments with a gateway fail until its not_before",
			forge.F("not_before", trust.NotBefore.UTC().Format(time.RFC3339)))
	case ready == nil && trust.NotAfter.Sub(now) < keyExpiryWarning:
		log.Warn("the database gateway signing key in dbgateway.signing_key_file expires within 30 days: after that the gateway endpoints answer 503 and deployed gateways keep the routes they have. A new key also needs each gateway redeployed, as docs/postgres-services.md describes",
			forge.F("not_after", trust.NotAfter.UTC().Format(time.RFC3339)))
	}
}

// Start has nothing to start.
func (e *Extension) Start(context.Context) error {
	e.MarkStarted()
	return nil
}

// auditor records route pauses and resumes in Chronicle, in the
// organization's stream: the route, its resource and new state, and the
// project, environment and gateway it belongs to. Never a key or a salt.
type auditor struct {
	chronicle *chronicle.Chronicle
	appID     string
	log       forge.Logger
}

var _ dbgateway.Auditor = auditor{}

func (a auditor) Record(ctx context.Context, action string, p security.Principal, gw *dbgateway.Gateway, route *dbgateway.RouteRecord, version uint64) {
	err := a.chronicle.Info(ctx, action, "db_gateway_route", route.ID).
		Category("database_gateways").AppID(a.appID).TenantID(p.OrgID).UserID(p.UserID).Outcome("success").
		Meta("project_id", gw.ProjectID).Meta("environment_id", gw.EnvironmentID).Meta("gateway_id", gw.ID).
		Meta("resource", route.Resource).Meta("state", route.State).Meta("snapshot_version", version).Record()
	if err != nil {
		a.log.Error("record audit event", forge.F("action", action), forge.F("error", err.Error()))
	}
}

// resourceURLs reads resource addresses from the Vault resource secrets the
// deployment runner writes them to.
type resourceURLs struct {
	secrets deployments.ResourceSecrets
}

var (
	_ dbgateway.ResourceURLs = resourceURLs{}
	_ dbgateway.URLVersions  = resourceURLs{}
)

// versionedSecrets is ResourceSecrets that can say which version of a
// credential it holds without reading it, as *deployments.VaultResourceSecrets
// does.
type versionedSecrets interface {
	Version(ctx context.Context, key string) (string, error)
}

// URLVersion implements dbgateway.URLVersions. Secrets that can't say fail
// it, and the service compiles every poll as it would without it.
func (u resourceURLs) URLVersion(ctx context.Context, org, environmentID, resource string) (string, error) {
	vs, ok := u.secrets.(versionedSecrets)
	if !ok {
		return "", errors.New("these resource secrets keep no versions")
	}
	v, err := vs.Version(ctx, deployments.ResourceURLKey(org, environmentID, resource))
	if errors.Is(err, deployments.ErrNoCredential) {
		return "", dbgateway.ErrNoURL
	}
	return v, err
}

func (u resourceURLs) URL(ctx context.Context, org, environmentID, resource string) (string, error) {
	v, err := u.secrets.Get(ctx, deployments.ResourceURLKey(org, environmentID, resource))
	if errors.Is(err, deployments.ErrNoCredential) {
		return "", dbgateway.ErrNoURL
	}
	if err != nil {
		return "", err
	}
	return string(v), nil
}
