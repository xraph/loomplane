package detect

import (
	"context"
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/pkg/discovery"
)

// Node finds JavaScript and TypeScript packages, the workspaces and build
// systems around them, the framework each one is built on, and the
// drivers in its runtime dependencies.
type Node struct{}

// Name implements discovery.Detector.
func (Node) Name() string { return "node" }

type packageJSON struct {
	Name            string            `json:"name"`
	Main            string            `json:"main"`
	Bin             json.RawMessage   `json:"bin"`
	Workspaces      json.RawMessage   `json:"workspaces"`
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	Engines         map[string]string `json:"engines"`
	PackageManager  string            `json:"packageManager"`
	Prisma          struct {
		Schema string `json:"schema"`
	} `json:"prisma"`
}

type nodePackage struct {
	dir, file string
	data      []byte
	pkg       packageJSON
}

type nodeFramework struct {
	name string
	kind discovery.FrameworkKind
}

// nodeFrameworks maps a dependency to the framework it means. Some need a
// second look (see nodeRefine).
var nodeFrameworks = map[string]nodeFramework{
	"next":                  {"next", discovery.Web},
	"nuxt":                  {"nuxt", discovery.Web},
	"@remix-run/react":      {"remix", discovery.Web},
	"@react-router/dev":     {"react-router", discovery.Web},
	"@sveltejs/kit":         {"sveltekit", discovery.Web},
	"astro":                 {"astro", discovery.Static},
	"gatsby":                {"gatsby", discovery.Static},
	"react-scripts":         {"create-react-app", discovery.Static},
	"@angular/core":         {"angular", discovery.Static},
	"@vue/cli-service":      {"vue-cli", discovery.Static},
	"@docusaurus/core":      {"docusaurus", discovery.Static},
	"vitepress":             {"vitepress", discovery.Static},
	"@tanstack/react-start": {"tanstack-start", discovery.Web},
	"@solidjs/start":        {"solid-start", discovery.Web},
	"@builder.io/qwik-city": {"qwik-city", discovery.Web},
	"express":               {"express", discovery.HTTP},
	"fastify":               {"fastify", discovery.HTTP},
	"koa":                   {"koa", discovery.HTTP},
	"@hapi/hapi":            {"hapi", discovery.HTTP},
	"hono":                  {"hono", discovery.HTTP},
	"@nestjs/core":          {"nestjs", discovery.HTTP},
	"restify":               {"restify", discovery.HTTP},
	"@adonisjs/core":        {"adonis", discovery.HTTP},
	"elysia":                {"elysia", discovery.HTTP},
	"react-native":          {"react-native", discovery.Native},
	"expo":                  {"expo", discovery.Native},
	"electron":              {"electron", discovery.Native},
}

// nodeResources maps runtime dependencies to backing services.
var nodeResources = map[string]string{
	"pg":                             "postgres",
	"postgres":                       "postgres",
	"pg-promise":                     "postgres",
	"slonik":                         "postgres",
	"@neondatabase/serverless":       "postgres",
	"@vercel/postgres":               "postgres",
	"mysql":                          "mysql",
	"mysql2":                         "mysql",
	"mongodb":                        "mongodb",
	"mongoose":                       "mongodb",
	"redis":                          "redis",
	"ioredis":                        "redis",
	"@redis/client":                  "redis",
	"@upstash/redis":                 "redis",
	"amqplib":                        "rabbitmq",
	"amqp-connection-manager":        "rabbitmq",
	"kafkajs":                        "kafka",
	"@confluentinc/kafka-javascript": "kafka",
	"nats":                           "nats",
	"@aws-sdk/client-s3":             "object-storage",
	"minio":                          "object-storage",
	"@google-cloud/storage":          "object-storage",
	"@elastic/elasticsearch":         "search",
	"@opensearch-project/opensearch": "search",
}

var prismaProviders = map[string]string{
	"postgresql":  "postgres",
	"postgres":    "postgres",
	"cockroachdb": "postgres",
	"mysql":       "mysql",
	"mongodb":     "mongodb",
	"sqlserver":   "sqlserver",
}

// Detect implements discovery.Detector.
func (Node) Detect(ctx context.Context, repo discovery.Repo, r *discovery.Report) error {
	var pkgs []*nodePackage
	for _, f := range repo.Named("package.json") {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, n := read(repo, f)
		if n != nil {
			note(r, n)
			continue
		}
		p := &nodePackage{dir: dir(f), file: f, data: data}
		if err := json.Unmarshal(data, &p.pkg); err != nil {
			note(r, &discovery.Note{Path: f, Message: "not a valid package.json"})
			continue
		}
		pkgs = append(pkgs, p)
	}
	projects := nxProjects(repo, r)
	nodeBuildSystems(repo, r)
	if len(pkgs) == 0 && len(projects) == 0 {
		return nil
	}
	dirs := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		dirs = append(dirs, p.dir)
	}
	sort.Strings(dirs)
	nodeWorkspaces(repo, r, pkgs, dirs)

	byName := map[string]string{}
	for _, p := range pkgs {
		if p.pkg.Name != "" {
			byName[p.pkg.Name] = p.dir
		}
	}
	for _, p := range pkgs {
		u := nodeUnit(repo, r, p, byName)
		if nx, ok := projects[p.dir]; ok {
			nxApply(&u, nx)
			delete(projects, p.dir)
		}
		nodeEntrypoint(&u)
		r.Units = append(r.Units, u)
	}
	// Nx projects with no package.json of their own.
	for _, d := range sortedProjectDirs(projects) {
		nx := projects[d]
		u := discovery.Unit{ID: discovery.UnitID(discovery.Node, d), Ecosystem: discovery.Node, Name: nx.name, Path: d,
			Evidence: []discovery.Evidence{nx.ev}}
		nxApply(&u, nx)
		nodeEntrypoint(&u)
		r.Units = append(r.Units, u)
	}
	return nil
}

func nodeWorkspaces(repo discovery.Repo, r *discovery.Report, pkgs []*nodePackage, dirs []string) {
	for _, p := range pkgs {
		patterns := workspacePatterns(p.pkg.Workspaces)
		if len(patterns) == 0 {
			continue
		}
		rel := make([]string, len(patterns))
		for i, pat := range patterns {
			rel[i] = relPattern(p.dir, pat)
		}
		r.Workspaces = append(r.Workspaces, discovery.Workspace{
			Ecosystem: discovery.Node, Tool: nodeTool(repo, p), Path: p.dir,
			Members:  without(expand(rel, dirs), p.dir),
			Evidence: []discovery.Evidence{evidence(p.data, p.file, lineOf(p.data, regexp.MustCompile(`"workspaces"\s*:`), 1))},
		})
	}
	for _, f := range repo.Named("pnpm-workspace.yaml") {
		data, n := read(repo, f)
		if n != nil {
			note(r, n)
			continue
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
			note(r, &discovery.Note{Path: f, Message: "not a valid pnpm-workspace.yaml"})
			continue
		}
		ws := discovery.Workspace{Ecosystem: discovery.Node, Tool: "pnpm", Path: dir(f)}
		var rel []string
		if seq := yamlKey(doc.Content[0], "packages"); seq != nil && seq.Kind == yaml.SequenceNode {
			for _, item := range seq.Content {
				rel = append(rel, relPattern(dir(f), item.Value))
				ws.Evidence = append(ws.Evidence, evidence(data, f, item.Line))
			}
		}
		if len(rel) == 0 {
			continue
		}
		ws.Members = without(expand(rel, dirs), dir(f))
		r.Workspaces = append(r.Workspaces, ws)
	}
}

func relPattern(base, pattern string) string {
	if neg, ok := strings.CutPrefix(pattern, "!"); ok {
		return "!" + join(base, neg)
	}
	return join(base, pattern)
}

func without(list []string, s string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

// workspacePatterns reads package.json's workspaces, an array or an object
// with a packages array (Yarn's form).
func workspacePatterns(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var obj struct {
		Packages []string `json:"packages"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Packages
	}
	return nil
}

func nodeTool(repo discovery.Repo, p *nodePackage) string {
	if pm, _, ok := strings.Cut(p.pkg.PackageManager, "@"); ok && pm != "" {
		return pm
	}
	switch {
	case repo.Has(join(p.dir, "pnpm-lock.yaml")):
		return "pnpm"
	case repo.Has(join(p.dir, "yarn.lock")):
		return "yarn"
	case repo.Has(join(p.dir, "bun.lock")), repo.Has(join(p.dir, "bun.lockb")):
		return "bun"
	}
	return "npm"
}

func nodeBuildSystems(repo discovery.Repo, r *discovery.Report) {
	for _, bs := range []struct{ file, name string }{
		{"turbo.json", "turborepo"}, {"nx.json", "nx"}, {"lerna.json", "lerna"},
	} {
		for _, f := range repo.Named(bs.file) {
			r.BuildSystems = append(r.BuildSystems, discovery.BuildSystem{
				Name: bs.name, Path: dir(f), Evidence: discovery.Evidence{Path: f, Detail: bs.file},
			})
		}
	}
}

func yamlKey(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// jsonDepLine finds the line of dependency name inside section.
func jsonDepLine(data []byte, section, name string) int {
	from := lineOf(data, regexp.MustCompile(`"`+regexp.QuoteMeta(section)+`"\s*:`), 1)
	if from == 0 {
		return 0
	}
	return lineOf(data, regexp.MustCompile(`"`+regexp.QuoteMeta(name)+`"\s*:`), from)
}

func nodeUnit(repo discovery.Repo, r *discovery.Report, p *nodePackage, byName map[string]string) discovery.Unit {
	name := p.pkg.Name
	if name == "" {
		name = path.Base(p.dir)
		if p.dir == "." {
			name = "root"
		}
	}
	u := discovery.Unit{
		ID:        discovery.UnitID(discovery.Node, p.dir),
		Ecosystem: discovery.Node,
		Name:      name,
		Path:      p.dir,
		Commands: discovery.Commands{
			Build: p.pkg.Scripts["build"], Start: p.pkg.Scripts["start"], Dev: p.pkg.Scripts["dev"],
		},
		Evidence: []discovery.Evidence{{Path: p.file, Detail: "package.json"}},
	}
	if l := lineOf(p.data, regexp.MustCompile(`"name"\s*:`), 1); l > 0 && p.pkg.Name != "" {
		u.Evidence = []discovery.Evidence{evidence(p.data, p.file, l)}
	}
	if v := p.pkg.Engines["node"]; v != "" {
		u.Runtime = "node " + v
	}

	all := map[string]string{}
	for k, v := range p.pkg.DevDependencies {
		all[k] = v
	}
	for k, v := range p.pkg.Dependencies {
		all[k] = v
	}
	for _, dep := range sortedStringKeys(all) {
		section := "devDependencies"
		if _, ok := p.pkg.Dependencies[dep]; ok {
			section = "dependencies"
		}
		ev := evidence(p.data, p.file, jsonDepLine(p.data, section, dep))
		if target, ok := byName[dep]; ok && target != p.dir {
			u.DependsOn = append(u.DependsOn, discovery.Dependency{Unit: discovery.UnitID(discovery.Node, target), Evidence: ev})
			continue
		}
		if fw, ok := nodeFrameworks[dep]; ok {
			u.Frameworks = addFramework(u.Frameworks, discovery.Framework{Name: fw.name, Kind: fw.kind, Confidence: discovery.Likely, Evidence: ev})
		}
		if dep == "vite" && repo.Has(join(p.dir, "index.html")) {
			u.Frameworks = addFramework(u.Frameworks, discovery.Framework{Name: "vite", Kind: discovery.Static, Confidence: discovery.Likely, Evidence: ev})
		}
		if section != "dependencies" {
			continue // dev tooling doesn't run in production
		}
		if kind, ok := nodeResources[dep]; ok {
			u.Resources = addResource(u.Resources, discovery.ResourceUse{Kind: kind, Confidence: discovery.Possible, Evidence: ev})
		}
		if dep == "@prisma/client" {
			for _, res := range prismaResources(repo, r, p) {
				u.Resources = addResource(u.Resources, res)
			}
		}
	}
	if len(p.pkg.Bin) > 0 && string(p.pkg.Bin) != "null" {
		u.Frameworks = addFramework(u.Frameworks, discovery.Framework{Name: "bin", Kind: discovery.CLI, Confidence: discovery.Likely,
			Evidence: evidence(p.data, p.file, lineOf(p.data, regexp.MustCompile(`"bin"\s*:`), 1))})
	}
	nodeRefine(repo, p, &u)
	nodeAppEvidence(repo, p, &u)
	sort.Slice(u.DependsOn, func(i, j int) bool { return u.DependsOn[i].Unit < u.DependsOn[j].Unit })
	sortFrameworks(u.Frameworks)
	sortResources(u.Resources)
	return u
}

var nextExport = regexp.MustCompile(`output\s*:\s*["']export["']`)

// nodeRefine settles frameworks whose kind depends on configuration: a
// Next.js app with output "export" is static, SvelteKit with the static
// adapter is static, Astro with a server adapter isn't.
func nodeRefine(repo discovery.Repo, p *nodePackage, u *discovery.Unit) {
	for i, fw := range u.Frameworks {
		switch fw.Name {
		case "next":
			for _, cfg := range []string{"next.config.js", "next.config.mjs", "next.config.ts", "next.config.cjs"} {
				name := join(p.dir, cfg)
				if !repo.Has(name) {
					continue
				}
				data, n := read(repo, name)
				if n != nil {
					continue
				}
				if l := lineOf(data, nextExport, 1); l > 0 {
					u.Frameworks[i].Kind = discovery.Static
					u.Frameworks[i].Evidence = evidence(data, name, l)
				}
			}
		case "sveltekit":
			if _, ok := p.pkg.DevDependencies["@sveltejs/adapter-static"]; ok {
				u.Frameworks[i].Kind = discovery.Static
				u.Frameworks[i].Evidence = evidence(p.data, p.file, jsonDepLine(p.data, "devDependencies", "@sveltejs/adapter-static"))
			}
		case "astro":
			for _, adapter := range []string{"@astrojs/node", "@astrojs/vercel", "@astrojs/netlify", "@astrojs/cloudflare"} {
				if _, ok := p.pkg.Dependencies[adapter]; ok {
					u.Frameworks[i].Kind = discovery.Web
					u.Frameworks[i].Evidence = evidence(p.data, p.file, jsonDepLine(p.data, "dependencies", adapter))
				}
			}
		}
	}
}

// nodeAppConfigs are the config files an app built on a framework has. A
// shared package that only uses the framework's APIs (a component library
// on Next.js) has none.
var nodeAppConfigs = map[string][]string{
	"next":           {"next.config.js", "next.config.mjs", "next.config.ts", "next.config.cjs"},
	"nuxt":           {"nuxt.config.ts", "nuxt.config.js", "nuxt.config.mjs"},
	"remix":          {"remix.config.js", "vite.config.ts", "vite.config.js"},
	"react-router":   {"react-router.config.ts", "react-router.config.js"},
	"sveltekit":      {"svelte.config.js", "svelte.config.ts"},
	"astro":          {"astro.config.mjs", "astro.config.ts", "astro.config.js"},
	"gatsby":         {"gatsby-config.js", "gatsby-config.ts", "gatsby-config.mjs"},
	"angular":        {"angular.json"},
	"vue-cli":        {"vue.config.js"},
	"docusaurus":     {"docusaurus.config.js", "docusaurus.config.ts"},
	"tanstack-start": {"app.config.ts", "vite.config.ts"},
	"solid-start":    {"app.config.ts", "vite.config.ts"},
	"qwik-city":      {"vite.config.ts", "vite.config.js"},
	"expo":           {"app.json", "app.config.ts", "app.config.js"},
	"react-native":   {"app.json", "metro.config.js"},
	"electron":       {"electron-builder.yml", "forge.config.js"},
}

// nodeAppCommands are the CLIs an app's scripts run.
var nodeAppCommands = map[string]string{
	"next": "next", "nuxt": "nuxt", "remix": "remix", "react-router": "react-router", "sveltekit": "vite",
	"astro": "astro", "gatsby": "gatsby", "create-react-app": "react-scripts", "angular": "ng",
	"vue-cli": "vue-cli-service", "docusaurus": "docusaurus", "vitepress": "vitepress",
	"tanstack-start": "vinxi", "solid-start": "vinxi", "qwik-city": "vite",
	"expo": "expo", "react-native": "react-native", "electron": "electron",
}

// nodeAppEvidence keeps a framework likely only when the package is an app
// built on it: it has the framework's config file, or a script that runs
// its CLI. A server framework needs a start or dev script. Anything else
// drops to possible, which doesn't make the package a service.
func nodeAppEvidence(repo discovery.Repo, p *nodePackage, u *discovery.Unit) {
	for i := range u.Frameworks {
		fw := &u.Frameworks[i]
		switch fw.Kind {
		case discovery.Web, discovery.Static, discovery.Native:
			if fw.Name == "vite" {
				continue // already required an index.html
			}
			if ev, ok := nodeAppConfig(repo, p, fw.Name); ok {
				fw.Evidence = ev
				continue
			}
			if ev, ok := nodeScriptRuns(p, nodeAppCommands[fw.Name]); ok {
				fw.Evidence = ev
				continue
			}
			fw.Confidence = discovery.Possible
		case discovery.HTTP, discovery.GRPC:
			if u.Commands.Start == "" && u.Commands.Dev == "" {
				fw.Confidence = discovery.Possible
			}
		case discovery.CLI:
		}
	}
}

func nodeAppConfig(repo discovery.Repo, p *nodePackage, framework string) (discovery.Evidence, bool) {
	for _, cfg := range nodeAppConfigs[framework] {
		if name := join(p.dir, cfg); repo.Has(name) {
			return discovery.Evidence{Path: name, Detail: cfg}, true
		}
	}
	return discovery.Evidence{}, false
}

func nodeScriptRuns(p *nodePackage, command string) (discovery.Evidence, bool) {
	if command == "" {
		return discovery.Evidence{}, false
	}
	re := regexp.MustCompile(`(^|[\s;&|(])` + regexp.QuoteMeta(command) + `($|\s)`)
	for _, name := range sortedStringKeys(p.pkg.Scripts) {
		if re.MatchString(p.pkg.Scripts[name]) {
			l := lineOf(p.data, regexp.MustCompile(`"`+regexp.QuoteMeta(name)+`"\s*:`), lineOf(p.data, regexp.MustCompile(`"scripts"\s*:`), 1))
			return evidence(p.data, p.file, l), true
		}
	}
	return discovery.Evidence{}, false
}

var prismaProvider = regexp.MustCompile(`^\s*provider\s*=\s*"([a-z]+)"`)

// prismaResources reads the datasource provider from the package's Prisma
// schema.
func prismaResources(repo discovery.Repo, r *discovery.Report, p *nodePackage) []discovery.ResourceUse {
	candidates := []string{"prisma/schema.prisma", "schema.prisma"}
	if p.pkg.Prisma.Schema != "" {
		candidates = append([]string{p.pkg.Prisma.Schema}, candidates...)
	}
	for _, c := range candidates {
		name := join(p.dir, c)
		if !repo.Has(name) {
			continue
		}
		data, n := read(repo, name)
		if n != nil {
			note(r, n)
			return nil
		}
		from := lineOf(data, regexp.MustCompile(`^\s*datasource\s+\w+\s*\{`), 1)
		if from == 0 {
			return nil
		}
		l := lineOf(data, prismaProvider, from)
		if l == 0 {
			return nil
		}
		m := prismaProvider.FindStringSubmatch(lineText(data, l))
		if kind, ok := prismaProviders[m[1]]; ok {
			return []discovery.ResourceUse{{Kind: kind, Confidence: discovery.Likely, Evidence: evidence(data, name, l)}}
		}
		return nil
	}
	return nil
}

// nodeEntrypoint gives a package that builds something deployable one
// entrypoint: the package itself.
func nodeEntrypoint(u *discovery.Unit) {
	runnable := u.Commands.Start != ""
	for _, fw := range u.Frameworks {
		if fw.Confidence.Rank() >= discovery.Likely.Rank() {
			runnable = true
		}
	}
	if !runnable {
		return
	}
	ev := u.Evidence
	if u.Commands.Start != "" {
		ev = append(ev, discovery.Evidence{Path: join(u.Path, "package.json"), Detail: "start: " + u.Commands.Start})
	}
	u.Entrypoints = []discovery.Entrypoint{{
		Name: path.Base(u.Name), Path: u.Path, Frameworks: u.Frameworks, Resources: u.Resources, Evidence: ev,
	}}
}

type nxProject struct {
	name      string
	ev        discovery.Evidence
	executors []discovery.Evidence
	kinds     []nodeFramework
}

// nxExecutors maps Nx executor prefixes to frameworks.
var nxExecutors = []struct {
	prefix string
	fw     nodeFramework
}{
	{"@nx/next:", nodeFramework{"next", discovery.Web}},
	{"@nx/remix:", nodeFramework{"remix", discovery.Web}},
	{"@nx/angular:", nodeFramework{"angular", discovery.Static}},
	{"@nx/vite:", nodeFramework{"vite", discovery.Static}},
	{"@nx/webpack:", nodeFramework{"webpack", discovery.Static}},
	{"@nx/node:", nodeFramework{"node", discovery.HTTP}},
	{"@nx/js:node", nodeFramework{"node", discovery.HTTP}},
	{"@nx/expo:", nodeFramework{"expo", discovery.Native}},
	{"@nx/react-native:", nodeFramework{"react-native", discovery.Native}},
}

var nxExecutorLine = regexp.MustCompile(`"executor"\s*:\s*"([^"]+)"`)

func nxProjects(repo discovery.Repo, r *discovery.Report) map[string]*nxProject {
	out := map[string]*nxProject{}
	for _, f := range repo.Named("project.json") {
		data, n := read(repo, f)
		if n != nil {
			note(r, n)
			continue
		}
		var pj struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &pj) != nil {
			continue
		}
		p := &nxProject{name: pj.Name, ev: discovery.Evidence{Path: f, Detail: "project.json"}}
		if p.name == "" {
			p.name = path.Base(dir(f))
		}
		for line := lineOf(data, nxExecutorLine, 1); line > 0; line = lineOf(data, nxExecutorLine, line+1) {
			exec := nxExecutorLine.FindStringSubmatch(lineText(data, line))[1]
			for _, x := range nxExecutors {
				if strings.HasPrefix(exec, x.prefix) {
					p.kinds = append(p.kinds, x.fw)
					p.executors = append(p.executors, evidence(data, f, line))
				}
			}
		}
		out[dir(f)] = p
	}
	return out
}

func nxApply(u *discovery.Unit, nx *nxProject) {
	for i, fw := range nx.kinds {
		u.Frameworks = addFramework(u.Frameworks, discovery.Framework{
			Name: fw.name, Kind: fw.kind, Confidence: discovery.Likely, Evidence: nx.executors[i],
		})
	}
	sortFrameworks(u.Frameworks)
}

func sortedProjectDirs(m map[string]*nxProject) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
