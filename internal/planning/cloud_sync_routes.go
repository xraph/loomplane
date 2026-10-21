package planning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
)

func changedSyncTarget(before cloudprofiles.Target, in cloudprofiles.ChangeInput) (cloudprofiles.Target, error) {
	after := before
	if in.Provider != "" {
		after.Provider = in.Provider
	}
	after.Region = in.Region
	if in.Runtime != "" {
		after.Runtime = in.Runtime
	}
	if after.Runtime == "" {
		after.Runtime = "managed"
	}
	switch after.Runtime {
	case "server":
		if in.PoolID != "" {
			after.PoolID = in.PoolID
		}
		if after.PoolID == "" || after.Provider != "server" || in.ConnectionID != "" {
			return after, errors.New("server targets require a pool and no direct connection")
		}
		after.ConnectionID = ""
	case "managed":
		if in.PoolID != "" {
			return after, errors.New("managed targets cannot select a server pool")
		}
		after.PoolID = ""
		after.ConnectionID = in.ConnectionID
		if after.ConnectionID == "" {
			return after, errors.New("managed targets require a connection")
		}
	default:
		return after, errors.New("runtime must be managed or server")
	}
	return after, nil
}

func (s *Service) syncPoolPlacements(ctx context.Context, plan *Plan, bindings []*Binding, assignment *cloudprofiles.Assignment, before *cloudprofiles.Target, options []ConnectionOption) (map[string]CloudPlacement, error) {
	result := map[string]CloudPlacement{}
	if assignment == nil {
		return result, nil
	}
	resolver, ok := s.store.(poolResolver)
	var planned []cloudinfra.PlannedPlacement
	for _, b := range bindings {
		if b.Subject != ServiceSubject {
			continue
		}
		var old ServiceConfig
		if err := json.Unmarshal(b.Config, &old); err != nil {
			return nil, err
		}
		if old.CloudPlacement == nil {
			continue
		}
		i := slices.IndexFunc(assignment.Targets, func(t cloudprofiles.Target) bool { return t.Name == old.CloudPlacement.TargetName })
		if i < 0 || assignment.Targets[i].PoolID == "" {
			continue
		}
		if !ok {
			return nil, errors.New("pool capacity resolver is unavailable")
		}
		target := assignment.Targets[i]
		cpu, mem, err := ServerResources(old.Size)
		if err != nil {
			return nil, err
		}
		req := cloudinfra.PlacementRequest{PlanID: plan.ID, OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, CloudID: assignment.CloudID, TargetID: target.ID, TargetRevision: target.Revision, PoolID: target.PoolID, Service: b.Name, CPUMillis: cpu, MemoryBytes: mem, Replicas: 1, Planned: planned, LocalData: old.Catalog != nil, Architecture: old.TargetSettings["architecture"]}
		if old.CloudPlacement.PoolID == target.PoolID {
			req.StickyServerID = old.CloudPlacement.ServerID
			req.Allocation = old.CloudPlacement.Allocation
		} else if old.Catalog != nil {
			return nil, fmt.Errorf("%s needs explicit local data migration before switching pools", b.Name)
		}
		if before != nil && target.ID == before.ID {
			req.TargetChange = true
			req.TargetRevision = before.Revision
		}
		placed, err := resolver.PreviewPlacement(ctx, req)
		if err != nil {
			return nil, err
		}
		ci := slices.IndexFunc(options, func(c ConnectionOption) bool { return c.ID == placed.ConnectionID })
		if ci < 0 {
			return nil, errors.New("selected pool connection is unavailable")
		}
		connection := options[ci]
		result[b.Name] = CloudPlacement{Allocation: placed.Allocation, CloudID: assignment.CloudID, CloudRevision: assignment.CloudRevision, AssignmentRevision: assignment.Revision, GrantRevision: assignment.GrantRevision, TargetID: target.ID, TargetRevision: target.Revision, TargetName: target.Name, Provider: "server", ConnectionID: placed.ConnectionID, AccountIdentity: connections.AccountIdentity(connection.Cloud, connection.Account, connection.Details), RegionSource: old.CloudPlacement.RegionSource, PoolID: placed.PoolID, PoolRevision: placed.PoolRevision, ServerID: placed.ServerID, ServerRevision: placed.ServerRevision, ConnectionGeneration: placed.ConnectionGeneration, CPUMillis: placed.CPUMillis, MemoryBytes: placed.MemoryBytes}
		planned = append(planned, cloudinfra.PlannedPlacement{Service: b.Name, Placement: *placed})
	}
	return result, nil
}

func prepareAcceptedPlans(sync *cloudprofiles.Sync, snapshot *cloudprofiles.Snapshot) error {
	for i := range snapshot.Environments {
		env := &snapshot.Environments[i]
		var plan Plan
		if err := json.Unmarshal(env.Plan, &plan); err != nil {
			return err
		}
		if !slices.ContainsFunc(plan.Actions, func(a Action) bool { return a.Op != Keep || a.AdoptCloudPlacement }) {
			continue
		}
		revision, err := Digest(plan.Actions)
		if err != nil {
			return err
		}
		result, err := json.Marshal(Result{CloudSyncID: sync.ID, Actions: plan.Actions, Problems: plan.Problems, Warnings: plan.Warnings, Summary: plan.Summary, Executable: plan.Executable, Connections: plan.Connections})
		if err != nil {
			return err
		}
		env.AcceptedPlan = &cloudprofiles.AcceptedPlan{ID: migrationPlanID(sync.ID, env.EnvironmentID), Revision: revision, ManifestID: plan.ManifestID, ManifestRevision: plan.ManifestRevision, Result: result}
		for j := range sync.Environments {
			if sync.Environments[j].EnvironmentID == env.EnvironmentID {
				sync.Environments[j].PlanID = env.AcceptedPlan.ID
			}
		}
	}
	return nil
}

// ValidateSyncPool also checks targets with no deployed participants.
func (s *PgStore) ValidateSyncPool(ctx context.Context, org, cloud, pool string) error {
	_, err := cloudinfra.NewPgStore(s.db).GetPool(ctx, org, cloud, pool)
	return err
}
