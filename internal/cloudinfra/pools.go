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
	"github.com/xraph/loomplane/pkg/provider"
)

// Pool groups compatible runtimes. Membership does not create a private network.
type Pool struct {
	ID       string       `json:"id" forge:"id"`
	OrgID    string       `json:"org_id"`
	CloudID  string       `json:"cloud_id"`
	Name     string       `json:"name"`
	Revision int64        `json:"revision"`
	Members  []PoolMember `json:"members"`
	// AcceleratorKind "" is a CPU pool. A GPU pool holds servers with this kind.
	AcceleratorKind string `json:"accelerator_kind,omitempty"`
}

// PoolMember carries an explicitly reviewed usable host budget.
type PoolMember struct {
	ServerID    string `json:"server_id"`
	CPUMillis   int64  `json:"cpu_millis"`
	MemoryBytes int64  `json:"memory_bytes"`
	Draining    bool   `json:"draining"`
	GPUs        int64  `json:"gpus,omitempty"` // budget of the pool's kind on this server
}
type PoolInput struct {
	Name            string       `json:"name"`
	Revision        int64        `json:"revision"`
	Members         []PoolMember `json:"members"`
	AcceleratorKind string       `json:"accelerator_kind,omitempty"`
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
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 64 || len(in.Members) > 100 {
		return nil, ErrInvalid
	}
	if err := validatePoolGPUs(in); err != nil {
		return nil, err
	}
	policies, available := s.Store.(CapacityPolicyStore)
	var policy *CapacityPolicy
	if available && id != "" {
		var policyErr error
		if policy, policyErr = policies.GetCapacityPolicy(ctx, p.OrgID, cloud); policyErr != nil {
			return nil, policyErr
		}
	}
	var template PoolExpansionPolicy
	configured := false
	if policy != nil {
		template, configured = expansionPool(policy, id)
	}
	if len(in.Members) == 0 && (policy == nil || !policy.Enabled || !configured) {
		return nil, ErrInvalid
	}
	// Expansions add servers of the template's kind, so the pool has to hold the same kind.
	if configured {
		if err := poolKindMatchesTemplate(id, in.AcceleratorKind, template.AcceleratorKind); err != nil {
			return nil, err
		}
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
	pool := &Pool{ID: id, OrgID: p.OrgID, CloudID: cloud, Name: strings.TrimSpace(in.Name), Revision: in.Revision + 1, Members: append([]PoolMember{}, in.Members...), AcceleratorKind: in.AcceleratorKind}
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
	if checkErr := lockCapacityCloud(ctx, tx, p.OrgID, p.CloudID); checkErr != nil {
		return checkErr
	}
	// Policy saves take the same Cloud lock, so the template can't change under this check.
	policy, err := capacityPolicyTx(ctx, tx, p.OrgID, p.CloudID)
	if err != nil {
		return err
	}
	if template, configured := expansionPool(policy, p.ID); configured {
		if checkErr := poolKindMatchesTemplate(p.ID, p.AcceleratorKind, template.AcceleratorKind); checkErr != nil {
			return checkErr
		}
	}
	// All membership edits and reservations share the same ordered host locks.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('pool-config/'||$1,0))`, p.OrgID); err != nil {
		return err
	}

	// Lock every verified alias before the shared daemon admission lock. Approval
	// takes connection locks in the same order, then these same identity locks.
	if _, err = tx.Exec(ctx, `SELECT c.id FROM loomplane_cloud_connections c WHERE c.cloud='server' AND c.details->>'docker_id' IN (SELECT body->'health'->>'docker_id' FROM loomplane_cloud_servers WHERE org_id=$1 AND (id=ANY($2::text[]) OR id IN (SELECT server_id FROM loomplane_server_pool_members WHERE pool_id=$3))) ORDER BY c.id FOR UPDATE`, p.OrgID, memberIDs(p.Members), p.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('host-admission/'||identity,0)) FROM (SELECT DISTINCT body->'health'->>'docker_id' identity FROM loomplane_cloud_servers WHERE org_id=$1 AND (id=ANY($2::text[]) OR id IN (SELECT server_id FROM loomplane_server_pool_members WHERE pool_id=$3)) ORDER BY identity) identities`, p.OrgID, memberIDs(p.Members), p.ID); err != nil {
		return err
	}
	// A direct apply holds its daemon's direct-apply lock through its effects
	// and takes the organization lock inside them. Waiting for it here, under
	// the organization lock, could close a cycle, so refuse instead.
	var free bool
	if checkErr := tx.QueryRow(ctx, `SELECT coalesce(bool_and(pg_try_advisory_xact_lock(hashtextextended('direct-apply/'||identity,0))),true) FROM (SELECT DISTINCT body->'health'->>'docker_id' identity FROM loomplane_cloud_servers WHERE org_id=$1 AND (id=ANY($2::text[]) OR id IN (SELECT server_id FROM loomplane_server_pool_members WHERE pool_id=$3)) ORDER BY identity) identities`, p.OrgID, memberIDs(p.Members), p.ID).Scan(&free); checkErr != nil {
		return checkErr
	}
	if !free {
		return ErrDirectApplyRunning
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
		if checkErr := usableGPUs(&v, p.AcceleratorKind, m); checkErr != nil {
			return checkErr
		}
		var unmanaged bool
		if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_bindings WHERE (config->'target_settings'->>'docker_id'=$1 OR connection_id IN(SELECT id FROM loomplane_cloud_connections WHERE details->>'docker_id'=$1)) AND coalesce(config->'cloud_placement'->>'pool_id','')='') OR EXISTS(SELECT 1 FROM loomplane_plans p CROSS JOIN LATERAL jsonb_array_elements(coalesce(p.result->'actions','[]')) a WHERE p.status='approved' AND (a->'service'->'target_settings'->>'docker_id'=$1 OR a->>'connection_id' IN(SELECT id FROM loomplane_cloud_connections WHERE details->>'docker_id'=$1)) AND coalesce(a->'service'->'cloud_placement'->>'pool_id','')='' AND NOT EXISTS(SELECT 1 FROM loomplane_deployments d WHERE d.plan_id=p.id AND d.status='succeeded'))`, v.Health.DockerID).Scan(&unmanaged); checkErr != nil {
			return checkErr
		}
		if unmanaged {
			return fmt.Errorf("%w: existing direct workloads need explicit migration before pooling", ErrConflict)
		}
		var other bool
		if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_server_pool_members WHERE org_id=$1 AND server_id=$2 AND pool_id<>$3 AND (cpu_millis<>$4 OR memory_bytes<>$5 OR gpus<>$6))`, p.OrgID, m.ServerID, p.ID, m.CPUMillis, m.MemoryBytes, m.GPUs).Scan(&other); checkErr != nil {
			return checkErr
		}
		if other {
			return fmt.Errorf("%w: a server must use the same budget in every pool", ErrInvalid)
		}
		var cpu, mem, gpus int64
		if checkErr := tx.QueryRow(ctx, `SELECT c.cpu,c.memory,loomplane_server_gpus($1) FROM loomplane_server_capacity($1) c`, m.ServerID).Scan(&cpu, &mem, &gpus); checkErr != nil {
			return checkErr
		}
		if cpu > m.CPUMillis || mem > m.MemoryBytes || gpus > m.GPUs {
			return fmt.Errorf("%w: budget is below reserved capacity", ErrConflict)
		}
	}
	// Reservations already in the pool keep their GPU kind, CPU work included.
	var mismatched bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_capacity_reservations WHERE org_id=$1 AND pool_id=$2 AND gpu_kind<>$3)`, p.OrgID, p.ID, p.AcceleratorKind).Scan(&mismatched); checkErr != nil {
		return checkErr
	}
	if mismatched {
		return fmt.Errorf("%w: drain and clean workloads before changing the pool's GPU kind", ErrConflict)
	}
	var busy bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_capacity_reservations WHERE org_id=$1 AND pool_id=$2 AND NOT(server_id=ANY($3::text[])))`, p.OrgID, p.ID, memberIDs(p.Members)).Scan(&busy); checkErr != nil {
		return checkErr
	}
	if busy {
		return fmt.Errorf("%w: drain and clean workloads before removing a member", ErrConflict)
	}
	if checkErr := checkPoolCapacityEdit(ctx, tx, p); checkErr != nil {
		return checkErr
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
		if _, err = tx.Exec(ctx, `INSERT INTO loomplane_server_pool_members(org_id,pool_id,server_id,cpu_millis,memory_bytes,draining,gpus) VALUES($1,$2,$3,$4,$5,$6,$7)`, p.OrgID, p.ID, m.ServerID, m.CPUMillis, m.MemoryBytes, m.Draining, m.GPUs); err != nil {
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

// validatePoolGPUs checks a pool's GPU kind and member budgets before any
// server is read: a CPU pool gives no GPUs, a GPU pool gives each member some.
func validatePoolGPUs(in PoolInput) error {
	if in.AcceleratorKind != "" && !nvidiaKind(in.AcceleratorKind) {
		return fmt.Errorf("%w: a GPU pool needs a known NVIDIA kind", ErrInvalid)
	}
	for _, m := range in.Members {
		if in.AcceleratorKind == "" && m.GPUs != 0 {
			return fmt.Errorf("%w: a CPU pool member has no GPU budget", ErrInvalid)
		}
		if in.AcceleratorKind != "" && m.GPUs <= 0 {
			return fmt.Errorf("%w: a GPU pool member needs a GPU budget", ErrInvalid)
		}
	}
	return nil
}

// usableGPUs checks a GPU pool member against the server's last health check:
// enough devices of the pool's kind, and a container runtime that can use them.
func usableGPUs(v *Server, kind string, m PoolMember) error {
	if kind == "" {
		if m.GPUs != 0 {
			return fmt.Errorf("%w: a CPU pool member has no GPU budget", ErrInvalid)
		}
		return nil
	}
	var have int64
	for _, d := range v.Health.GPUs {
		if d.Kind == kind {
			have++
		}
	}
	if m.GPUs <= 0 || m.GPUs > have {
		return fmt.Errorf("%w: GPU budget exceeds the server's %s GPUs", ErrInvalid, kind)
	}
	if v.Health.GPURuntime != provider.GPUStatusReady {
		return fmt.Errorf("%w: the server's GPU runtime isn't ready", ErrConflict)
	}
	return nil
}

// poolKindMatchesTemplate refuses a pool whose GPU kind differs from the kind
// its expansion template buys, in either direction.
func poolKindMatchesTemplate(poolID, poolKind, templateKind string) error {
	if poolKind == templateKind {
		return nil
	}
	name := func(kind string) string {
		if kind == "" {
			return "CPU"
		}
		return kind
	}
	return fmt.Errorf("%w: pool %s holds %s servers but its expansion template buys %s servers", ErrInvalid, poolID, name(poolKind), name(templateKind))
}
