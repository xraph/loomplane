package cloudinfra

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xraph/grove/driver"
)

// reserveLegacyAttempt keeps an unresolved effect visible if cluster accounting
// starts after this transaction. The caller holds the organization and Cloud locks.
func reserveLegacyAttempt(ctx context.Context, tx driver.Tx, o *Operation) error {
	p, err := capacityPolicyTx(ctx, tx, o.OrgID, o.CloudID)
	if err != nil || p.Revision != 0 || o.Provider == "server" {
		return err
	}
	if o.CapacityTarget == nil || !nonnegativeCapacity(*o.CapacityTarget) {
		return ErrConflict
	}
	// The full resized target is a conservative hold until inventory settles it.
	delta := *o.CapacityTarget
	r := infrastructureReservation{LegacyAttempt: true, OperationID: o.ID, Provider: o.Provider, AccountID: o.AccountID, ServerID: o.ServerID, Delta: delta, Target: delta, CreatedAt: time.Now().UTC()}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_capacity_reservations(org_id,cloud_id,id,policy_revision,intent_hash,servers,cpu_millis,memory_bytes,storage_bytes,status,body) VALUES($1,$2,$3,0,$3,$4,$5,$6,$7,'reserved',$8) ON CONFLICT(org_id,cloud_id,id) DO NOTHING`, o.OrgID, o.CloudID, o.ID, delta.Servers, delta.CPUMillis, delta.MemoryBytes, delta.StorageBytes, raw)
	return err
}
