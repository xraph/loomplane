package graph

import (
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/xraph/loomplane/pkg/discovery"
)

// kindOrder decides a service's kind when its entrypoint has several
// frameworks: a Next.js app with a custom Express server is a web app, a
// Forge server configured with Cobra flags is an HTTP service.
var kindOrder = []struct {
	fw   discovery.FrameworkKind
	kind ServiceKind
}{
	{discovery.Native, NativeApp},
	{discovery.Web, WebApp},
	{discovery.Static, StaticSite},
	{discovery.HTTP, HTTPService},
	{discovery.GRPC, HTTPService},
	{discovery.CLI, Tool},
}

type builder struct {
	g         *Graph
	resources map[string]*Resource
	edges     map[string]*Edge
}

// Build turns a discovery report into a graph. It's deterministic: the
// same report always gives the same graph.
func Build(r *discovery.Report) *Graph {
	b := &builder{
		g: &Graph{
			Version: SchemaVersion, Services: []Service{}, Packages: []Package{}, Resources: []Resource{}, Edges: []Edge{},
			Workspaces: r.Workspaces, BuildSystems: r.BuildSystems, Notes: r.Notes,
		},
		resources: map[string]*Resource{},
		edges:     map[string]*Edge{},
	}

	depended := map[string]bool{}
	for _, u := range r.Units {
		for _, d := range u.DependsOn {
			depended[d.Unit] = true
		}
	}
	nodeRoots := map[string]bool{}
	for _, ws := range r.Workspaces {
		if ws.Ecosystem == discovery.Node {
			nodeRoots[ws.Path] = true
		}
	}

	used := map[string]bool{}
	for _, u := range r.Units {
		// A Node workspace root with nothing to run is the monorepo's
		// tooling, not a package anything builds with.
		isToolingRoot := u.Ecosystem == discovery.Node && nodeRoots[u.Path] && len(u.Entrypoints) == 0
		if (len(u.Entrypoints) == 0 || depended[u.ID]) && !isToolingRoot {
			b.addPackage(u)
		}
		for _, ep := range u.Entrypoints {
			svc := service(u, ep)
			if img, build := matchImage(r.Images, u, ep, used); img != nil {
				used[img.Path] = true
				svc.Image = img.Path
				svc.Ports = img.Ports
				svc.Evidence = append(svc.Evidence, img.Evidence...)
				if build != nil {
					svc.Evidence = append(svc.Evidence, *build)
				}
			}
			b.g.Services = append(b.g.Services, svc)
			for _, d := range u.DependsOn {
				b.edge(svc.ID, packageID(d.Unit), Uses, discovery.Certain, d.Evidence)
			}
			for _, ru := range ep.Resources {
				b.edge(svc.ID, b.resource(ru), Needs, ru.Confidence, ru.Evidence)
			}
		}
	}

	for _, img := range r.Images {
		if !used[img.Path] {
			b.g.Services = append(b.g.Services, imageService(img))
		}
	}
	for _, ru := range r.Resources {
		b.resource(ru)
	}
	b.finish()
	return b.g
}

func packageID(unitID string) string { return "pkg:" + unitID }

func (b *builder) addPackage(u discovery.Unit) {
	id := packageID(u.ID)
	b.g.Packages = append(b.g.Packages, Package{
		ID: id, Name: u.Name, Ecosystem: u.Ecosystem, Path: u.Path, Runtime: u.Runtime,
		Requires: slices.Clone(u.Requires), Evidence: u.Evidence,
	})
	for _, d := range u.DependsOn {
		b.edge(id, packageID(d.Unit), Uses, discovery.Certain, d.Evidence)
	}
	for _, ru := range u.Resources {
		b.edge(id, b.resource(ru), Needs, ru.Confidence, ru.Evidence)
	}
}

// service classifies an entrypoint by its strongest framework. Only a
// framework the entrypoint likely uses decides: a Rust binary that merely
// shares a crate with an axum server is a possible worker, not a server.
func service(u discovery.Unit, ep discovery.Entrypoint) Service {
	kind, conf := Worker, discovery.Possible
	var framework string
	for _, k := range kindOrder {
		if fw := pick(ep.Frameworks, k.fw, discovery.Likely); fw != nil {
			kind, framework, conf = k.kind, fw.Name, fw.Confidence
			break
		}
	}
	return Service{
		ID:         "svc:" + string(u.Ecosystem) + ":" + ep.Path,
		Name:       ep.Name,
		Kind:       kind,
		Deployable: kind.Deployable(),
		Ecosystem:  u.Ecosystem,
		Unit:       u.ID,
		UnitName:   u.Name,
		Requires:   slices.Clone(u.Requires),
		Path:       u.Path,
		Entrypoint: ep.Path,
		Framework:  framework,
		Runtime:    u.Runtime,
		Commands:   u.Commands,
		Confidence: conf,
		Evidence:   append([]discovery.Evidence(nil), ep.Evidence...),
	}
}

// pick returns the first framework of kind whose confidence is at least
// min.
func pick(fws []discovery.Framework, kind discovery.FrameworkKind, minimum discovery.Confidence) *discovery.Framework {
	for i := range fws {
		if fws[i].Kind == kind && fws[i].Confidence.Rank() >= minimum.Rank() {
			return &fws[i]
		}
	}
	return nil
}

// matchImage finds the Dockerfile that builds an entrypoint: one whose
// context or directory is the entrypoint's, or the unit's when the unit
// has only the one entrypoint. Failing that, a Dockerfile higher up whose
// build step names the entrypoint (`go build ./cmd/api`, `cargo build
// --bin api`), returned with that step as evidence.
func matchImage(images []discovery.Image, u discovery.Unit, ep discovery.Entrypoint, used map[string]bool) (*discovery.Image, *discovery.Evidence) {
	dirs := []string{ep.Path}
	if len(u.Entrypoints) == 1 {
		dirs = append(dirs, u.Path)
	}
	for _, d := range dirs {
		for i := range images {
			img := &images[i]
			if !used[img.Path] && (img.Context == d || path.Dir(img.Path) == d) {
				return img, nil
			}
		}
	}
	for i := range images {
		img := &images[i]
		if used[img.Path] || !within(ep.Path, img.Context) {
			continue
		}
		if ev := buildStep(img, u.Ecosystem, ep); ev != nil {
			return img, ev
		}
	}
	return nil, nil
}

func within(name, dir string) bool {
	return dir == "." || name == dir || strings.HasPrefix(name, dir+"/")
}

// buildStep returns the build instruction in img that names ep.
func buildStep(img *discovery.Image, eco discovery.Ecosystem, ep discovery.Entrypoint) *discovery.Evidence {
	var re *regexp.Regexp
	switch eco {
	case discovery.Rust:
		re = regexp.MustCompile(`--bin[ =]` + regexp.QuoteMeta(ep.Name) + `($|\s)`)
	case discovery.Go, discovery.Node, discovery.Container:
		rel := strings.TrimPrefix(ep.Path, img.Context+"/")
		if img.Context == "." {
			rel = ep.Path
		}
		if rel == "" || rel == "." || rel == ep.Path && img.Context != "." {
			return nil
		}
		re = regexp.MustCompile(`(^|[\s="'])(\./)?` + regexp.QuoteMeta(rel) + `($|[\s"'/])`)
	}
	if re == nil {
		return nil
	}
	for i := range img.Builds {
		if re.MatchString(img.Builds[i].Detail) {
			return &img.Builds[i]
		}
	}
	return nil
}

// imageService is a service known only from its Dockerfile: code in a
// language Loomplane doesn't read, built into a container all the same.
func imageService(img discovery.Image) Service {
	kind, conf := Worker, discovery.Possible
	if len(img.Ports) > 0 {
		kind, conf = HTTPService, discovery.Likely
	}
	name := path.Base(img.Context)
	if img.Context == "." {
		name = "app"
	}
	return Service{
		ID: "svc:container:" + img.Path, Name: name, Kind: kind, Deployable: true, Ecosystem: discovery.Container,
		Path: img.Context, Entrypoint: img.Path, Image: img.Path, Ports: img.Ports, Confidence: conf,
		Evidence: append([]discovery.Evidence(nil), img.Evidence...),
	}
}

// resource adds the use to its resource node and returns the node's ID.
func (b *builder) resource(ru discovery.ResourceUse) string {
	id := "res:" + ru.Kind
	res := b.resources[id]
	if res == nil {
		res = &Resource{ID: id, Kind: ru.Kind, Confidence: ru.Confidence}
		b.resources[id] = res
	}
	if ru.Confidence.Rank() > res.Confidence.Rank() {
		res.Confidence = ru.Confidence
	}
	res.Evidence = addEvidence(res.Evidence, ru.Evidence)
	return id
}

func (b *builder) edge(from, to string, kind EdgeKind, conf discovery.Confidence, evs ...discovery.Evidence) {
	key := from + "|" + to + "|" + string(kind)
	e := b.edges[key]
	if e == nil {
		e = &Edge{From: from, To: to, Kind: kind, Confidence: conf}
		b.edges[key] = e
	}
	if conf.Rank() > e.Confidence.Rank() {
		e.Confidence = conf
	}
	for _, ev := range evs {
		e.Evidence = addEvidence(e.Evidence, ev)
	}
}

func addEvidence(list []discovery.Evidence, ev discovery.Evidence) []discovery.Evidence {
	for _, x := range list {
		if x == ev {
			return list
		}
	}
	return append(list, ev)
}

func (b *builder) finish() {
	g := b.g
	for _, res := range b.resources {
		res.NeedsConfirmation = res.Confidence == discovery.Possible
		sortEvidence(res.Evidence)
		g.Resources = append(g.Resources, *res)
	}
	for _, e := range b.edges {
		sortEvidence(e.Evidence)
		g.Edges = append(g.Edges, *e)
	}
	sort.SliceStable(g.Services, func(i, j int) bool { return g.Services[i].ID < g.Services[j].ID })
	sort.SliceStable(g.Packages, func(i, j int) bool { return g.Packages[i].ID < g.Packages[j].ID })
	sort.SliceStable(g.Resources, func(i, j int) bool { return g.Resources[i].ID < g.Resources[j].ID })
	sort.SliceStable(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].To < g.Edges[j].To
	})
}

func sortEvidence(list []discovery.Evidence) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Path != list[j].Path {
			return list[i].Path < list[j].Path
		}
		return list[i].Line < list[j].Line
	})
}
