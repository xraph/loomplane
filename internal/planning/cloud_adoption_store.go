package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// CloudAdoptionStore updates only placement metadata if the serving row has not
// changed since it was checked. Implementations must compare and write atomically.
type CloudAdoptionStore interface {
	AdoptCloudPlacement(ctx context.Context, expected *Binding, placement *CloudPlacement, at time.Time) (bool, error)
}

// AdoptCloudPlacement leaves every applied provider field and unknown config
// field intact. PostgreSQL rechecks this predicate after a concurrent row write.
func (s *PgStore) AdoptCloudPlacement(ctx context.Context, expected *Binding, placement *CloudPlacement, at time.Time) (bool, error) {
	raw, err := json.Marshal(placement)
	if err != nil {
		return false, fmt.Errorf("encode Cloud placement: %w", err)
	}
	tag, err := s.db.Exec(ctx, `UPDATE loomplane_bindings
 SET config=jsonb_set(config,'{cloud_placement}',$1::jsonb,true),updated_at=$2
 WHERE org_id=$3 AND project_id=$4 AND environment_id=$5 AND id=$6
 AND subject=$7 AND name=$8 AND provider=$9 AND region=$10 AND connection_id=$11
 AND external_id=$12 AND revision=$13 AND plan_id=$14 AND config=$15::jsonb
 AND updated_at=$16 AND drift=''`, raw, at, expected.OrgID, expected.ProjectID, expected.EnvironmentID, expected.ID,
		expected.Subject, expected.Name, expected.Provider, expected.Region, expected.ConnectionID, expected.ExternalID,
		expected.Revision, expected.PlanID, []byte(expected.Config), expected.UpdatedAt)
	if err != nil {
		return false, fmt.Errorf("adopt Cloud placement: %w", err)
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count adopted Cloud placement: %w", err)
	}
	return n == 1, nil
}
