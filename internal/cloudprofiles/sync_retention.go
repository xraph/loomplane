package cloudprofiles

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/connections"
)

func validateLockedConnections(ctx context.Context, tx driver.Tx, org string, snapshot *Snapshot) error {
	for _, id := range snapshot.Connections {
		if id == "" {
			continue
		}
		var cloud, account, status string
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT cloud,account,status,details FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2 FOR SHARE`, org, id).Scan(&cloud, &account, &status, &raw); err != nil {
			return storeError(err)
		}
		var details map[string]string
		if err := json.Unmarshal(raw, &details); err != nil {
			return err
		}
		if expected := snapshot.ConnectionIdentities[id]; expected != "" && connections.AccountIdentity(cloud, account, details) != expected {
			return fmt.Errorf("%w: connection %s changed identity", ErrConflict, id)
		}
	}
	return nil
}
