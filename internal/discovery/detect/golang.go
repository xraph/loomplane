package detect

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/xraph/loomplane/pkg/discovery"
)

// maxGoFileBytes caps a Go source file the detector parses. Bigger files
// are almost always generated, and never hold a main function worth
// finding.
const maxGoFileBytes = 1 << 20

// Go finds Go modules, the go.work that ties them together, each main
// package, and what each main package reaches: frameworks and drivers.
type Go struct{}

// Name implements discovery.Detector.
func (Go) Name() string { return "go" }

type goModule struct {
	dir  string
	path string
	file string
	data []byte
	mod  *modfile.File
}

type goImport struct {
	path string
	ev   discovery.Evidence
}

type goPackage struct {
	dir      string
	module   *goModule
	isMain   bool
	mainEv   discovery.Evidence
	imports  []goImport
	serverEv *discovery.Evidence
}

// goServeCall matches a net/http server being started. An import of
// net/http alone proves nothing: clients import it too.
var goServeCall = regexp.MustCompile(`\.ListenAndServe(TLS)?\(|http\.Server\{|http\.Serve\(`)

// goFrameworks maps import path prefixes to frameworks.
var goFrameworks = []struct {
	prefix, name string
	kind         discovery.FrameworkKind
}{
	{"github.com/xraph/forge", "forge", discovery.HTTP},
	{"github.com/gin-gonic/gin", "gin", discovery.HTTP},
	{"github.com/labstack/echo", "echo", discovery.HTTP},
	{"github.com/gofiber/fiber", "fiber", discovery.HTTP},
	{"github.com/go-chi/chi", "chi", discovery.HTTP},
	{"github.com/gorilla/mux", "gorilla/mux", discovery.HTTP},
	{"github.com/julienschmidt/httprouter", "httprouter", discovery.HTTP},
	{"github.com/danielgtaylor/huma", "huma", discovery.HTTP},
	{"connectrpc.com/connect", "connect", discovery.GRPC},
	{"google.golang.org/grpc", "grpc", discovery.GRPC},
	{"github.com/spf13/cobra", "cobra", discovery.CLI},
	{"github.com/urfave/cli", "urfave/cli", discovery.CLI},
	{"github.com/alecthomas/kong", "kong", discovery.CLI},
}

// goResources maps import path prefixes to the backing service they talk to.
var goResources = []struct{ prefix, kind string }{
	{"github.com/jackc/pgx", "postgres"},
	{"github.com/lib/pq", "postgres"},
	{"gorm.io/driver/postgres", "postgres"},
	{"github.com/uptrace/bun/driver/pgdriver", "postgres"},
	{"github.com/xraph/grove/drivers/pgdriver", "postgres"},
	{"github.com/go-pg/pg", "postgres"},
	{"github.com/go-sql-driver/mysql", "mysql"},
	{"gorm.io/driver/mysql", "mysql"},
	{"github.com/redis/go-redis", "redis"},
	{"github.com/go-redis/redis", "redis"},
	{"github.com/gomodule/redigo", "redis"},
	{"github.com/redis/rueidis", "redis"},
	{"go.mongodb.org/mongo-driver", "mongodb"},
	{"github.com/rabbitmq/amqp091-go", "rabbitmq"},
	{"github.com/streadway/amqp", "rabbitmq"},
	{"github.com/segmentio/kafka-go", "kafka"},
	{"github.com/IBM/sarama", "kafka"},
	{"github.com/Shopify/sarama", "kafka"},
	{"github.com/confluentinc/confluent-kafka-go", "kafka"},
	{"github.com/twmb/franz-go", "kafka"},
	{"github.com/nats-io/nats.go", "nats"},
	{"github.com/aws/aws-sdk-go-v2/service/s3", "object-storage"},
	{"github.com/aws/aws-sdk-go/service/s3", "object-storage"},
	{"github.com/minio/minio-go", "object-storage"},
	{"cloud.google.com/go/storage", "object-storage"},
	{"github.com/elastic/go-elasticsearch", "search"},
	{"github.com/opensearch-project/opensearch-go", "search"},
}

func hasPathPrefix(p, prefix string) bool {
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

// Detect implements discovery.Detector.
func (Go) Detect(ctx context.Context, repo discovery.Repo, r *discovery.Report) error {
	mods := goModules(repo, r)
	if len(mods) == 0 {
		return nil
	}
	goWorkspaces(repo, r, mods)

	pkgs, err := goPackages(ctx, repo, r, mods)
	if err != nil {
		return err
	}
	for _, m := range mods {
		r.Units = append(r.Units, goUnit(m, mods, pkgs))
	}
	return nil
}

func goModules(repo discovery.Repo, r *discovery.Report) []*goModule {
	var mods []*goModule
	for _, f := range repo.Named("go.mod") {
		if goSkipped(f) {
			continue
		}
		data, n := read(repo, f)
		if n != nil {
			note(r, n)
			continue
		}
		// Strict parsing keeps replace directives, which ParseLax drops by
		// design. Lax is the fallback for directives newer than x/mod.
		mf, err := modfile.Parse(f, data, nil)
		if err != nil {
			mf, err = modfile.ParseLax(f, data, nil)
		}
		if err != nil || mf.Module == nil {
			note(r, &discovery.Note{Path: f, Message: "not a valid go.mod"})
			continue
		}
		mods = append(mods, &goModule{dir: dir(f), path: mf.Module.Mod.Path, file: f, data: data, mod: mf})
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].dir < mods[j].dir })
	return mods
}

// goSkipped reports whether the go command itself would ignore the file:
// testdata, and directories starting with "." or "_".
func goSkipped(name string) bool {
	for _, seg := range strings.Split(path.Dir(name), "/") {
		if seg == "testdata" || (seg != "." && (strings.HasPrefix(seg, ".") || strings.HasPrefix(seg, "_"))) {
			return true
		}
	}
	return false
}

func goWorkspaces(repo discovery.Repo, r *discovery.Report, mods []*goModule) {
	for _, f := range repo.Named("go.work") {
		if goSkipped(f) {
			continue
		}
		data, n := read(repo, f)
		if n != nil {
			note(r, n)
			continue
		}
		wf, err := modfile.ParseWork(f, data, nil)
		if err != nil {
			note(r, &discovery.Note{Path: f, Message: "not a valid go.work"})
			continue
		}
		ws := discovery.Workspace{Ecosystem: discovery.Go, Tool: "go-work", Path: dir(f),
			Evidence: []discovery.Evidence{{Path: f, Detail: "go.work"}}}
		for _, u := range wf.Use {
			member := join(dir(f), u.Path)
			if !goHasModule(mods, member) {
				note(r, &discovery.Note{Path: f, Message: "uses " + u.Path + ", which has no go.mod"})
				continue
			}
			ws.Members = append(ws.Members, member)
			ws.Evidence = append(ws.Evidence, evidence(data, f, u.Syntax.Start.Line))
		}
		sort.Strings(ws.Members)
		r.Workspaces = append(r.Workspaces, ws)
	}
}

func goHasModule(mods []*goModule, d string) bool {
	for _, m := range mods {
		if m.dir == d {
			return true
		}
	}
	return false
}

func goPackages(ctx context.Context, repo discovery.Repo, r *discovery.Report, mods []*goModule) (map[string]*goPackage, error) {
	dirs := make([]string, len(mods))
	byDir := make(map[string]*goModule, len(mods))
	for i, m := range mods {
		dirs[i] = m.dir
		byDir[m.dir] = m
	}
	pkgs := map[string]*goPackage{}
	fset := token.NewFileSet()
	for _, f := range repo.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.HasSuffix(f, ".go") || strings.HasSuffix(f, "_test.go") || goSkipped(f) {
			continue
		}
		modDir := owner(f, dirs)
		if modDir == "" {
			continue
		}
		data, n := read(repo, f)
		if n != nil {
			continue // generated or unreadable; neither holds a service's main
		}
		if len(data) > maxGoFileBytes {
			continue
		}
		file, err := parser.ParseFile(fset, f, data, parser.ImportsOnly|parser.ParseComments)
		if err != nil {
			note(r, &discovery.Note{Path: f, Message: "Go syntax error"})
			continue
		}
		if goIgnored(file) {
			continue
		}
		p := pkgs[dir(f)]
		if p == nil {
			p = &goPackage{dir: dir(f), module: byDir[modDir]}
			pkgs[p.dir] = p
		}
		for _, imp := range file.Imports {
			ip, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			line := fset.Position(imp.Pos()).Line
			p.imports = append(p.imports, goImport{path: ip, ev: discovery.Evidence{Path: f, Line: line, Detail: "import " + strconv.Quote(ip)}})
		}
		if p.serverEv == nil {
			if l := lineOf(data, goServeCall, 1); l > 0 {
				ev := evidence(data, f, l)
				p.serverEv = &ev
			}
		}
		if file.Name.Name == "main" && !p.isMain {
			if l := goMainLine(fset, f, data); l > 0 {
				p.isMain = true
				p.mainEv = evidence(data, f, l)
			}
		}
	}
	return pkgs, nil
}

// goIgnored reports whether a file is excluded from every build with a
// "//go:build ignore" line, the convention for go:generate helpers.
func goIgnored(f *ast.File) bool {
	for _, cg := range f.Comments {
		if cg.Pos() > f.Package {
			break
		}
		for _, c := range cg.List {
			if expr, ok := strings.CutPrefix(c.Text, "//go:build "); ok && strings.TrimSpace(expr) == "ignore" {
				return true
			}
		}
	}
	return false
}

// goMainLine parses the whole file and returns the line of func main, or 0.
func goMainLine(fset *token.FileSet, name string, data []byte) int {
	file, err := parser.ParseFile(fset, name, data, parser.SkipObjectResolution)
	if err != nil {
		return 0
	}
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "main" {
			return fset.Position(fn.Pos()).Line
		}
	}
	return 0
}

// resolve maps an import path to the directory of a package in this
// repository, or "".
func resolve(ip string, mods []*goModule) string {
	var best *goModule
	for _, m := range mods {
		if hasPathPrefix(ip, m.path) && (best == nil || len(m.path) > len(best.path)) {
			best = m
		}
	}
	if best == nil {
		return ""
	}
	return join(best.dir, strings.TrimPrefix(strings.TrimPrefix(ip, best.path), "/"))
}

func goUnit(m *goModule, mods []*goModule, pkgs map[string]*goPackage) discovery.Unit {
	u := discovery.Unit{
		ID:        discovery.UnitID(discovery.Go, m.dir),
		Ecosystem: discovery.Go,
		Name:      m.path,
		Path:      m.dir,
		Evidence:  []discovery.Evidence{evidence(m.data, m.file, m.mod.Module.Syntax.Start.Line)},
	}
	if m.mod.Go != nil {
		u.Runtime = "go " + m.mod.Go.Version
	}
	u.DependsOn = goDependencies(m, mods)

	var mains []*goPackage
	for _, p := range pkgs {
		if p.module != m {
			continue
		}
		for _, imp := range p.imports {
			if res, ok := goResource(imp); ok {
				u.Resources = addResource(u.Resources, res)
			}
		}
		if p.isMain {
			mains = append(mains, p)
		}
	}
	sort.Slice(mains, func(i, j int) bool { return mains[i].dir < mains[j].dir })
	for _, p := range mains {
		u.Entrypoints = append(u.Entrypoints, goEntrypoint(p, mods, pkgs))
	}
	sortResources(u.Resources)
	return u
}

func goDependencies(m *goModule, mods []*goModule) []discovery.Dependency {
	var deps []discovery.Dependency
	seen := map[string]bool{}
	for _, req := range m.mod.Require {
		for _, other := range mods {
			if other != m && other.path == req.Mod.Path && !seen[other.dir] {
				seen[other.dir] = true
				deps = append(deps, discovery.Dependency{
					Unit:     discovery.UnitID(discovery.Go, other.dir),
					Evidence: evidence(m.data, m.file, req.Syntax.Start.Line),
				})
			}
		}
	}
	for _, rep := range m.mod.Replace {
		if !modfile.IsDirectoryPath(rep.New.Path) {
			continue
		}
		target := join(m.dir, rep.New.Path)
		for _, other := range mods {
			if other != m && other.dir == target && !seen[other.dir] {
				seen[other.dir] = true
				deps = append(deps, discovery.Dependency{
					Unit:     discovery.UnitID(discovery.Go, other.dir),
					Evidence: evidence(m.data, m.file, rep.Syntax.Start.Line),
				})
			}
		}
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].Unit < deps[j].Unit })
	return deps
}

// goEntrypoint walks everything the main package imports inside this
// repository, and reports the frameworks and drivers it reaches.
func goEntrypoint(p *goPackage, mods []*goModule, pkgs map[string]*goPackage) discovery.Entrypoint {
	name := path.Base(p.dir)
	if p.dir == p.module.dir || name == "." {
		name = path.Base(p.module.path)
	}
	ep := discovery.Entrypoint{Name: name, Path: p.dir, Evidence: []discovery.Evidence{p.mainEv}}

	seen := map[string]bool{p.dir: true}
	queue := []*goPackage{p}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.serverEv != nil {
			ep.Frameworks = addFramework(ep.Frameworks, discovery.Framework{
				Name: "net/http", Kind: discovery.HTTP, Confidence: discovery.Likely, Evidence: *cur.serverEv,
			})
		}
		for _, imp := range cur.imports {
			if d := resolve(imp.path, mods); d != "" {
				if next := pkgs[d]; next != nil && !seen[d] {
					seen[d] = true
					queue = append(queue, next)
				}
				continue
			}
			for _, fw := range goFrameworks {
				if hasPathPrefix(imp.path, fw.prefix) {
					ep.Frameworks = addFramework(ep.Frameworks, discovery.Framework{
						Name: fw.name, Kind: fw.kind, Confidence: discovery.Likely, Evidence: imp.ev,
					})
				}
			}
			if res, ok := goResource(imp); ok {
				ep.Resources = addResource(ep.Resources, res)
			}
		}
	}
	sortFrameworks(ep.Frameworks)
	sortResources(ep.Resources)
	return ep
}

func goResource(imp goImport) (discovery.ResourceUse, bool) {
	for _, res := range goResources {
		if hasPathPrefix(imp.path, res.prefix) {
			return discovery.ResourceUse{Kind: res.kind, Confidence: discovery.Possible, Evidence: imp.ev}, true
		}
	}
	return discovery.ResourceUse{}, false
}
