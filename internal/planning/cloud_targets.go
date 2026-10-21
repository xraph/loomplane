package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/xraph/loomplane/internal/cloudinfra"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/manifest"
)

// CloudPlacement pins the reviewed Cloud configuration and verified account.
type CloudPlacement struct {
	PoolID               string `json:"pool_id,omitempty"`
	PoolRevision         int64  `json:"pool_revision,omitempty"`
	ServerID             string `json:"server_id,omitempty"`
	ServerRevision       int64  `json:"server_revision,omitempty"`
	ConnectionGeneration int64  `json:"connection_generation,omitempty"`
	CPUMillis            int64  `json:"cpu_millis,omitempty"`
	MemoryBytes          int64  `json:"memory_bytes,omitempty"`
	GrantRevision        int64  `json:"grant_revision,omitempty"`
	CloudID              string `json:"cloud_id"`
	CloudRevision        int64  `json:"cloud_revision"`
	AssignmentRevision   int64  `json:"assignment_revision"`
	TargetID             string `json:"target_id"`
	TargetRevision       int64  `json:"target_revision"`
	TargetName           string `json:"target_name"`
	Provider             string `json:"provider"`
	ConnectionID         string `json:"connection_id"`
	AccountIdentity      string `json:"account_identity"`
	Region               string `json:"region,omitempty"`
	// RegionSource records the effective manifest override before defaults resolve.
	RegionSource string `json:"region_source,omitempty"`
}

// ResolveCloudTargets returns an independent manifest with effective placement.
// The caller checks the assignment's organization, project and environment.
func ResolveCloudTargets(m *manifest.Manifest, environment string, assignment *cloudprofiles.Assignment, options []ConnectionOption, pools ...map[string]*cloudinfra.Placement) (*manifest.Manifest, map[string]CloudPlacement, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, nil, err
	}
	var out manifest.Manifest
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, nil, err
	}
	placements := map[string]CloudPlacement{}
	for _, name := range m.ServiceNames() {
		s := out.Services[name]
		set := s.For(environment, m.Defaults)
		if set.CloudTarget == "" || !manifest.On(set.Enabled) {
			continue
		}
		path := "services." + name + ".cloud_target"
		if set.Provider != "" || set.Target != "" {
			return nil, nil, fmt.Errorf("%s conflicts with effective provider or target placement; remove direct placement, including defaults, to use a Cloud target", path)
		}
		if assignment == nil {
			return nil, nil, fmt.Errorf("%s needs a Cloud assigned to this environment", path)
		}
		if assignment.CloudRevision < 1 || assignment.Revision < 1 {
			return nil, nil, fmt.Errorf("%s has no reviewed Cloud or assignment revision", path)
		}
		if assignment.OrgID == "" || assignment.ProjectID == "" || assignment.EnvironmentID == "" || assignment.CloudID == "" {
			return nil, nil, fmt.Errorf("%s has an invalid assignment scope", path)
		}
		i := slices.IndexFunc(assignment.Targets, func(t cloudprofiles.Target) bool { return t.Name == set.CloudTarget })
		if i < 0 {
			return nil, nil, fmt.Errorf("%s: target %q is missing from the assigned Cloud; choose an existing target", path, set.CloudTarget)
		}
		target := assignment.Targets[i]
		if target.Revision < 1 {
			return nil, nil, fmt.Errorf("%s has no reviewed target revision", path)
		}
		if target.OrgID != assignment.OrgID || target.CloudID != assignment.CloudID {
			return nil, nil, fmt.Errorf("%s: target is outside the assignment scope", path)
		}
		connectionID := target.ConnectionID
		var pool *cloudinfra.Placement
		if target.PoolID != "" {
			if len(pools) == 0 || pools[0][name] == nil {
				return nil, nil, fmt.Errorf("%s needs resolved pool capacity", path)
			}
			pool = pools[0][name]
			if pool.PoolID != target.PoolID {
				return nil, nil, fmt.Errorf("%s resolved another pool", path)
			}
			connectionID = pool.ConnectionID
		}
		ci := slices.IndexFunc(options, func(c ConnectionOption) bool { return c.ID == connectionID })
		if ci < 0 || !options[ci].Ready {
			return nil, nil, fmt.Errorf("%s: target connection is missing or not ready; check it under Integrations", path)
		}
		c := options[ci]
		identity := connections.AccountIdentity(c.Cloud, c.Account, c.Details)
		if identity == "" {
			return nil, nil, fmt.Errorf("%s: target account identity is unverified; recheck Vercel or reconnect owned infrastructure under Integrations", path)
		}
		regionSource := "manifest"
		if set.Region == "" {
			regionSource = "default"
		}
		set.Provider = target.Provider
		if set.Region == "" {
			set.Region = target.Region
		}
		if OwnedCloud(c.Cloud) {
			set.Target = c.ID
		}
		placements[name] = CloudPlacement{GrantRevision: assignment.GrantRevision, CloudID: assignment.CloudID, CloudRevision: assignment.CloudRevision, AssignmentRevision: assignment.Revision, TargetID: target.ID, TargetRevision: target.Revision, TargetName: target.Name, Provider: target.Provider, ConnectionID: connectionID, AccountIdentity: identity, Region: set.Region, RegionSource: regionSource}
		if pool != nil {
			p := placements[name]
			p.PoolID = pool.PoolID
			p.PoolRevision = pool.PoolRevision
			p.ServerID = pool.ServerID
			p.ServerRevision = pool.ServerRevision
			p.ConnectionGeneration = pool.ConnectionGeneration
			p.CPUMillis = pool.CPUMillis
			p.MemoryBytes = pool.MemoryBytes
			placements[name] = p
		}
		set.CloudTarget = ""
		s.Settings, s.Environments = set, nil
		out.Services[name] = s
	}
	return &out, placements, nil
}

func serviceConnection(cfg *ServiceConfig) string {
	if cfg.CloudPlacement != nil {
		return cfg.CloudPlacement.ConnectionID
	}
	return cfg.Target
}

func (p *planner) allCloudActionsPinned(cloud string) bool {
	found := false
	for _, a := range p.actions {
		prov, err := p.in.Providers.Get(a.Provider)
		if err != nil || prov.Cloud != cloud {
			continue
		}
		found = true
		if a.ConnectionID == "" {
			return false
		}
	}
	return found
}

// SameProviderConfig compares applied inputs without placement bookkeeping.
// Keep metadata actions use it to reject a changed serving configuration.
func SameProviderConfig(before json.RawMessage, after *ServiceConfig) bool {
	return len(changes(before, after)) == 0
}

func stripProviderMetadata(config map[string]any) {
	delete(config, "cloud_placement")
	delete(config, "applied_inputs")
}

// CloudAdoption identifies the serving binding reviewed for metadata adoption.
type CloudAdoption struct {
	BindingID    string `json:"binding_id"`
	ExternalID   string `json:"external_id"`
	Revision     string `json:"revision"`
	PlanID       string `json:"plan_id"`
	ConfigDigest string `json:"config_digest"`
}

// ReviewCloudAdoption captures a canonical configuration and resource identity.
func ReviewCloudAdoption(b *Binding) *CloudAdoption {
	var config map[string]any
	if json.Unmarshal(b.Config, &config) != nil {
		return nil
	}
	digest, err := Digest(config)
	if err != nil {
		return nil
	}
	return &CloudAdoption{BindingID: b.ID, ExternalID: b.ExternalID, Revision: b.Revision, PlanID: b.PlanID, ConfigDigest: digest}
}

// MatchesCloudAdoption refuses a serving replacement or changed applied input.
func MatchesCloudAdoption(b *Binding, reviewed *CloudAdoption) bool {
	current := ReviewCloudAdoption(b)
	return reviewed != nil && current != nil && *reviewed == *current
}

// cloudResolutionManifest excludes held services from desired placement lookup.
func cloudResolutionManifest(m *manifest.Manifest, in Input) (*manifest.Manifest, bool, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, false, err
	}
	var out manifest.Manifest
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false, err
	}
	p := &planner{in: in}
	byName := map[string]*Binding{}
	for _, b := range in.Bindings {
		if b.Subject == ServiceSubject {
			byName[b.Name] = b
		}
	}
	needed := false
	for _, name := range out.ServiceNames() {
		s := out.Services[name]
		_, held := p.heldBinding(byName[name])
		repo := s.Repo
		if repo == "" && len(in.Graph.Sources) == 1 {
			repo = in.Graph.Sources[0].Alias
		}
		if held || in.Held[repo] {
			s.CloudTarget = ""
			for env, override := range s.Environments {
				override.CloudTarget = ""
				s.Environments[env] = override
			}
			out.Services[name] = s
			continue
		}
		set := s.For(in.Environment, out.Defaults)
		needed = needed || set.CloudTarget != "" && manifest.On(set.Enabled)
	}
	return &out, needed, nil
}

func (p *planner) sameDeploymentAccount(a, b *Action) bool {
	cloud := p.cloud(a.Provider)
	if cloud != p.cloud(b.Provider) {
		return false
	}
	aid, bid := a.ConnectionID, b.ConnectionID
	if aid == "" {
		aid = p.connectionFor(cloud)
	}
	if bid == "" {
		bid = p.connectionFor(cloud)
	}
	if aid == bid {
		return true
	}
	var ai, bi string
	for _, c := range p.in.Connections {
		if c.ID == aid {
			ai = connections.AccountIdentity(c.Cloud, c.Account, c.Details)
		}
		if c.ID == bid {
			bi = connections.AccountIdentity(c.Cloud, c.Account, c.Details)
		}
	}
	return ai != "" && ai == bi
}

// PoolPlacement returns the capacity fields used by admission and runtime checks.
func (p *CloudPlacement) PoolPlacement() cloudinfra.Placement {
	return cloudinfra.Placement{PoolID: p.PoolID, PoolRevision: p.PoolRevision, ServerID: p.ServerID, ServerRevision: p.ServerRevision, ConnectionID: p.ConnectionID, ConnectionGeneration: p.ConnectionGeneration, CPUMillis: p.CPUMillis, MemoryBytes: p.MemoryBytes}
}

type poolResolver interface {
	PreviewPlacement(context.Context, cloudinfra.PlacementRequest) (*cloudinfra.Placement, error)
}

func (s *PgStore) PreviewPlacement(ctx context.Context, in cloudinfra.PlacementRequest) (*cloudinfra.Placement, error) {
	return cloudinfra.NewPgStore(s.db).PreviewPlacement(ctx, in)
}
func (s *PgStore) ValidatePlacement(ctx context.Context, org, project, env, plan, service string, p cloudinfra.Placement) error {
	return cloudinfra.NewPgStore(s.db).ValidatePlacement(ctx, org, project, env, plan, service, p)
}
func (s *Service) resolvePools(ctx context.Context, plan *Plan, m *manifest.Manifest, a *cloudprofiles.Assignment, bindings []*Binding, options []ConnectionOption) (map[string]*cloudinfra.Placement, error) {
	out := map[string]*cloudinfra.Placement{}
	if a == nil {
		return out, nil
	}
	resolver, ok := s.store.(poolResolver)
	var planned []cloudinfra.PlannedPlacement
	for _, name := range m.ServiceNames() {
		svc := m.Services[name]
		set := svc.For(plan.Environment, m.Defaults)
		if set.CloudTarget == "" || !manifest.On(set.Enabled) {
			continue
		}
		i := slices.IndexFunc(a.Targets, func(t cloudprofiles.Target) bool { return t.Name == set.CloudTarget })
		if i < 0 || a.Targets[i].PoolID == "" {
			continue
		}
		t := a.Targets[i]
		if !ok {
			return nil, fmt.Errorf("pool capacity resolver is unavailable")
		}
		cpu, mem, err := ServerResources(set.Size)
		if err != nil {
			return nil, err
		}
		req := cloudinfra.PlacementRequest{PlanID: plan.ID, Planned: planned, OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, CloudID: a.CloudID, TargetID: t.ID, TargetRevision: t.Revision, PoolID: t.PoolID, Service: name, CPUMillis: cpu, MemoryBytes: mem, Replicas: 1}
		if set.Scale != nil {
			if set.Scale.Min != 1 || set.Scale.Max != 1 {
				return nil, fmt.Errorf("%s needs one replica on a pool server", name)
			}
		}
		for _, b := range bindings {
			if b.Subject != ServiceSubject || b.Name != name {
				continue
			}
			var old ServiceConfig
			if checkErr := json.Unmarshal(b.Config, &old); checkErr != nil {
				return nil, checkErr
			}
			if old.CloudPlacement != nil {
				req.StickyServerID = old.CloudPlacement.ServerID
			}
			req.LocalData = old.Catalog != nil
			req.Architecture = old.TargetSettings["architecture"]
		}
		if set.Build != nil && set.Build.Builder != "" {
			for _, c := range options {
				if c.ID == set.Build.Builder {
					if req.Architecture != "" && c.Details["architecture"] != "" && req.Architecture != c.Details["architecture"] {
						return nil, fmt.Errorf("%s builder architecture differs from the pinned runtime", name)
					}
					req.Architecture = c.Details["architecture"]
				}
			}
		}
		out[name], err = resolver.PreviewPlacement(ctx, req)
		if err == nil && req.StickyServerID != "" && out[name].ServerID != req.StickyServerID {
			for _, b := range bindings {
				if b.Subject != ServiceSubject || b.Name != name {
					continue
				}
				var old ServiceConfig
				if checkErr := json.Unmarshal(b.Config, &old); checkErr != nil {
					return nil, checkErr
				}
				req.CPUMillis, req.MemoryBytes, err = ServerResources(old.Size)
				if err != nil {
					return nil, err
				}
				req.DestinationServerID = out[name].ServerID
				out[name], err = resolver.PreviewPlacement(ctx, req)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		planned = append(planned, cloudinfra.PlannedPlacement{Service: name, Placement: *out[name]})
	}
	return out, nil
}

func (s *PgStore) PooledConnections(ctx context.Context, org string) (map[string]bool, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT c.id FROM loomplane_cloud_connections c JOIN loomplane_cloud_servers s ON (s.runtime_connection_id=c.id OR (coalesce(c.details->>'docker_id','')<>'' AND c.details->>'docker_id'=s.body->'health'->>'docker_id')) JOIN loomplane_server_pool_members m ON m.server_id=s.id WHERE c.org_id=$1 AND c.cloud='server' AND s.status NOT IN ('deleted','disconnected')`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if checkErr := rows.Scan(&id); checkErr != nil {
			return nil, checkErr
		}
		out[id] = true
	}
	return out, rows.Err()
}

func poolRelocation(bindings []*Binding, placements map[string]CloudPlacement) bool {
	for _, b := range bindings {
		p, ok := placements[b.Name]
		if !ok || p.PoolID == "" {
			continue
		}
		var old ServiceConfig
		if json.Unmarshal(b.Config, &old) == nil && old.CloudPlacement != nil && old.CloudPlacement.ServerID != "" && old.CloudPlacement.ServerID != p.ServerID {
			return true
		}
	}
	return false
}
