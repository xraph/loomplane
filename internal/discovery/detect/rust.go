package detect

import (
	"context"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/xraph/loomplane/pkg/discovery"
)

// Rust finds Cargo workspaces and crates, each crate's binaries, and what
// its dependencies say it serves and talks to.
type Rust struct{}

// Name implements discovery.Detector.
func (Rust) Name() string { return "rust" }

type cargoManifest struct {
	Workspace *struct {
		Members      []string       `toml:"members"`
		Exclude      []string       `toml:"exclude"`
		Dependencies map[string]any `toml:"dependencies"`
	} `toml:"workspace"`
	Package *struct {
		Name        string `toml:"name"`
		Edition     any    `toml:"edition"`
		RustVersion any    `toml:"rust-version"`
		AutoBins    *bool  `toml:"autobins"`
	} `toml:"package"`
	Lib *struct {
		Name string `toml:"name"`
	} `toml:"lib"`
	Bin []struct {
		Name string `toml:"name"`
		Path string `toml:"path"`
	} `toml:"bin"`
	Dependencies map[string]any `toml:"dependencies"`
}

type crate struct {
	dir, file string
	data      []byte
	m         cargoManifest
}

// cargoFrameworks maps crate names to frameworks.
var cargoFrameworks = map[string]struct {
	name string
	kind discovery.FrameworkKind
}{
	"axum":      {"axum", discovery.HTTP},
	"actix-web": {"actix-web", discovery.HTTP},
	"rocket":    {"rocket", discovery.HTTP},
	"warp":      {"warp", discovery.HTTP},
	"poem":      {"poem", discovery.HTTP},
	"tide":      {"tide", discovery.HTTP},
	"salvo":     {"salvo", discovery.HTTP},
	"loco-rs":   {"loco", discovery.HTTP},
	"tonic":     {"tonic", discovery.GRPC},
	"leptos":    {"leptos", discovery.Web},
	"dioxus":    {"dioxus", discovery.Web},
	"yew":       {"yew", discovery.Static},
	"clap":      {"clap", discovery.CLI},
	"structopt": {"structopt", discovery.CLI},
	"argh":      {"argh", discovery.CLI},
	"tauri":     {"tauri", discovery.Native},
}

// cargoResources maps crate names to backing services. Crates that speak
// several databases (sqlx, diesel, sea-orm) are decided by their features.
var cargoResources = map[string]string{
	"tokio-postgres":    "postgres",
	"postgres":          "postgres",
	"deadpool-postgres": "postgres",
	"bb8-postgres":      "postgres",
	"redis":             "redis",
	"deadpool-redis":    "redis",
	"bb8-redis":         "redis",
	"fred":              "redis",
	"mongodb":           "mongodb",
	"lapin":             "rabbitmq",
	"rdkafka":           "kafka",
	"async-nats":        "nats",
	"nats":              "nats",
	"aws-sdk-s3":        "object-storage",
	"rusoto_s3":         "object-storage",
	"object_store":      "object-storage",
	"elasticsearch":     "search",
	"opensearch":        "search",
}

var cargoFeatureResources = []struct{ feature, kind string }{
	{"postgres", "postgres"},
	{"mysql", "mysql"},
}

// Detect implements discovery.Detector.
func (Rust) Detect(ctx context.Context, repo discovery.Repo, r *discovery.Report) error {
	var crates, workspaces []*crate
	for _, f := range repo.Named("Cargo.toml") {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, n := read(repo, f)
		if n != nil {
			note(r, n)
			continue
		}
		c := &crate{dir: dir(f), file: f, data: data}
		if _, err := toml.Decode(string(data), &c.m); err != nil {
			note(r, &discovery.Note{Path: f, Message: "not a valid Cargo.toml"})
			continue
		}
		if c.m.Workspace != nil {
			workspaces = append(workspaces, c)
		}
		if c.m.Package != nil {
			crates = append(crates, c)
		}
	}
	if len(crates) == 0 {
		return nil
	}
	crateDirs := make([]string, len(crates))
	byDir := make(map[string]*crate, len(crates))
	for i, c := range crates {
		crateDirs[i] = c.dir
		byDir[c.dir] = c
	}
	sort.Strings(crateDirs)

	wsOf := map[string]*crate{}
	for _, ws := range workspaces {
		members := cargoMembers(ws, crateDirs)
		w := discovery.Workspace{Ecosystem: discovery.Rust, Tool: "cargo", Path: ws.dir, Members: members,
			Evidence: []discovery.Evidence{evidence(ws.data, ws.file, lineOf(ws.data, cargoSection("workspace"), 1))}}
		r.Workspaces = append(r.Workspaces, w)
		for _, m := range members {
			wsOf[m] = ws
		}
	}

	for _, d := range crateDirs {
		r.Units = append(r.Units, rustUnit(repo, byDir[d], wsOf[d], byDir))
	}
	return nil
}

func cargoMembers(ws *crate, crateDirs []string) []string {
	rel := func(patterns []string) []string {
		out := make([]string, 0, len(patterns))
		for _, p := range patterns {
			out = append(out, join(ws.dir, p))
		}
		return out
	}
	var members []string
	excluded := rel(ws.m.Workspace.Exclude)
	for _, d := range expand(rel(ws.m.Workspace.Members), crateDirs) {
		skip := false
		for _, x := range excluded {
			if matchGlob(x, d) {
				skip = true
			}
		}
		if !skip {
			members = append(members, d)
		}
	}
	// A crate at the workspace root is a member too.
	for _, d := range crateDirs {
		if d == ws.dir && !contains(members, d) {
			members = append(members, d)
		}
	}
	sort.Strings(members)
	return members
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func cargoSection(name string) *regexp.Regexp {
	return regexp.MustCompile(`^\s*\[` + regexp.QuoteMeta(name) + `\]\s*(#.*)?$`)
}

// cargoDepLine finds the line declaring dependency name.
func cargoDepLine(data []byte, name string) int {
	q := regexp.QuoteMeta(name)
	if l := lineOf(data, regexp.MustCompile(`^\s*\[dependencies\.`+q+`\]`), 1); l > 0 {
		return l
	}
	from := lineOf(data, cargoSection("dependencies"), 1)
	if from == 0 {
		return 0
	}
	return lineOf(data, regexp.MustCompile(`^\s*"?`+q+`"?\s*=`), from)
}

func rustUnit(repo discovery.Repo, c, ws *crate, byDir map[string]*crate) discovery.Unit {
	pkg := c.m.Package
	u := discovery.Unit{
		ID:        discovery.UnitID(discovery.Rust, c.dir),
		Ecosystem: discovery.Rust,
		Name:      pkg.Name,
		Path:      c.dir,
		Evidence:  []discovery.Evidence{evidence(c.data, c.file, lineOf(c.data, cargoSection("package"), 1))},
	}
	if v, ok := pkg.RustVersion.(string); ok && v != "" {
		u.Runtime = "rust " + v
	} else if e, ok := pkg.Edition.(string); ok && e != "" {
		u.Runtime = "rust " + e + " edition"
	}

	for _, name := range sortedKeys(c.m.Dependencies) {
		spec := c.m.Dependencies[name]
		pathDep, features := cargoSpec(spec)
		if inherits(spec) && ws != nil && ws.m.Workspace != nil {
			wsPath, wsFeatures := cargoSpec(ws.m.Workspace.Dependencies[name])
			if wsPath != "" {
				pathDep = join(ws.dir, wsPath)
			}
			features = append(features, wsFeatures...)
		} else if pathDep != "" {
			pathDep = join(c.dir, pathDep)
		}
		ev := evidence(c.data, c.file, cargoDepLine(c.data, name))
		if pathDep != "" {
			if _, ok := byDir[pathDep]; ok && pathDep != c.dir {
				u.DependsOn = append(u.DependsOn, discovery.Dependency{Unit: discovery.UnitID(discovery.Rust, pathDep), Evidence: ev})
			}
			continue
		}
		remote := spec
		if inherits(spec) && ws != nil && ws.m.Workspace != nil {
			remote = ws.m.Workspace.Dependencies[name]
		}
		version, git := cargoRemote(remote)
		u.Requires = append(u.Requires, discovery.Requirement{Name: name, Version: version, Git: git, Evidence: ev})
		if fw, ok := cargoFrameworks[name]; ok {
			u.Frameworks = addFramework(u.Frameworks, discovery.Framework{Name: fw.name, Kind: fw.kind, Confidence: discovery.Likely, Evidence: ev})
		}
		if kind, ok := cargoResources[name]; ok {
			u.Resources = addResource(u.Resources, discovery.ResourceUse{Kind: kind, Confidence: discovery.Possible, Evidence: ev})
		}
		if name == "sqlx" || name == "diesel" || name == "sea-orm" {
			for _, f := range features {
				for _, fr := range cargoFeatureResources {
					if strings.Contains(f, fr.feature) {
						u.Resources = addResource(u.Resources, discovery.ResourceUse{Kind: fr.kind, Confidence: discovery.Possible, Evidence: ev})
					}
				}
			}
		}
	}
	sort.Slice(u.DependsOn, func(i, j int) bool { return u.DependsOn[i].Unit < u.DependsOn[j].Unit })
	sortFrameworks(u.Frameworks)
	sortResources(u.Resources)

	// Rust dependencies belong to the crate, so every binary shares them.
	// A binary that names a framework crate in its own source keeps it as
	// likely, and so does one that calls the crate's own library, which is
	// where the framework is used (a Tauri app's main.rs is one line:
	// app_lib::run()). One that does neither (a backfill tool beside an
	// axum server) only possibly uses it.
	lib := rustLibName(repo, c)
	bins := rustBins(repo, c)
	via := make([]map[string]rustUse, 0, len(bins))
	for _, bin := range bins {
		src, _ := read(repo, bin.Path)
		libLine := 0
		if lib != "" {
			libLine = lineOf(src, regexp.MustCompile(`\b`+regexp.QuoteMeta(lib)+`::`), 1)
		}
		uses := map[string]rustUse{}
		for _, fw := range u.Frameworks {
			use := regexp.MustCompile(`\b` + regexp.QuoteMeta(strings.ReplaceAll(cargoCrateOf(fw.Name), "-", "_")) + `::`)
			switch l := lineOf(src, use, 1); {
			case l > 0:
				fw.Evidence = evidence(src, bin.Path, l)
				uses[fw.Name] = rustNamed
			case libLine > 0:
				fw.Evidence = evidence(src, bin.Path, libLine)
				uses[fw.Name] = rustThroughLib
			default:
				fw.Confidence = discovery.Possible
			}
			bin.Frameworks = append(bin.Frameworks, fw)
		}
		bin.Resources = u.Resources
		u.Entrypoints = append(u.Entrypoints, bin)
		via = append(via, uses)
	}
	rustSharedServers(u.Entrypoints, via)
	return u
}

// rustUse is how a binary reaches a framework: by naming its crate, or by
// calling the crate's own library, which names it.
type rustUse int

const (
	rustNamed rustUse = iota + 1
	rustThroughLib
)

// rustSharedServers lowers to possible a server framework a binary
// reaches only through its crate's library when the binary names a CLI
// crate itself and another binary reaches the same server. That's an admin
// tool beside a server that shares its library (clap in src/bin/admin.rs,
// axum in lib.rs), not a second server. A crate with one binary keeps it:
// main.rs parsing flags with clap and then calling lib::serve is a server.
func rustSharedServers(bins []discovery.Entrypoint, via []map[string]rustUse) {
	server := func(k discovery.FrameworkKind) bool {
		return k == discovery.HTTP || k == discovery.GRPC || k == discovery.Web
	}
	for i := range bins {
		cli := false
		for _, fw := range bins[i].Frameworks {
			if fw.Kind == discovery.CLI && via[i][fw.Name] == rustNamed {
				cli = true
			}
		}
		if !cli {
			continue
		}
		for j := range bins[i].Frameworks {
			fw := &bins[i].Frameworks[j]
			if !server(fw.Kind) || via[i][fw.Name] != rustThroughLib {
				continue
			}
			for k := range bins {
				if k != i && via[k][fw.Name] != 0 {
					fw.Confidence = discovery.Possible
					break
				}
			}
		}
	}
}

// rustLibName is the name a crate's library is used by in Rust code, or ""
// when the crate has no library.
func rustLibName(repo discovery.Repo, c *crate) string {
	if c.m.Lib == nil && !repo.Has(join(c.dir, "src/lib.rs")) {
		return ""
	}
	name := c.m.Package.Name
	if c.m.Lib != nil && c.m.Lib.Name != "" {
		name = c.m.Lib.Name
	}
	return strings.ReplaceAll(name, "-", "_")
}

// cargoCrateOf is the crate a framework name came from.
func cargoCrateOf(framework string) string {
	for crateName, fw := range cargoFrameworks {
		if fw.name == framework {
			return crateName
		}
	}
	return framework
}

// rustBins lists the crate's binaries the way Cargo finds them: [[bin]]
// tables, then src/main.rs and src/bin/ unless autobins is off.
func rustBins(repo discovery.Repo, c *crate) []discovery.Entrypoint {
	var bins []discovery.Entrypoint
	seen := map[string]bool{}
	add := func(name, file string, ev discovery.Evidence) {
		if seen[file] || !repo.Has(file) {
			return
		}
		seen[file] = true
		bins = append(bins, discovery.Entrypoint{Name: name, Path: file, Evidence: []discovery.Evidence{ev}})
	}
	binLine := lineOf(c.data, regexp.MustCompile(`^\s*\[\[bin\]\]`), 1)
	for _, b := range c.m.Bin {
		name := b.Name
		if name == "" {
			name = c.m.Package.Name
		}
		file := b.Path
		if file == "" {
			file = "src/main.rs"
			if name != c.m.Package.Name {
				file = "src/bin/" + name + ".rs"
			}
		}
		add(name, join(c.dir, file), evidence(c.data, c.file, binLine))
	}
	if c.m.Package.AutoBins != nil && !*c.m.Package.AutoBins {
		return bins
	}
	mainRS := join(c.dir, "src/main.rs")
	add(c.m.Package.Name, mainRS, discovery.Evidence{Path: mainRS, Detail: "src/main.rs"})
	binDir := join(c.dir, "src/bin")
	for _, f := range repo.Files {
		if !within(f, binDir) || !strings.HasSuffix(f, ".rs") {
			continue
		}
		rest := strings.TrimPrefix(f, binDir+"/")
		switch {
		case !strings.Contains(rest, "/"):
			add(strings.TrimSuffix(rest, ".rs"), f, discovery.Evidence{Path: f, Detail: "src/bin/" + rest})
		case strings.Count(rest, "/") == 1 && path.Base(rest) == "main.rs":
			add(path.Dir(rest), f, discovery.Evidence{Path: f, Detail: "src/bin/" + rest})
		}
	}
	sort.Slice(bins, func(i, j int) bool { return bins[i].Name < bins[j].Name })
	return bins
}

// cargoSpec reads a dependency spec: its path, if it's a path dependency,
// and its features.
func cargoSpec(spec any) (depPath string, features []string) {
	t, ok := spec.(map[string]any)
	if !ok {
		return "", nil
	}
	if p, ok := t["path"].(string); ok {
		depPath = p
	}
	if fs, ok := t["features"].([]any); ok {
		for _, f := range fs {
			if s, ok := f.(string); ok {
				features = append(features, s)
			}
		}
	}
	return depPath, features
}

// cargoRemote reads where a non-path dependency comes from: a version, a
// git repository, or both.
func cargoRemote(spec any) (version, git string) {
	switch s := spec.(type) {
	case string:
		return s, ""
	case map[string]any:
		if v, ok := s["version"].(string); ok {
			version = v
		}
		if g, ok := s["git"].(string); ok {
			git = g
		}
	}
	return version, git
}

// inherits reports whether a dependency is `{ workspace = true }`.
func inherits(spec any) bool {
	t, ok := spec.(map[string]any)
	if !ok {
		return false
	}
	w, ok := t["workspace"].(bool)
	return ok && w
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
