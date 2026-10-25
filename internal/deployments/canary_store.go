package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/releasehealth"
)

// encodeCanary is c as the canary column stores it, nil (SQL NULL) for no
// canary.
func encodeCanary(c *Canary) ([]byte, error) {
	if c == nil {
		return nil, nil
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encode canary: %w", err)
	}
	return raw, nil
}

// SetCanary implements CanaryStore.
func (s *PgStore) SetCanary(ctx context.Context, orgID, deploymentID string, c *Canary) error {
	raw, err := encodeCanary(c)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(ctx, `UPDATE loomplane_deployments SET canary = $3 WHERE org_id = $1 AND id = $2`, orgID, deploymentID, raw)
	if err != nil {
		return fmt.Errorf("set canary: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set canary: rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordVerdict implements CanaryStore. It sets v.ID from the deployment,
// action, step and attempt, so a retried judgement finds its row and leaves
// it as it is. The project, and so the realtime event's scope, is the
// deployment's: a deployment outside v.OrgID records nothing and returns
// ErrNotFound.
func (s *PgStore) RecordVerdict(ctx context.Context, v *CanaryVerdict) error {
	v.ID = VerdictID(v.DeploymentID, v.ActionID, v.Step, v.Attempt)
	baseline, err := json.Marshal(v.Baseline)
	if err != nil {
		return fmt.Errorf("encode baseline health: %w", err)
	}
	candidate, err := json.Marshal(v.Candidate)
	if err != nil {
		return fmt.Errorf("encode candidate health: %w", err)
	}
	thresholds, err := json.Marshal(v.Thresholds)
	if err != nil {
		return fmt.Errorf("encode thresholds: %w", err)
	}
	var project string
	err = s.db.QueryRow(ctx, `WITH d AS (SELECT org_id, project_id, id FROM loomplane_deployments WHERE org_id = $2 AND id = $3),
		ins AS (INSERT INTO loomplane_canary_verdicts (`+verdictColumns+`)
			SELECT $1, d.org_id, d.project_id, d.id, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16 FROM d
			ON CONFLICT (id) DO NOTHING)
		SELECT project_id FROM d`,
		v.ID, v.OrgID, v.DeploymentID, v.ActionID, v.Service, v.Step, v.Attempt, v.Weight, string(v.Verdict),
		v.Reason, baseline, candidate, thresholds, v.WindowStart, v.WindowEnd, v.DecidedAt).Scan(&project)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("record verdict: %w", err)
	}
	v.ProjectID = project
	return nil
}

const verdictColumns = `id, org_id, project_id, deployment_id, action_id, service, step, attempt, weight, verdict,
	reason, baseline, candidate, thresholds, window_start, window_end, decided_at`

func scanVerdict(row pgx.Row) (CanaryVerdict, error) {
	var v CanaryVerdict
	var verdict string
	var baseline, candidate, thresholds []byte
	if err := row.Scan(&v.ID, &v.OrgID, &v.ProjectID, &v.DeploymentID, &v.ActionID, &v.Service, &v.Step, &v.Attempt, &v.Weight, &verdict,
		&v.Reason, &baseline, &candidate, &thresholds, &v.WindowStart, &v.WindowEnd, &v.DecidedAt); err != nil {
		return v, err
	}
	v.Verdict = releasehealth.Verdict(verdict)
	for _, f := range []struct {
		raw  []byte
		into any
	}{{baseline, &v.Baseline}, {candidate, &v.Candidate}, {thresholds, &v.Thresholds}} {
		if err := json.Unmarshal(f.raw, f.into); err != nil {
			return v, fmt.Errorf("decode verdict %s: %w", v.ID, err)
		}
	}
	v.WindowStart, v.WindowEnd, v.DecidedAt = v.WindowStart.UTC(), v.WindowEnd.UTC(), v.DecidedAt.UTC()
	return v, nil
}

// Verdicts implements CanaryStore.
func (s *PgStore) Verdicts(ctx context.Context, orgID, deploymentID string) ([]CanaryVerdict, error) {
	rows, err := s.db.Query(ctx, `SELECT `+verdictColumns+` FROM loomplane_canary_verdicts
		WHERE org_id = $1 AND deployment_id = $2 ORDER BY step, attempt, action_id`, orgID, deploymentID)
	if err != nil {
		return nil, fmt.Errorf("list verdicts: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []CanaryVerdict{}
	for rows.Next() {
		v, err := scanVerdict(rows)
		if err != nil {
			return nil, fmt.Errorf("scan verdict: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list verdicts: %w", err)
	}
	return out, nil
}

// RecentCanaries implements CanaryStore.
func (s *PgStore) RecentCanaries(ctx context.Context, orgID, projectID, environmentID string, limit int) ([]*Deployment, error) {
	rows, err := s.db.Query(ctx, `SELECT `+columns+` FROM loomplane_deployments
		WHERE org_id = $1 AND project_id = $2 AND environment_id = $3 AND canary IS NOT NULL
		ORDER BY started_at DESC, id DESC LIMIT $4`, orgID, projectID, environmentID, limit)
	if err != nil {
		return nil, fmt.Errorf("list canaries: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Deployment{}
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan deployment: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list canaries: %w", err)
	}
	return out, nil
}

// RampingCanary implements CanaryStore. Planning blocks new plans with the
// same read, so it's planning's.
func (s *PgStore) RampingCanary(ctx context.Context, orgID, environmentID string) (string, error) {
	return planning.NewPgStore(s.db).RampingCanary(ctx, orgID, environmentID)
}

// CanaryBinding implements CanaryStore.
func (s *PgStore) CanaryBinding(ctx context.Context, orgID, bindingID string) (bool, error) {
	var ramps bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM loomplane_deployments
		WHERE org_id = $1 AND status = 'running' AND canary->>'status' = ANY($3::text[])
		AND canary->'services' @> jsonb_build_array(jsonb_build_object('binding_id', $2::text)))`,
		orgID, bindingID, canaryActive).Scan(&ramps); err != nil {
		return false, fmt.Errorf("find canary binding: %w", err)
	}
	return ramps, nil
}
