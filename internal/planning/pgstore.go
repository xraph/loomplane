package planning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/page"
	"github.com/xraph/loomplane/internal/security"
)

const pgUniqueViolation = "23505"

// ErrConflict is a plan started while another for the same environment was
// being created.
var ErrConflict = errors.New("another plan for this environment was just started")

// PgStore is the Postgres Store. The schema is migration group "loomplane"
// (database/migrations).
type PgStore struct {
	db *pgdriver.PgDB
}

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

const planColumns = `id, org_id, project_id, environment_id, environment, status, revision,
	manifest_id, manifest_revision, result, executable, error,
	created_by, created_at, planned_at, expires_at, decided_by, decided_at, decision_note, catalog`

// open lists the statuses of a plan that hasn't been settled.
const open = `('planning', 'ready')`

func scanPlan(row pgx.Row) (*Plan, error) {
	var p Plan
	var result []byte
	if err := row.Scan(&p.ID, &p.OrgID, &p.ProjectID, &p.EnvironmentID, &p.Environment, &p.Status, &p.Revision,
		&p.ManifestID, &p.ManifestRevision, &result, &p.Executable, &p.Error,
		&p.CreatedBy, &p.CreatedAt, &p.PlannedAt, &p.ExpiresAt, &p.DecidedBy, &p.DecidedAt, &p.DecisionNote, &p.Catalog); err != nil {
		return nil, err
	}
	if len(result) > 0 {
		var r Result
		if err := json.Unmarshal(result, &r); err != nil {
			return nil, fmt.Errorf("decode plan: %w", err)
		}
		p.setResult(&r)
	}
	p.normalize()
	return &p, nil
}

func rollback(tx driver.Tx, err error) error {
	if err == nil {
		return nil
	}
	if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, pgx.ErrTxClosed) {
		return errors.Join(err, fmt.Errorf("rollback: %w", rerr))
	}
	return err
}

// CreatePlan implements Store. It supersedes the environment's open plans
// in the same transaction; a unique index allows one open plan per
// environment, so of two plans started at once, one fails with
// ErrConflict.
func (s *PgStore) CreatePlan(ctx context.Context, p *Plan) (superseded []string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { err = rollback(tx, err) }()
	superseded, err = supersede(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_plans (id, org_id, project_id, environment_id, environment, status,
		manifest_id, manifest_revision, created_by, created_at, catalog)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		p.ID, p.OrgID, p.ProjectID, p.EnvironmentID, p.Environment, p.Status,
		p.ManifestID, p.ManifestRevision, p.CreatedBy, p.CreatedAt, p.Catalog)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, fmt.Errorf("insert plan: %w", err)
	}
	for _, ps := range p.Sources {
		if _, err = tx.Exec(ctx, `INSERT INTO loomplane_plan_sources
			(plan_id, source_id, alias, repository, root, ref, analysis_id, commit_sha, in_scope)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			p.ID, ps.SourceID, ps.Alias, ps.Repository, ps.Root, ps.Ref, ps.AnalysisID, ps.CommitSHA, ps.InScope); err != nil {
			return nil, fmt.Errorf("insert plan source %s: %w", ps.Alias, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return superseded, nil
}

// supersede settles the environment's open plans and returns their IDs.
func supersede(ctx context.Context, tx driver.Tx, p *Plan) ([]string, error) {
	rows, err := tx.Query(ctx, `UPDATE loomplane_plans SET status = 'superseded', decided_at = $3
		WHERE org_id = $1 AND environment_id = $2 AND status IN `+open+` RETURNING id`,
		p.OrgID, p.EnvironmentID, p.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("supersede plans: %w", err)
	}
	defer rows.Close() // Err below reports iteration failures
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("supersede plans: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("supersede plans: %w", err)
	}
	return ids, nil
}

// GetPlan implements Store.
func (s *PgStore) GetPlan(ctx context.Context, orgID, id string) (*Plan, error) {
	p, err := scanPlan(s.db.QueryRow(ctx, `SELECT `+planColumns+` FROM loomplane_plans
		WHERE org_id = $1 AND id = $2`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get plan: %w", err)
	}
	if err := s.withSources(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// ListPlans implements Store.
func (s *PgStore) ListPlans(ctx context.Context, orgID, projectID, environmentID string, before page.Cursor, limit int) ([]*Plan, error) {
	rows, err := s.db.Query(ctx, `SELECT `+planColumns+` FROM loomplane_plans
		WHERE org_id = $1 AND project_id = $2 AND ($3 = '' OR environment_id = $3)
			AND ($5 = '' OR (created_at, id) < ($6, $5))
		ORDER BY created_at DESC, id DESC LIMIT $4`, orgID, projectID, environmentID, limit, before.ID, before.At)
	if err != nil {
		return nil, fmt.Errorf("list plans: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Plan{}
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan plan: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list plans: %w", err)
	}
	if err := s.withSources(ctx, out...); err != nil {
		return nil, err
	}
	return out, nil
}

// withSources loads each plan's lockset in one query.
func (s *PgStore) withSources(ctx context.Context, plans ...*Plan) error {
	if len(plans) == 0 {
		return nil
	}
	ids := make([]string, len(plans))
	by := map[string]*Plan{}
	for i, p := range plans {
		ids[i], by[p.ID] = p.ID, p
		p.Sources = []PlanSource{}
	}
	rows, err := s.db.Query(ctx, `SELECT plan_id, source_id, alias, repository, root, ref, analysis_id, commit_sha, in_scope
		FROM loomplane_plan_sources WHERE plan_id = ANY($1) ORDER BY plan_id, alias COLLATE "C"`, ids)
	if err != nil {
		return fmt.Errorf("load plan sources: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	for rows.Next() {
		var planID string
		var ps PlanSource
		if err := rows.Scan(&planID, &ps.SourceID, &ps.Alias, &ps.Repository, &ps.Root, &ps.Ref, &ps.AnalysisID, &ps.CommitSHA, &ps.InScope); err != nil {
			return fmt.Errorf("scan plan source: %w", err)
		}
		by[planID].Sources = append(by[planID].Sources, ps)
	}
	return rows.Err()
}

// DeployedSources implements Store.
func (s *PgStore) DeployedSources(ctx context.Context, orgID, environmentID string) ([]*DeployedSource, error) {
	rows, err := s.db.Query(ctx, `SELECT environment_id, source_id, analysis_id, commit_sha, plan_id, deployed_at
		FROM loomplane_environment_sources WHERE org_id = $1 AND environment_id = $2 ORDER BY source_id`, orgID, environmentID)
	if err != nil {
		return nil, fmt.Errorf("list deployed sources: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*DeployedSource{}
	for rows.Next() {
		var d DeployedSource
		if err := rows.Scan(&d.EnvironmentID, &d.SourceID, &d.AnalysisID, &d.CommitSHA, &d.PlanID, &d.DeployedAt); err != nil {
			return nil, fmt.Errorf("scan deployed source: %w", err)
		}
		d.DeployedAt = d.DeployedAt.UTC()
		out = append(out, &d)
	}
	return out, rows.Err()
}

// RecordDeployed implements Store.
func (s *PgStore) RecordDeployed(ctx context.Context, orgID string, d *DeployedSource) error {
	if _, err := s.db.Exec(ctx, `INSERT INTO loomplane_environment_sources
			(environment_id, source_id, org_id, analysis_id, commit_sha, plan_id, deployed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (environment_id, source_id) DO UPDATE SET
			analysis_id = EXCLUDED.analysis_id, commit_sha = EXCLUDED.commit_sha,
			plan_id = EXCLUDED.plan_id, deployed_at = EXCLUDED.deployed_at`,
		d.EnvironmentID, d.SourceID, orgID, d.AnalysisID, d.CommitSHA, d.PlanID, d.DeployedAt.UTC()); err != nil {
		return fmt.Errorf("record deployed source: %w", err)
	}
	return nil
}

// FinishPlanning implements Store.
func (s *PgStore) FinishPlanning(ctx context.Context, p *Plan) (bool, error) {
	result, err := json.Marshal(Result{
		CloudSyncID: p.CloudSyncID,
		Actions:     p.Actions, Problems: p.Problems, Warnings: p.Warnings, Summary: p.Summary, Executable: p.Executable,
		Connections: p.Connections,
	})
	if err != nil {
		return false, fmt.Errorf("encode plan: %w", err)
	}
	tag, err := s.db.Exec(ctx, `UPDATE loomplane_plans
		SET status = $3, revision = $4, result = $5, executable = $6, planned_at = $7, expires_at = $8
		WHERE org_id = $1 AND id = $2 AND status = 'planning'`,
		p.OrgID, p.ID, p.Status, p.Revision, result, p.Executable, p.PlannedAt, p.ExpiresAt)
	if err != nil {
		return false, fmt.Errorf("store plan: %w", err)
	}
	return affected(tag)
}

// FailPlanning implements Store.
func (s *PgStore) FailPlanning(ctx context.Context, orgID, id, reason string, now time.Time) error {
	if _, err := s.db.Exec(ctx, `UPDATE loomplane_plans SET status = 'failed', error = $3, planned_at = $4
		WHERE org_id = $1 AND id = $2 AND status = 'planning'`, orgID, id, reason, now); err != nil {
		return fmt.Errorf("fail plan: %w", err)
	}
	return nil
}

// Decide implements Store.
func (s *PgStore) Decide(ctx context.Context, orgID, id, status, by, note string, now time.Time) (changed bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, orgID); checkErr != nil {
		return false, checkErr
	}
	if actor, ok := security.PrincipalFrom(ctx); ok {
		raw, marshalErr := json.Marshal(actor)
		if marshalErr != nil {
			return false, marshalErr
		}
		if _, err = tx.Exec(ctx, `SELECT set_config('loomplane.capacity_actor',$1,true)`, string(raw)); err != nil {
			return false, err
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE loomplane_plans SET status = $3, decided_by = $4, decision_note = $5, decided_at = $6
 WHERE org_id = $1 AND id = $2 AND status = 'ready'`, orgID, id, status, by, note, now)
	if err != nil {
		return false, fmt.Errorf("decide plan: %w", err)
	}
	changed, err = affected(tag)
	if err != nil {
		return false, err
	}
	err = tx.Commit()
	return changed, err
}

func affected(r driver.Result) (bool, error) {
	n, err := r.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	return n == 1, nil
}

const bindingColumns = `id, org_id, project_id, environment_id, subject, name, provider, region, engine,
	external_id, config, url, connection_id, revision, commit_sha, drift, drift_message, observed_at,
	plan_id, created_at, updated_at, replaced, physical_id`

// ListBindings implements Store.
func (s *PgStore) ListBindings(ctx context.Context, orgID, environmentID string) ([]*Binding, error) {
	rows, err := s.db.Query(ctx, `SELECT `+bindingColumns+` FROM loomplane_bindings
		WHERE org_id = $1 AND environment_id = $2 ORDER BY subject, name`, orgID, environmentID)
	if err != nil {
		return nil, fmt.Errorf("list bindings: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Binding{}
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bindings: %w", err)
	}
	return out, nil
}

func scanBinding(row pgx.Row) (*Binding, error) {
	var b Binding
	var cfg, replaced []byte
	if err := row.Scan(&b.ID, &b.OrgID, &b.ProjectID, &b.EnvironmentID, &b.Subject, &b.Name, &b.Provider, &b.Region,
		&b.Engine, &b.ExternalID, &cfg, &b.URL, &b.ConnectionID, &b.Revision, &b.CommitSHA, &b.Drift, &b.DriftMessage,
		&b.ObservedAt, &b.PlanID, &b.CreatedAt, &b.UpdatedAt, &replaced, &b.PhysicalID); err != nil {
		return nil, fmt.Errorf("scan binding: %w", err)
	}
	b.Config = cfg
	if err := json.Unmarshal(replaced, &b.Replaced); err != nil {
		return nil, fmt.Errorf("decode what binding %s replaced: %w", b.ID, err)
	}
	b.CreatedAt, b.UpdatedAt = b.CreatedAt.UTC(), b.UpdatedAt.UTC()
	if b.ObservedAt != nil {
		t := b.ObservedAt.UTC()
		b.ObservedAt = &t
	}
	return &b, nil
}

// AllBindings implements Store.
func (s *PgStore) AllBindings(ctx context.Context, after string, limit int) ([]*Binding, error) {
	rows, err := s.db.Query(ctx, `SELECT `+bindingColumns+` FROM loomplane_bindings
		WHERE id > $1 ORDER BY id LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list bindings: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Binding{}
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bindings: %w", err)
	}
	return out, nil
}

// RecordDrift implements Store.
func (s *PgStore) RecordDrift(ctx context.Context, orgID, id, drift, message string, at time.Time) error {
	if _, err := s.db.Exec(ctx, `UPDATE loomplane_bindings SET drift = $3, drift_message = $4, observed_at = $5
		WHERE org_id = $1 AND id = $2`, orgID, id, drift, message, at); err != nil {
		return fmt.Errorf("record drift: %w", err)
	}
	return nil
}

// PutBinding implements Store: it records what runs a service or resource
// in an environment, replacing what was recorded before.
func (s *PgStore) PutBinding(ctx context.Context, b *Binding) error {
	id := syncMutation(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // a no-op once committed
	if guardErr := cloudinfra.GuardPoolWrite(ctx, tx); guardErr != nil {
		return guardErr
	}
	if err := putBinding(ctx, tx, b); err != nil {
		return err
	}
	if id != "" {
		if err := s.checkpointCloudBinding(ctx, tx, id, b.OrgID, b.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func putBinding(ctx context.Context, db bindingExecutor, b *Binding) error {
	cfg := []byte(b.Config)
	if len(cfg) == 0 {
		cfg = []byte("{}")
	}
	replaced, err := encodeReplaced(b.Replaced)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO loomplane_bindings (`+bindingColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)
		ON CONFLICT (environment_id, subject, name) DO UPDATE SET
			provider = EXCLUDED.provider, region = EXCLUDED.region, engine = EXCLUDED.engine,
			external_id = EXCLUDED.external_id, config = EXCLUDED.config, url = EXCLUDED.url,
			connection_id = EXCLUDED.connection_id, revision = EXCLUDED.revision, commit_sha = EXCLUDED.commit_sha,
			drift = EXCLUDED.drift, drift_message = EXCLUDED.drift_message, observed_at = EXCLUDED.observed_at,
			plan_id = EXCLUDED.plan_id, updated_at = EXCLUDED.updated_at, replaced = EXCLUDED.replaced, physical_id = EXCLUDED.physical_id
		WHERE loomplane_bindings.org_id = EXCLUDED.org_id`,
		b.ID, b.OrgID, b.ProjectID, b.EnvironmentID, b.Subject, b.Name, b.Provider, b.Region, b.Engine,
		b.ExternalID, cfg, b.URL, b.ConnectionID, b.Revision, b.CommitSHA, b.Drift, b.DriftMessage, b.ObservedAt,
		b.PlanID, b.CreatedAt, b.UpdatedAt, replaced, b.PhysicalID)
	if err != nil {
		return fmt.Errorf("put binding: %w", err)
	}
	return nil
}

// SetReplaced implements Store.
func (s *PgStore) SetReplaced(ctx context.Context, orgID, id string, replaced []Binding) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // no-op after commit
	if guardErr := cloudinfra.GuardPoolWrite(ctx, tx); guardErr != nil {
		return guardErr
	}

	raw, err := encodeReplaced(replaced)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE loomplane_bindings SET replaced = $3 WHERE org_id = $1 AND id = $2`,
		orgID, id, raw); err != nil {
		return fmt.Errorf("record what binding %s replaced: %w", id, err)
	}
	return tx.Commit()
}

// encodeReplaced is what a binding replaced as it's stored: a list,
// empty for nothing.
func encodeReplaced(replaced []Binding) ([]byte, error) {
	if replaced == nil {
		replaced = []Binding{}
	}
	raw, err := json.Marshal(replaced)
	if err != nil {
		return nil, fmt.Errorf("encode what a binding replaced: %w", err)
	}
	return raw, nil
}

// DeleteBinding implements Store.
func (s *PgStore) DeleteBinding(ctx context.Context, orgID, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // no-op after commit
	if guardErr := cloudinfra.GuardPoolWrite(ctx, tx); guardErr != nil {
		return guardErr
	}

	if _, err := tx.Exec(ctx, `DELETE FROM loomplane_bindings WHERE org_id = $1 AND id = $2`, orgID, id); err != nil {
		return fmt.Errorf("delete binding: %w", err)
	}
	return tx.Commit()
}

// EnvironmentConnections implements Store.
func (s *PgStore) EnvironmentConnections(ctx context.Context, orgID, environmentID string) (map[string]string, error) {
	rows, err := s.db.Query(ctx, `SELECT cloud, connection_id FROM loomplane_environment_connections
		WHERE org_id = $1 AND environment_id = $2`, orgID, environmentID)
	if err != nil {
		return nil, fmt.Errorf("list environment connections: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := map[string]string{}
	for rows.Next() {
		var cloud, id string
		if err := rows.Scan(&cloud, &id); err != nil {
			return nil, fmt.Errorf("scan environment connection: %w", err)
		}
		out[cloud] = id
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list environment connections: %w", err)
	}
	return out, nil
}

// SetEnvironmentConnection implements Store.
func (s *PgStore) SetEnvironmentConnection(ctx context.Context, orgID, projectID, environmentID, cloud, connectionID string) error {
	var err error
	if connectionID == "" {
		_, err = s.db.Exec(ctx, `DELETE FROM loomplane_environment_connections
			WHERE org_id = $1 AND environment_id = $2 AND cloud = $3`, orgID, environmentID, cloud)
	} else {
		_, err = s.db.Exec(ctx, `INSERT INTO loomplane_environment_connections (org_id, project_id, environment_id, cloud, connection_id)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (environment_id, cloud) DO UPDATE SET connection_id = EXCLUDED.connection_id
			WHERE loomplane_environment_connections.org_id = EXCLUDED.org_id`,
			orgID, projectID, environmentID, cloud, connectionID)
	}
	if err != nil {
		return fmt.Errorf("set environment connection: %w", err)
	}
	return nil
}
