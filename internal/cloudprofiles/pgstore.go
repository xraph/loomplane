package cloudprofiles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xraph/loomplane/internal/projects"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
)

// PgStore persists scoped Cloud configuration in Postgres.
type PgStore struct{ db *pgdriver.PgDB }

// NewPgStore returns a store over the centrally migrated database.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)
var _ Resolver = (*PgStore)(nil)

const profileColumns = `id,org_id,project_id,name,revision,created_at,updated_at,default_provider,default_connection_id,default_region`

// profileSelect adds what only the control plane writes to what a read returns.
const profileSelect = profileColumns + `,platform_owned`
const targetColumns = `id,org_id,project_id,cloud_id,name,display_name,provider,connection_id,region,revision,created_at,updated_at,runtime,pool_id,tags,coalesce(managed_resource_id,''),coalesce(location_id,'')`

func scanProfile(row pgx.Row) (*Profile, error) {
	var p Profile
	var provider, connection *string
	var region string
	if err := row.Scan(&p.ID, &p.OrgID, &p.ProjectID, &p.Name, &p.Revision, &p.CreatedAt, &p.UpdatedAt, &provider, &connection, &region, &p.PlatformOwned); err != nil {
		return nil, storeError(err)
	}
	if provider != nil && connection != nil {
		p.DefaultPlacement = &DefaultPlacement{Provider: *provider, ConnectionID: *connection, Region: region}
	}
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	return &p, nil
}
func scanTarget(row pgx.Row) (*Target, error) {
	var t Target
	var connection *string
	var pool *string
	if err := row.Scan(&t.ID, &t.OrgID, &t.ProjectID, &t.CloudID, &t.Name, &t.DisplayName, &t.Provider, &connection, &t.Region, &t.Revision, &t.CreatedAt, &t.UpdatedAt, &t.Runtime, &pool, &t.Tags, &t.ManagedResourceID, &t.LocationID); err != nil {
		return nil, storeError(err)
	}
	if t.Tags == nil {
		t.Tags = []string{}
	}
	if connection != nil {
		t.ConnectionID = *connection
	}
	if pool != nil {
		t.PoolID = *pool
	}
	t.CreatedAt, t.UpdatedAt = t.CreatedAt.UTC(), t.UpdatedAt.UTC()
	return &t, nil
}
func storeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "55P03":
			return ErrBusy
		case "23505":
			return ErrExists
		case "23503":
			return ErrNotFound
		case "23514":
			if IsStaleCleanupReview(err) {
				return ErrConflict
			}
			return ErrInvalid
		}
	}
	return fmt.Errorf("cloud configuration store: %w", err)
}
func deleteError(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23503" {
		return ErrInUse
	}
	return storeError(err)
}

// CreateProfile inserts a Cloud at revision one.
func (s *PgStore) CreateProfile(ctx context.Context, p *Profile) error {
	if p.Revision != 1 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackSync(tx)
	provider, connection, region := defaultColumns(p.DefaultPlacement)
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_profiles (`+profileColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, p.ID, p.OrgID, p.ProjectID, p.Name, p.Revision, p.CreatedAt, p.UpdatedAt, provider, connection, region)
	if err != nil {
		return storeError(err)
	}
	if p.ProjectID != "" {
		_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_project_grants(org_id,cloud_id,project_id) VALUES($1,$2,$3)`, p.OrgID, p.ID, p.ProjectID)
		if err != nil {
			return storeError(err)
		}
	}
	return tx.Commit()
}

// GetProfile reads a Cloud only within its organization and project.
func (s *PgStore) GetProfile(ctx context.Context, orgID, projectID, id string) (*Profile, error) {
	return scanProfile(s.db.QueryRow(ctx, `SELECT `+profileSelect+` FROM loomplane_cloud_profiles c WHERE org_id=$1 AND ($2='' OR EXISTS(SELECT 1 FROM loomplane_cloud_project_grants g WHERE g.org_id=c.org_id AND g.cloud_id=c.id AND g.project_id=$2 AND NOT g.revoked)) AND id=$3`, orgID, projectID, id))
}

// ListProfiles returns a deterministic list in the requested project.
func (s *PgStore) ListProfiles(ctx context.Context, orgID, projectID string) ([]*Profile, error) {
	rows, err := s.db.Query(ctx, `SELECT `+profileSelect+` FROM loomplane_cloud_profiles c WHERE org_id=$1 AND ($2='' OR EXISTS(SELECT 1 FROM loomplane_cloud_project_grants g WHERE g.org_id=c.org_id AND g.cloud_id=c.id AND g.project_id=$2 AND NOT g.revoked)) ORDER BY name,id`, orgID, projectID)
	if err != nil {
		return nil, storeError(err)
	}
	defer rows.Close()
	out := []*Profile{}
	for rows.Next() {
		p, scanErr := scanProfile(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, p)
	}
	if err = rows.Err(); err != nil {
		return nil, storeError(err)
	}
	return out, nil
}

// UpdateProfile writes metadata after checking the previous revision.
func (s *PgStore) UpdateProfile(ctx context.Context, p *Profile, previousRevision int64) error {
	if previousRevision < 1 || p.Revision != previousRevision+1 {
		return ErrConflict
	}
	tx, err := s.metadataTransaction(ctx, p.OrgID, p.ProjectID)
	if err != nil {
		return err
	}
	defer rollbackSync(tx)
	provider, connection, region := defaultColumns(p.DefaultPlacement)
	// The organization lock is held, so no default Cloud can change while
	// this runs. Setting a first default is safe: services it places had none.
	var redirects bool
	err = tx.QueryRow(ctx, `SELECT default_provider IS NOT NULL AND (default_provider IS DISTINCT FROM $3 OR default_connection_id IS DISTINCT FROM $4 OR default_region<>$5)
 AND (EXISTS(SELECT 1 FROM loomplane_cloud_project_grants g WHERE g.org_id=c.org_id AND g.cloud_id=c.id AND g.is_default AND NOT g.revoked)
  OR EXISTS(SELECT 1 FROM loomplane_bindings b WHERE b.org_id=c.org_id AND b.subject='service' AND b.config->'cloud_placement'->>'cloud_id'=c.id AND (b.config->'cloud_placement'->>'default')::boolean))
 FROM loomplane_cloud_profiles c WHERE org_id=$1 AND id=$2`, p.OrgID, p.ID, provider, connection, region).Scan(&redirects)
	if err != nil {
		return storeError(err)
	}
	if redirects {
		return fmt.Errorf("%w: this Cloud is in use; review the default placement change as a Cloud change with default set, which moves its services", ErrInUse)
	}
	tag, err := tx.Exec(ctx, `UPDATE loomplane_cloud_profiles SET name=$4,revision=$5,updated_at=$6,default_provider=$8,default_connection_id=$9,default_region=$10 WHERE org_id=$1 AND ($2='' OR project_id=$2) AND id=$3 AND revision=$7`, p.OrgID, p.ProjectID, p.ID, p.Name, p.Revision, p.UpdatedAt, previousRevision, provider, connection, region)
	if err != nil {
		return storeError(err)
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return storeError(err)
	}
	if n != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

// DeleteProfile removes an unassigned Cloud. Its targets cascade together.
func (s *PgStore) DeleteProfile(ctx context.Context, orgID, projectID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM loomplane_cloud_profiles c WHERE org_id=$1 AND ($2='' OR EXISTS(SELECT 1 FROM loomplane_cloud_project_grants g WHERE g.org_id=c.org_id AND g.cloud_id=c.id AND g.project_id=$2 AND NOT g.revoked)) AND id=$3`, orgID, projectID, id)
	if err != nil {
		return deleteError(err)
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return storeError(err)
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}

// CreateTarget inserts the target's initial placement.
func (s *PgStore) CreateTarget(ctx context.Context, t *Target) error {
	if t.Revision != 1 {
		return ErrInvalid
	}
	if _, err := s.GetProfile(ctx, t.OrgID, t.ProjectID, t.CloudID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_cloud_targets (id,org_id,project_id,cloud_id,name,display_name,provider,connection_id,region,revision,created_at,updated_at,runtime,pool_id,tags,managed_resource_id,location_id) VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,''),$9,$10,$11,$12,$13,nullif($14,''),$15,nullif($16,''),nullif($17,''))`, t.ID, t.OrgID, t.ProjectID, t.CloudID, t.Name, t.DisplayName, t.Provider, t.ConnectionID, t.Region, t.Revision, t.CreatedAt, t.UpdatedAt, targetRuntime(t), t.PoolID, targetTags(t), t.ManagedResourceID, t.LocationID)
	return storeError(err)
}

// GetTarget reads one target by its full scope.
func (s *PgStore) GetTarget(ctx context.Context, orgID, projectID, cloudID, id string) (*Target, error) {
	return scanTarget(s.db.QueryRow(ctx, `SELECT `+targetColumns+` FROM loomplane_cloud_targets t WHERE org_id=$1 AND ($2='' OR EXISTS(SELECT 1 FROM loomplane_cloud_project_grants g WHERE g.org_id=t.org_id AND g.cloud_id=t.cloud_id AND g.project_id=$2 AND NOT g.revoked AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids)))) AND cloud_id=$3 AND id=$4`, orgID, projectID, cloudID, id))
}

// ListTargets returns targets ordered by slug and ID.
func (s *PgStore) ListTargets(ctx context.Context, orgID, projectID, cloudID string) ([]*Target, error) {
	rows, err := s.db.Query(ctx, `SELECT `+targetColumns+` FROM loomplane_cloud_targets t WHERE org_id=$1 AND ($2='' OR EXISTS(SELECT 1 FROM loomplane_cloud_project_grants g WHERE g.org_id=t.org_id AND g.cloud_id=t.cloud_id AND g.project_id=$2 AND NOT g.revoked AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids)))) AND cloud_id=$3 ORDER BY name,id`, orgID, projectID, cloudID)
	if err != nil {
		return nil, storeError(err)
	}
	defer rows.Close()
	out := []*Target{}
	for rows.Next() {
		t, scanErr := scanTarget(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, t)
	}
	if err = rows.Err(); err != nil {
		return nil, storeError(err)
	}
	return out, nil
}

// UpdateTarget changes display metadata and tags; all placement fields must match.
func (s *PgStore) UpdateTarget(ctx context.Context, t *Target, previousRevision int64) error {
	if previousRevision < 1 || t.Revision != previousRevision+1 {
		return ErrConflict
	}
	tx, err := s.metadataTransaction(ctx, t.OrgID, t.ProjectID)
	if err != nil {
		return err
	}
	defer rollbackSync(tx)
	tag, err := tx.Exec(ctx, `UPDATE loomplane_cloud_targets SET display_name=$5,revision=$6,updated_at=$7,tags=$15 WHERE org_id=$1 AND ($2='' OR project_id=$2) AND cloud_id=$3 AND id=$4 AND revision=$8 AND name=$9 AND provider=$10 AND coalesce(connection_id,'')=$11 AND region=$12 AND runtime=$13 AND coalesce(pool_id,'')=$14 AND coalesce(managed_resource_id,'')=$16 AND coalesce(location_id,'')=$17`, t.OrgID, t.ProjectID, t.CloudID, t.ID, t.DisplayName, t.Revision, t.UpdatedAt, previousRevision, t.Name, t.Provider, t.ConnectionID, t.Region, targetRuntime(t), t.PoolID, targetTags(t), t.ManagedResourceID, t.LocationID)
	if err != nil {
		return storeError(err)
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return storeError(err)
	}
	if n != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

// DeleteTarget refuses deletion while any environment uses its Cloud.
func (s *PgStore) DeleteTarget(ctx context.Context, orgID, projectID, cloudID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM loomplane_cloud_targets t WHERE org_id=$1 AND ($2='' OR EXISTS(SELECT 1 FROM loomplane_cloud_project_grants g WHERE g.org_id=t.org_id AND g.cloud_id=t.cloud_id AND g.project_id=$2 AND NOT g.revoked AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids)))) AND cloud_id=$3 AND id=$4`, orgID, projectID, cloudID, id)
	if err != nil {
		return deleteError(err)
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return storeError(err)
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}

// ResolveCloud reads the project's view of one Cloud, or of its default
// Cloud when cloud is "", in a single snapshot.
func (s *PgStore) ResolveCloud(ctx context.Context, orgID, projectID, cloudID string) (*ProjectCloud, error) {
	return s.projectCloud(ctx, orgID, projectID, cloudID, false)
}

// RetainedCloud reads a Cloud the project placed services on, for reviewed
// removal and compensation. It answers after the grant is revoked, with
// every target, since cleanup must reach what the grant once allowed.
func (s *PgStore) RetainedCloud(ctx context.Context, orgID, projectID, cloudID string) (*ProjectCloud, error) {
	return s.projectCloud(ctx, orgID, projectID, cloudID, true)
}

// DefaultCloud reads the project's default Cloud and its grant revision:
// "" and 0 when the project has none.
func (s *PgStore) DefaultCloud(ctx context.Context, orgID, projectID string) (cloudID string, grantRevision int64, err error) {
	var cloud *string
	var revision *int64
	err = s.db.QueryRow(ctx, `SELECT g.cloud_id,g.revision FROM loomplane_projects p
 LEFT JOIN loomplane_cloud_project_grants g ON g.org_id=p.org_id AND g.project_id=p.id AND g.is_default AND NOT g.revoked
 WHERE p.org_id=$1 AND p.id=$2`, orgID, projectID).Scan(&cloud, &revision)
	if err != nil {
		return "", 0, storeError(err)
	}
	if cloud == nil {
		return "", 0, nil
	}
	return *cloud, *revision, nil
}

func (s *PgStore) projectCloud(ctx context.Context, orgID, projectID, cloudID string, retained bool) (*ProjectCloud, error) {
	var id *string
	var cloudRevision, grantRevision *int64
	var isDefault bool
	var targets []byte
	var defaultProvider, defaultConnection, defaultRegion *string
	var wholeCloud *bool
	err := s.db.QueryRow(ctx, `SELECT c.id,c.revision,g.revision,coalesce(g.is_default,false),c.default_provider,c.default_connection_id,c.default_region,cardinality(g.target_ids)=0,coalesce(jsonb_agg(to_jsonb(t) ORDER BY t.name,t.id) FILTER(WHERE t.id IS NOT NULL),'[]'::jsonb)
 FROM loomplane_projects p
 LEFT JOIN loomplane_cloud_profiles c ON c.org_id=p.org_id AND c.id=CASE WHEN $3='' THEN (SELECT d.cloud_id FROM loomplane_cloud_project_grants d WHERE d.org_id=p.org_id AND d.project_id=p.id AND d.is_default AND NOT d.revoked) ELSE $3 END
 LEFT JOIN loomplane_cloud_project_grants g ON g.org_id=c.org_id AND g.cloud_id=c.id AND g.project_id=p.id AND NOT g.revoked
 LEFT JOIN loomplane_cloud_targets t ON t.org_id=c.org_id AND t.cloud_id=c.id AND ($4 OR (g.revision IS NOT NULL AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids))))
 AND ($4 OR t.managed_resource_id IS NULL OR (t.project_id=p.id AND EXISTS(SELECT 1 FROM loomplane_runtime_attachments ra JOIN loomplane_managed_resources mr ON mr.org_id=ra.org_id AND mr.id=ra.cluster_id WHERE ra.org_id=t.org_id AND ra.cloud_id=t.cloud_id AND ra.cluster_id=t.managed_resource_id AND ra.project_id=p.id AND ra.runtime_connection_id=t.connection_id AND ra.deleted_at IS NULL AND mr.deleted_at IS NULL)))
 WHERE p.org_id=$1 AND p.id=$2
 GROUP BY c.id,c.revision,g.revision,g.is_default,c.default_provider,c.default_connection_id,c.default_region,g.target_ids`, orgID, projectID, cloudID, retained).Scan(&id, &cloudRevision, &grantRevision, &isDefault, &defaultProvider, &defaultConnection, &defaultRegion, &wholeCloud, &targets)
	if err != nil {
		return nil, storeError(err)
	}
	if id == nil {
		if cloudID == "" {
			return nil, nil
		}
		return nil, ErrNotFound
	}
	if grantRevision == nil {
		if !retained {
			return nil, ErrNotFound
		}
		zero := int64(0)
		grantRevision = &zero
	}
	c := &ProjectCloud{GrantRevision: *grantRevision, OrgID: orgID, ProjectID: projectID, CloudID: *id, CloudRevision: *cloudRevision, Default: isDefault}
	if err = json.Unmarshal(targets, &c.Targets); err != nil {
		return nil, fmt.Errorf("decode granted targets: %w", err)
	}
	for i := range c.Targets {
		if c.Targets[i].Tags == nil {
			c.Targets[i].Tags = []string{}
		}
	}
	// Cleanup keeps the default it placed with; fresh placement needs a grant
	// covering the whole Cloud, not just chosen targets.
	if defaultProvider != nil && defaultConnection != nil && (retained || wholeCloud != nil && *wholeCloud) {
		c.DefaultPlacement = &DefaultPlacement{Provider: *defaultProvider, ConnectionID: *defaultConnection}
		if defaultRegion != nil {
			c.DefaultPlacement.Region = *defaultRegion
		}
	}
	return c, nil
}

// metadataTransaction serializes edits with acceptance. Retained syncs pin both
// Cloud and target revisions until recovery or compensation releases them.
func (s *PgStore) metadataTransaction(ctx context.Context, org, _ string) (driver.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err = LockOrganization(ctx, tx, org); err != nil {
		rollbackSync(tx)
		return nil, err
	}
	var retained bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cloud_syncs WHERE org_id=$1 AND status NOT IN ('succeeded','reverted'))`, org).Scan(&retained)
	if err != nil || retained {
		rollbackSync(tx)
		if err != nil {
			return nil, storeError(err)
		}
		return nil, fmt.Errorf("%w: finish or revert the project Cloud sync before renaming a Cloud or target", ErrBusy)
	}
	return tx, nil
}

func targetTags(t *Target) []string {
	if t.Tags == nil {
		return []string{}
	}
	return t.Tags
}

// defaultColumns maps an absent default to NULL provider and connection.
func defaultColumns(p *DefaultPlacement) (provider, connection *string, region string) {
	if p == nil {
		return nil, nil, ""
	}
	return &p.Provider, &p.ConnectionID, p.Region
}

func targetRuntime(t *Target) string {
	if t.Runtime == "" {
		return "managed"
	}
	return t.Runtime
}

// IsStaleCleanupReview recognizes the immutable-target trigger without exposing SQL.
func IsStaleCleanupReview(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23514" && (pg.Message == "cleanup target changed; review again" || pg.Message == "cleanup target is unresolved or untrusted")
}

// Planning, deployment and the Cloud infrastructure service reach these
// through run-time assertions, where a drifted signature would read as a
// missing capability rather than fail the build.
var (
	_ GrantStore = (*PgStore)(nil)
	_ SyncStore  = (*PgStore)(nil)
	_ interface {
		RetainedCloud(ctx context.Context, orgID, projectID, cloudID string) (*ProjectCloud, error)
	} = (*PgStore)(nil)
	_ interface {
		EnvironmentsUsing(ctx context.Context, orgID, cloudID string) ([]*projects.Environment, error)
	} = (*PgStore)(nil)
)
