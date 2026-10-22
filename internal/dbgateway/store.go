package dbgateway

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/drivers/pgdriver"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/dbgateway/plane"
)

var (
	// ErrNotFound is a gateway or route that doesn't exist, or belongs to
	// another organization.
	ErrNotFound = errors.New("database gateway not found")
	// ErrInvalid is input the store refuses before it reaches the database.
	ErrInvalid = errors.New("invalid database gateway input")
)

const (
	// liveWindow is how long after its last report an instance counts as live.
	liveWindow = 30 * time.Second
	// saltSize is the SCRAM salt length.
	saltSize = 16

	// Type ID prefixes for gateway and route ids, which the plane's id grammar
	// accepts as they are.
	gatewayPrefix = "dbgw"
	routePrefix   = "dbgr"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Gateway is one deployed database gateway. Its keys are public: the control
// plane verifies the gateway's requests with SigningPublicKey and seals
// secrets to SealingPublicKey. The private halves live in the gateway's
// environment and never come here.
type Gateway struct {
	OrgID, ID, ProjectID, EnvironmentID, Name, Provider, Region, Suffix string
	SigningPublicKey                                                    ed25519.PublicKey
	SealingPublicKey                                                    *ecdh.PublicKey
}

// RouteRecord is one opted-in Postgres resource on a gateway. Salt is minted
// once, so a route's SCRAM verifiers stay valid from one snapshot to the next.
type RouteRecord struct {
	ID, Resource, State string
	Salt                []byte
}

// InstanceRecord is the last report from one running gateway instance.
type InstanceRecord struct {
	InstanceID      string
	SnapshotVersion uint64
	Draining        bool
	Metrics         []plane.RouteStats
	LastSeenAt      time.Time
}

// Store keeps gateways, their routes, the latest snapshot version and each
// instance's last report in Postgres. The schema is migration group
// "loomplane" (database/migrations).
type Store struct {
	db *pgdriver.PgDB
}

// NewStore returns a Store over db.
func NewStore(db *pgdriver.PgDB) *Store { return &Store{db: db} }

const gatewayColumns = `org_id, id, project_id, environment_id, name, provider, region, suffix, signing_public_key, sealing_public_key`

type scanner interface{ Scan(dest ...any) error }

func scanGateway(row scanner) (Gateway, error) {
	var g Gateway
	var signing, sealing []byte
	if err := row.Scan(&g.OrgID, &g.ID, &g.ProjectID, &g.EnvironmentID, &g.Name, &g.Provider, &g.Region, &g.Suffix, &signing, &sealing); err != nil {
		return Gateway{}, err
	}
	if len(signing) != ed25519.PublicKeySize {
		return Gateway{}, fmt.Errorf("gateway %s has a %d byte signing key", g.ID, len(signing))
	}
	g.SigningPublicKey = ed25519.PublicKey(signing)
	var err error
	if g.SealingPublicKey, err = ecdh.X25519().NewPublicKey(sealing); err != nil {
		return Gateway{}, fmt.Errorf("gateway %s sealing key: %w", g.ID, err)
	}
	return g, nil
}

// newID returns a type ID like the other stores' ids, e.g. dbgw_01h....
func newID(prefix string) (string, error) {
	id, err := typeid.Generate(prefix)
	if err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return id.String(), nil
}

// missingGateway turns the foreign key failure of a write against a gateway
// that isn't there into ErrNotFound.
func missingGateway(err error, id string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return err
}

// EnsureGateway stores g when its environment has no gateway of that name
// and reports true. Otherwise it returns the stored gateway and false, and
// never touches the stored keys: they are minted once, so a gateway that is
// deployed again keeps the identity its running instances were given. An
// empty g.ID gets a generated one.
func (s *Store) EnsureGateway(ctx context.Context, g Gateway) (Gateway, bool, error) {
	switch {
	case g.OrgID == "" || g.ProjectID == "" || g.EnvironmentID == "" || g.Name == "":
		return Gateway{}, false, fmt.Errorf("%w: a gateway needs an organization, project, environment and name", ErrInvalid)
	case len(g.SigningPublicKey) != ed25519.PublicKeySize:
		return Gateway{}, false, fmt.Errorf("%w: the signing key must be %d bytes", ErrInvalid, ed25519.PublicKeySize)
	case g.SealingPublicKey == nil || g.SealingPublicKey.Curve() != ecdh.X25519():
		return Gateway{}, false, fmt.Errorf("%w: the sealing key must be an X25519 public key", ErrInvalid)
	}
	if g.ID == "" {
		id, err := newID(gatewayPrefix)
		if err != nil {
			return Gateway{}, false, err
		}
		g.ID = id
	}
	if !idPattern.MatchString(g.ID) {
		return Gateway{}, false, fmt.Errorf("%w: gateway id %q", ErrInvalid, g.ID)
	}
	res, err := s.db.Exec(ctx, `INSERT INTO loomplane_db_gateways (`+gatewayColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (org_id, environment_id, name) DO NOTHING`,
		g.OrgID, g.ID, g.ProjectID, g.EnvironmentID, g.Name, g.Provider, g.Region, g.Suffix,
		[]byte(g.SigningPublicKey), g.SealingPublicKey.Bytes())
	if err != nil {
		return Gateway{}, false, fmt.Errorf("insert database gateway: %w", err)
	}
	if n, rerr := res.RowsAffected(); rerr == nil && n == 1 {
		return g, true, nil
	}
	stored, err := scanGateway(s.db.QueryRow(ctx, `SELECT `+gatewayColumns+` FROM loomplane_db_gateways
		WHERE org_id = $1 AND environment_id = $2 AND name = $3`, g.OrgID, g.EnvironmentID, g.Name))
	if err != nil {
		return Gateway{}, false, fmt.Errorf("read database gateway: %w", err)
	}
	return stored, false, nil
}

// Gateway returns one gateway, or ErrNotFound.
func (s *Store) Gateway(ctx context.Context, org, id string) (*Gateway, error) {
	g, err := scanGateway(s.db.QueryRow(ctx, `SELECT `+gatewayColumns+` FROM loomplane_db_gateways
		WHERE org_id = $1 AND id = $2`, org, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("read database gateway: %w", err)
	}
	return &g, nil
}

// Gateways lists an organization's gateways, oldest first. A non-empty
// environmentID narrows the list to that environment.
func (s *Store) Gateways(ctx context.Context, org, environmentID string) ([]Gateway, error) {
	rows, err := s.db.Query(ctx, `SELECT `+gatewayColumns+` FROM loomplane_db_gateways
		WHERE org_id = $1 AND ($2 = '' OR environment_id = $2)
		ORDER BY created_at, id`, org, environmentID)
	if err != nil {
		return nil, fmt.Errorf("list database gateways: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []Gateway{}
	for rows.Next() {
		g, err := scanGateway(rows)
		if err != nil {
			return nil, fmt.Errorf("scan database gateway: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list database gateways: %w", err)
	}
	return out, nil
}

// EnsureRoute returns the gateway's route for resource, creating it active
// with a fresh random salt the first time. The salt never changes after.
func (s *Store) EnsureRoute(ctx context.Context, org, gatewayID, resource string) (RouteRecord, error) {
	if resource == "" {
		return RouteRecord{}, fmt.Errorf("%w: a route needs a resource", ErrInvalid)
	}
	id, err := newID(routePrefix)
	if err != nil {
		return RouteRecord{}, err
	}
	salt := make([]byte, saltSize)
	if _, err = rand.Read(salt); err != nil {
		return RouteRecord{}, fmt.Errorf("generate route salt: %w", err)
	}
	if _, err = s.db.Exec(ctx, `INSERT INTO loomplane_db_gateway_routes (org_id, gateway_id, id, resource, salt)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (org_id, gateway_id, resource) DO NOTHING`, org, gatewayID, id, resource, salt); err != nil {
		return RouteRecord{}, fmt.Errorf("insert database gateway route: %w", missingGateway(err, gatewayID))
	}
	var r RouteRecord
	err = s.db.QueryRow(ctx, `SELECT id, resource, state, salt FROM loomplane_db_gateway_routes
		WHERE org_id = $1 AND gateway_id = $2 AND resource = $3`, org, gatewayID, resource).Scan(&r.ID, &r.Resource, &r.State, &r.Salt)
	if err != nil {
		return RouteRecord{}, fmt.Errorf("read database gateway route: %w", err)
	}
	return r, nil
}

// SetRouteState pauses or resumes a route. The state is "active" or "paused".
func (s *Store) SetRouteState(ctx context.Context, org, gatewayID, routeID, state string) error {
	if state != "active" && state != "paused" {
		return fmt.Errorf("%w: route state %q", ErrInvalid, state)
	}
	res, err := s.db.Exec(ctx, `UPDATE loomplane_db_gateway_routes SET state = $4, updated_at = now()
		WHERE org_id = $1 AND gateway_id = $2 AND id = $3`, org, gatewayID, routeID, state)
	if err != nil {
		return fmt.Errorf("set database gateway route state: %w", err)
	}
	if n, rerr := res.RowsAffected(); rerr == nil && n == 0 {
		return fmt.Errorf("%w: route %s", ErrNotFound, routeID)
	}
	return nil
}

// Routes lists a gateway's routes by resource.
func (s *Store) Routes(ctx context.Context, org, gatewayID string) ([]RouteRecord, error) {
	rows, err := s.db.Query(ctx, `SELECT id, resource, state, salt FROM loomplane_db_gateway_routes
		WHERE org_id = $1 AND gateway_id = $2 ORDER BY resource, id`, org, gatewayID)
	if err != nil {
		return nil, fmt.Errorf("list database gateway routes: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []RouteRecord{}
	for rows.Next() {
		var r RouteRecord
		if err := rows.Scan(&r.ID, &r.Resource, &r.State, &r.Salt); err != nil {
			return nil, fmt.Errorf("scan database gateway route: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list database gateway routes: %w", err)
	}
	return out, nil
}

// BumpSnapshot records digest as the gateway's latest snapshot. The version
// goes up by one when the digest differs from the stored one and stays put
// when it doesn't; changed says which. It is one upsert, so callers racing
// with the same new digest bump the version once between them: the loser
// waits for the row lock, sees the digest already stored, and changes nothing.
func (s *Store) BumpSnapshot(ctx context.Context, org, gatewayID, digest string) (version uint64, changed bool, err error) {
	if digest == "" {
		return 0, false, fmt.Errorf("%w: a snapshot needs a digest", ErrInvalid)
	}
	var v int64
	err = s.db.QueryRow(ctx, `INSERT INTO loomplane_db_gateway_snapshots AS s (org_id, gateway_id, version, digest)
		VALUES ($1, $2, 1, $3)
		ON CONFLICT (org_id, gateway_id) DO UPDATE
			SET version = s.version + 1, digest = EXCLUDED.digest, created_at = now()
			WHERE s.digest <> EXCLUDED.digest
		RETURNING version`, org, gatewayID, digest).Scan(&v)
	switch {
	case err == nil:
		return uint64(v), true, nil
	case errors.Is(err, pgx.ErrNoRows):
		// The stored digest already matches, so nothing was written.
	default:
		return 0, false, fmt.Errorf("bump database gateway snapshot: %w", missingGateway(err, gatewayID))
	}
	if err = s.db.QueryRow(ctx, `SELECT version FROM loomplane_db_gateway_snapshots
		WHERE org_id = $1 AND gateway_id = $2`, org, gatewayID).Scan(&v); err != nil {
		return 0, false, fmt.Errorf("read database gateway snapshot: %w", err)
	}
	return uint64(v), false, nil
}

// Report stores an instance's report, replacing its last one, and returns how
// many of the gateway's instances reported in the 30 seconds up to now. now is
// the report's time and the clock the window is measured against, so a caller
// that passes its own gets the same answer whatever the database's clock says.
// A report older than the stored one for the same instance is ignored.
func (s *Store) Report(ctx context.Context, org, gatewayID string, r plane.Report, now time.Time) (live int, err error) {
	if r.InstanceID == "" {
		return 0, fmt.Errorf("%w: a report needs an instance id", ErrInvalid)
	}
	if r.SnapshotVersion > math.MaxInt64 {
		return 0, fmt.Errorf("%w: snapshot version %d", ErrInvalid, r.SnapshotVersion)
	}
	routes := r.Routes
	if routes == nil {
		routes = []plane.RouteStats{}
	}
	metrics, err := json.Marshal(routes)
	if err != nil {
		return 0, fmt.Errorf("encode instance metrics: %w", err)
	}
	now = now.UTC()
	if _, err = s.db.Exec(ctx, `INSERT INTO loomplane_db_gateway_instances AS i
			(org_id, gateway_id, instance_id, snapshot_version, draining, metrics, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (org_id, gateway_id, instance_id) DO UPDATE
			SET snapshot_version = EXCLUDED.snapshot_version, draining = EXCLUDED.draining,
				metrics = EXCLUDED.metrics, last_seen_at = EXCLUDED.last_seen_at
			WHERE i.last_seen_at <= EXCLUDED.last_seen_at`,
		org, gatewayID, r.InstanceID, int64(r.SnapshotVersion), r.Draining, metrics, now); err != nil {
		return 0, fmt.Errorf("store instance report: %w", missingGateway(err, gatewayID))
	}
	if err = s.db.QueryRow(ctx, `SELECT count(*) FROM loomplane_db_gateway_instances
		WHERE org_id = $1 AND gateway_id = $2 AND last_seen_at > $3`, org, gatewayID, now.Add(-liveWindow)).Scan(&live); err != nil {
		return 0, fmt.Errorf("count live instances: %w", err)
	}
	return live, nil
}

// Instances lists every instance that has reported for the gateway, live or
// not, by instance id.
func (s *Store) Instances(ctx context.Context, org, gatewayID string) ([]InstanceRecord, error) {
	rows, err := s.db.Query(ctx, `SELECT instance_id, snapshot_version, draining, metrics, last_seen_at
		FROM loomplane_db_gateway_instances WHERE org_id = $1 AND gateway_id = $2 ORDER BY instance_id`, org, gatewayID)
	if err != nil {
		return nil, fmt.Errorf("list database gateway instances: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []InstanceRecord{}
	for rows.Next() {
		var in InstanceRecord
		var version int64
		var metrics []byte
		if err := rows.Scan(&in.InstanceID, &version, &in.Draining, &metrics, &in.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scan database gateway instance: %w", err)
		}
		in.SnapshotVersion = uint64(version)
		in.Metrics = []plane.RouteStats{}
		if err := json.Unmarshal(metrics, &in.Metrics); err != nil {
			return nil, fmt.Errorf("decode metrics of instance %s: %w", in.InstanceID, err)
		}
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list database gateway instances: %w", err)
	}
	return out, nil
}
