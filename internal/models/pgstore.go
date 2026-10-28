package models

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
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/security"
)

// PgStore is the Postgres Store. The schema is migration group "loomplane"
// (database/migrations, 20261126000200_ai_models).
type PgStore struct {
	db  *pgdriver.PgDB
	now func() time.Time
}

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db, now: time.Now} }

var (
	_ Store         = (*PgStore)(nil)
	_ WaitingLister = (*PgStore)(nil)
)

// Money goes in and out as text, so no amount passes through a float.
const deploymentColumns = `id, org_id, project_id, environment_id, resource, entry_id, quantization, engine,
	monthly_budget_usd::text, hourly_usd_override::text, phase,
	server_cloud_id, server_operation_id, server_name, plan_id, message, request, requested_by,
	month, month_cost_usd::text, accrued_at, alerted_projected, alerted_crossed,
	revision, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanDeployment(row scanner) (*Deployment, error) {
	var (
		d                   Deployment
		budget, override    *string
		request, principal  []byte
		accruedAt           *time.Time
		createdAt, updateAt time.Time
	)
	if err := row.Scan(&d.ID, &d.OrgID, &d.ProjectID, &d.EnvironmentID, &d.Resource, &d.EntryID, &d.Quantization, &d.Engine,
		&budget, &override, &d.Phase,
		&d.ServerCloudID, &d.ServerOperationID, &d.ServerName, &d.PlanID, &d.Message, &request, &principal,
		&d.Month, &d.MonthCostUSD, &accruedAt, &d.AlertedProjected, &d.AlertedCrossed,
		&d.Revision, &createdAt, &updateAt); err != nil {
		return nil, err
	}
	if budget != nil {
		d.MonthlyBudgetUSD = *budget
	}
	if override != nil {
		d.HourlyUSDOverride = *override
	}
	if request != nil {
		d.Request = &DeployInput{}
		if err := json.Unmarshal(request, d.Request); err != nil {
			return nil, fmt.Errorf("decode model deployment request: %w", err)
		}
	}
	if principal != nil {
		if err := json.Unmarshal(principal, &d.RequestedBy); err != nil {
			return nil, fmt.Errorf("decode model deployment principal: %w", err)
		}
	}
	if accruedAt != nil {
		d.AccruedAt = accruedAt.UTC()
	}
	d.CreatedAt, d.UpdatedAt = createdAt.UTC(), updateAt.UTC()
	return &d, nil
}

// writeValues are the JSON and nullable columns of d as Postgres takes
// them: nil for an empty amount, request or principal.
type writeValues struct {
	budget, override   *string
	request, principal []byte
}

func valuesOf(d *Deployment) (writeValues, error) {
	var (
		v   writeValues
		err error
	)
	if d.MonthlyBudgetUSD != "" {
		v.budget = &d.MonthlyBudgetUSD
	}
	if d.HourlyUSDOverride != "" {
		v.override = &d.HourlyUSDOverride
	}
	if d.Request != nil {
		if v.request, err = json.Marshal(d.Request); err != nil {
			return v, fmt.Errorf("encode model deployment request: %w", err)
		}
	}
	if d.RequestedBy != (security.Principal{}) {
		if v.principal, err = json.Marshal(d.RequestedBy); err != nil {
			return v, fmt.Errorf("encode model deployment principal: %w", err)
		}
	}
	return v, nil
}

// storeError turns the constraint errors Postgres raises into the Store's.
func storeError(op string, err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505": // unique_violation
			return ErrConflict
		case "23514", "22003": // check_violation, numeric_value_out_of_range
			return fmt.Errorf("%w: %s", ErrInvalid, pg.Message)
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}

func rollback(tx driver.Tx) { _ = tx.Rollback() } //nolint:errcheck // rollback is a no-op after commit

// Create implements Store.
func (s *PgStore) Create(ctx context.Context, d *Deployment) error {
	if err := d.validate(); err != nil {
		return err
	}
	id := d.ID
	if id == "" {
		tid, err := typeid.Generate("aimodel")
		if err != nil {
			return fmt.Errorf("generate model deployment id: %w", err)
		}
		id = tid.String()
	}
	v, err := valuesOf(d)
	if err != nil {
		return err
	}
	got, err := scanDeployment(s.db.QueryRow(ctx, `INSERT INTO loomplane_ai_model_deployments
			(id, org_id, project_id, environment_id, resource, entry_id, quantization, engine,
			monthly_budget_usd, hourly_usd_override, phase,
			server_cloud_id, server_operation_id, server_name, plan_id, message, request, requested_by,
			alerted_projected, alerted_crossed, revision, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric(12, 2), $10::numeric(10, 4), $11,
			$12, $13, $14, $15, $16, $17, $18, $19, $20, 1, $21, $21)
		RETURNING `+deploymentColumns,
		id, d.OrgID, d.ProjectID, d.EnvironmentID, d.Resource, d.EntryID, d.Quantization, d.Engine,
		v.budget, v.override, d.Phase,
		d.ServerCloudID, d.ServerOperationID, d.ServerName, d.PlanID, d.Message, v.request, v.principal,
		d.AlertedProjected, d.AlertedCrossed, s.now().UTC()))
	if err != nil {
		return storeError("create model deployment", err)
	}
	*d = *got
	return nil
}

// Get implements Store.
func (s *PgStore) Get(ctx context.Context, orgID, projectID, environmentID, resource string) (*Deployment, error) {
	d, err := scanDeployment(s.db.QueryRow(ctx, `SELECT `+deploymentColumns+` FROM loomplane_ai_model_deployments
		WHERE org_id = $1 AND project_id = $2 AND environment_id = $3 AND resource = $4`,
		orgID, projectID, environmentID, resource))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get model deployment: %w", err)
	}
	return d, nil
}

// List implements Store.
func (s *PgStore) List(ctx context.Context, orgID string) ([]*Deployment, error) {
	return s.list(ctx, `SELECT `+deploymentColumns+` FROM loomplane_ai_model_deployments
		WHERE org_id = $1 ORDER BY project_id, environment_id, resource`, orgID)
}

// ListProject implements Store.
func (s *PgStore) ListProject(ctx context.Context, orgID, projectID string) ([]*Deployment, error) {
	return s.list(ctx, `SELECT `+deploymentColumns+` FROM loomplane_ai_model_deployments
		WHERE org_id = $1 AND project_id = $2 ORDER BY environment_id, resource`, orgID, projectID)
}

// Waiting implements WaitingLister. It's the one read across organizations: the
// job that carries waiting deployments on runs for all of them.
func (s *PgStore) Waiting(ctx context.Context) ([]*Deployment, error) {
	return s.list(ctx, `SELECT `+deploymentColumns+` FROM loomplane_ai_model_deployments
		WHERE phase = 'waiting_for_server' ORDER BY updated_at, id`)
}

func (s *PgStore) list(ctx context.Context, query string, args ...any) ([]*Deployment, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list model deployments: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Deployment{}
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, fmt.Errorf("scan model deployment: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list model deployments: %w", err)
	}
	return out, nil
}

// Update implements Store.
func (s *PgStore) Update(ctx context.Context, d *Deployment) error {
	if err := d.validate(); err != nil {
		return err
	}
	v, err := valuesOf(d)
	if err != nil {
		return err
	}
	got, err := scanDeployment(s.db.QueryRow(ctx, `UPDATE loomplane_ai_model_deployments SET
			entry_id = $4, quantization = $5, engine = $6,
			monthly_budget_usd = $7::numeric(12, 2), hourly_usd_override = $8::numeric(10, 4), phase = $9,
			server_cloud_id = $10, server_operation_id = $11, server_name = $12, plan_id = $13, message = $14,
			request = $15, requested_by = $16, alerted_projected = $17, alerted_crossed = $18,
			revision = revision + 1, updated_at = $19
		WHERE org_id = $1 AND id = $2 AND revision = $3
		RETURNING `+deploymentColumns,
		d.OrgID, d.ID, d.Revision, d.EntryID, d.Quantization, d.Engine,
		v.budget, v.override, d.Phase,
		d.ServerCloudID, d.ServerOperationID, d.ServerName, d.PlanID, d.Message,
		v.request, v.principal, d.AlertedProjected, d.AlertedCrossed, s.now().UTC()))
	if errors.Is(err, pgx.ErrNoRows) {
		return s.missingOrStale(ctx, d.OrgID, d.ID)
	}
	if err != nil {
		return storeError("update model deployment", err)
	}
	*d = *got
	return nil
}

// missingOrStale tells a row that's gone from one that moved on.
func (s *PgStore) missingOrStale(ctx context.Context, orgID, id string) error {
	var exists bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM loomplane_ai_model_deployments WHERE org_id = $1 AND id = $2)`,
		orgID, id).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check model deployment: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	return ErrStale
}

// Delete implements Store.
func (s *PgStore) Delete(ctx context.Context, orgID, projectID, environmentID, resource string) error {
	res, err := s.db.Exec(ctx, `DELETE FROM loomplane_ai_model_deployments
		WHERE org_id = $1 AND project_id = $2 AND environment_id = $3 AND resource = $4`,
		orgID, projectID, environmentID, resource)
	if err != nil {
		return fmt.Errorf("delete model deployment: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete model deployment: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Accrue implements Store.
func (s *PgStore) Accrue(ctx context.Context, d *Deployment, month, addUSD string, at time.Time) error {
	if !monthPattern.MatchString(month) {
		return fmt.Errorf("%w: month %q isn't like 2026-11", ErrInvalid, month)
	}
	add, err := costAmount.parse(addUSD)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("accrue model cost: %w", err)
	}
	defer rollback(tx)

	// The caller's AccruedAt is the last accrual it saw. A zero one is a
	// row never accrued, stored as NULL.
	var read *time.Time
	if !d.AccruedAt.IsZero() {
		t := d.AccruedAt.UTC()
		read = &t
	}
	var (
		storedMonth, storedCost string
		sameRead                bool
	)
	err = tx.QueryRow(ctx, `SELECT month, month_cost_usd::text, accrued_at IS NOT DISTINCT FROM $3::timestamptz
		FROM loomplane_ai_model_deployments
		WHERE org_id = $1 AND id = $2 FOR UPDATE`, d.OrgID, d.ID, read).Scan(&storedMonth, &storedCost, &sameRead)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock model deployment: %w", err)
	}
	if !sameRead {
		return fmt.Errorf("%w: it was accrued again since it was read", ErrStale)
	}
	var total int64
	switch {
	case month < storedMonth:
		return fmt.Errorf("%w: it's accruing %s already, not %s", ErrStale, storedMonth, month)
	case month == storedMonth:
		cost, parseErr := costAmount.parse(storedCost)
		if parseErr != nil {
			return fmt.Errorf("read model cost: %w", parseErr)
		}
		total = cost + add
	default: // a new month starts from nothing
		total = add
	}

	var (
		gotMonth, gotCost string
		gotAt             time.Time
	)
	err = tx.QueryRow(ctx, `UPDATE loomplane_ai_model_deployments
		SET month = $3, month_cost_usd = $4::numeric(12, 4), accrued_at = $5
		WHERE org_id = $1 AND id = $2
		RETURNING month, month_cost_usd::text, accrued_at`,
		d.OrgID, d.ID, month, formatUSD(total), at.UTC()).Scan(&gotMonth, &gotCost, &gotAt)
	if err != nil {
		return storeError("accrue model cost", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("accrue model cost: %w", err)
	}
	d.Month, d.MonthCostUSD, d.AccruedAt = gotMonth, gotCost, gotAt.UTC()
	return nil
}
