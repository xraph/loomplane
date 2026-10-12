package graph

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/xraph/loomplane/pkg/discovery"
)

// SourceRef says where one part of a composed graph was read from.
type SourceRef struct {
	Alias      string `json:"alias"`
	Repository string `json:"repository"`
	Root       string `json:"root,omitempty"`
	CommitSHA  string `json:"commit_sha"`
	AnalysisID string `json:"analysis_id"`
}

// Part is one source's graph, as its analysis stored it.
type Part struct {
	Source SourceRef
	// Graph is nil when the source has no successful analysis yet.
	Graph *Graph
}

// Compose joins one graph per source into the application's graph. Each
// service and package ID gains its source's alias, a source's root keeps
// only what's under it, resources of one kind become one node, and a
// dependency one source declares on a package another publishes becomes
// an edge between them. It's deterministic, like Build: the same parts, in
// any order, give the same graph.
func Compose(parts []Part) *Graph {
	parts = slices.Clone(parts)
	sort.SliceStable(parts, func(i, j int) bool { return parts[i].Source.Alias < parts[j].Source.Alias })
	b := &builder{
		g: &Graph{
			Version: SchemaVersion, Sources: []SourceRef{}, Services: []Service{}, Packages: []Package{},
			Resources: []Resource{}, Edges: []Edge{}, Workspaces: []discovery.Workspace{},
			BuildSystems: []discovery.BuildSystem{}, Notes: []discovery.Note{},
		},
		resources: map[string]*Resource{},
		edges:     map[string]*Edge{},
	}
	for _, p := range parts {
		b.g.Sources = append(b.g.Sources, p.Source)
		b.add(p)
	}
	b.link()
	b.finish()
	sort.SliceStable(b.g.Notes, func(i, j int) bool {
		x, y := b.g.Notes[i], b.g.Notes[j]
		if x.Source != y.Source {
			return x.Source < y.Source
		}
		if x.Path != y.Path {
			return x.Path < y.Path
		}
		return x.Message < y.Message
	})
	return b.g
}

func (b *builder) add(p Part) {
	alias := p.Source.Alias
	if p.Graph == nil {
		b.g.Notes = append(b.g.Notes, discovery.Note{Source: alias,
			Message: fmt.Sprintf("%s has no successful analysis yet, so nothing from %s is in the graph.", alias, p.Source.Repository)})
		return
	}
	g := rooted(p.Graph, p.Source.Root)
	if g.Version < SchemaVersion {
		b.g.Notes = append(b.g.Notes, discovery.Note{Source: alias,
			Message: fmt.Sprintf("%s was analyzed before Loomplane read dependencies between repositories. Analyze it again to find them.", alias)})
	}
	for _, s := range g.Services {
		s.ID, s.Source, s.Evidence, s.Requires = qualify(s.ID, alias), alias, tagged(s.Evidence, alias), taggedRequires(s.Requires, alias)
		b.g.Services = append(b.g.Services, s)
	}
	for _, pk := range g.Packages {
		pk.ID, pk.Source, pk.Evidence, pk.Requires = qualify(pk.ID, alias), alias, tagged(pk.Evidence, alias), taggedRequires(pk.Requires, alias)
		b.g.Packages = append(b.g.Packages, pk)
	}
	for _, r := range g.Resources {
		res := b.resources[r.ID]
		if res == nil {
			res = &Resource{ID: r.ID, Kind: r.Kind, Confidence: r.Confidence}
			b.resources[r.ID] = res
		}
		if r.Confidence.Rank() > res.Confidence.Rank() {
			res.Confidence = r.Confidence
		}
		for _, ev := range tagged(r.Evidence, alias) {
			res.Evidence = addEvidence(res.Evidence, ev)
		}
	}
	for _, e := range g.Edges {
		b.edge(qualify(e.From, alias), qualify(e.To, alias), e.Kind, e.Confidence, tagged(e.Evidence, alias)...)
	}
	for _, w := range g.Workspaces {
		w.Source, w.Evidence = alias, tagged(w.Evidence, alias)
		b.g.Workspaces = append(b.g.Workspaces, w)
	}
	for _, bs := range g.BuildSystems {
		bs.Source, bs.Evidence.Source = alias, alias
		b.g.BuildSystems = append(b.g.BuildSystems, bs)
	}
	for _, n := range g.Notes {
		n.Source = alias
		b.g.Notes = append(b.g.Notes, n)
	}
}

// link adds an edge for each dependency one source declares on a package
// another source publishes. The edge is certain when the declaration
// names the other repository itself (a Rust git dependency, a Go module
// path on github.com) and likely when only the name matches. A name two
// other sources publish gets no edge, only a note: a wrong edge is worse
// than a missing one.
func (b *builder) link() {
	type key struct {
		eco  discovery.Ecosystem
		name string
	}
	published := map[key]map[string][]string{} // source alias to node IDs
	publish := func(k key, source, id string) {
		if published[k] == nil {
			published[k] = map[string][]string{}
		}
		published[k][source] = append(published[k][source], id)
	}
	hasPackage := map[string]bool{}
	for _, p := range b.g.Packages {
		if p.Name == "" {
			continue
		}
		publish(key{p.Ecosystem, p.Name}, p.Source, p.ID)
		hasPackage[p.Source+"\x00"+string(p.Ecosystem)+"\x00"+p.Name] = true
	}
	// A unit with no library, only services, is published by its services.
	for _, s := range b.g.Services {
		if s.UnitName != "" && !hasPackage[s.Source+"\x00"+string(s.Ecosystem)+"\x00"+s.UnitName] {
			publish(key{s.Ecosystem, s.UnitName}, s.Source, s.ID)
		}
	}
	repos := map[string]string{}
	for _, src := range b.g.Sources {
		repos[src.Alias] = src.Repository
	}
	noted := map[string]bool{}
	use := func(from, source string, eco discovery.Ecosystem, reqs []discovery.Requirement) {
		for _, req := range reqs {
			others := map[string][]string{}
			for src, nodes := range published[key{eco, req.Name}] {
				if src != source {
					others[src] = nodes
				}
			}
			if len(others) > 1 {
				names := slices.Sorted(maps.Keys(others))
				msg := fmt.Sprintf("%s is published by %s, so Loomplane can't tell which one %s uses.", req.Name, strings.Join(names, " and "), source)
				if !noted[msg] {
					noted[msg] = true
					b.g.Notes = append(b.g.Notes, discovery.Note{Source: source, Path: req.Evidence.Path, Message: msg})
				}
				continue
			}
			for target, nodes := range others {
				conf := discovery.Likely
				if structural(req, eco, repos[target]) {
					conf = discovery.Certain
				}
				for _, id := range nodes {
					b.edge(from, id, Uses, conf, req.Evidence)
				}
			}
		}
	}
	for _, s := range b.g.Services {
		use(s.ID, s.Source, s.Ecosystem, s.Requires)
	}
	for _, p := range b.g.Packages {
		use(p.ID, p.Source, p.Ecosystem, p.Requires)
	}
}

// structural reports whether a requirement names the repository itself,
// not only a package name anyone could publish. GitHub owners and names
// ignore case, so the comparison does too.
func structural(req discovery.Requirement, eco discovery.Ecosystem, repository string) bool {
	if repository == "" {
		return false
	}
	repo := strings.ToLower(repository)
	switch eco {
	case discovery.Rust:
		git := strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(req.Git)), "/"), ".git")
		for _, prefix := range []string{"https://github.com/", "http://github.com/", "ssh://git@github.com/", "git@github.com:"} {
			if rest, ok := strings.CutPrefix(git, prefix); ok {
				return rest == repo
			}
		}
	case discovery.Go:
		name := strings.ToLower(req.Name)
		return name == "github.com/"+repo || strings.HasPrefix(name, "github.com/"+repo+"/")
	}
	return false
}

// qualify puts alias into a service or package ID. Resource IDs are the
// project's, not a repository's, so they stay as they are.
func qualify(id, alias string) string {
	for _, prefix := range []string{"svc:", "pkg:"} {
		if rest, ok := strings.CutPrefix(id, prefix); ok {
			return prefix + alias + ":" + rest
		}
	}
	return id
}

func tagged(evs []discovery.Evidence, alias string) []discovery.Evidence {
	out := make([]discovery.Evidence, len(evs))
	for i, ev := range evs {
		ev.Source = alias
		out[i] = ev
	}
	return out
}

func taggedRequires(reqs []discovery.Requirement, alias string) []discovery.Requirement {
	if reqs == nil {
		return nil
	}
	out := make([]discovery.Requirement, len(reqs))
	for i, r := range reqs {
		r.Evidence.Source = alias
		out[i] = r
	}
	return out
}

// rooted keeps the part of g under root: the services whose entrypoint is
// there, the packages there, everything those reach through edges however
// deep (a web app under apps/web still builds with packages/ui), and the
// resources whose evidence is there.
func rooted(g *Graph, root string) *Graph {
	if root == "" {
		return g
	}
	keep := map[string]bool{}
	var queue []string
	for _, s := range g.Services {
		if under(s.Entrypoint, root) {
			keep[s.ID] = true
			queue = append(queue, s.ID)
		}
	}
	for _, p := range g.Packages {
		if under(p.Path, root) && !keep[p.ID] {
			keep[p.ID] = true
			queue = append(queue, p.ID)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, e := range g.Edges {
			if e.From == id && !keep[e.To] {
				keep[e.To] = true
				queue = append(queue, e.To)
			}
		}
	}
	out := *g
	out.Services = slices.DeleteFunc(slices.Clone(g.Services), func(s Service) bool { return !keep[s.ID] })
	out.Packages = slices.DeleteFunc(slices.Clone(g.Packages), func(p Package) bool { return !keep[p.ID] })
	out.Resources = slices.DeleteFunc(slices.Clone(g.Resources), func(r Resource) bool {
		return !keep[r.ID] && !slices.ContainsFunc(r.Evidence, func(ev discovery.Evidence) bool { return under(ev.Path, root) })
	})
	out.Edges = slices.DeleteFunc(slices.Clone(g.Edges), func(e Edge) bool { return !keep[e.From] || !keep[e.To] })
	return &out
}

func under(p, root string) bool { return p == root || strings.HasPrefix(p, root+"/") }
