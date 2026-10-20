package planning

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudprofiles"
)

type syncMutationKey struct{}

// WithCloudSyncMutation marks the binding transaction whose after-value belongs
// to a reviewed migration. It carries no authority by itself.
func WithCloudSyncMutation(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, syncMutationKey{}, id)
}

func syncMutation(ctx context.Context) string {
	id, ok := ctx.Value(syncMutationKey{}).(string)
	if !ok {
		return ""
	}
	return id
}

type bindingExecutor interface {
	Exec(context.Context, string, ...any) (driver.Result, error)
}

func (s *PgStore) checkpointCloudBinding(ctx context.Context, tx driver.Tx, syncID, org, binding string) error {
	// Capture the database row, including unknown configuration and replaced resources.
	_, err := tx.Exec(ctx, `INSERT INTO loomplane_cloud_sync_bindings(sync_id,org_id,environment_id,binding_id,after_value)
 SELECT $1,org_id,environment_id,id,to_jsonb(b)-'observed_at'-'drift_message' FROM loomplane_bindings b WHERE org_id=$2 AND id=$3
 ON CONFLICT(sync_id,binding_id) DO UPDATE SET after_value=EXCLUDED.after_value`, syncID, org, binding)
	return err
}

// CheckCloudCompensation refuses to undo a changed serving value, including an
// untouched service edited after the original review. Provider drift also blocks.
func (s *PgStore) CheckCloudCompensation(ctx context.Context, org, syncID, environment string, before json.RawMessage) error {
	var matches bool
	err := s.db.QueryRow(ctx, cloudprofiles.CompareCompensationSQL, org, syncID, []byte(before), environment).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return errors.New("a serving resource changed outside this sync; compensation needs a new review")
	}
	return nil
}
