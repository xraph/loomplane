package planning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/cloudinfra"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/manifest"
)

// CloudPlacement pins the reviewed Cloud configuration and verified account.
type CloudPlacement struct {
	Allocation string `json:"allocation,omitempty"`

	PoolID               string `json:"pool_id,omitempty"`
	PoolRevision         int64  `json:"pool_revision,omitempty"`
	ServerID             string `json:"server_id,omitempty"`
	ServerRevision       int64  `json:"server_revision,omitempty"`
	ConnectionGeneration int64  `json:"connection_generation,omitempty"`
	CPUMillis            int64  `json:"cpu_millis,omitempty"`
	MemoryBytes          int64  `json:"memory_bytes,omitempty"`
	// GPUKind and GPUs are what a pool reserves for a service that asks
	// for GPUs. A CPU pin leaves them out, so its stored shape is unchanged.
	GPUKind            string `json:"gpu_kind,omitempty"`
	GPUs               int64  `json:"gpus,omitempty"`
	GrantRevision      int64  `json:"grant_revision,omitempty"`
	CloudID            string `json:"cloud_id"`
	CloudRevision      int64  `json:"cloud_revision"`
	AssignmentRevision int64  `json:"assignment_revision"`
	// Default marks a service placed by the Cloud's default rather than a
	// target. It has no target ID, name or revision; the Cloud revision pins it.
	Default         bool   `json:"default,omitempty"`
	TargetID        string `json:"target_id"`
	TargetRevision  int64  `json:"target_revision"`
	TargetName      string `json:"target_name"`
	Provider        string `json:"provider"`
	ConnectionID    string `json:"connection_id"`
	AccountIdentity string `json:"account_identity"`
	Region          string `json:"region,omitempty"`
	// RegionSource records the effective manifest override before defaults resolve.
	RegionSource string `json:"region_source,omitempty"`
}

// CloudPlacementError is a placement finding found before planning. Service
// planning reports it under its own code and manifest path.
type CloudPlacementError struct {
	Code    string
	Path    string
	Message string
}

func (e *CloudPlacementError) Error() string { return e.Path + ": " + e.Message }

// targetPlacement pins a workload to one of the assignment's targets,
// reached through the target's own connection c. A direct target records
// the Cloud server c runs, when it runs exactly one; a pool target's
// server is its capacity resolution's to choose.
func targetPlacement(a *cloudprofiles.Assignment, t cloudprofiles.Target, c ConnectionOption, identity, region, regionSource string) CloudPlacement {
	p := CloudPlacement{GrantRevision: a.GrantRevision, CloudID: a.CloudID, CloudRevision: a.CloudRevision, AssignmentRevision: a.Revision, TargetID: t.ID, TargetRevision: t.Revision, TargetName: t.Name, Provider: t.Provider, ConnectionID: t.ConnectionID, AccountIdentity: identity, Region: region, RegionSource: regionSource}
	if servers := c.cloudServers(a.CloudID); t.PoolID == "" && len(servers) == 1 {
		p.ServerID = servers[0]
	}
	return p
}

// cloudServers are the servers of cloud that c is the runtime connection of.
func (c ConnectionOption) cloudServers(cloud string) []string {
	var out []string
	for _, s := range c.Servers {
		if s.CloudID == cloud {
			out = append(out, s.ID)
		}
	}
	return out
}

// checkCloudServer warns about a direct placement whose connection is the
// runtime of more than one Cloud server: none is recorded, so it can't
// take domain routes. A connection that runs no Cloud server is a plain
// direct target and isn't worth a finding.
//
// The schema can't produce this today. The loomplane_cloud_server_runtime
// unique index (database/migrations/cloud_infrastructure.go) allows one
// live server per organization and runtime connection, and ConnectionHosts
// reads only live servers in the connection's organization. The check
// stays so that loosening that index can't silently pick a server.
func (p *planner) checkCloudServer(subject Subject, name, path string, placement CloudPlacement) {
	if placement.PoolID != "" || placement.ServerID != "" {
		return
	}
	i := slices.IndexFunc(p.in.Connections, func(c ConnectionOption) bool { return c.ID == placement.ConnectionID })
	if i < 0 || len(p.in.Connections[i].cloudServers(placement.CloudID)) < 2 {
		return
	}
	target := placement.TargetName
	if target == "" {
		target = placement.ConnectionID
	}
	p.warn(Finding{
		Code: "cloud-server-ambiguous", Subject: subject, Name: name, Path: path,
		Message: fmt.Sprintf("%s's target %s is the runtime connection of more than one Cloud server, so Loomplane can't tell which one it runs on, and it can't take domain routes. Give each server its own connection.", name, target),
	})
}

// composedResources are the manifest's composed catalog resources that are
// on in the environment.
func composedResources(m *manifest.Manifest, environment string, kinds *recipes.Registry) []string {
	if m == nil {
		return nil
	}
	if kinds == nil {
		kinds = recipes.Default()
	}
	var out []string
	for _, name := range m.ResourceNames() {
		r := m.Resources[name]
		if kinds.Composed(r.Kind) && resourceOn(kinds, r, environment, m.Defaults) {
			out = append(out, name)
		}
	}
	return out
}

// composedPlacements places each of the named composed resources whose
// target is a direct-connection target of the assignment, as a service on
// that target is placed. Pool targets aren't matched. A placement pins
// reviewed revisions and a verified account, as the runner checks it
// before it records one; without them the resource isn't placed.
func composedPlacements(m *manifest.Manifest, environment string, names []string, assignment *cloudprofiles.Assignment, options []ConnectionOption) map[string]CloudPlacement {
	out := map[string]CloudPlacement{}
	if assignment == nil || assignment.CloudRevision < 1 || assignment.Revision < 1 {
		return out
	}
	for _, name := range names {
		// The resource runs on the target its environment gives it.
		set := m.Resources[name].For(environment, m.Defaults)
		i := slices.IndexFunc(assignment.Targets, func(t cloudprofiles.Target) bool {
			return t.PoolID == "" && t.ConnectionID != "" && t.ConnectionID == set.Target && t.OrgID == assignment.OrgID && t.CloudID == assignment.CloudID && t.Revision > 0
		})
		ci := slices.IndexFunc(options, func(c ConnectionOption) bool { return c.ID == set.Target })
		if i < 0 || ci < 0 {
			continue
		}
		identity := connections.AccountIdentity(options[ci].Cloud, options[ci].Account, options[ci].Details)
		if identity == "" {
			continue
		}
		regionSource := "manifest"
		if set.Region == "" {
			regionSource = "default"
		}
		out[name] = targetPlacement(assignment, assignment.Targets[i], options[ci], identity, set.Region, regionSource)
	}
	return out
}

// unverifiedPlacements are the named composed resources whose target is a
// direct-connection target of the assignment's Cloud that composedPlacements
// still doesn't place: the Cloud's or the target's revision isn't
// reviewed, or the target's connection is missing or has no verified
// account.
func unverifiedPlacements(m *manifest.Manifest, environment string, names []string, assignment *cloudprofiles.Assignment, options []ConnectionOption) map[string]bool {
	out := map[string]bool{}
	if assignment == nil {
		return out
	}
	placed := composedPlacements(m, environment, names, assignment, options)
	for _, name := range names {
		if _, ok := placed[name]; ok {
			continue
		}
		target := m.Resources[name].For(environment, m.Defaults).Target
		if slices.ContainsFunc(assignment.Targets, func(t cloudprofiles.Target) bool {
			return t.PoolID == "" && t.ConnectionID != "" && t.ConnectionID == target && t.OrgID == assignment.OrgID && t.CloudID == assignment.CloudID
		}) {
			out[name] = true
		}
	}
	return out
}

// SelectCloudTarget returns the target a service's settings name or tag, or
// nil when they select none. Tags match the targets the project's grant
// allows; a service gets a target only when exactly one carries every tag.
func SelectCloudTarget(set manifest.Settings, assignment *cloudprofiles.Assignment, path string) (*cloudprofiles.Target, error) {
	if assignment == nil || set.CloudTarget == "" && len(set.CloudTags) == 0 {
		return nil, nil
	}
	if set.CloudTarget != "" {
		i := slices.IndexFunc(assignment.Targets, func(t cloudprofiles.Target) bool { return t.Name == set.CloudTarget })
		if i < 0 {
			return nil, fmt.Errorf("%s: target %q is missing from the assigned Cloud; choose an existing target", path, set.CloudTarget)
		}
		return &assignment.Targets[i], nil
	}
	var matches []int
	for i, t := range assignment.Targets {
		if carriesTags(t.Tags, set.CloudTags) {
			matches = append(matches, i)
		}
	}
	tags := strings.Join(set.CloudTags, ", ")
	switch len(matches) {
	case 1:
		return &assignment.Targets[matches[0]], nil
	case 0:
		return nil, &CloudPlacementError{Code: "cloud-tag-unmatched", Path: path, Message: fmt.Sprintf("no target this project may use in the assigned Cloud carries %s. Tag a target, or ask an organization admin to grant this project one that does.", tags)}
	}
	names := make([]string, 0, len(matches))
	for _, i := range matches {
		names = append(names, assignment.Targets[i].Name)
	}
	return nil, &CloudPlacementError{Code: "cloud-tag-ambiguous", Path: path, Message: fmt.Sprintf("%s matches %d targets (%s). Add a tag only one of them carries, or name the target with cloud_target.", tags, len(names), strings.Join(names, ", "))}
}

func carriesTags(have, want []string) bool {
	for _, tag := range want {
		if !slices.Contains(have, tag) {
			return false
		}
	}
	return true
}

// usesCloudDefault reports whether a service with no placement of its own
// goes to the assigned Cloud's default provider and account. Targets never
// change this: adding one only places services that name or tag it.
func usesCloudDefault(set manifest.Settings, assignment *cloudprofiles.Assignment) bool {
	return assignment != nil && assignment.DefaultPlacement != nil && set.CloudTarget == "" && len(set.CloudTags) == 0 && set.Provider == "" && set.Target == ""
}

func cloudPath(name string, set manifest.Settings) string {
	switch {
	case set.CloudTarget != "":
		return "services." + name + ".cloud_target"
	case len(set.CloudTags) > 0:
		return "services." + name + ".cloud_tags"
	}
	return "services." + name + ".provider"
}

// ResolveCloudTargets returns an independent manifest with effective placement.
// The caller checks the assignment's organization, project and environment.
func ResolveCloudTargets(m *manifest.Manifest, environment string, assignment *cloudprofiles.Assignment, options []ConnectionOption, pools ...map[string]*cloudinfra.Placement) (*manifest.Manifest, map[string]CloudPlacement, error) {
	var resolved map[string]*cloudinfra.Placement
	if len(pools) > 0 {
		resolved = pools[0]
	}
	return resolveCloudPlacements(m, environment, assignment, options, resolved, nil)
}

// resolveCloudPlacements leaves held services exactly as their manifest says.
func resolveCloudPlacements(m *manifest.Manifest, environment string, assignment *cloudprofiles.Assignment, options []ConnectionOption, pools map[string]*cloudinfra.Placement, held map[string]bool, future ...map[string]*CapacityIntent) (*manifest.Manifest, map[string]CloudPlacement, error) {
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
		if held[name] {
			continue
		}
		s := out.Services[name]
		set := s.For(environment, m.Defaults)
		if !manifest.On(set.Enabled) {
			continue
		}
		fallback := usesCloudDefault(set, assignment)
		if set.CloudTarget == "" && len(set.CloudTags) == 0 && !fallback {
			continue
		}
		path := cloudPath(name, set)
		if !fallback && (set.Provider != "" || set.Target != "") {
			return nil, nil, fmt.Errorf("%s conflicts with effective provider or target placement; remove direct placement, including defaults, to use a Cloud target", path)
		}
		deferred := len(future) > 0 && future[0][name] != nil
		placement, resolvedSet, placeErr := resolveCloudPlacement(name, path, set, fallback, assignment, options, pools, deferred)
		if errors.Is(placeErr, errCapacityDeferred) {
			s.Settings, s.Environments = resolvedSet, nil
			out.Services[name] = s
			continue
		}
		if placeErr != nil {
			return nil, nil, placeErr
		}
		placements[name] = placement
		s.Settings, s.Environments = resolvedSet, nil
		out.Services[name] = s
	}
	if err := resolveDatabaseGatewayPlacement(&out, environment, assignment, options, pools, placements); err != nil {
		return nil, nil, err
	}
	// Every service the default tags applied to is resolved above.
	out.Defaults.CloudTags = nil
	return &out, placements, nil
}

// errCapacityDeferred is how resolveCloudPlacement says a pool service
// waits on capacity a later plan creates, so it gets no placement yet.
var errCapacityDeferred = errors.New("pool capacity deferred to a later plan")

// resolveCloudPlacement pins the target set names or tags, or the Cloud's
// default when fallback is set, and returns the settings with the target's
// provider and region in place of the Cloud's names and tags. When deferred
// is set and the target is a pool, it returns errCapacityDeferred with the
// settings carrying only the target's provider.
func resolveCloudPlacement(name, path string, set manifest.Settings, fallback bool, assignment *cloudprofiles.Assignment, options []ConnectionOption, pools map[string]*cloudinfra.Placement, deferred bool) (CloudPlacement, manifest.Settings, error) {
	if assignment == nil {
		return CloudPlacement{}, set, fmt.Errorf("%s needs a Cloud assigned to this environment", path)
	}
	if assignment.CloudRevision < 1 || assignment.Revision < 1 {
		return CloudPlacement{}, set, fmt.Errorf("%s has no reviewed Cloud or assignment revision", path)
	}
	if assignment.OrgID == "" || assignment.ProjectID == "" || assignment.EnvironmentID == "" || assignment.CloudID == "" {
		return CloudPlacement{}, set, fmt.Errorf("%s has an invalid assignment scope", path)
	}
	var target cloudprofiles.Target
	if fallback {
		d := assignment.DefaultPlacement
		target = cloudprofiles.Target{OrgID: assignment.OrgID, CloudID: assignment.CloudID, Provider: d.Provider, ConnectionID: d.ConnectionID, Region: d.Region}
	} else {
		selected, selectErr := SelectCloudTarget(set, assignment, path)
		if selectErr != nil {
			return CloudPlacement{}, set, selectErr
		}
		target = *selected
		if target.Revision < 1 {
			return CloudPlacement{}, set, fmt.Errorf("%s has no reviewed target revision", path)
		}
	}
	if target.OrgID != assignment.OrgID || target.CloudID != assignment.CloudID {
		return CloudPlacement{}, set, fmt.Errorf("%s: target is outside the assignment scope", path)
	}
	connectionID := target.ConnectionID
	var pool *cloudinfra.Placement
	if target.PoolID != "" {
		if deferred {
			set.Provider, set.CloudTarget, set.CloudTags, set.Target = target.Provider, "", nil, ""
			return CloudPlacement{}, set, errCapacityDeferred
		}
		if pools[name] == nil {
			return CloudPlacement{}, set, fmt.Errorf("%s needs resolved pool capacity", path)
		}
		pool = pools[name]
		if pool.PoolID != target.PoolID {
			return CloudPlacement{}, set, fmt.Errorf("%s resolved another pool", path)
		}
		connectionID = pool.ConnectionID
	}
	ci := slices.IndexFunc(options, func(c ConnectionOption) bool { return c.ID == connectionID })
	if ci < 0 || !options[ci].Ready {
		return CloudPlacement{}, set, fmt.Errorf("%s: target connection is missing or not ready; check it under Integrations", path)
	}
	c := options[ci]
	identity := connections.AccountIdentity(c.Cloud, c.Account, c.Details)
	if identity == "" {
		return CloudPlacement{}, set, fmt.Errorf("%s: target account identity is unverified; recheck Vercel or reconnect owned infrastructure under Integrations", path)
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
	placement := targetPlacement(assignment, target, c, identity, set.Region, regionSource)
	placement.Default, placement.ConnectionID = fallback, connectionID
	if pool != nil {
		placement.PoolID = pool.PoolID
		placement.PoolRevision = pool.PoolRevision
		placement.ServerID = pool.ServerID
		placement.ServerRevision = pool.ServerRevision
		placement.ConnectionGeneration = pool.ConnectionGeneration
		placement.Allocation = pool.Allocation
		placement.CPUMillis = pool.CPUMillis
		placement.MemoryBytes = pool.MemoryBytes
		placement.GPUKind, placement.GPUs = pool.GPUKind, pool.GPUs
	}
	set.CloudTarget, set.CloudTags = "", nil
	return placement, set, nil
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

// SameResourceConfig is SameProviderConfig for a composed resource or one
// of its components.
func SameResourceConfig(before json.RawMessage, after *ResourceConfig) bool {
	return len(changes(before, after)) == 0
}

func stripProviderMetadata(config map[string]any) {
	delete(config, "cloud_placement")
	delete(config, "applied_inputs")
	delete(config, "artifact")
	// A frontend demand names the plan that counted it.
	delete(config, "frontend_demand")
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

// cloudNeeds says why a plan reads the environment's Cloud. Explicit services
// name or tag a target; fallback services have no placement and may use the
// Cloud's default. Held services keep their manifest placement untouched.
type cloudNeeds struct {
	Explicit bool
	Fallback bool
	Held     map[string]bool
}

// cloudResolutionManifest excludes held services from desired placement lookup.
func cloudResolutionManifest(m *manifest.Manifest, in Input) (*manifest.Manifest, cloudNeeds, error) {
	needs := cloudNeeds{Held: map[string]bool{}}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, needs, err
	}
	var out manifest.Manifest
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, needs, err
	}
	p := &planner{in: in}
	byName := map[string]*Binding{}
	for _, b := range in.Bindings {
		if b.Subject == ServiceSubject {
			byName[b.Name] = b
		}
	}
	for _, name := range out.ServiceNames() {
		s := out.Services[name]
		_, held := p.heldBinding(byName[name])
		repo := s.Repo
		if repo == "" && len(in.Graph.Sources) == 1 {
			repo = in.Graph.Sources[0].Alias
		}
		if held || in.Held[repo] {
			s.CloudTarget, s.CloudTags = "", nil
			for env, override := range s.Environments {
				override.CloudTarget, override.CloudTags = "", nil
				s.Environments[env] = override
			}
			out.Services[name] = s
			needs.Held[name] = true
			continue
		}
		set := s.For(in.Environment, out.Defaults)
		if !manifest.On(set.Enabled) {
			continue
		}
		needs.Explicit = needs.Explicit || set.CloudTarget != "" || len(set.CloudTags) > 0
		needs.Fallback = needs.Fallback || set.CloudTarget == "" && len(set.CloudTags) == 0 && set.Provider == "" && set.Target == ""
	}
	if set, ok := dbGatewayToPlace(&out, in.Environment); ok {
		needs.Explicit = needs.Explicit || len(set.CloudTags) > 0
		needs.Fallback = needs.Fallback || set.Provider == "" && len(set.CloudTags) == 0
	}
	return &out, needs, nil
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
	return cloudinfra.Placement{Allocation: p.Allocation, PoolID: p.PoolID, PoolRevision: p.PoolRevision, ServerID: p.ServerID, ServerRevision: p.ServerRevision, ConnectionID: p.ConnectionID, ConnectionGeneration: p.ConnectionGeneration, CPUMillis: p.CPUMillis, MemoryBytes: p.MemoryBytes, GPUKind: p.GPUKind, GPUs: p.GPUs}
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
func (s *Service) resolvePools(ctx context.Context, plan *Plan, m *manifest.Manifest, a *cloudprofiles.Assignment, bindings []*Binding, options []ConnectionOption, held map[string]bool, future ...map[string]*CapacityIntent) (map[string]*cloudinfra.Placement, error) {
	out := map[string]*cloudinfra.Placement{}
	if a == nil {
		return out, nil
	}
	resolver, ok := s.store.(poolResolver)
	var planned []cloudinfra.PlannedPlacement
	for _, name := range m.ServiceNames() {
		svc := m.Services[name]
		set := svc.For(plan.Environment, m.Defaults)
		if held[name] || !manifest.On(set.Enabled) {
			continue
		}
		// Resolution reports a missing or ambiguous target; capacity skips it.
		selected, selectErr := SelectCloudTarget(set, a, name)
		if selectErr != nil || selected == nil || selected.PoolID == "" {
			continue
		}
		t := *selected
		if !ok {
			return nil, fmt.Errorf("pool capacity resolver is unavailable")
		}
		cpu, mem, err := ServerResources(set.Size)
		if err != nil {
			return nil, err
		}
		req := cloudinfra.PlacementRequest{PlanID: plan.ID, Planned: planned, OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, CloudID: a.CloudID, TargetID: t.ID, TargetRevision: t.Revision, PoolID: t.PoolID, Service: name, CPUMillis: cpu, MemoryBytes: mem, Replicas: 1}
		req.GPUKind, req.GPUs = ServerGPUs(set.Size)
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
			// Only a pool placement's server is one a pool can keep it on.
			if old.CloudPlacement != nil && old.CloudPlacement.PoolID != "" {
				req.StickyServerID = old.CloudPlacement.ServerID
				req.Allocation = old.CloudPlacement.Allocation
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
				// The GPUs stay the new size's: the runtime labels the
				// container with them, so the reservation must hold the
				// same count on the destination.
				req.DestinationServerID = out[name].ServerID
				out[name], err = resolver.PreviewPlacement(ctx, req)
			}
		}
		if err != nil && len(future) > 0 {
			intent, intentErr := s.futureCapacity(ctx, plan, a, t, req, svc.Repo, err)
			if intentErr == nil {
				future[0][name] = intent
				delete(out, name)
				continue
			}
			err = intentErr
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		planned = append(planned, cloudinfra.PlannedPlacement{Service: name, Placement: *out[name]})
	}
	return out, nil
}

// ConnectionHosts reads what runs behind each of the organization's server
// connections. A connection is pooled when it reaches a pool member, as
// its runtime or through the same Docker daemon. Its Cloud servers are
// only those it's the runtime connection of, as a domain route needs.
func (s *PgStore) ConnectionHosts(ctx context.Context, org string) (map[string]ConnectionHost, error) {
	rows, err := s.db.Query(ctx, `SELECT c.id,s.id,s.cloud_id,s.runtime_connection_id=c.id AND s.org_id=c.org_id,EXISTS(SELECT 1 FROM loomplane_server_pool_members m WHERE m.server_id=s.id) FROM loomplane_cloud_connections c JOIN loomplane_cloud_servers s ON (s.runtime_connection_id=c.id OR (coalesce(c.details->>'docker_id','')<>'' AND c.details->>'docker_id'=s.body->'health'->>'docker_id')) WHERE c.org_id=$1 AND c.cloud='server' AND s.status NOT IN ('deleted','disconnected') ORDER BY c.id,s.id`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ConnectionHost{}
	for rows.Next() {
		var connection string
		var server CloudServer
		var runtime, pooled bool
		if checkErr := rows.Scan(&connection, &server.ID, &server.CloudID, &runtime, &pooled); checkErr != nil {
			return nil, checkErr
		}
		host := out[connection]
		host.Pooled = host.Pooled || pooled
		if runtime {
			host.Servers = append(host.Servers, server)
		}
		out[connection] = host
	}
	return out, rows.Err()
}

// PooledConnections are the server connections that reach a pool member.
func (s *PgStore) PooledConnections(ctx context.Context, org string) (map[string]bool, error) {
	hosts, err := s.ConnectionHosts(ctx, org)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for id, host := range hosts {
		if host.Pooled {
			out[id] = true
		}
	}
	return out, nil
}

func poolRelocation(bindings []*Binding, placements map[string]CloudPlacement) bool {
	for _, b := range bindings {
		p, ok := placements[b.Name]
		if !ok || p.PoolID == "" {
			continue
		}
		var old ServiceConfig
		if json.Unmarshal(b.Config, &old) == nil && old.CloudPlacement != nil && old.CloudPlacement.PoolID != "" && old.CloudPlacement.ServerID != "" && old.CloudPlacement.ServerID != p.ServerID {
			return true
		}
	}
	return false
}
