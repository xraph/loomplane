package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// PlacementRequest is scoped to one service and its reviewed target.
type PlacementRequest struct {
	PlanID                                                              string
	Planned                                                             []PlannedPlacement
	OrgID, ProjectID, EnvironmentID, CloudID, TargetID, PoolID, Service string
	TargetRevision                                                      int64
	CPUMillis, MemoryBytes                                              int64
	Architecture                                                        string
	StickyServerID                                                      string
	DestinationServerID                                                 string
	LocalData                                                           bool
	Replicas                                                            int
}

type PlannedPlacement struct {
	Service string `json:"service"`
	Placement
}

// Placement pins the exact runtime and capacity reviewed for a service.
type Placement struct {
	PoolID               string `json:"pool_id"`
	PoolRevision         int64  `json:"pool_revision"`
	ServerID             string `json:"server_id"`
	ServerRevision       int64  `json:"server_revision"`
	ConnectionID         string `json:"connection_id"`
	ConnectionGeneration int64  `json:"connection_generation"`
	CPUMillis            int64  `json:"cpu_millis"`
	MemoryBytes          int64  `json:"memory_bytes"`
}

func (s *PgStore) PreviewPlacement(ctx context.Context, in PlacementRequest) (*Placement, error) {
	if in.Replicas != 1 || in.CPUMillis <= 0 || in.MemoryBytes <= 0 {
		return nil, fmt.Errorf("%w: pool services need one bounded replica", ErrInvalid)
	}
	var allowed bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cloud_targets t JOIN loomplane_cloud_project_grants g ON g.org_id=t.org_id AND g.cloud_id=t.cloud_id AND g.project_id=$2 AND NOT g.revoked WHERE t.org_id=$1 AND t.cloud_id=$3 AND t.id=$4 AND t.revision=$5 AND t.pool_id=$6 AND EXISTS(SELECT 1 FROM loomplane_environments e WHERE e.org_id=$1 AND e.project_id=$2 AND e.id=$7) AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids)))`, in.OrgID, in.ProjectID, in.CloudID, in.TargetID, in.TargetRevision, in.PoolID, in.EnvironmentID).Scan(&allowed); err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrNotFound
	}
	var unresolved bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_capacity_reservations WHERE org_id=$1 AND environment_id=$2 AND service=$3 AND binding_id='' AND plan_id<>$4)`, in.OrgID, in.EnvironmentID, in.Service, in.PlanID).Scan(&unresolved); err != nil {
		return nil, err
	}
	if unresolved {
		return nil, fmt.Errorf("%w: recover or clean the accepted deployment before planning another placement", ErrConflict)
	}
	p, err := s.GetPool(ctx, in.OrgID, in.CloudID, in.PoolID)
	if err != nil {
		return nil, err
	}
	if in.DestinationServerID == "" && in.StickyServerID != "" {
		var destination string
		err = s.db.QueryRow(ctx, `SELECT destination_server_id FROM loomplane_placement_moves WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND service=$4 AND cloud_id=$5 AND pool_id=$6 AND source_server_id=$7 AND expires_at>now() AND EXISTS(SELECT 1 FROM loomplane_bindings b WHERE b.org_id=loomplane_placement_moves.org_id AND b.environment_id=loomplane_placement_moves.environment_id AND b.subject='service' AND b.name=loomplane_placement_moves.service AND b.config=loomplane_placement_moves.source_config)`, in.OrgID, in.ProjectID, in.EnvironmentID, in.Service, in.CloudID, in.PoolID, in.StickyServerID).Scan(&destination)
		if err == nil {
			in.DestinationServerID = destination
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	selected := in.StickyServerID
	if in.DestinationServerID != "" {
		if in.LocalData && selected != "" && selected != in.DestinationServerID {
			return nil, fmt.Errorf("%w: local data needs explicit migration", ErrInvalid)
		}
		selected = in.DestinationServerID
	}
	for _, m := range p.Members {
		if selected != "" && selected != m.ServerID {
			continue
		}
		var globallyDraining bool
		if checkErr := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_server_pool_members WHERE server_id=$1 AND draining)`, m.ServerID).Scan(&globallyDraining); checkErr != nil {
			return nil, checkErr
		}
		if (m.Draining || globallyDraining) && m.ServerID != in.StickyServerID {
			if selected == m.ServerID {
				return nil, fmt.Errorf("%w: server is draining; review an explicit destination", ErrConflict)
			}
			continue
		}
		v, err := s.GetServer(ctx, in.OrgID, in.CloudID, m.ServerID)
		if err != nil {
			return nil, err
		}
		if v.Draining && (m.ServerID != in.StickyServerID || in.DestinationServerID != "") {
			continue
		}
		if err = usable(v, m, time.Now()); err != nil {
			if selected != "" {
				return nil, err
			}
			continue
		}
		if in.Architecture != "" && in.Architecture != v.Health.Architecture {
			if selected != "" {
				return nil, fmt.Errorf("%w: pinned architecture is incompatible", ErrInvalid)
			}
			continue
		}
		var cpu, mem int64
		planned := append([]PlannedPlacement{}, in.Planned...)
		planned = append(planned, PlannedPlacement{Service: in.Service, Placement: Placement{ServerID: v.ID, CPUMillis: in.CPUMillis, MemoryBytes: in.MemoryBytes}})
		raw, marshalErr := json.Marshal(planned)
		if marshalErr != nil {
			return nil, marshalErr
		}
		err = s.db.QueryRow(ctx, `SELECT coalesce(sum(cpu),0),coalesce(sum(memory),0) FROM (SELECT max(cpu_millis) cpu,max(memory_bytes) memory FROM (SELECT environment_id,service,cpu_millis,memory_bytes FROM loomplane_capacity_reservations WHERE org_id=$1 AND server_id=$2 UNION ALL SELECT $3,p->>'service',(p->>'cpu_millis')::bigint,(p->>'memory_bytes')::bigint FROM jsonb_array_elements($4::jsonb) p WHERE p->>'server_id'=$2) demands GROUP BY environment_id,service) r`, in.OrgID, v.ID, in.EnvironmentID, raw).Scan(&cpu, &mem)
		if err != nil {
			return nil, err
		}
		if cpu > m.CPUMillis || mem > m.MemoryBytes {
			continue
		}
		var generation int64
		if err = s.db.QueryRow(ctx, `SELECT generation FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2 AND status='ready'`, in.OrgID, v.RuntimeConnectionID).Scan(&generation); err != nil {
			continue
		}
		return &Placement{PoolID: p.ID, PoolRevision: p.Revision, ServerID: v.ID, ServerRevision: v.Revision, ConnectionID: v.RuntimeConnectionID, ConnectionGeneration: generation, CPUMillis: in.CPUMillis, MemoryBytes: in.MemoryBytes}, nil
	}
	return nil, fmt.Errorf("%w: no compatible capacity for the reviewed workload; current placements stay pinned", ErrConflict)
}

// ValidatePlacement checks the persisted reservation before runtime execution.
func (s *PgStore) ValidatePlacement(ctx context.Context, org, project, env, plan, service string, p Placement) error {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT r.body FROM loomplane_capacity_reservations r JOIN loomplane_cloud_servers s ON s.org_id=r.org_id AND s.id=r.server_id WHERE r.org_id=$1 AND r.project_id=$2 AND r.environment_id=$3 AND r.plan_id=$4 AND r.service=$5 AND r.server_id=$6 AND s.status='ready' AND s.runtime_connection_id=r.connection_id AND (s.body->'health'->>'checked_at')::timestamptz>=now()-interval '15 minutes' AND (SELECT coalesce(sum(cpu),0) FROM (SELECT max(cpu_millis) cpu FROM loomplane_capacity_reservations x WHERE x.server_id=s.id GROUP BY environment_id,service) used)<=(s.body->'health'->>'effective_cpu_millis')::bigint AND (SELECT coalesce(sum(memory),0) FROM (SELECT max(memory_bytes) memory FROM loomplane_capacity_reservations x WHERE x.server_id=s.id GROUP BY environment_id,service) used)<=(s.body->'health'->>'effective_memory_bytes')::bigint AND (s.body->'health'->>'effective_cpu_millis')::bigint>=r.cpu_millis AND (s.body->'health'->>'effective_memory_bytes')::bigint>=r.memory_bytes`, org, project, env, plan, service, p.ServerID).Scan(&raw)
	if err != nil {
		return storeError(err)
	}
	var saved Placement
	if checkErr := json.Unmarshal(raw, &saved); checkErr != nil {
		return checkErr
	}
	if saved != p {
		return ErrConflict
	}
	return nil
}
