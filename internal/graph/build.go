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
	{discovery.Worker, Worker},
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
	// reach holds, per service, the Go packages its main imports.
	var reach []map[string]bool
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
				svc.Image, svc.Context = img.Path, img.Context
				svc.Ports = img.Ports
				svc.Evidence = append(svc.Evidence, img.Evidence...)
				if build != nil {
					svc.Evidence = append(svc.Evidence, *build)
				}
			}
			b.g.Services = append(b.g.Services, svc)
			reach = append(reach, packageSet(ep.Packages))
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
	reach = append(reach, make([]map[string]bool, len(b.g.Services)-len(reach))...)
	assignEnv(b.g.Services, reach, r.Env)
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
		if fw := pick(ep.Frameworks, k.fw, discovery.Certain); fw != nil {
			kind, framework, conf = k.kind, fw.Name, fw.Confidence
			break
		}
	}
	for i, k := range kindOrder {
		if conf == discovery.Certain {
			break
		}
		if fw := pick(ep.Frameworks, k.fw, discovery.Likely); fw != nil {
			kind, framework, conf = k.kind, fw.Name, fw.Confidence
			// A framework that would have made it something else, had it
			// been likely, leaves the kind in doubt.
			for _, before := range kindOrder[:i] {
				if pick(ep.Frameworks, before.fw, discovery.Possible) != nil {
					conf = discovery.Possible
				}
			}
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
		Commands:   commandsOf(u, ep),
		Confidence: conf,
		Evidence:   append([]discovery.Evidence(nil), ep.Evidence...),
	}
}

// pick returns the first framework of kind whose confidence is at least
// min.
// commandsOf are an entrypoint's commands: its own, else its unit's.
func commandsOf(u discovery.Unit, ep discovery.Entrypoint) discovery.Commands {
	if ep.Commands != nil {
		return *ep.Commands
	}
	return u.Commands
}

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
	case discovery.Go, discovery.Node, discovery.Python, discovery.JVM, discovery.Container:
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
		Path: img.Context, Entrypoint: img.Path, Image: img.Path, Context: img.Context, Ports: img.Ports, Confidence: conf,
		Evidence: append([]discovery.Evidence(nil), img.Evidence...),
	}
}

// assignEnv gives each service the variables read in its own code. A file
// belongs to the services whose entrypoint directory holds it most
// closely, then to those whose source directory does. A file none of them
// holds, such as a root .env.example beside apps/api and apps/web, is a
// stray: it goes to the services at the root, or to every service when
// none is, and a source file only to services of its own ecosystem.
//
// A Go source file goes by imports instead: it belongs to the services
// whose main package reaches its package, so a config package only one
// command imports doesn't show up for every command of the module. One
// no main reaches is linked into none of them, and goes by directory to
// the other services only when one holds it.
func assignEnv(services []Service, reach []map[string]bool, vars []discovery.EnvUse) {
	var all, plain []int // plain are the services that aren't Go mains
	for i := range services {
		all = append(all, i)
		if reach[i] == nil {
			plain = append(plain, i)
		}
	}
	srcPath := func(s Service) string { return s.Path }
	owners := map[string][]int{}
	ownersOf := func(file string) []int {
		if o, ok := owners[file]; ok {
			return o
		}
		candidates := all
		goFile := path.Ext(file) == ".go" && len(plain) < len(services)
		if goFile {
			candidates = plain
			for i, pkgs := range reach {
				if pkgs[path.Dir(file)] {
					owners[file] = append(owners[file], i)
				}
			}
			if o := owners[file]; o != nil {
				return o
			}
		}
		o := holding(services, candidates, file, entryDir)
		if o == nil {
			o = atRoot(services, candidates, entryDir)
		}
		if o == nil {
			o = holding(services, candidates, file, srcPath)
		}
		if o == nil {
			if dotenvFile(file) {
				o = strayEnvFile(services, candidates, file)
			} else if eco := sourceEcosystem(file); eco != "" {
				var same []int
				for _, i := range candidates {
					if e := services[i].Ecosystem; e == eco || e == discovery.Container {
						same = append(same, i)
					}
				}
				if o = atRoot(services, same, srcPath); o == nil && !goFile {
					o = same
				}
			} else {
				if o = atRoot(services, candidates, srcPath); o == nil && !goFile {
					o = candidates
				}
			}
		}
		owners[file] = o
		return o
	}
	for _, v := range vars {
		got := map[int]*discovery.EnvUse{}
		var order []int
		for _, ev := range v.Evidence {
			for _, i := range ownersOf(ev.Path) {
				if got[i] == nil {
					got[i] = &discovery.EnvUse{Name: v.Name, Example: v.Example, Confidence: v.Confidence}
					order = append(order, i)
				}
				got[i].Evidence = append(got[i].Evidence, ev)
			}
		}
		for _, i := range order {
			services[i].Env = append(services[i].Env, *got[i])
		}
	}
}

// strayEnvFile decides who gets a dotenv file, such as an .env.example,
// that no candidate service's directory holds. That's a root file beside
// apps/api and apps/web, but also a deploy stack's own file such as
// deployments/production/.env.example, which sits under no service.
//
// Only a root file is shared, with the root services or else every
// service. One in a subdirectory describes something else, such as a
// deploy stack, and goes only to the service its directory is named for:
// deployments/push-gateway to loomplane-push-gateway.
func strayEnvFile(services []Service, candidates []int, file string) []int {
	dir := path.Dir(file)
	if dir == "." {
		if o := atRoot(services, candidates, func(s Service) string { return s.Path }); o != nil {
			return o
		}
		return candidates
	}
	name := path.Base(dir)
	var out []int
	for _, i := range candidates {
		if n := services[i].Name; n == name || strings.HasSuffix(n, "-"+name) {
			out = append(out, i)
		}
	}
	return out
}

// dotenvFile reports whether file is a dotenv file, an example or not.
func dotenvFile(file string) bool {
	base := strings.ToLower(path.Base(file))
	return base == ".env" || strings.HasPrefix(base, ".env.") || strings.HasSuffix(base, ".env") || base == "env.example"
}

// sourceEcosystem is the ecosystem a source file's language builds with,
// or "" for one no detector builds, such as Ruby.
func sourceEcosystem(file string) discovery.Ecosystem {
	switch path.Ext(file) {
	case ".go":
		return discovery.Go
	case ".rs":
		return discovery.Rust
	case ".py":
		return discovery.Python
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".mts", ".cts", ".vue", ".svelte", ".astro":
		return discovery.Node
	case ".java", ".kt", ".kts", ".scala", ".groovy", ".properties", ".yml", ".yaml":
		return discovery.JVM
	}
	return ""
}

// entryDir is the directory a service's entrypoint is: the entrypoint
// itself, unless it names a file such as a Rust binary or a Dockerfile.
func entryDir(s Service) string {
	if path.Ext(s.Entrypoint) != "" || strings.HasPrefix(path.Base(s.Entrypoint), "Dockerfile") {
		return path.Dir(s.Entrypoint)
	}
	return s.Entrypoint
}

// packageSet is a Go main's packages as a set, nil for an entrypoint
// that has none.
func packageSet(dirs []string) map[string]bool {
	if len(dirs) == 0 {
		return nil
	}
	set := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		set[d] = true
	}
	return set
}

// holding returns the candidate services whose directory, by dirOf,
// holds file most closely, or nil when none does. A root directory isn't
// counted here: see atRoot.
func holding(services []Service, candidates []int, file string, dirOf func(Service) string) []int {
	var out []int
	best := -1
	for _, i := range candidates {
		d := path.Clean(dirOf(services[i]))
		if d == "." || d == "" || !within(file, d) {
			continue
		}
		switch n := len(d); {
		case n > best:
			best, out = n, []int{i}
		case n == best:
			out = append(out, i)
		}
	}
	return out
}

// atRoot returns the candidate services whose directory, by dirOf, is the
// repository root.
func atRoot(services []Service, candidates []int, dirOf func(Service) string) []int {
	var out []int
	for _, i := range candidates {
		if d := path.Clean(dirOf(services[i])); d == "." || d == "" {
			out = append(out, i)
		}
	}
	return out
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
