package cloudprofiles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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

const profileColumns = `id,org_id,project_id,name,revision,created_at,updated_at`
const targetColumns = `id,org_id,project_id,cloud_id,name,display_name,provider,connection_id,region,revision,created_at,updated_at,runtime,pool_id`

func scanProfile(row pgx.Row) (*Profile, error) {
	var p Profile
	if err := row.Scan(&p.ID, &p.OrgID, &p.ProjectID, &p.Name, &p.Revision, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, storeError(err)
	}
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	return &p, nil
}
func scanTarget(row pgx.Row) (*Target, error) {
	var t Target
	var connection *string
	var pool *string
	if err := row.Scan(&t.ID, &t.OrgID, &t.ProjectID, &t.CloudID, &t.Name, &t.DisplayName, &t.Provider, &connection, &t.Region, &t.Revision, &t.CreatedAt, &t.UpdatedAt, &t.Runtime, &pool); err != nil {
		return nil, storeError(err)
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
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_profiles (`+profileColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7)`, p.ID, p.OrgID, p.ProjectID, p.Name, p.Revision, p.CreatedAt, p.UpdatedAt)
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
	return scanProfile(s.db.QueryRow(ctx, `SELECT `+profileColumns+` FROM loomplane_cloud_profiles c WHERE org_id=$1 AND ($2='' OR EXISTS(SELECT 1 FROM loomplane_cloud_project_grants g WHERE g.org_id=c.org_id AND g.cloud_id=c.id AND g.project_id=$2 AND NOT g.revoked)) AND id=$3`, orgID, projectID, id))
}

// ListProfiles returns a deterministic list in the requested project.
func (s *PgStore) ListProfiles(ctx context.Context, orgID, projectID string) ([]*Profile, error) {
	rows, err := s.db.Query(ctx, `SELECT `+profileColumns+` FROM loomplane_cloud_profiles c WHERE org_id=$1 AND ($2='' OR EXISTS(SELECT 1 FROM loomplane_cloud_project_grants g WHERE g.org_id=c.org_id AND g.cloud_id=c.id AND g.project_id=$2 AND NOT g.revoked)) ORDER BY name,id`, orgID, projectID)
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
	tag, err := tx.Exec(ctx, `UPDATE loomplane_cloud_profiles SET name=$4,revision=$5,updated_at=$6 WHERE org_id=$1 AND ($2='' OR project_id=$2) AND id=$3 AND revision=$7`, p.OrgID, p.ProjectID, p.ID, p.Name, p.Revision, p.UpdatedAt, previousRevision)
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
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_cloud_targets (`+targetColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,''),$9,$10,$11,$12,$13,nullif($14,''))`, t.ID, t.OrgID, t.ProjectID, t.CloudID, t.Name, t.DisplayName, t.Provider, t.ConnectionID, t.Region, t.Revision, t.CreatedAt, t.UpdatedAt, targetRuntime(t), t.PoolID)
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

// UpdateTarget changes display metadata only; all placement fields must match.
func (s *PgStore) UpdateTarget(ctx context.Context, t *Target, previousRevision int64) error {
	if previousRevision < 1 || t.Revision != previousRevision+1 {
		return ErrConflict
	}
	tx, err := s.metadataTransaction(ctx, t.OrgID, t.ProjectID)
	if err != nil {
		return err
	}
	defer rollbackSync(tx)
	tag, err := tx.Exec(ctx, `UPDATE loomplane_cloud_targets SET display_name=$5,revision=$6,updated_at=$7 WHERE org_id=$1 AND ($2='' OR project_id=$2) AND cloud_id=$3 AND id=$4 AND revision=$8 AND name=$9 AND provider=$10 AND coalesce(connection_id,'')=$11 AND region=$12 AND runtime=$13 AND coalesce(pool_id,'')=$14`, t.OrgID, t.ProjectID, t.CloudID, t.ID, t.DisplayName, t.Revision, t.UpdatedAt, previousRevision, t.Name, t.Provider, t.ConnectionID, t.Region, targetRuntime(t), t.PoolID)
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

// Resolve reads the environment, assignment and targets in a single snapshot.
// No assignment returns nil only when the environment exists in this scope.
func (s *PgStore) Resolve(ctx context.Context, orgID, projectID, environmentID string) (*Assignment, error) {
	assignment, _, err := s.EnvironmentCloud(ctx, orgID, projectID, environmentID)
	return assignment, err
}

// EnvironmentCloud reads the nullable assignment and its generation atomically.
func (s *PgStore) EnvironmentCloud(ctx context.Context, orgID, projectID, environmentID string) (*Assignment, int64, error) {
	return s.environmentCloud(ctx, orgID, projectID, environmentID, false)
}

// RetainedAssignment reads an existing placement for reviewed removal or reassignment.
func (s *PgStore) RetainedAssignment(ctx context.Context, org, project, env string) (*Assignment, error) {
	a, _, err := s.environmentCloud(ctx, org, project, env, true)
	return a, err
}
func (s *PgStore) environmentCloud(ctx context.Context, orgID, projectID, environmentID string, retained bool) (*Assignment, int64, error) {
	var cloudID *string
	var revision, cloudRevision, grantRevision *int64
	var targets []byte
	err := s.db.QueryRow(ctx, `SELECT a.cloud_id,a.revision,c.revision,g.revision,coalesce(jsonb_agg(to_jsonb(t) ORDER BY t.name,t.id) FILTER(WHERE t.id IS NOT NULL),'[]'::jsonb)
 FROM loomplane_environments e
 JOIN loomplane_projects p ON p.org_id=e.org_id AND p.id=e.project_id
 LEFT JOIN loomplane_cloud_assignments a ON a.org_id=e.org_id AND a.project_id=e.project_id AND a.environment_id=e.id
 LEFT JOIN loomplane_cloud_profiles c ON c.org_id=a.org_id AND c.id=a.cloud_id
 LEFT JOIN loomplane_cloud_project_grants g ON g.org_id=a.org_id AND g.cloud_id=a.cloud_id AND g.project_id=a.project_id AND NOT g.revoked
 LEFT JOIN loomplane_cloud_targets t ON t.org_id=a.org_id AND t.cloud_id=a.cloud_id AND ($4 OR (g.revision IS NOT NULL AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids))))
 WHERE e.org_id=$1 AND e.project_id=$2 AND e.id=$3
 GROUP BY e.id,a.cloud_id,a.revision,c.revision,g.revision`, orgID, projectID, environmentID, retained).Scan(&cloudID, &revision, &cloudRevision, &grantRevision, &targets)
	if err != nil {
		return nil, 0, storeError(err)
	}
	if cloudID == nil {
		if revision != nil {
			return nil, *revision, nil
		}
		return nil, 0, nil
	}
	if grantRevision == nil {
		if !retained {
			return nil, *revision, ErrNotFound
		}
		zero := int64(0)
		grantRevision = &zero
	}
	a := &Assignment{GrantRevision: *grantRevision, OrgID: orgID, ProjectID: projectID, EnvironmentID: environmentID, CloudID: *cloudID, Revision: *revision, CloudRevision: *cloudRevision}
	if err = json.Unmarshal(targets, &a.Targets); err != nil {
		return nil, 0, fmt.Errorf("decode assigned targets: %w", err)
	}
	return a, *revision, nil
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

func targetRuntime(t *Target) string {
	if t.Runtime == "" {
		return "managed"
	}
	return t.Runtime
}
