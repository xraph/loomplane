package octopusloom

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	"go.jetify.com/typeid/v2"
)

const pgUniqueViolation = "23505"

// PgStore is the Postgres Store. The schema is migration group "loomplane"
// (database/migrations).
type PgStore struct {
	db  *pgdriver.PgDB
	now func() time.Time
}

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db, now: time.Now} }

var _ Store = (*PgStore)(nil)

const gatewayColumns = `org_id, project_id, environment_id, resource, provider, namespace, farp_hostnames,
	generation, applied_generation, state, revision, updated_at`

const routeColumns = `id, org_id, project_id, environment_id, resource, hostname, path, methods, service, port,
	strip_prefix, note, revision, created_by, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanGateway(row scanner) (*Gateway, error) {
	var (
		g     Gateway
		state []byte
	)
	if err := row.Scan(&g.OrgID, &g.ProjectID, &g.EnvironmentID, &g.Resource, &g.Provider, &g.Namespace,
		&g.FarpHostnames, &g.Generation, &g.AppliedGeneration, &state, &g.Revision, &g.UpdatedAt); err != nil {
		return nil, err
	}
	if g.FarpHostnames == nil {
		g.FarpHostnames = []string{}
	}
	if err := json.Unmarshal(state, &g.State); err != nil {
		return nil, fmt.Errorf("decode gateway sync state: %w", err)
	}
	return &g, nil
}

func scanRoute(row scanner) (*Route, error) {
	var r Route
	if err := row.Scan(&r.ID, &r.OrgID, &r.ProjectID, &r.EnvironmentID, &r.Resource, &r.Hostname, &r.Path,
		&r.Methods, &r.Service, &r.Port, &r.StripPrefix, &r.Note, &r.Revision, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	if r.Methods == nil {
		r.Methods = []string{}
	}
	return &r, nil
}

func rollback(tx driver.Tx) { _ = tx.Rollback() } //nolint:errcheck // rollback is a no-op after commit

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// EnsureGateway implements Store.
func (s *PgStore) EnsureGateway(ctx context.Context, g *Gateway) error {
	if g.Provider != ProviderServer && g.Provider != ProviderKubernetes {
		return fmt.Errorf("%w: a gateway runs on %q or %q, not %q", ErrInvalid, ProviderServer, ProviderKubernetes, g.Provider)
	}
	// The row's updated_at moves only when the provider or namespace does.
	got, err := scanGateway(s.db.QueryRow(ctx, `INSERT INTO loomplane_octopus_gateways AS g
			(org_id, project_id, environment_id, resource, provider, namespace, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (org_id, environment_id, resource) DO UPDATE SET
			provider = EXCLUDED.provider,
			namespace = EXCLUDED.namespace,
			updated_at = CASE WHEN g.provider IS DISTINCT FROM EXCLUDED.provider OR g.namespace IS DISTINCT FROM EXCLUDED.namespace
				THEN EXCLUDED.updated_at ELSE g.updated_at END
		RETURNING `+gatewayColumns,
		g.OrgID, g.ProjectID, g.EnvironmentID, g.Resource, g.Provider, g.Namespace, s.now().UTC()))
	if err != nil {
		return fmt.Errorf("ensure octopus gateway: %w", err)
	}
	*g = *got
	return nil
}

// Gateway implements Store.
func (s *PgStore) Gateway(ctx context.Context, ref GatewayRef) (*Gateway, error) {
	g, err := scanGateway(s.db.QueryRow(ctx, `SELECT `+gatewayColumns+` FROM loomplane_octopus_gateways
		WHERE org_id = $1 AND environment_id = $2 AND resource = $3`, ref.OrgID, ref.EnvironmentID, ref.Resource))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get octopus gateway: %w", err)
	}
	return g, nil
}

// Gateways implements Store.
func (s *PgStore) Gateways(ctx context.Context) ([]*Gateway, error) {
	return s.listGateways(ctx, `SELECT `+gatewayColumns+` FROM loomplane_octopus_gateways
		ORDER BY org_id, environment_id, resource`)
}

// GatewaysIn implements Store.
func (s *PgStore) GatewaysIn(ctx context.Context, orgID, environmentID string) ([]*Gateway, error) {
	return s.listGateways(ctx, `SELECT `+gatewayColumns+` FROM loomplane_octopus_gateways
		WHERE org_id = $1 AND environment_id = $2 ORDER BY resource`, orgID, environmentID)
}

func (s *PgStore) listGateways(ctx context.Context, query string, args ...any) ([]*Gateway, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list octopus gateways: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Gateway{}
	for rows.Next() {
		g, err := scanGateway(rows)
		if err != nil {
			return nil, fmt.Errorf("scan octopus gateway: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list octopus gateways: %w", err)
	}
	return out, nil
}

// SetFarpHostnames implements Store.
func (s *PgStore) SetFarpHostnames(ctx context.Context, ref GatewayRef, hostnames []string, revision int64) (*Gateway, error) {
	g, err := scanGateway(s.db.QueryRow(ctx, `UPDATE loomplane_octopus_gateways
		SET farp_hostnames = $4, generation = generation + 1, revision = revision + 1, updated_at = $5
		WHERE org_id = $1 AND environment_id = $2 AND resource = $3 AND revision = $6
		RETURNING `+gatewayColumns, ref.OrgID, ref.EnvironmentID, ref.Resource, nonNil(hostnames), s.now().UTC(), revision))
	if errors.Is(err, pgx.ErrNoRows) {
		_, getErr := s.Gateway(ctx, ref)
		if getErr == nil {
			return nil, ErrStale
		}
		return nil, getErr
	}
	if err != nil {
		return nil, fmt.Errorf("set octopus farp hostnames: %w", err)
	}
	return g, nil
}

// SaveSyncState implements Store.
func (s *PgStore) SaveSyncState(ctx context.Context, ref GatewayRef, applied int64, state SyncState) error {
	body, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode gateway sync state: %w", err)
	}
	tag, err := s.db.Exec(ctx, `UPDATE loomplane_octopus_gateways SET applied_generation = $4, state = $5::jsonb
		WHERE org_id = $1 AND environment_id = $2 AND resource = $3`,
		ref.OrgID, ref.EnvironmentID, ref.Resource, applied, string(body))
	if err != nil {
		return fmt.Errorf("save octopus sync state: %w", err)
	}
	if n, err := tag.RowsAffected(); err != nil {
		return fmt.Errorf("octopus rows affected: %w", err)
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// BumpGeneration implements Store.
func (s *PgStore) BumpGeneration(ctx context.Context, ref GatewayRef) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if bumpErr := s.bump(ctx, tx, ref); bumpErr != nil {
		return bumpErr
	}
	return tx.Commit()
}

// bump raises the gateway's generation inside tx. It also takes the gateway
// row's lock, so route changes on one gateway apply one at a time.
func (s *PgStore) bump(ctx context.Context, tx driver.Tx, ref GatewayRef) error {
	tag, err := tx.Exec(ctx, `UPDATE loomplane_octopus_gateways SET generation = generation + 1, updated_at = $4
		WHERE org_id = $1 AND environment_id = $2 AND resource = $3`,
		ref.OrgID, ref.EnvironmentID, ref.Resource, s.now().UTC())
	if err != nil {
		return fmt.Errorf("bump octopus generation: %w", err)
	}
	if n, err := tag.RowsAffected(); err != nil {
		return fmt.Errorf("octopus rows affected: %w", err)
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteGateway implements Store.
func (s *PgStore) DeleteGateway(ctx context.Context, ref GatewayRef) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM loomplane_octopus_gateways
		WHERE org_id = $1 AND environment_id = $2 AND resource = $3`, ref.OrgID, ref.EnvironmentID, ref.Resource); err != nil {
		return fmt.Errorf("delete octopus gateway: %w", err)
	}
	return nil
}

// Routes implements Store.
func (s *PgStore) Routes(ctx context.Context, ref GatewayRef) ([]*Route, error) {
	rows, err := s.db.Query(ctx, `SELECT `+routeColumns+` FROM loomplane_octopus_routes
		WHERE org_id = $1 AND environment_id = $2 AND resource = $3 ORDER BY hostname, path, id`,
		ref.OrgID, ref.EnvironmentID, ref.Resource)
	if err != nil {
		return nil, fmt.Errorf("list octopus routes: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Route{}
	for rows.Next() {
		r, err := scanRoute(rows)
		if err != nil {
			return nil, fmt.Errorf("scan octopus route: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list octopus routes: %w", err)
	}
	return out, nil
}

// CreateRoute implements Store.
func (s *PgStore) CreateRoute(ctx context.Context, r *Route) error {
	if r.ID == "" {
		id, err := typeid.Generate("octoroute")
		if err != nil {
			return fmt.Errorf("generate octopus route id: %w", err)
		}
		r.ID = id.String()
	}
	now := s.now().UTC()
	r.Revision, r.CreatedAt, r.UpdatedAt = 1, now, now
	r.Methods = nonNil(r.Methods)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if bumpErr := s.bump(ctx, tx, r.GatewayRef); bumpErr != nil {
		return bumpErr
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_octopus_routes (`+routeColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		r.ID, r.OrgID, r.ProjectID, r.EnvironmentID, r.Resource, r.Hostname, r.Path, r.Methods, r.Service, r.Port,
		r.StripPrefix, r.Note, r.Revision, r.CreatedBy, r.CreatedAt, r.UpdatedAt)
	if isUniqueViolation(err) {
		return pathTaken(r.Path, r.Hostname)
	}
	if err != nil {
		return fmt.Errorf("insert octopus route: %w", err)
	}
	return tx.Commit()
}

func pathTaken(path, hostname string) error {
	return conflictf("A route for %s on %s already exists. Edit it instead.", path, hostLabelFor(hostname))
}

// UpdateRoute implements Store.
func (s *PgStore) UpdateRoute(ctx context.Context, r *Route) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if bumpErr := s.bump(ctx, tx, r.GatewayRef); bumpErr != nil {
		return bumpErr
	}
	var (
		revision  int64
		projectID string
		createdBy string
		createdAt time.Time
		updatedAt = s.now().UTC()
	)
	err = tx.QueryRow(ctx, `UPDATE loomplane_octopus_routes
		SET hostname = $5, path = $6, methods = $7, service = $8, port = $9, strip_prefix = $10, note = $11,
			revision = revision + 1, updated_at = $12
		WHERE id = $1 AND org_id = $2 AND environment_id = $3 AND resource = $4 AND revision = $13
		RETURNING revision, project_id, created_by, created_at`,
		r.ID, r.OrgID, r.EnvironmentID, r.Resource, r.Hostname, r.Path, nonNil(r.Methods), r.Service, r.Port,
		r.StripPrefix, r.Note, updatedAt, r.Revision).Scan(&revision, &projectID, &createdBy, &createdAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return s.missingOrStale(ctx, r.GatewayRef, r.ID)
	case isUniqueViolation(err):
		return pathTaken(r.Path, r.Hostname)
	case err != nil:
		return fmt.Errorf("update octopus route: %w", err)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return commitErr
	}
	r.Revision, r.ProjectID, r.CreatedBy, r.CreatedAt, r.UpdatedAt = revision, projectID, createdBy, createdAt, updatedAt
	r.Methods = nonNil(r.Methods)
	return nil
}

// DeleteRoute implements Store.
func (s *PgStore) DeleteRoute(ctx context.Context, ref GatewayRef, id string, revision int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if bumpErr := s.bump(ctx, tx, ref); bumpErr != nil {
		return bumpErr
	}
	tag, err := tx.Exec(ctx, `DELETE FROM loomplane_octopus_routes
		WHERE id = $1 AND org_id = $2 AND environment_id = $3 AND resource = $4 AND revision = $5`,
		id, ref.OrgID, ref.EnvironmentID, ref.Resource, revision)
	if err != nil {
		return fmt.Errorf("delete octopus route: %w", err)
	}
	if n, err := tag.RowsAffected(); err != nil {
		return fmt.Errorf("octopus rows affected: %w", err)
	} else if n == 0 {
		return s.missingOrStale(ctx, ref, id)
	}
	return tx.Commit()
}

// missingOrStale says why a revision-guarded write matched no row: the
// route is gone, or someone changed it since it was read.
func (s *PgStore) missingOrStale(ctx context.Context, ref GatewayRef, id string) error {
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM loomplane_octopus_routes
		WHERE id = $1 AND org_id = $2 AND environment_id = $3 AND resource = $4)`,
		id, ref.OrgID, ref.EnvironmentID, ref.Resource).Scan(&exists); err != nil {
		return fmt.Errorf("check octopus route: %w", err)
	}
	if exists {
		return ErrStale
	}
	return ErrNotFound
}

// syncLease is how long a sync holds its gateway's lease: longer than the
// longest sync (writeSyncTimeout and reconcileSyncTimeout, 10 seconds) and
// the save after it (saveTimeout, 5 seconds), so a lease runs out only
// when its holder died.
const syncLease = 30 * time.Second

// leaseReleaseTimeout bounds giving the lease back, which is done apart from
// the sync's own context so a sync that ran out of time still releases.
const leaseReleaseTimeout = 5 * time.Second

// WithGatewayLock implements Store. The lock is a lease on the gateway's
// row, taken and given back with one short statement each, so no
// connection is held while run talks to Octopus. A process that crashes
// holding it leaves the lease to run out after syncLease.
func (s *PgStore) WithGatewayLock(ctx context.Context, ref GatewayRef, run func() error) error {
	token := rand.Text()
	var took string
	err := s.db.QueryRow(ctx, `UPDATE loomplane_octopus_gateways
		SET sync_lease_token = $4, sync_lease_until = now() + make_interval(secs => $5)
		WHERE org_id = $1 AND environment_id = $2 AND resource = $3
			AND (sync_lease_until IS NULL OR sync_lease_until < now())
		RETURNING sync_lease_token`,
		ref.OrgID, ref.EnvironmentID, ref.Resource, token, syncLease.Seconds()).Scan(&took)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := s.Gateway(ctx, ref); getErr != nil {
			return getErr
		}
		return ErrBusy
	}
	if err != nil {
		return fmt.Errorf("lock octopus gateway: %w", err)
	}
	defer s.releaseLease(ctx, ref, token)
	return run()
}

// releaseLease gives back the lease token holds, if it still does. A lease
// that isn't given back runs out on its own, so a failure here only
// delays the next sync.
func (s *PgStore) releaseLease(ctx context.Context, ref GatewayRef, token string) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseReleaseTimeout)
	defer cancel()
	//nolint:errcheck // the lease expires by itself
	s.db.Exec(releaseCtx, `UPDATE loomplane_octopus_gateways SET sync_lease_until = NULL
		WHERE org_id = $1 AND environment_id = $2 AND resource = $3 AND sync_lease_token = $4`,
		ref.OrgID, ref.EnvironmentID, ref.Resource, token)
}
