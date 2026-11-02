package providerresources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
)

// PgStore persists observations and safe sync history. No table holds a DSN.
type PgStore struct{ db *pgdriver.PgDB }

func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

func (s *PgStore) Get(ctx context.Context, org, id string) (*ProviderResource, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT body FROM loomplane_provider_resources WHERE org_id=$1 AND id=$2`, org, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var r ProviderResource
	if decodeErr := json.Unmarshal(raw, &r); decodeErr != nil {
		return nil, decodeErr
	}
	return &r, nil
}
func (s *PgStore) List(ctx context.Context, org string) (*ProviderResourceList, error) {
	out := &ProviderResourceList{Resources: []*ProviderResource{}, Syncs: []*ProviderResourceSync{}}
	rows, err := s.db.Query(ctx, `SELECT body FROM loomplane_provider_resources WHERE org_id=$1 ORDER BY id`, org)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw []byte
		if scanErr := rows.Scan(&raw); scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		var r ProviderResource
		if decodeErr := json.Unmarshal(raw, &r); decodeErr != nil {
			rows.Close()
			return nil, decodeErr
		}
		out.Resources = append(out.Resources, &r)
	}
	rows.Close()
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	rows, err = s.db.Query(ctx, `SELECT body FROM loomplane_provider_resource_syncs WHERE org_id=$1 ORDER BY body->>'started_at' DESC LIMIT 100`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if scanErr := rows.Scan(&raw); scanErr != nil {
			return nil, scanErr
		}
		var r ProviderResourceSync
		if decodeErr := json.Unmarshal(raw, &r); decodeErr != nil {
			return nil, decodeErr
		}
		out.Syncs = append(out.Syncs, &r)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	return out, nil
}
func rollback(tx driver.Tx) { _ = tx.Rollback() } //nolint:errcheck // a committed transaction cannot roll back

// Update serializes the verified account rather than the connection ID. Holding
// the connection row lock through commit fences credential rotation as well.
func (s *PgStore) Update(ctx context.Context, org, cloud, account, conn string, generation int64, fn func(*AccountState) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, insertErr := tx.Exec(ctx, `INSERT INTO loomplane_provider_resource_accounts(org_id,provider,account_identity,sequence) VALUES($1,$2,$3,0) ON CONFLICT DO NOTHING`, org, cloud, account); insertErr != nil {
		return insertErr
	}
	state := &AccountState{Resources: []*ProviderResource{}, Syncs: []*ProviderResourceSync{}}
	if lockErr := tx.QueryRow(ctx, `SELECT sequence FROM loomplane_provider_resource_accounts WHERE org_id=$1 AND provider=$2 AND account_identity=$3 FOR UPDATE`, org, cloud, account).Scan(&state.Sequence); lockErr != nil {
		return lockErr
	}
	if conn != "" {
		var current int64
		var status string
		err = tx.QueryRow(ctx, `SELECT generation,status FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2 AND cloud=$3 FOR SHARE`, org, conn, cloud).Scan(&current, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrChanged
		}
		if err != nil {
			return err
		}
		if current != generation || status != "ready" {
			return ErrChanged
		}
	}
	beforeResources := map[string][]byte{}
	beforeSyncs := map[string][]byte{}
	for _, table := range []string{"loomplane_provider_resources", "loomplane_provider_resource_syncs"} {
		rows, qerr := tx.Query(ctx, `SELECT body FROM `+table+` WHERE org_id=$1 AND provider=$2 AND account_identity=$3`, org, cloud, account)
		if qerr != nil {
			return qerr
		}
		for rows.Next() {
			var raw []byte
			if scanErr := rows.Scan(&raw); scanErr != nil {
				rows.Close()
				return scanErr
			}
			if table == "loomplane_provider_resources" {
				var r ProviderResource
				if decodeErr := json.Unmarshal(raw, &r); decodeErr != nil {
					rows.Close()
					return decodeErr
				}
				normalized, marshalErr := json.Marshal(&r)
				if marshalErr != nil {
					rows.Close()
					return marshalErr
				}
				beforeResources[r.ID] = normalized
				state.Resources = append(state.Resources, &r)
			} else {
				var r ProviderResourceSync
				if decodeErr := json.Unmarshal(raw, &r); decodeErr != nil {
					rows.Close()
					return decodeErr
				}
				normalized, marshalErr := json.Marshal(&r)
				if marshalErr != nil {
					rows.Close()
					return marshalErr
				}
				beforeSyncs[r.ID] = normalized
				state.Syncs = append(state.Syncs, &r)
			}
		}
		rows.Close()
		if rowsErr := rows.Err(); rowsErr != nil {
			return rowsErr
		}
	}
	if err := fn(state); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE loomplane_provider_resource_accounts SET sequence=$4 WHERE org_id=$1 AND provider=$2 AND account_identity=$3`, org, cloud, account, state.Sequence); err != nil {
		return err
	}
	for _, r := range state.Resources {
		if r.OrgID != org || r.Provider != cloud || r.AccountIdentity != account {
			return ErrInvalid
		}
		raw, merr := json.Marshal(r)
		if merr != nil {
			return merr
		}
		if bytes.Equal(raw, beforeResources[r.ID]) {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO loomplane_provider_resources(id,org_id,provider,account_identity,body) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO UPDATE SET body=EXCLUDED.body WHERE loomplane_provider_resources.org_id=EXCLUDED.org_id AND loomplane_provider_resources.provider=EXCLUDED.provider AND loomplane_provider_resources.account_identity=EXCLUDED.account_identity`, r.ID, org, cloud, account, raw); err != nil {
			return err
		}
	}
	for _, r := range state.Syncs {
		if r.OrgID != org || r.Provider != cloud || r.AccountIdentity != account {
			return ErrInvalid
		}
		raw, merr := json.Marshal(r)
		if merr != nil {
			return merr
		}
		if bytes.Equal(raw, beforeSyncs[r.ID]) {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO loomplane_provider_resource_syncs(id,org_id,provider,account_identity,body) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO UPDATE SET body=EXCLUDED.body WHERE loomplane_provider_resource_syncs.org_id=EXCLUDED.org_id`, r.ID, org, cloud, account, raw); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Tracked returns the newest authorized attempt for every tracked connection,
// independent of the shorter history shown in the Resources screen.
func (s *PgStore) Tracked(ctx context.Context, org string) ([]*ProviderResourceSync, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT ON (body->>'connection_id') body FROM loomplane_provider_resource_syncs WHERE org_id=$1 AND body->>'refresh_opt_in'='true' ORDER BY body->>'connection_id',body->>'started_at' DESC`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ProviderResourceSync{}
	for rows.Next() {
		var raw []byte
		if scanErr := rows.Scan(&raw); scanErr != nil {
			return nil, scanErr
		}
		var r ProviderResourceSync
		if decodeErr := json.Unmarshal(raw, &r); decodeErr != nil {
			return nil, decodeErr
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}
