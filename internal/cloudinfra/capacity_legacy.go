package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
)

// Historical attempted operations are durable unknown holds when their approved
// bodies predate capacity targets. The organization lock serializes this scan
// with first attempts and inventory replacement across Clouds and connections.
func unresolvedLegacyCapacity(ctx context.Context, tx driver.Tx, org, provider, account string) (bool, error) {
	rows, err := tx.Query(ctx, `SELECT body FROM loomplane_infrastructure_operations WHERE org_id=$1 AND body->>'provider'=$2 AND body->>'account_id'=$3 AND body->>'attempted'='true' AND body->'input'->>'kind' IN ('create','resize') AND (body->'capacity_target' IS NULL OR body->'capacity_target'='null'::jsonb)`, org, provider, account)
	if err != nil {
		return false, err
	}
	var pending []Operation
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return false, err
		}
		var o Operation
		if err = json.Unmarshal(raw, &o); err != nil {
			rows.Close()
			return false, err
		}
		pending = append(pending, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(pending) == 0 {
		return false, err
	}
	var raw, accountRaw []byte
	if err = tx.QueryRow(ctx, `SELECT membership,body FROM loomplane_cloud_capacity_accounts WHERE org_id=$1 AND provider=$2 AND account_id=$3 FOR UPDATE`, org, provider, account).Scan(&raw, &accountRaw); errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	if len(raw) == 0 {
		return true, nil
	}
	var snapshot CapacityAccountSnapshot
	if checkErr := json.Unmarshal(accountRaw, &snapshot); checkErr != nil {
		return false, checkErr
	}
	checked := snapshot.CheckedAt
	var membership *accountMembership
	if checkErr := json.Unmarshal(raw, &membership); checkErr != nil {
		return false, checkErr
	}
	if membership == nil || !membership.Complete || membership.Provider != provider || membership.AccountID != account || !membership.CheckedAt.Equal(checked) || !freshCapacity(checked, time.Now()) {
		return true, nil
	}
	for _, o := range pending {
		settled, checkErr := legacyCapacitySettled(ctx, tx, &o, membership)
		if checkErr != nil || !settled {
			return !settled, checkErr
		}
	}
	return false, nil
}

func legacyCapacitySettled(ctx context.Context, tx driver.Tx, o *Operation, membership *accountMembership) (bool, error) {
	if o.ResourceID == "" || o.UpdatedAt.IsZero() || membership.CheckedAt.Before(o.UpdatedAt) {
		return false, nil
	}
	for _, server := range membership.Servers {
		if server.ID != o.ResourceID {
			continue
		}
		// A matching physical server alone cannot settle a resize still in flight.
		// All accepted actions must complete before this account sample starts.
		if len(o.Actions) == 0 || server.CPUMillis == nil || server.MemoryBytes == nil || server.StorageBytes == nil {
			return false, nil
		}
		for _, action := range o.Actions {
			if action.ID == "" || action.ResourceID != o.ResourceID || action.Kind != o.Input.Kind || action.Status != "completed" {
				return false, nil
			}
		}
		return true, nil
	}
	// An empty account snapshot alone cannot prove a lost create never happened.
	// Require a later GetServer absence receipt for this exact resolved identity.
	var absent bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_infrastructure_operations WHERE org_id=$1 AND cloud_id=$2 AND body->>'server_id'=$3 AND body->>'provider'=$4 AND body->>'account_id'=$5 AND body->>'resource_id'=$6 AND body->'input'->>'kind'='delete' AND (body->>'absence_confirmed_at')::timestamptz >= $7 AND (body->>'absence_confirmed_at')::timestamptz <= $8)`, o.OrgID, o.CloudID, o.ServerID, o.Provider, o.AccountID, o.ResourceID, o.UpdatedAt, membership.CheckedAt).Scan(&absent)
	return absent, err
}
