package cloudprofiles

import (
	"context"
	"encoding/json"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/security"
)

// setManagedCleanupActor binds cleanup acceptance to the authenticated request.
// The preview's actor describes its author and cannot authorize new cleanup.
func setManagedCleanupActor(ctx context.Context, tx driver.Tx, snapshot *Snapshot, sync *Sync) error {
	managed := false
	for _, environment := range snapshot.Environments {
		if len(environment.Plan) == 0 {
			continue
		}
		var plan struct {
			Actions []struct {
				CleanupTargets []json.RawMessage `json:"cleanup_targets"`
			} `json:"actions"`
		}
		if err := json.Unmarshal(environment.Plan, &plan); err != nil {
			return err
		}
		for _, action := range plan.Actions {
			managed = managed || len(action.CleanupTargets) > 0
		}
	}
	if !managed {
		return nil
	}
	actor, ok := security.PrincipalFrom(ctx)
	if !ok || actor.OrgID != sync.OrgID || actor.UserID == "" || actor.UserID != sync.CreatedBy || actor.ReadOnly || sync.Actor != actor || snapshot.Actor != actor {
		return security.ErrForbidden
	}
	raw, err := json.Marshal(actor)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT set_config('loomplane.capacity_actor',$1,true)`, string(raw))
	return err
}
