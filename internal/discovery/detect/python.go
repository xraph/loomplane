package detect

import (
	"bufio"
	"bytes"
	"context"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/xraph/loomplane/pkg/discovery"
)

// Python finds Python projects (pyproject.toml, requirements.txt or a
// Pipfile), uv workspaces, and what each project's dependencies say it
// serves and talks to. A project that runs something is one entrypoint, at
// its directory: Python has no main package to find, so a web framework, a
// Procfile, manage.py or a main module is what says it runs.
type Python struct{}

// Name implements discovery.Detector.
func (Python) Name() string { return "python" }

type pyproject struct {
	Project *struct {
		Name           string              `toml:"name"`
		RequiresPython string              `toml:"requires-python"`
		Dependencies   []string            `toml:"dependencies"`
		Scripts        map[string]string   `toml:"scripts"`
		Optional       map[string][]string `toml:"optional-dependencies"`
	} `toml:"project"`
	Tool struct {
		Poetry *struct {
			Name         string         `toml:"name"`
			Dependencies map[string]any `toml:"dependencies"`
			Scripts      map[string]any `toml:"scripts"`
		} `toml:"poetry"`
		UV *struct {
			Workspace *struct {
				Members []string `toml:"members"`
				Exclude []string `toml:"exclude"`
			} `toml:"workspace"`
			Sources map[string]map[string]any `toml:"sources"`
		} `toml:"uv"`
	} `toml:"tool"`
}

type pipfile struct {
	Packages map[string]any `toml:"packages"`
	Requires struct {
		PythonVersion string `toml:"python_version"`
	} `toml:"requires"`
}

// pyDep is one declared dependency, by its normalized name.
type pyDep struct {
	name, version string
	// local is a directory in the checkout the dependency comes from.
	local string
	// workspace says uv takes it from the workspace, by project name.
	workspace bool
	ev        discovery.Evidence
}

type pyProject struct {
	dir     string
	name    string
	runtime string
	deps    []pyDep
	scripts bool
	ev      discovery.Evidence
	members []string // a uv workspace root's members, as patterns
	exclude []string
	wsEv    discovery.Evidence
}

// pyFrameworks maps distribution names to frameworks.
var pyFrameworks = map[string]struct {
	name string
	kind discovery.FrameworkKind
}{
	"django":              {"django", discovery.HTTP},
	"djangorestframework": {"django", discovery.HTTP},
	"flask":               {"flask", discovery.HTTP},
	"fastapi":             {"fastapi", discovery.HTTP},
	"starlette":           {"starlette", discovery.HTTP},
	"litestar":            {"litestar", discovery.HTTP},
	"sanic":               {"sanic", discovery.HTTP},
	"aiohttp":             {"aiohttp", discovery.HTTP},
	"tornado":             {"tornado", discovery.HTTP},
	"falcon":              {"falcon", discovery.HTTP},
	"quart":               {"quart", discovery.HTTP},
	"bottle":              {"bottle", discovery.HTTP},
	"pyramid":             {"pyramid", discovery.HTTP},
	"streamlit":           {"streamlit", discovery.HTTP},
	"gradio":              {"gradio", discovery.HTTP},
	"dash":                {"dash", discovery.HTTP},
	"grpcio":              {"grpc", discovery.GRPC},
	"click":               {"click", discovery.CLI},
	"typer":               {"typer", discovery.CLI},
	"fire":                {"fire", discovery.CLI},
}

// pyWorkers are task queues: a project using one and no web framework is
// a worker, which is what a service with no framework already is. They
// still make it something that runs rather than a library.
var pyWorkers = map[string]bool{
	"celery": true, "rq": true, "dramatiq": true, "arq": true, "huey": true, "faust-streaming": true,
}

// pyResources maps distribution names to backing services.
var pyResources = map[string]string{
	"psycopg":                "postgres",
	"psycopg2":               "postgres",
	"psycopg2-binary":        "postgres",
	"asyncpg":                "postgres",
	"pg8000":                 "postgres",
	"mysqlclient":            "mysql",
	"pymysql":                "mysql",
	"mysql-connector-python": "mysql",
	"aiomysql":               "mysql",
	"asyncmy":                "mysql",
	"redis":                  "redis",
	"aioredis":               "redis",
	"pymongo":                "mongodb",
	"motor":                  "mongodb",
	"beanie":                 "mongodb",
	"pika":                   "rabbitmq",
	"aio-pika":               "rabbitmq",
	"kafka-python":           "kafka",
	"confluent-kafka":        "kafka",
	"aiokafka":               "kafka",
	"nats-py":                "nats",
	"google-cloud-storage":   "object-storage",
	"minio":                  "object-storage",
	"elasticsearch":          "search",
	"opensearch-py":          "search",
	"pymemcache":             "memcached",
	"python-memcached":       "memcached",
	"pylibmc":                "memcached",
	"clickhouse-connect":     "clickhouse",
	"clickhouse-driver":      "clickhouse",
}

// pyMains are the module files that, at a project's root or in its
// src directory, say it runs as a program.
var pyMains = []string{"main.py", "app.py", "wsgi.py", "asgi.py", "server.py", "manage.py", "__main__.py"}

var (
	pyNameRE      = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)`)
	pyNormalizeRE = regexp.MustCompile(`[-_.]+`)
	pyEggRE       = regexp.MustCompile(`#egg=([A-Za-z0-9][A-Za-z0-9._-]*)`)
)

// pyNormalize is a distribution name as PEP 503 compares it.
func pyNormalize(name string) string {
	return strings.ToLower(pyNormalizeRE.ReplaceAllString(name, "-"))
}

// pySplit reads a PEP 508 requirement: its name and the version spec,
// without extras or markers.
func pySplit(req string) (name, version string) {
	req, _, _ = strings.Cut(req, ";")
	m := pyNameRE.FindStringSubmatch(req)
	if m == nil {
		return "", ""
	}
	rest := strings.TrimSpace(req[len(m[0]):])
	if strings.HasPrefix(rest, "[") {
		if i := strings.Index(rest, "]"); i >= 0 {
			rest = strings.TrimSpace(rest[i+1:])
		}
	}
	if strings.HasPrefix(rest, "@") {
		rest = "" // a URL, not a version
	}
	return pyNormalize(m[1]), rest
}

// pyDepLine finds the line that declares dependency raw in a manifest.
func pyDepLine(data []byte, raw string) int {
	q := regexp.QuoteMeta(raw)
	if l := lineOf(data, regexp.MustCompile(`(?i)["']`+q+`\s*([\[<>=!~;@"' ]|$)`), 1); l > 0 {
		return l
	}
	return lineOf(data, regexp.MustCompile(`(?i)^\s*"?`+q+`"?\s*(=|[\[<>=!~;@ ]|$)`), 1)
}

// Detect implements discovery.Detector.
func (Python) Detect(ctx context.Context, repo discovery.Repo, r *discovery.Report) error {
	byDir := map[string]*pyProject{}
	get := func(d string) *pyProject {
		p := byDir[d]
		if p == nil {
			p = &pyProject{dir: d, name: path.Base(d)}
			if d == "." {
				p.name = "app"
			}
			byDir[d] = p
		}
		return p
	}
	for _, f := range repo.Named("pyproject.toml") {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, n := read(repo, f)
		if n != nil {
			note(r, n)
			continue
		}
		var m pyproject
		if _, err := toml.Decode(string(data), &m); err != nil {
			note(r, &discovery.Note{Path: f, Message: "not a valid pyproject.toml"})
			continue
		}
		readPyproject(get, f, data, &m)
	}
	for _, f := range repo.Named("requirements.txt") {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, n := read(repo, f)
		if n != nil {
			note(r, n)
			continue
		}
		p := get(dir(f))
		if p.ev.Path == "" {
			p.ev = discovery.Evidence{Path: f, Detail: "requirements.txt"}
		}
		p.deps = append(p.deps, readRequirements(f, data)...)
	}
	for _, f := range repo.Named("Pipfile") {
		data, n := read(repo, f)
		if n != nil {
			note(r, n)
			continue
		}
		var m pipfile
		if _, err := toml.Decode(string(data), &m); err != nil {
			note(r, &discovery.Note{Path: f, Message: "not a valid Pipfile"})
			continue
		}
		p := get(dir(f))
		if p.ev.Path == "" {
			p.ev = discovery.Evidence{Path: f, Detail: "Pipfile"}
		}
		if p.runtime == "" && m.Requires.PythonVersion != "" {
			p.runtime = "python " + m.Requires.PythonVersion
		}
		for _, raw := range sortedKeys(m.Packages) {
			name := pyNormalize(raw)
			var version string
			switch spec := m.Packages[raw].(type) {
			case string:
				version = spec
			case map[string]any:
				if v, ok := spec["version"].(string); ok {
					version = v
				}
			}
			if version == "*" {
				version = ""
			}
			p.deps = append(p.deps, pyDep{name: name, version: version, ev: evidence(data, f, pyDepLine(data, raw))})
		}
	}
	if len(byDir) == 0 {
		return nil
	}

	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	byName := map[string]string{}
	for _, d := range dirs {
		byName[pyNormalize(byDir[d].name)] = d
	}

	for _, d := range dirs {
		p := byDir[d]
		if p.members == nil {
			continue
		}
		var rel, excluded []string
		for _, m := range p.members {
			rel = append(rel, join(d, m))
		}
		for _, x := range p.exclude {
			excluded = append(excluded, join(d, x))
		}
		var members []string
		for _, m := range expand(rel, dirs) {
			if m == d || slicesAny(excluded, func(x string) bool { return matchGlob(x, m) }) {
				continue
			}
			members = append(members, m)
		}
		r.Workspaces = append(r.Workspaces, discovery.Workspace{
			Ecosystem: discovery.Python, Tool: "uv", Path: d, Members: members,
			Evidence: []discovery.Evidence{p.wsEv},
		})
	}

	for _, d := range dirs {
		if u, ok := pythonUnit(repo, byDir[d], byDir, byName); ok {
			r.Units = append(r.Units, u)
		}
	}
	return nil
}

func slicesAny(list []string, f func(string) bool) bool {
	for _, x := range list {
		if f(x) {
			return true
		}
	}
	return false
}

func readPyproject(get func(string) *pyProject, f string, data []byte, m *pyproject) {
	d := dir(f)
	uv := m.Tool.UV
	if m.Project == nil && m.Tool.Poetry == nil {
		if uv != nil && uv.Workspace != nil {
			// A virtual workspace root: it holds projects, and is none.
			p := get(d)
			p.members, p.exclude = uv.Workspace.Members, uv.Workspace.Exclude
			p.wsEv = evidence(data, f, lineOf(data, regexp.MustCompile(`^\s*\[tool\.uv\.workspace\]`), 1))
		}
		return
	}
	p := get(d)
	if uv != nil && uv.Workspace != nil {
		p.members, p.exclude = uv.Workspace.Members, uv.Workspace.Exclude
		p.wsEv = evidence(data, f, lineOf(data, regexp.MustCompile(`^\s*\[tool\.uv\.workspace\]`), 1))
	}
	sources := map[string]map[string]any{}
	if uv != nil {
		for k, v := range uv.Sources {
			sources[pyNormalize(k)] = v
		}
	}
	local := func(spec map[string]any) (string, bool) {
		if w, ok := spec["workspace"].(bool); ok && w {
			return "", true
		}
		if pth, ok := spec["path"].(string); ok {
			return join(d, pth), false
		}
		return "", false
	}

	if pr := m.Project; pr != nil {
		if pr.Name != "" {
			p.name = pr.Name
		}
		if pr.RequiresPython != "" {
			p.runtime = "python " + pr.RequiresPython
		}
		p.ev = evidence(data, f, lineOf(data, regexp.MustCompile(`^\s*\[project\]`), 1))
		p.scripts = p.scripts || len(pr.Scripts) > 0
		for _, req := range pr.Dependencies {
			name, version := pySplit(req)
			if name == "" {
				continue
			}
			raw := pyNameRE.FindStringSubmatch(req)[1]
			dep := pyDep{name: name, version: version, ev: evidence(data, f, pyDepLine(data, raw))}
			if spec, ok := sources[name]; ok {
				dep.local, dep.workspace = local(spec)
			}
			p.deps = append(p.deps, dep)
		}
	}
	if po := m.Tool.Poetry; po != nil {
		if p.name == path.Base(d) && po.Name != "" {
			p.name = po.Name
		}
		if p.ev.Path == "" {
			p.ev = evidence(data, f, lineOf(data, regexp.MustCompile(`^\s*\[tool\.poetry\]`), 1))
		}
		p.scripts = p.scripts || len(po.Scripts) > 0
		for _, raw := range sortedKeys(po.Dependencies) {
			name := pyNormalize(raw)
			spec := po.Dependencies[raw]
			if name == "python" {
				if v, ok := spec.(string); ok && p.runtime == "" {
					p.runtime = "python " + v
				}
				continue
			}
			dep := pyDep{name: name, ev: evidence(data, f, pyDepLine(data, raw))}
			switch s := spec.(type) {
			case string:
				dep.version = s
			case map[string]any:
				if v, ok := s["version"].(string); ok {
					dep.version = v
				}
				if pth, ok := s["path"].(string); ok {
					dep.local = join(d, pth)
				}
			}
			p.deps = append(p.deps, dep)
		}
	}
}

// readRequirements reads a requirements file. Editable installs of a
// directory (-e ../lib) are local dependencies; options and includes are
// skipped.
func readRequirements(f string, data []byte) []pyDep {
	var deps []pyDep
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), maxManifestBytes)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if i := strings.Index(text, " #"); i >= 0 {
			text = strings.TrimSpace(text[:i])
		}
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		ev := discovery.Evidence{Path: f, Line: line, Detail: strings.TrimSpace(sc.Text())}
		if rest, ok := strings.CutPrefix(text, "-e "); ok {
			rest = strings.TrimSpace(rest)
			if strings.HasPrefix(rest, ".") || strings.HasPrefix(rest, "/") {
				deps = append(deps, pyDep{name: pyNormalize(path.Base(rest)), local: join(dir(f), rest), ev: ev})
				continue
			}
			text = rest
		}
		if strings.HasPrefix(text, "-") {
			continue
		}
		if strings.Contains(text, "://") {
			if m := pyEggRE.FindStringSubmatch(text); m != nil {
				deps = append(deps, pyDep{name: pyNormalize(m[1]), ev: ev})
			}
			continue
		}
		if name, version := pySplit(text); name != "" {
			deps = append(deps, pyDep{name: name, version: version, ev: ev})
		}
	}
	return deps
}

func pythonUnit(repo discovery.Repo, p *pyProject, byDir map[string]*pyProject, byName map[string]string) (discovery.Unit, bool) {
	if p.ev.Path == "" {
		return discovery.Unit{}, false // a virtual workspace root
	}
	u := discovery.Unit{
		ID: discovery.UnitID(discovery.Python, p.dir), Ecosystem: discovery.Python,
		Name: p.name, Path: p.dir, Runtime: p.runtime,
		Evidence: []discovery.Evidence{p.ev},
	}
	if u.Runtime == "" {
		for _, f := range []string{".python-version", "runtime.txt"} {
			if data, n := read(repo, join(p.dir, f)); n == nil {
				if v := strings.TrimPrefix(strings.TrimSpace(string(data)), "python-"); v != "" && !strings.Contains(v, "\n") {
					u.Runtime = "python " + v
					break
				}
			}
		}
	}

	runs := p.scripts
	seen := map[string]bool{}
	for _, dep := range p.deps {
		if seen[dep.name] {
			continue
		}
		seen[dep.name] = true
		target := dep.local
		if dep.workspace {
			target = byName[dep.name]
		}
		if target != "" {
			if _, ok := byDir[target]; ok && target != p.dir {
				u.DependsOn = append(u.DependsOn, discovery.Dependency{Unit: discovery.UnitID(discovery.Python, target), Evidence: dep.ev})
			}
			continue
		}
		u.Requires = append(u.Requires, discovery.Requirement{Name: dep.name, Version: dep.version, Evidence: dep.ev})
		if fw, ok := pyFrameworks[dep.name]; ok {
			u.Frameworks = addFramework(u.Frameworks, discovery.Framework{Name: fw.name, Kind: fw.kind, Confidence: discovery.Likely, Evidence: dep.ev})
			runs = runs || fw.kind != discovery.CLI
		}
		if pyWorkers[dep.name] {
			runs = true
		}
		if kind, ok := pyResources[dep.name]; ok {
			u.Resources = addResource(u.Resources, discovery.ResourceUse{Kind: kind, Confidence: discovery.Possible, Evidence: dep.ev})
		}
	}
	sort.Slice(u.DependsOn, func(i, j int) bool { return u.DependsOn[i].Unit < u.DependsOn[j].Unit })
	sortFrameworks(u.Frameworks)
	sortResources(u.Resources)

	ep := discovery.Entrypoint{Name: p.name, Path: p.dir, Evidence: []discovery.Evidence{p.ev}}
	procfile := join(p.dir, "Procfile")
	if data, n := read(repo, procfile); n == nil {
		if l := lineOf(data, regexp.MustCompile(`^web:`), 1); l > 0 {
			u.Commands.Start = strings.TrimSpace(strings.TrimPrefix(lineText(data, l), "web:"))
			ep.Evidence = append(ep.Evidence, evidence(data, procfile, l))
			runs = true
		}
	}
	for _, m := range pyMains {
		for _, d := range []string{p.dir, join(p.dir, "src")} {
			if f := join(d, m); repo.Has(f) {
				ep.Evidence = append(ep.Evidence, discovery.Evidence{Path: f, Detail: m})
				runs = true
			}
		}
	}
	if !runs {
		return u, true // a library
	}
	ep.Frameworks = u.Frameworks
	ep.Resources = u.Resources
	u.Entrypoints = []discovery.Entrypoint{ep}
	return u, true
}
