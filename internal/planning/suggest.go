package planning

import (
	"context"
	"errors"
	"slices"

	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/security"
	discoveryapi "github.com/xraph/loomplane/pkg/discovery"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// ServiceSuggestions are the services analysis found that the project's
// manifest doesn't run yet, each written the way add_service takes it, so
// adding one is a single edit.
type ServiceSuggestions struct {
	// ProjectID keys the suggestions for the dashboard's client.
	ProjectID string `json:"project_id" forge:"id"`
	// Analyzed is false until an analysis has finished, when there's
	// nothing to suggest from yet.
	Analyzed bool                `json:"analyzed"`
	Services []ServiceSuggestion `json:"services"`
}

// ServiceSuggestion is one service to add, and what analysis knows about
// it to help someone decide.
type ServiceSuggestion struct {
	// ID is the service's ID in the project's graph.
	ID        string                 `json:"id"`
	Kind      graph.ServiceKind      `json:"kind"`
	Ecosystem discoveryapi.Ecosystem `json:"ecosystem"`
	Framework string                 `json:"framework,omitempty"`
	// Path is its directory in the repository.
	Path  string `json:"path"`
	Ports []int  `json:"ports,omitempty"`
	// Env are the variables its code reads, which may need setting.
	Env []string `json:"env,omitempty"`
	// Service is the add_service edit that adds it: a free name, its
	// source, a provider that runs it unless the manifest's default does,
	// and what it needs among what the manifest already has.
	Service manifests.NewServiceInput `json:"service"`
}

// SuggestServices lists what analysis found that the manifest doesn't run.
// A project with no manifest yet gets every deployable service.
func (s *Service) SuggestServices(ctx context.Context, p security.Principal, projectID string) (*ServiceSuggestions, error) {
	if err := s.authz.Require(ctx, p, security.ProjectRead, projectID); err != nil {
		return nil, err
	}
	proj, err := s.projects.GetProject(ctx, p.OrgID, projectID)
	if err != nil {
		return nil, err
	}
	out := &ServiceSuggestions{ProjectID: projectID, Services: []ServiceSuggestion{}}
	g, states, err := discovery.ProjectGraph(ctx, s.analyses, p.OrgID, projectID)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(states, func(st discovery.SourceState) bool { return st.Shown != nil }) {
		return out, nil
	}
	out.Analyzed = true
	g, _ = graph.Exclude(g, proj.ExclusionRules())
	m := &manifest.Manifest{Version: manifest.Version}
	switch rev, err := s.manifests.Latest(ctx, p.OrgID, projectID); {
	case errors.Is(err, manifests.ErrNotFound):
	case err != nil:
		return nil, err
	case rev.Manifest != nil:
		m = rev.Manifest
	}
	out.Services = Suggest(g, m, s.providers)
	return out, nil
}

// Suggest proposes an add_service edit for each deployable service in g
// that no service in m runs, in the graph's order.
func Suggest(g *graph.Graph, m *manifest.Manifest, reg *provider.Registry) []ServiceSuggestion {
	taken := map[string]bool{}
	for _, section := range [][]string{m.ServiceNames(), m.ResourceNames(), m.GatewayNames(), m.LinkNames()} {
		for _, name := range section {
			taken[name] = true
		}
	}
	// What each graph node is called in the manifest, where it's there.
	named := map[string]string{}
	for i := range g.Services {
		gs := &g.Services[i]
		for _, name := range m.ServiceNames() {
			if runs(m.Services[name], gs) {
				named[gs.ID] = name
				break
			}
		}
	}
	for _, r := range g.Resources {
		for _, name := range m.ResourceNames() {
			if m.Resources[name].Kind == r.Kind {
				named[r.ID] = name
				break
			}
		}
	}
	uses := map[string][]string{}
	for _, e := range g.Edges {
		uses[e.From] = append(uses[e.From], e.To)
	}
	// A suggestion lands beside the manifest's services: on a provider
	// they use, and on their server or cluster when they share one.
	used := map[string][]string{}
	for _, name := range m.ServiceNames() {
		svc := m.Services[name]
		if svc.Provider != "" {
			used[svc.Provider] = append(used[svc.Provider], svc.Target)
		}
	}

	out := []ServiceSuggestion{}
	for i := range g.Services {
		gs := &g.Services[i]
		if !gs.Deployable || named[gs.ID] != "" {
			continue
		}
		in := manifests.NewServiceInput{
			Name:   unique(nameOf(gs.Name, "service"), taken),
			Source: gs.Local(),
		}
		if len(g.Sources) > 1 {
			in.Repo = gs.Source
		}
		in.Provider = suggestedProvider(reg, gs.Kind, m.Defaults.Provider, used)
		if targets := used[in.Provider]; len(targets) > 0 && targets[0] != "" && !slices.ContainsFunc(targets, func(t string) bool { return t != targets[0] }) {
			in.Target = targets[0]
		}
		for _, to := range reach(gs.ID, uses) {
			if need := named[to]; need != "" && !slices.Contains(in.Needs, need) {
				in.Needs = append(in.Needs, need)
			}
		}
		slices.Sort(in.Needs)
		var env []string
		for _, e := range gs.Env {
			if !slices.Contains(env, e.Name) {
				env = append(env, e.Name)
			}
		}
		out = append(out, ServiceSuggestion{
			ID: gs.ID, Kind: gs.Kind, Ecosystem: gs.Ecosystem, Framework: gs.Framework,
			Path: gs.Path, Ports: gs.Ports, Env: env, Service: in,
		})
	}
	return out
}

// runs says whether a manifest service runs the graph service, by the
// rules the planner matches them with: in its repository, at its source,
// its entrypoint or its unit's directory.
func runs(s manifest.Service, gs *graph.Service) bool {
	if s.Repo != "" && s.Repo != gs.Source {
		return false
	}
	return s.Source == gs.Local() || s.Source == gs.Entrypoint || s.Source == gs.Path
}

// suggestedProvider is "" when the manifest's default provider runs the
// kind, so the service inherits it; else the first provider, in the
// catalog's order of fit for the kind, that the manifest already uses;
// else the first that runs the kind at all.
func suggestedProvider(reg *provider.Registry, kind graph.ServiceKind, fallback string, used map[string][]string) string {
	if reg == nil {
		return ""
	}
	able := slices.DeleteFunc(reg.Running(provider.Workload(kind)), func(p *provider.Provider) bool { return p.Unlisted })
	if len(able) == 0 {
		return ""
	}
	if fallback != "" && slices.ContainsFunc(able, func(p *provider.Provider) bool { return p.Name == fallback }) {
		return ""
	}
	for _, p := range able {
		if _, ok := used[p.Name]; ok {
			return p.Name
		}
	}
	return able[0].Name
}
