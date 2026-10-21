package cloudinfra

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/security"
)

// Pool groups compatible runtimes. Membership does not create a private network.
type Pool struct {
	ID       string       `json:"id" forge:"id"`
	OrgID    string       `json:"org_id"`
	CloudID  string       `json:"cloud_id"`
	Name     string       `json:"name"`
	Revision int64        `json:"revision"`
	Members  []PoolMember `json:"members"`
}

// PoolMember carries an explicitly reviewed usable host budget.
type PoolMember struct {
	ServerID    string `json:"server_id"`
	CPUMillis   int64  `json:"cpu_millis"`
	MemoryBytes int64  `json:"memory_bytes"`
	Draining    bool   `json:"draining"`
}
type PoolInput struct {
	Name     string       `json:"name"`
	Revision int64        `json:"revision"`
	Members  []PoolMember `json:"members"`
}
type PoolStore interface {
	GetPool(context.Context, string, string, string) (*Pool, error)
	ListPools(context.Context, string, string) ([]*Pool, error)
	SavePool(context.Context, *Pool, int64) error
}

func (s *Service) ListPools(ctx context.Context, p security.Principal, cloud string) ([]*Pool, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	st, ok := s.Store.(PoolStore)
	if !ok {
		return nil, ErrInvalid
	}
	return st.ListPools(ctx, p.OrgID, cloud)
}
func (s *Service) SavePool(ctx context.Context, p security.Principal, cloud, id string, in PoolInput) (*Pool, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	st, ok := s.Store.(PoolStore)
	if !ok {
		return nil, ErrInvalid
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 64 || len(in.Members) == 0 || len(in.Members) > 100 {
		return nil, ErrInvalid
	}
	if id == "" {
		if in.Revision != 0 {
			return nil, ErrConflict
		}
		v, err := typeid.Generate("pool")
		if err != nil {
			return nil, err
		}
		id = v.String()
	}
	pool := &Pool{ID: id, OrgID: p.OrgID, CloudID: cloud, Name: strings.TrimSpace(in.Name), Revision: in.Revision + 1, Members: append([]PoolMember{}, in.Members...)}
	slices.SortFunc(pool.Members, func(a, b PoolMember) int { return strings.Compare(a.ServerID, b.ServerID) })
	for i, m := range pool.Members {
		if m.ServerID == "" || i > 0 && pool.Members[i-1].ServerID == m.ServerID {
			return nil, ErrInvalid
		}
	}
	if err := st.SavePool(ctx, pool, in.Revision); err != nil {
		return nil, err
	}
	return pool, nil
}
func (s *PgStore) GetPool(ctx context.Context, org, cloud, id string) (*Pool, error) {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT body FROM loomplane_server_pools WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, org, cloud, id).Scan(&raw); err != nil {
		return nil, storeError(err)
	}
	var p Pool
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
func (s *PgStore) ListPools(ctx context.Context, org, cloud string) ([]*Pool, error) {
	rows, err := s.db.Query(ctx, `SELECT body FROM loomplane_server_pools WHERE org_id=$1 AND cloud_id=$2 ORDER BY id`, org, cloud)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Pool{}
	for rows.Next() {
		var raw []byte
		var p Pool
		if checkErr := rows.Scan(&raw); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := json.Unmarshal(raw, &p); checkErr != nil {
			return nil, checkErr
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}
func (s *PgStore) SavePool(ctx context.Context, p *Pool, previous int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	// All membership edits and reservations share the same ordered host locks.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('pool-config/'||$1,0))`, p.OrgID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM loomplane_cloud_servers WHERE org_id=$1 AND (id=ANY($2::text[]) OR id IN (SELECT server_id FROM loomplane_server_pool_members WHERE pool_id=$3)) ORDER BY id FOR UPDATE`, p.OrgID, memberIDs(p.Members), p.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
	}
	rows.Close()
	if checkErr := rows.Err(); checkErr != nil {
		return checkErr
	}
	for _, m := range p.Members {
		var raw []byte
		if err = tx.QueryRow(ctx, `SELECT body FROM loomplane_cloud_servers WHERE org_id=$1 AND cloud_id=$2 AND id=$3 FOR UPDATE`, p.OrgID, p.CloudID, m.ServerID).Scan(&raw); err != nil {
			return storeError(err)
		}
		var v Server
		if checkErr := json.Unmarshal(raw, &v); checkErr != nil {
			return checkErr
		}
		if checkErr := usable(&v, m, time.Now()); checkErr != nil {
			return checkErr
		}
		var unmanaged bool
		if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_bindings WHERE org_id=$1 AND connection_id=$2 AND coalesce(config->'cloud_placement'->>'pool_id','')='') OR EXISTS(SELECT 1 FROM loomplane_plans p CROSS JOIN LATERAL jsonb_array_elements(coalesce(p.result->'actions','[]')) a WHERE p.org_id=$1 AND p.status='approved' AND a->>'connection_id'=$2 AND coalesce(a->'service'->'cloud_placement'->>'pool_id','')='' AND NOT EXISTS(SELECT 1 FROM loomplane_deployments d WHERE d.plan_id=p.id AND d.status='succeeded'))`, p.OrgID, v.RuntimeConnectionID).Scan(&unmanaged); checkErr != nil {
			return checkErr
		}
		if unmanaged {
			return fmt.Errorf("%w: existing direct workloads need explicit migration before pooling", ErrConflict)
		}
		var other bool
		if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_server_pool_members WHERE org_id=$1 AND server_id=$2 AND pool_id<>$3 AND (cpu_millis<>$4 OR memory_bytes<>$5))`, p.OrgID, m.ServerID, p.ID, m.CPUMillis, m.MemoryBytes).Scan(&other); checkErr != nil {
			return checkErr
		}
		if other {
			return fmt.Errorf("%w: a server must use the same budget in every pool", ErrInvalid)
		}
		var cpu, mem int64
		if checkErr := tx.QueryRow(ctx, `SELECT coalesce(sum(cpu),0),coalesce(sum(memory),0) FROM (SELECT max(cpu_millis) cpu,max(memory_bytes) memory FROM loomplane_capacity_reservations WHERE org_id=$1 AND server_id=$2 GROUP BY environment_id,service) r`, p.OrgID, m.ServerID).Scan(&cpu, &mem); checkErr != nil {
			return checkErr
		}
		if cpu > m.CPUMillis || mem > m.MemoryBytes {
			return fmt.Errorf("%w: budget is below reserved capacity", ErrConflict)
		}
	}
	var busy bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_capacity_reservations WHERE org_id=$1 AND pool_id=$2 AND NOT(server_id=ANY($3::text[])))`, p.OrgID, p.ID, memberIDs(p.Members)).Scan(&busy); checkErr != nil {
		return checkErr
	}
	if busy {
		return fmt.Errorf("%w: drain and clean workloads before removing a member", ErrConflict)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `INSERT INTO loomplane_server_pools(id,org_id,cloud_id,revision,body) SELECT $1,$2,$3,$4,$5 WHERE $6=0 ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,body=excluded.body WHERE loomplane_server_pools.org_id=$2 AND loomplane_server_pools.cloud_id=$3 AND loomplane_server_pools.revision=$6`, p.ID, p.OrgID, p.CloudID, p.Revision, raw, previous)
	// Updates need a separate statement because INSERT ... WHERE false has no conflict.
	if previous > 0 {
		result, err = tx.Exec(ctx, `UPDATE loomplane_server_pools SET revision=$4,body=$5 WHERE id=$1 AND org_id=$2 AND cloud_id=$3 AND revision=$6`, p.ID, p.OrgID, p.CloudID, p.Revision, raw, previous)
	}
	if err != nil {
		return storeError(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `DELETE FROM loomplane_server_pool_members WHERE pool_id=$1`, p.ID); err != nil {
		return err
	}
	for _, m := range p.Members {
		if _, err = tx.Exec(ctx, `INSERT INTO loomplane_server_pool_members(org_id,pool_id,server_id,cpu_millis,memory_bytes,draining) VALUES($1,$2,$3,$4,$5,$6)`, p.OrgID, p.ID, m.ServerID, m.CPUMillis, m.MemoryBytes, m.Draining); err != nil {
			return storeError(err)
		}
	}
	return tx.Commit()
}
func memberIDs(members []PoolMember) []string {
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = m.ServerID
	}
	return ids
}
func usable(v *Server, m PoolMember, now time.Time) error {
	h := v.Health
	if v.Status != "ready" || v.RuntimeConnectionID == "" || h.DockerID == "" || h.CheckedAt.After(now.Add(time.Minute)) || now.Sub(h.CheckedAt) > 15*time.Minute {
		return fmt.Errorf("%w: server readiness needs a fresh check", ErrConflict)
	}
	if h.EffectiveCPUMillis <= 0 || h.EffectiveMemoryBytes <= 0 {
		return fmt.Errorf("%w: effective host limits are unverified", ErrInvalid)
	}
	if m.CPUMillis <= 0 || m.MemoryBytes <= 0 || m.CPUMillis > h.EffectiveCPUMillis || m.MemoryBytes > h.EffectiveMemoryBytes {
		return fmt.Errorf("%w: usable budget exceeds effective host limits", ErrInvalid)
	}
	return nil
}
