package planning

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/manifest"
)

// CloudPlacement pins the reviewed Cloud configuration and verified account.
type CloudPlacement struct {
	CloudID            string `json:"cloud_id"`
	CloudRevision      int64  `json:"cloud_revision"`
	AssignmentRevision int64  `json:"assignment_revision"`
	TargetID           string `json:"target_id"`
	TargetRevision     int64  `json:"target_revision"`
	TargetName         string `json:"target_name"`
	Provider           string `json:"provider"`
	ConnectionID       string `json:"connection_id"`
	AccountIdentity    string `json:"account_identity"`
	Region             string `json:"region,omitempty"`
}

// ResolveCloudTargets returns an independent manifest with effective placement.
// The caller checks the assignment's organization, project and environment.
func ResolveCloudTargets(m *manifest.Manifest, environment string, assignment *cloudprofiles.Assignment, options []ConnectionOption) (*manifest.Manifest, map[string]CloudPlacement, error) {
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
		if target.OrgID != assignment.OrgID || target.ProjectID != assignment.ProjectID || target.CloudID != assignment.CloudID {
			return nil, nil, fmt.Errorf("%s: target is outside the assignment scope", path)
		}
		ci := slices.IndexFunc(options, func(c ConnectionOption) bool { return c.ID == target.ConnectionID })
		if ci < 0 || !options[ci].Ready {
			return nil, nil, fmt.Errorf("%s: target connection is missing or not ready; check it under Integrations", path)
		}
		c := options[ci]
		identity := connections.AccountIdentity(c.Cloud, c.Account, c.Details)
		if identity == "" {
			return nil, nil, fmt.Errorf("%s: target account identity is unverified; recheck Vercel or reconnect owned infrastructure under Integrations", path)
		}
		set.Provider = target.Provider
		if set.Region == "" {
			set.Region = target.Region
		}
		if OwnedCloud(c.Cloud) {
			set.Target = c.ID
		}
		placements[name] = CloudPlacement{CloudID: assignment.CloudID, CloudRevision: assignment.CloudRevision, AssignmentRevision: assignment.Revision, TargetID: target.ID, TargetRevision: target.Revision, TargetName: target.Name, Provider: target.Provider, ConnectionID: target.ConnectionID, AccountIdentity: identity, Region: set.Region}
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
