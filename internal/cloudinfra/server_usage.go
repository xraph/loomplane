package cloudinfra

import (
	"context"

	"github.com/xraph/loomplane/internal/security"
)

type ServerWorkload struct {
	ExecutionRevision int64  `json:"execution_revision"`
	ProjectID         string `json:"project_id"`
	EnvironmentID     string `json:"environment_id"`
	Service           string `json:"service"`
	PlanID            string `json:"plan_id"`
	DeploymentID      string `json:"deployment_id,omitempty"`
	PoolID            string `json:"pool_id"`
	BindingID         string `json:"binding_id,omitempty"`
	Phase             string `json:"phase"`
	Active            bool   `json:"active"`
	CPUMillis         int64  `json:"cpu_millis"`
	MemoryBytes       int64  `json:"memory_bytes"`
}

// RetainedPoolStorage reports cleanup that deliberately preserved storage.
// It is not a claim that a particular volume exists; storage needs its own review.
type RetainedPoolStorage struct {
	PlanID        string `json:"plan_id"`
	Service       string `json:"service"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
}
type ServerUsage struct {
	RetainedStorage     []RetainedPoolStorage `json:"retained_storage"`
	ServerID            string                `json:"server_id"`
	Draining            bool                  `json:"draining"`
	ReservedCPUMillis   int64                 `json:"reserved_cpu_millis"`
	ReservedMemoryBytes int64                 `json:"reserved_memory_bytes"`
	Workloads           []ServerWorkload      `json:"workloads"`
}

func (s *Service) ServerUsage(ctx context.Context, p security.Principal, cloud, id string) (*ServerUsage, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	st, ok := s.Store.(*PgStore)
	if !ok {
		return nil, ErrInvalid
	}
	return st.serverUsage(ctx, p.OrgID, cloud, id)
}
func (s *PgStore) serverUsage(ctx context.Context, org, cloud, id string) (*ServerUsage, error) {
	server, err := s.GetServer(ctx, org, cloud, id)
	if err != nil {
		return nil, err
	}
	out := &ServerUsage{ServerID: id, Draining: server.Draining, Workloads: []ServerWorkload{}, RetainedStorage: []RetainedPoolStorage{}}
	rows, err := s.db.Query(ctx, `SELECT r.project_id,r.environment_id,r.service,r.plan_id,coalesce(d.id,''),r.pool_id,r.binding_id,CASE WHEN r.binding_id<>'' THEN CASE WHEN b.config->'cloud_placement'->>'server_id'=r.server_id AND b.plan_id=r.plan_id THEN 'serving' ELSE 'replaced' END WHEN x.phase<>'reserved' THEN x.phase WHEN x.active THEN 'running' WHEN x.started THEN 'pending_recovery' ELSE 'reserved' END,coalesce(x.active,false),coalesce(x.revision,0),r.cpu_millis,r.memory_bytes FROM loomplane_capacity_reservations r LEFT JOIN loomplane_pool_execution x ON x.org_id=r.org_id AND x.plan_id=r.plan_id AND x.service=r.service LEFT JOIN loomplane_deployments d ON d.org_id=r.org_id AND d.plan_id=r.plan_id LEFT JOIN loomplane_bindings b ON b.id=r.binding_id WHERE r.org_id=$1 AND r.server_id=$2 ORDER BY r.project_id,r.environment_id,r.service,r.plan_id`, org, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v ServerWorkload
		if scanErr := rows.Scan(&v.ProjectID, &v.EnvironmentID, &v.Service, &v.PlanID, &v.DeploymentID, &v.PoolID, &v.BindingID, &v.Phase, &v.Active, &v.ExecutionRevision, &v.CPUMillis, &v.MemoryBytes); scanErr != nil {
			return nil, scanErr
		}
		out.Workloads = append(out.Workloads, v)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	if capacityErr := s.db.QueryRow(ctx, `SELECT cpu,memory FROM loomplane_server_capacity($1)`, id).Scan(&out.ReservedCPUMillis, &out.ReservedMemoryBytes); capacityErr != nil {
		return nil, capacityErr
	}
	rows.Close()
	storage, err := s.db.Query(ctx, `SELECT x.plan_id,x.service,p.project_id,p.environment_id FROM loomplane_pool_execution x JOIN loomplane_plans p ON p.id=x.plan_id AND p.org_id=x.org_id WHERE x.org_id=$1 AND x.phase='released' AND x.storage_retained AND EXISTS(SELECT 1 FROM jsonb_array_elements(p.result->'actions') a WHERE a->>'name'=x.service AND a->'service'->'cloud_placement'->>'server_id'=$2) ORDER BY x.plan_id,x.service`, org, id)
	if err != nil {
		return nil, err
	}
	defer storage.Close()
	for storage.Next() {
		var v RetainedPoolStorage
		if scanErr := storage.Scan(&v.PlanID, &v.Service, &v.ProjectID, &v.EnvironmentID); scanErr != nil {
			return nil, scanErr
		}
		out.RetainedStorage = append(out.RetainedStorage, v)
	}
	return out, storage.Err()
}
