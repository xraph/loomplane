package currentauthority

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"

	"github.com/xraph/go-utils/metrics"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/observability"
	"github.com/xraph/warden"
	wardenpg "github.com/xraph/warden/store/postgres"

	"github.com/xraph/loomplane/internal/security"
)

// builtInInstallation retains the sole pool pair and qualified startup identity.
// Both profiles capture this candidate before its single immutable publication.
type builtInInstallation struct {
	main          *pgdriver.PgDB
	backend       *nativeBackend
	engine        *warden.Engine
	dsnDigest     [32]byte
	appID, schema string
	owner         uint32
}

// ProfileStatus reports startup qualification without granting effect permission.
type ProfileStatus uint8

const (
	ProfileNotRequested ProfileStatus = iota
	ProfileReady
	ProfileUnavailable
)

// InstallationReport records the independently qualified startup profiles.
// A common connection or schema failure prevents all profile publication.
type InstallationReport struct {
	Ordinary ProfileStatus
	Fleet    ProfileStatus
}

// InstallBuiltInFleetProfile qualifies only Fleet authority at fixed startup.
func InstallBuiltInFleetProfile(ctx context.Context, c *Coordinator, main *pgdriver.PgDB, engine *warden.Engine, primaryDSN, appID string, telemetry *observability.Telemetry, sink metrics.Metrics) error {
	report, err := installBuiltInProfiles(ctx, c, main, engine, nil, primaryDSN, appID, false, false, telemetry, sink)
	if err != nil {
		return err
	}
	if report.Fleet != ProfileReady {
		return ErrUnavailable
	}
	return nil
}

// InstallBuiltInProfiles qualifies ordinary and Fleet authority before one
// immutable publication. Call it after migrations and identity bootstrap.
// Reinstallation requires a new coordinator and never replaces live profiles.
func InstallBuiltInProfiles(ctx context.Context, c *Coordinator, main *pgdriver.PgDB, engine *warden.Engine, authorizer *security.Authorizer, primaryDSN, appID string, requireEmail bool, telemetry *observability.Telemetry, sink metrics.Metrics) (InstallationReport, error) {
	return installBuiltInProfiles(ctx, c, main, engine, authorizer, primaryDSN, appID, requireEmail, true, telemetry, sink)
}

func installBuiltInProfiles(ctx context.Context, c *Coordinator, main *pgdriver.PgDB, engine *warden.Engine, authorizer *security.Authorizer, primaryDSN, appID string, requireEmail, requestOrdinary bool, telemetry *observability.Telemetry, sink metrics.Metrics) (report InstallationReport, err error) {
	report.Fleet = ProfileUnavailable
	if requestOrdinary {
		report.Ordinary = ProfileUnavailable
	}

	if c == nil || main == nil || engine == nil || telemetry == nil || strings.TrimSpace(primaryDSN) == "" || strings.TrimSpace(appID) == "" {
		return report, ErrUnavailable
	}
	router, ok := engine.Store().(*storeRouter)
	if !ok || router == nil || router.owner != c || router.ordinary == nil {
		return report, ErrUnavailable
	}
	p, err := captureEngineProfile(engine, router)
	if err != nil {
		return report, err
	}
	c.mu.Lock()
	if c.installing || c.stopping || c.closed || c.closing || c.backend != nil || c.profile != nil || len(c.active) != 0 {
		c.mu.Unlock()
		return report, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, AttemptLimit)
	c.installing, c.installCancel = true, cancel
	c.mu.Unlock()
	var owned *nativeBackend
	published := false
	defer func() {
		cancel()
		if !published {
			report.Fleet = ProfileUnavailable
			if requestOrdinary {
				report.Ordinary = ProfileUnavailable
			}
			if owned != nil {
				err = errors.Join(err, owned.close())
			}
		}
		c.mu.Lock()
		c.installing, c.installCancel = false, nil
		if c.changed != nil {
			close(c.changed)
			c.changed = nil
		}
		c.mu.Unlock()
	}()
	schema, owner, err := installationIdentity(ctx, main)
	if err != nil {
		return report, err
	}
	owned, err = openPools(ctx, primaryDSN, sink)
	if err != nil {
		return report, err
	}
	connections, err := inspectConnections(ctx, main, owned, schema)
	if err != nil {
		return report, err
	}
	if connections.RoleID != owner {
		return report, ErrUnavailable
	}
	queries := connectionQueries(main, owned)
	for _, query := range queries {
		if schemaErr := inspectOrdinarySchema(ctx, query, schema, owner); schemaErr != nil {
			return report, schemaErr
		}
	}
	candidate := &builtInInstallation{main: main, backend: owned, engine: engine, dsnDigest: sha256.Sum256([]byte(primaryDSN)), appID: appID, schema: schema, owner: owner}
	if requestOrdinary {
		if ordinary, qualificationErr := captureOrdinaryProfile(c, candidate, authorizer, requireEmail); qualificationErr == nil {
			p.ordinary = ordinary
			report.Ordinary = ProfileReady
		}
	}
	fleetReady := true
	for _, query := range queries {
		if qualificationErr := inspectFleetSchema(ctx, query, schema, owner); qualificationErr != nil {
			if !errors.Is(qualificationErr, errFleetSchemaUnsupported) {
				return report, qualificationErr
			}
			fleetReady = false
			break
		}
	}
	if fleetReady {
		p.fleet = &fleetProfile{appID: appID}
		report.Fleet = ProfileReady
	}
	if report.Ordinary != ProfileReady && report.Fleet != ProfileReady {
		return report, ErrUnavailable
	}
	if profileErr := p.verify(); profileErr != nil {
		return report, profileErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopping || c.closed || ctx.Err() != nil {
		return report, errors.Join(ErrUnavailable, ctx.Err())
	}
	c.installation = candidate
	c.backend, c.reader = owned, wardenpg.New(owned.readerDB)
	c.schema, c.telemetry, c.source, c.profile = schema, telemetry, fixedFleetSource, p
	published = true
	return report, nil
}

// Owner trust comes from the authenticated startup session, never from the
// catalog object whose ownership we are about to verify.
func installationIdentity(ctx context.Context, main *pgdriver.PgDB) (schema string, owner uint32, err error) {
	var role, session, firstSchema string
	var primary, writable bool
	err = main.QueryRow(ctx, `SELECT pg_catalog.current_schema(),current_user,session_user,
 (SELECT oid::pg_catalog.int8 FROM pg_catalog.pg_roles WHERE rolname OPERATOR(pg_catalog.=) current_user),
 NOT pg_catalog.pg_is_in_recovery(),NOT pg_catalog.current_setting('transaction_read_only')::pg_catalog.bool,
 (pg_catalog.current_schemas(true))[1]`).Scan(&schema, &role, &session, &owner, &primary, &writable, &firstSchema)
	if err != nil || schema == "" || role == "" || role != session || owner == 0 || !primary || !writable || firstSchema != "pg_catalog" {
		return "", 0, errors.Join(ErrUnavailable, err)
	}
	return schema, owner, nil
}

func fixedFleetSource(ctx context.Context, tx driver.Tx, _ Binding) error {
	_, err := tx.Exec(ctx, `SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('variable-sources/v1',0))`)
	return err
}

// FleetAuthority exposes only the typed Fleet entry point to domain consumers.
// Keep this handle stable from registration through startup and shutdown.
type FleetAuthority struct{ coordinator *Coordinator }

// FleetAuthority returns a stable dependency handle that stays unavailable until installation.
func (c *Coordinator) FleetAuthority() *FleetAuthority { return &FleetAuthority{coordinator: c} }

// RunFleet executes one typed Fleet operation through the owned coordinator.
func (f *FleetAuthority) RunFleet(ctx context.Context, binding FleetBinding, work func(context.Context, *Attempt) error) error {
	if f == nil {
		return ErrUnavailable
	}
	return f.coordinator.RunFleet(ctx, binding, work)
}

// FleetSchema returns the installed schema for fixed receipt-only persistence.
// It proves qualification of the exact trusted main database, not current key
// or grant permission. Effect-authorizing writes still require RunFleet.
func (c *Coordinator) FleetSchema(main *pgdriver.PgDB) (string, error) {
	return c.fleetSchema(main, "")
}

func (c *Coordinator) fleetSchema(main *pgdriver.PgDB, appID string) (string, error) {
	if c == nil || main == nil {
		return "", ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	installed := c.installation
	if c.installing || c.stopping || c.closed || c.closing || installed == nil || installed.main != main || installed.appID == "" || (appID != "" && appID != installed.appID) || installed.backend == nil || c.backend != installed.backend || c.schema == "" || c.schema != installed.schema || c.profile == nil || c.profile.mode != installedProfile || c.profile.fleet == nil || c.profile.fleet.appID != installed.appID || c.profile.engine != installed.engine {
		return "", ErrUnavailable
	}
	if err := c.profile.verify(); err != nil {
		return "", err
	}
	return installed.schema, nil
}

// FleetSchema exposes the same installed-schema provenance through the typed
// Fleet dependency. It does not grant effect permission or pin receipt lifetime.
func (f *FleetAuthority) FleetSchema(main *pgdriver.PgDB) (string, error) {
	if f == nil {
		return "", ErrUnavailable
	}
	return f.coordinator.FleetSchema(main)
}

// FleetSchemaForApp returns the receipt schema only for the exact application
// bound to the installed main database. It grants no current key permission.
func (f *FleetAuthority) FleetSchemaForApp(main *pgdriver.PgDB, appID string) (string, error) {
	if f == nil || appID == "" {
		return "", ErrUnavailable
	}
	return f.coordinator.fleetSchema(main, appID)
}
