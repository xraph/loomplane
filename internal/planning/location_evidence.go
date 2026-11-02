package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

type locationSelection struct {
	PoolServerID string
	Server       *cloudinfra.Placement
	ResourceID   string
	Maximum      cloudinfra.ClusterWorkloadVector
	Decision     LocationPlacementDecision
	Review       *cloudprofiles.LocationReview
}

// locationCapacityReview reuses Cloud sync's exact reviewed host or deferred
// template. Before is the saved target whose change is being reviewed.
type locationCapacityReview struct {
	Intent    *CapacityIntent
	Placement *CloudPlacement
	Before    *cloudprofiles.Target
}

type locationEvidenceStore interface {
	LocationPolicy(context.Context, string, string) (*cloudprofiles.LocationPolicy, error)
	LocationCandidate(context.Context, *Plan, *cloudprofiles.Assignment, cloudprofiles.Target, manifest.Settings, cloudinfra.PlacementRequest, cloudinfra.ClusterWorkloadVector) (PlacementCandidate, error)
}

func (s *PgStore) LocationPolicy(ctx context.Context, org, cloud string) (*cloudprofiles.LocationPolicy, error) {
	return cloudprofiles.NewPgStore(s.db).GetLocationPolicy(ctx, org, cloud)
}

// LocationCandidate loads authoritative location and scheduler evidence. Pool
// previews retain the same cumulative workload accounting as ordinary planning.
func (s *PgStore) LocationCandidate(ctx context.Context, plan *Plan, a *cloudprofiles.Assignment, t cloudprofiles.Target, set manifest.Settings, request cloudinfra.PlacementRequest, cumulative cloudinfra.ClusterWorkloadVector) (PlacementCandidate, error) {
	c := PlacementCandidate{TargetID: t.ID, LocationID: t.LocationID, OrgID: t.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, Provider: t.Provider, Runtime: t.Runtime, TargetRevision: t.Revision, GrantRevision: a.GrantRevision, Granted: true}
	if t.LocationID != "" {
		l, err := cloudprofiles.NewPgStore(s.db).PlacementLocation(ctx, plan.OrgID, plan.ProjectID, a.CloudID, t.LocationID)
		if err != nil {
			return c, err
		}
		c.LocationRevision = l.Revision
		c.EvidenceRevision = l.EvidenceRevision
		if l.OrgID != plan.OrgID || l.CloudID != a.CloudID || l.Region != t.Region {
			c.Rejections = append(c.Rejections, LocationScopeInvalid)
		}
		if l.EvidenceRevision > 0 && l.EvidenceRevision == l.Revision && l.EvidenceSource == "operator-attested" {
			c.ResidencySource = l.EvidenceSource
			for _, f := range l.FailureDomains {
				if f.Kind == "residency" && f.Verified && f.Source == "operator-attested" && manifest.ResidencyCode(f.ID) {
					c.Residency = append(c.Residency, f.ID)
				}
			}
		}
	}
	if t.Provider == "server" || t.Provider == "kubernetes" {
		c.Runtime = t.Provider
	}
	connection := t.ConnectionID
	cpu, memory, err := ServerResources(set.Size)
	if err != nil {
		return c, err
	}
	if t.PoolID != "" {
		req := cloudinfra.PlacementRequest{PlanID: plan.ID, Planned: request.Planned, OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, CloudID: a.CloudID, TargetID: t.ID, TargetRevision: t.Revision, PoolID: t.PoolID, Service: request.Service, StickyServerID: request.StickyServerID, Allocation: request.Allocation, LocalData: request.LocalData, CPUMillis: cpu, MemoryBytes: memory, Replicas: 1}
		req.TargetChange, req.DestinationServerID = request.TargetChange, request.DestinationServerID
		if request.TargetChange {
			req.TargetRevision = request.TargetRevision
		}
		req.GPUKind, req.GPUs = ServerGPUs(set.Size)
		if set.Placement != nil {
			req.Architecture = set.Placement.Architecture
			if g := set.Placement.GPU; g != nil {
				req.GPUKind, req.GPUs = g.Kind, int64(g.Count)
			}
		}
		placement, e := s.PreviewPlacement(ctx, req)
		if e != nil {
			c.Rejections = append(c.Rejections, LocationCapacityUnavailable)
			return c, nil
		}
		connection = placement.ConnectionID
		c.ReadyCapacity = true
		if req.GPUs > 0 {
			c.GPU = &manifest.GPU{Kind: req.GPUKind, Count: int(req.GPUs)}
		}
	}
	var details map[string]string
	var checked *time.Time
	var status string
	if queryErr := s.db.QueryRow(ctx, `SELECT details,checked_at,status,generation FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2`, plan.OrgID, connection).Scan(&details, &checked, &status, &c.ConnectionGeneration); queryErr != nil {
		return c, queryErr
	}
	c.RuntimeConnectionID = connection
	c.CapacitySource = "managed"
	c.RuntimeQualified = status == "ready"
	c.Architecture = details["architecture"]
	if checked != nil {
		c.CheckedAt = *checked
		c.ExpiresAt = checked.Add(5 * time.Minute)
	}
	switch {
	case t.Provider == "server":
		c.CapacitySource = "server"
		var raw []byte
		if err = s.db.QueryRow(ctx, `SELECT id,body->'health' FROM loomplane_cloud_servers WHERE org_id=$1 AND cloud_id=$2 AND runtime_connection_id=$3 AND status='ready'`, plan.OrgID, a.CloudID, connection).Scan(&c.ServerID, &raw); err != nil {
			c.Rejections = append(c.Rejections, LocationInventoryStale)
			return c, nil
		}
		var health cloudinfra.RuntimeHealth
		if decodeErr := json.Unmarshal(raw, &health); decodeErr != nil {
			return c, decodeErr
		}
		c.CheckedAt = health.CheckedAt
		c.ExpiresAt = health.CheckedAt.Add(15 * time.Minute)
		c.Architecture = health.Architecture
		if t.PoolID == "" {
			planned := append([]cloudinfra.PlannedPlacement{}, request.Planned...)
			planned = append(planned, cloudinfra.PlannedPlacement{OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, Service: request.Service, Placement: cloudinfra.Placement{ServerID: c.ServerID, CPUMillis: cpu, MemoryBytes: memory}})
			raw, marshalErr := json.Marshal(planned)
			if marshalErr != nil {
				return c, marshalErr
			}
			var usedCPU, usedMemory int64
			if capacityErr := s.db.QueryRow(ctx, `SELECT cpu,memory FROM loomplane_server_capacity($1,$2,$3::jsonb)`, c.ServerID, plan.EnvironmentID, raw).Scan(&usedCPU, &usedMemory); capacityErr != nil {
				return c, capacityErr
			}
			c.ReadyCapacity = usedCPU <= health.EffectiveCPUMillis && usedMemory <= health.EffectiveMemoryBytes
		}
		if t.PoolID == "" && set.Placement != nil && set.Placement.GPU != nil {
			want := set.Placement.GPU
			if inv, ok := provider.ParseGPUInventory(details); ok && health.GPURuntime == provider.GPUStatusReady {
				if _, placeErr := inv.Place(provider.GPURequest{Kind: want.Kind, Count: want.Count}); placeErr == nil {
					c.GPU = want
				}
			}
		}
	case t.Provider == "kubernetes" && details["managed_attachment_id"] != "":
		c.CapacitySource = "managed-cluster"
		maximum, maxErr := managedWorkloadMaximum(&ServiceConfig{Size: set.Size, Scale: set.Scale})
		if maxErr != nil {
			return c, maxErr
		}
		snapshot, capacityReady, e := cloudinfra.NewPgStore(s.db).PreviewLocationWorkload(ctx, cloudinfra.ClusterWorkloadInput{OrgID: plan.OrgID, CloudID: a.CloudID, ProjectID: plan.ProjectID, TargetID: t.ID, Maximum: maximum}, cumulative)
		if e != nil {
			c.Rejections = append(c.Rejections, LocationInventoryStale)
			return c, nil
		}
		c.CheckedAt = snapshot.CheckedAt
		c.ExpiresAt = snapshot.CheckedAt.Add(cloudinfra.ClusterCapacityFreshness)
		c.ReadyCapacity = false
		for _, profile := range snapshot.SchedulingProfiles {
			if set.Placement != nil && set.Placement.Architecture != "" && profile.Architecture != set.Placement.Architecture {
				continue
			}
			compatible := true
			for _, node := range snapshot.Nodes {
				if !node.WorkloadCompatible(profile.Architecture) {
					compatible = false
				}
			}
			if !compatible {
				c.UnsupportedTaints = true
				continue
			}
			if profile.Available.CPUMillis >= cpu && profile.Available.MemoryBytes >= memory && profile.Available.Pods > 0 {
				c.ReadyCapacity = capacityReady
				c.Architecture = profile.Architecture
				break
			}
		}
	case t.Runtime == "managed":
		c.ReadyCapacity = true
	}
	return c, nil
}

func (s *Service) selectLocation(ctx context.Context, plan *Plan, a *cloudprofiles.Assignment, set manifest.Settings, name string, bindings []*Binding, options []ConnectionOption, planned []cloudinfra.PlannedPlacement, repo string, prior map[string]locationSelection, reviewed *locationCapacityReview, requirements locationRequirements) (*cloudprofiles.Target, locationSelection, error) {
	selected := locationSelection{}
	reader, ok := s.store.(locationEvidenceStore)
	if !ok || a == nil {
		return nil, selected, &CloudPlacementError{Code: "location-evidence-unavailable", Path: "services." + name + ".placement", Message: "Location placement needs a current Cloud assignment and evidence reader."}
	}
	policy, err := reader.LocationPolicy(ctx, plan.OrgID, a.CloudID)
	if err != nil {
		return nil, selected, err
	}
	in := LocationPlacementInput{Now: s.now(), OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, GrantRevision: a.GrantRevision, Constraints: *set.Placement, Policy: *policy}
	if set.Size != nil && set.Size.GPU != nil {
		if in.Constraints.GPU != nil && *in.Constraints.GPU != *set.Size.GPU {
			return nil, selected, fmt.Errorf("placement GPU differs from workload size")
		}
		in.Constraints.GPU = set.Size.GPU
	}
	set.Placement = &in.Constraints
	var current *ServiceConfig
	for _, b := range bindings {
		if b.Subject != ServiceSubject || b.Name != name {
			continue
		}
		var old ServiceConfig
		if decodeErr := json.Unmarshal(b.Config, &old); decodeErr != nil {
			return nil, selected, decodeErr
		}
		if old.Target == "" && old.CloudPlacement != nil {
			old.Target = old.CloudPlacement.ConnectionID
		}
		current = &old
		if old.CloudPlacement != nil {
			in.CurrentTargetID = old.CloudPlacement.TargetID
		}
	}
	for _, t := range a.Targets {
		if set.CloudTarget != "" && t.Name != set.CloudTarget || len(set.CloudTags) > 0 && !carriesTags(t.Tags, set.CloudTags) || set.Target != "" && t.ConnectionID != set.Target || set.Provider != "" && t.Provider != set.Provider || set.Region != "" && t.Region != set.Region {
			continue
		}
		var cumulative cloudinfra.ClusterWorkloadVector
		for _, p := range prior {
			if p.ResourceID != "" && p.ResourceID == t.ManagedResourceID {
				cumulative.CPUMillis += p.Maximum.CPUMillis
				cumulative.MemoryBytes += p.Maximum.MemoryBytes
				cumulative.Pods += p.Maximum.Pods
				cumulative.EphemeralStorageBytes += p.Maximum.EphemeralStorageBytes
			}
		}
		req := cloudinfra.PlacementRequest{Service: name, Planned: planned}
		if reviewed != nil {
			if reviewed.Before != nil && reviewed.Before.ID == t.ID {
				req.TargetChange, req.TargetRevision = true, reviewed.Before.Revision
			}
			if reviewed.Placement != nil && reviewed.Placement.TargetID == t.ID {
				req.DestinationServerID = reviewed.Placement.ServerID
			}
		}
		if current != nil {
			req.LocalData = current.Catalog != nil
			if current.CloudPlacement != nil && current.CloudPlacement.PoolID == t.PoolID {
				req.StickyServerID = current.CloudPlacement.ServerID
				req.Allocation = current.CloudPlacement.Allocation
			}
		}
		c, e := reader.LocationCandidate(ctx, plan, a, t, set, req, cumulative)
		if e != nil {
			return nil, selected, e
		}
		if reviewed == nil && current != nil && current.CloudPlacement != nil && current.CloudPlacement.PoolID != "" && t.PoolID != "" {
			old := current.CloudPlacement
			moving := c.ReadyCapacity && c.ServerID != old.ServerID
			needsReview := !c.ReadyCapacity && set.Placement.Architecture != "" && set.Placement.Architecture != current.TargetSettings["architecture"]
			if moving {
				allowed := false
				if moves, ok := s.store.(locationMoveReader); ok {
					allowed, e = moves.LocationMoveReviewed(ctx, plan, name, old, c.ServerID)
					if e != nil {
						return nil, selected, e
					}
				}
				needsReview = !allowed
			}
			if needsReview {
				c.Rejections = append(c.Rejections, LocationMovementRequired)
			}
		}
		if t.PoolID != "" && !c.ReadyCapacity && !slices.Contains(c.Rejections, LocationMovementRequired) {
			cpu, memory, resourceErr := ServerResources(set.Size)
			if resourceErr != nil {
				return nil, selected, resourceErr
			}
			req := cloudinfra.PlacementRequest{PlanID: plan.ID, OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, CloudID: a.CloudID, TargetID: t.ID, TargetRevision: t.Revision, PoolID: t.PoolID, Service: name, CPUMillis: cpu, MemoryBytes: memory, Replicas: 1, Architecture: set.Placement.Architecture}
			req.GPUKind, req.GPUs = ServerGPUs(set.Size)
			if g := set.Placement.GPU; g != nil {
				req.GPUKind, req.GPUs = g.Kind, int64(g.Count)
			}
			for _, b := range bindings {
				if b.Subject == ServiceSubject && b.Name == name {
					var old ServiceConfig
					if decodeErr := json.Unmarshal(b.Config, &old); decodeErr != nil {
						return nil, selected, decodeErr
					}
					req.LocalData = old.Catalog != nil
					if old.CloudPlacement != nil {
						req.StickyServerID = old.CloudPlacement.ServerID
					}
				}
			}
			var intent *CapacityIntent
			if reviewed != nil {
				intent = reviewed.Intent
			}
			var futureErr error
			if intent == nil || intent.Demand.TargetID != t.ID {
				intent, futureErr = s.futureCapacity(ctx, plan, a, t, req, repo, fmt.Errorf("%w: ready location capacity unavailable", cloudinfra.ErrConflict))
			}
			if futureErr == nil {
				c.Rejections = slices.DeleteFunc(c.Rejections, func(reason LocationReason) bool { return reason == LocationCapacityUnavailable })
				c.ExpansionAuthorized = true
				c.RuntimeQualified = true
				c.Architecture = intent.Template.Architecture
				c.CheckedAt = in.Now
				c.ExpiresAt = in.Now.Add(5 * time.Minute)
				c.RuntimeConnectionID = intent.Template.ConnectionID
				c.ConnectionGeneration = intent.ProviderPin.ConnectionGeneration
				c.CapacitySource = "expansion"
				if req.GPUs > 0 {
					c.GPU = &manifest.GPU{Kind: req.GPUKind, Count: int(req.GPUs)}
				}
			}
		}
		prov, e := s.providers.Get(t.Provider)
		if e != nil || !prov.Available {
			c.RuntimeQualified = false
		} else {
			if _, runs := prov.Running(requirements.Workload); !runs {
				c.RuntimeQualified = false
			}
		}
		if current != nil && current.Catalog != nil && (in.CurrentTargetID != t.ID || current.Target != "" && c.RuntimeConnectionID != current.Target) {
			c.Rejections = append(c.Rejections, LocationStorageUnverified)
		}
		if set.Placement.Runtime != "" && set.Placement.Runtime != c.Runtime {
			c.RuntimeQualified = false
		}
		if set.CloudTarget != "" {
			in.ExactTargetID = t.ID
		}
		if len(requirements.Dependencies) > 0 || requirements.Unverified {
			option := locationConnection(options, c.RuntimeConnectionID)
			if option == nil {
				c.Rejections = append(c.Rejections, LocationNetworkUnverified)
			} else {
				destination := &ServiceConfig{Provider: t.Provider, Region: t.Region, Target: option.ID, TargetSettings: ReviewedConnectionSettings(option.Cloud, option.Details), TargetAccountIdentity: connections.AccountIdentity(option.Cloud, option.Account, option.Details)}
				if !locationDependenciesReachable(requirements, destination, option.Cloud) {
					c.Rejections = append(c.Rejections, LocationNetworkUnverified)
				} else {
					c.Network = append(c.Network, "same-target")
				}
			}
		}
		if current != nil && in.CurrentTargetID == t.ID && current.Target != "" && c.RuntimeConnectionID != "" && current.Target != c.RuntimeConnectionID {
			in.CurrentTargetID = ""
		}

		in.Candidates = append(in.Candidates, c)
	}
	d, err := EvaluateLocations(in)
	if err != nil {
		return nil, selected, err
	}
	if current != nil && current.Target != "" {
		for _, c := range in.Candidates {
			if c.TargetID == d.TargetID && c.RuntimeConnectionID != "" && c.RuntimeConnectionID != current.Target {
				d.MovementRequired = true
			}
		}
	}
	if slices.Contains(d.Blockers, LocationMovementRequired) {
		d.MovementRequired = true
	}
	selected.Decision = d
	selected.Review = &cloudprofiles.LocationReview{Policy: *policy, SelectedTargetID: d.TargetID, CloudID: a.CloudID, PolicyRevision: policy.Revision, GrantRevision: a.GrantRevision, Candidates: d.Pins}
	if d.TargetID == "" {
		code := LocationNoCandidates
		if len(d.Blockers) > 0 {
			code = d.Blockers[0]
		}
		if slices.Contains(d.Blockers, LocationMovementRequired) {
			return nil, selected, &CloudPlacementError{Code: string(LocationMovementRequired), Path: "services." + name + ".placement", Message: "Review an explicit stateless pool move to a compatible destination, then plan again. Moving between pools requires supported Cloud sync."}
		}
		return nil, selected, &CloudPlacementError{Code: string(code), Path: "services." + name + ".placement", Message: "No granted target satisfies the reviewed location constraints. Review candidate explanations."}
	}
	i := slices.IndexFunc(a.Targets, func(t cloudprofiles.Target) bool { return t.ID == d.TargetID })
	if i < 0 {
		return nil, selected, fmt.Errorf("selected location target disappeared")
	}
	if a.Targets[i].PoolID != "" {
		for _, c := range in.Candidates {
			if c.TargetID == d.TargetID {
				selected.PoolServerID = c.ServerID
			}
		}
	}
	if a.Targets[i].Provider == "kubernetes" && a.Targets[i].ManagedResourceID != "" {
		selected.ResourceID = a.Targets[i].ManagedResourceID
		selected.Maximum, err = managedWorkloadMaximum(&ServiceConfig{Size: set.Size, Scale: set.Scale})
		if err != nil {
			return nil, selected, err
		}
	}
	if a.Targets[i].Provider == "server" && a.Targets[i].PoolID == "" {
		for _, candidate := range in.Candidates {
			if candidate.TargetID == d.TargetID {
				cpu, memory, resourceErr := ServerResources(set.Size)
				if resourceErr != nil {
					return nil, selected, resourceErr
				}
				selected.Server = &cloudinfra.Placement{ServerID: candidate.ServerID, CPUMillis: cpu, MemoryBytes: memory}
			}
		}
	}
	return &a.Targets[i], selected, nil
}

// locationMoveReader checks the existing exact move approval, never creates one.
type locationMoveReader interface {
	LocationMoveReviewed(context.Context, *Plan, string, *CloudPlacement, string) (bool, error)
}

func (s *PgStore) LocationMoveReviewed(ctx context.Context, plan *Plan, service string, source *CloudPlacement, destination string) (bool, error) {
	var allowed bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_placement_moves m JOIN loomplane_bindings b ON b.org_id=m.org_id AND b.project_id=m.project_id AND b.environment_id=m.environment_id AND b.subject='service' AND b.name=m.service AND b.config=m.source_config WHERE m.org_id=$1 AND m.project_id=$2 AND m.environment_id=$3 AND m.service=$4 AND m.cloud_id=$5 AND m.pool_id=$6 AND m.source_server_id=$7 AND m.destination_server_id=$8 AND m.expires_at>now())`, plan.OrgID, plan.ProjectID, plan.EnvironmentID, service, source.CloudID, source.PoolID, source.ServerID, destination).Scan(&allowed)
	return allowed, err
}
