package deployments

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// AppliedVariableStore chooses one immutable secret reference per action before
// any provider mutation. Losing writers never overwrite the winning secret.
type AppliedVariableStore interface {
	AppliedVariables(ctx context.Context, orgID, deploymentID, actionID string) (string, error)
	ClaimAppliedVariables(ctx context.Context, orgID, deploymentID, actionID, ref string) (string, error)
}

// AppliedVariables returns an action's captured base variables, if any.
func (s *PgStore) AppliedVariables(ctx context.Context, orgID, deploymentID, actionID string) (string, error) {
	var ref string
	err := s.db.QueryRow(ctx, `SELECT secret_ref FROM loomplane_applied_variables WHERE org_id=$1 AND deployment_id=$2 AND action_id=$3`, orgID, deploymentID, actionID).Scan(&ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return ref, err
}

// ClaimAppliedVariables publishes a fresh encrypted snapshot reference once.
func (s *PgStore) ClaimAppliedVariables(ctx context.Context, orgID, deploymentID, actionID, ref string) (string, error) {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_applied_variables(org_id,deployment_id,action_id,secret_ref) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, orgID, deploymentID, actionID, ref)
	if err != nil {
		return "", err
	}
	return s.AppliedVariables(ctx, orgID, deploymentID, actionID)
}
