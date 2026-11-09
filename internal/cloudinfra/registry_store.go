package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/connections"
)

func (s *PgStore) RegistryState(ctx context.Context, org, id string) (*RegistryState, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT body FROM loomplane_registry_states WHERE org_id=$1 AND connection_id=$2`, org, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var state RegistryState
	return &state, json.Unmarshal(raw, &state)
}
func (s *PgStore) SaveRegistryState(ctx context.Context, state *RegistryState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	var zone any
	if state.DNS != nil && state.Status != "removed" {
		zone = state.DNS.ZoneID
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_registry_states(org_id,connection_id,status,zone_id,body) VALUES($1,$2,$3,$4,$5) ON CONFLICT(org_id,connection_id) DO UPDATE SET status=excluded.status,zone_id=excluded.zone_id,body=excluded.body,updated_at=now()`, state.OrgID, state.ConnectionID, state.Status, zone, raw); err != nil {
		return err
	}
	if state.Status == "removed" {
		_, err = tx.Exec(ctx, `DELETE FROM loomplane_connection_uses WHERE org_id=$1 AND kind='managed_registry' AND owner_id=$2`, state.OrgID, state.ConnectionID)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES($1,$2,'managed_registry',$2) ON CONFLICT DO NOTHING`, state.OrgID, state.ConnectionID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *PgStore) RegistryCandidates(ctx context.Context) ([]*connections.Connection, error) {
	rows, err := s.db.Query(ctx, `SELECT c.org_id,c.id FROM loomplane_cloud_connections c LEFT JOIN loomplane_registry_states r ON r.org_id=c.org_id AND r.connection_id=c.id WHERE c.cloud='kubernetes' AND c.method IN ('token','in_cluster') AND coalesce(c.details->>'registry_hostname','')<>'' AND coalesce(r.status,'')<>'removed' ORDER BY coalesce(r.updated_at,'epoch'::timestamptz),c.id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	var ids [][2]string
	for rows.Next() {
		var item [2]string
		if err = rows.Scan(&item[0], &item[1]); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	store := connections.NewPgStore(s.db)
	out := make([]*connections.Connection, 0, len(ids))
	for _, id := range ids {
		c, e := store.GetConnection(ctx, id[0], id[1])
		if errors.Is(e, connections.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, nil
}
func (s *PgStore) RegistryInUse(ctx context.Context, org, id string) (bool, error) {
	var used bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_connection_uses WHERE org_id=$1 AND connection_id=$2 AND kind<>'managed_registry') OR EXISTS(SELECT 1 FROM loomplane_deployments WHERE org_id=$1 AND status NOT IN ('succeeded','failed','cancelled'))`, org, id).Scan(&used)
	return used, err
}

func (s *PgStore) RegistrySetupInUse(ctx context.Context, c *connections.Connection) (bool, error) {
	var used bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cloud_connections other LEFT JOIN loomplane_registry_states r ON r.org_id=other.org_id AND r.connection_id=other.id WHERE other.cloud='kubernetes' AND other.id<>$1 AND (other.details->>'cluster_ca'=$2 OR other.details->>'endpoint'=$3) AND coalesce(r.status,'')<>'removed')`, c.ID, c.Details["cluster_ca"], c.Details["endpoint"]).Scan(&used)
	return used, err
}
