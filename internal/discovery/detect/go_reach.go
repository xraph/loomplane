package detect

import (
	"go/ast"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/pkg/discovery"
)

type goSymbol struct{ imported, name, constructor, receiver string }

// goBody holds static references, including callbacks passed as values.
// Repository code is never evaluated to resolve a reference.
type goBody struct {
	imports    []goImport
	symbols    []goSymbol
	resources  []discovery.ResourceUse
	registered []string
	server     *discovery.Evidence
	receiver   string
	returns    []goSymbol
}

func goBodies(fset *token.FileSet, file string, data []byte, tree *ast.File, pkg *goPackage) {
	imports := map[string]goImport{}
	for _, spec := range tree.Imports {
		ip, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := goImportName(ip)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = goImport{path: ip, alias: name, ev: discovery.Evidence{Path: file, Line: fset.Position(spec.Pos()).Line, Detail: "import " + strconv.Quote(ip)}}
	}
	for _, decl := range tree.Decls {
		switch node := decl.(type) {
		case *ast.FuncDecl:
			body := goScanBody(fset, file, data, node, imports)
			if node.Name.Name == "init" {
				pkg.initializers = append(pkg.initializers, body)
			} else {
				pkg.functions[node.Name.Name] = append(pkg.functions[node.Name.Name], body)
			}
		case *ast.GenDecl:
			if node.Tok == token.TYPE {
				for _, spec := range node.Specs {
					if declared, ok := spec.(*ast.TypeSpec); ok {
						pkg.functions[declared.Name.Name] = append(pkg.functions[declared.Name.Name], goScanBody(fset, file, data, declared.Type, imports))
					}
				}
			}
			if node.Tok != token.VAR && node.Tok != token.CONST {
				continue
			}
			for _, spec := range node.Specs {
				if value, ok := spec.(*ast.ValueSpec); ok {
					for _, expr := range value.Values {
						pkg.initializers = append(pkg.initializers, goScanBody(fset, file, data, expr, imports))
					}
				}
			}
		}
	}
}

func goImportName(ip string) string {
	name := path.Base(ip)
	if len(name) > 1 && name[0] == 'v' {
		if _, err := strconv.Atoi(name[1:]); err == nil {
			name = path.Base(path.Dir(ip))
		}
	}
	return strings.TrimPrefix(name, "go-")
}

func goScanBody(fset *token.FileSet, file string, data []byte, node ast.Node, imports map[string]goImport) goBody {
	var body goBody
	receivers := map[string]goSymbol{}
	if fn, ok := node.(*ast.FuncDecl); ok {
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			ref := goReceiverType(fn.Recv.List[0].Type, imports)
			body.receiver = ref.receiver
			for _, name := range fn.Recv.List[0].Names {
				receivers[name.Name] = ref
			}
		}
		if fn.Type.Results != nil {
			for _, field := range fn.Type.Results.List {
				body.returns = append(body.returns, goReceiverType(field.Type, imports))
			}
		}
		if fn.Type.Params != nil {
			for _, field := range fn.Type.Params.List {
				for _, name := range field.Names {
					receivers[name.Name] = goReceiverType(field.Type, imports)
				}
			}
		}
	}
	var receiverPackage func(ast.Expr) goSymbol
	receiverPackage = func(expr ast.Expr) goSymbol {
		switch x := expr.(type) {
		case *ast.Ident:
			if ref, ok := receivers[x.Name]; ok {
				return ref
			}
			return goSymbol{imported: imports[x.Name].path}
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				ref := receiverPackage(sel.X)
				ref.constructor = sel.Sel.Name
				return ref
			}
			if id, ok := x.Fun.(*ast.Ident); ok {
				return goSymbol{constructor: id.Name}
			}
		case *ast.CompositeLit:
			return goReceiverType(x.Type, imports)
		case *ast.UnaryExpr:
			return receiverPackage(x.X)
		case *ast.ParenExpr:
			return receiverPackage(x.X)
		}
		return goSymbol{}
	}
	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range x.Lhs {
				if i >= len(x.Rhs) {
					break
				}
				if id, ok := lhs.(*ast.Ident); ok {
					if ref := receiverPackage(x.Rhs[i]); ref.imported != "" || ref.constructor != "" || ref.receiver != "" {
						receivers[id.Name] = ref
					}
				}
			}
		case *ast.ValueSpec:
			for i, id := range x.Names {
				if i >= len(x.Values) {
					break
				}
				if ref := receiverPackage(x.Values[i]); ref.imported != "" || ref.constructor != "" || ref.receiver != "" {
					receivers[id.Name] = ref
				}
			}
		}
		return true
	})
	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if x.Obj == nil || x.Obj.Kind == ast.Fun || x.Obj.Kind == ast.Typ {
				body.symbols = append(body.symbols, goSymbol{name: x.Name})
			}
		case *ast.SelectorExpr:
			id, ok := x.X.(*ast.Ident)
			if !ok {
				if ref := receiverPackage(x.X); ref.imported != "" || ref.constructor != "" || ref.receiver != "" {
					ref.name = x.Sel.Name
					body.symbols = append(body.symbols, ref)
				}
				break
			}
			imp, ok := imports[id.Name]
			if !ok {
				if ref, found := receivers[id.Name]; found {
					ref.name = x.Sel.Name
					body.symbols = append(body.symbols, ref)
					return false
				}
				break
			}
			body.imports = append(body.imports, imp)
			body.symbols = append(body.symbols, goSymbol{imported: imp.path, name: x.Sel.Name})
			return false
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok {
				break
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				break
			}
			imp, ok := imports[id.Name]
			if ok {
				if extension := goExtension(imp.path); extension != "" {
					if sel.Sel.Name == "New" || sel.Sel.Name == "NewExtension" {
						body.registered = append(body.registered, extension)
					}
					if res, found := goDriverOption(extension, sel.Sel.Name, x.Args); found {
						res.Evidence = discovery.Evidence{Path: file, Line: fset.Position(x.Pos()).Line, Detail: sel.Sel.Name + " driver " + res.Kind}
						body.resources = append(body.resources, res)
					}
				}
			}
			if (sel.Sel.Name == "ListenAndServe" || sel.Sel.Name == "ListenAndServeTLS") || ok && imp.path == "net/http" && (sel.Sel.Name == "Serve" || sel.Sel.Name == "ServeTLS") {
				ev := evidence(data, file, fset.Position(x.Pos()).Line)
				body.server = &ev
			}
		case *ast.CompositeLit:
			if sel, ok := x.Type.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && sel.Sel.Name == "Server" && imports[id.Name].path == "net/http" {
					ev := evidence(data, file, fset.Position(x.Pos()).Line)
					body.server = &ev
				}
			}
		}
		return true
	})
	return body
}

func goReceiverType(expr ast.Expr, imports map[string]goImport) goSymbol {
	switch x := expr.(type) {
	case *ast.StarExpr:
		return goReceiverType(x.X, imports)
	case *ast.ParenExpr:
		return goReceiverType(x.X, imports)
	case *ast.IndexExpr:
		return goReceiverType(x.X, imports)
	case *ast.IndexListExpr:
		return goReceiverType(x.X, imports)
	case *ast.Ident:
		return goSymbol{receiver: x.Name}
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return goSymbol{imported: imports[id.Name].path, receiver: x.Sel.Name}
		}
	}
	return goSymbol{}
}

func goExtension(ip string) string {
	const prefix = "github.com/xraph/"
	if !strings.HasPrefix(ip, prefix) {
		return ""
	}
	name := strings.TrimPrefix(ip, prefix)
	if name == "grove/kv/extension" {
		return "grove_kv"
	}
	if strings.HasPrefix(name, "forge/extensions/") {
		return strings.TrimPrefix(name, "forge/extensions/")
	}
	if !strings.HasSuffix(name, "/extension") {
		return ""
	}
	name = strings.TrimSuffix(name, "/extension")
	if strings.Contains(name, "/") {
		return ""
	}
	return name
}

func goDriverOption(extension, option string, args []ast.Expr) (discovery.ResourceUse, bool) {
	if len(args) == 0 {
		return discovery.ResourceUse{}, false
	}
	if option != "WithDSN" && option != "WithDriverFactory" && option != "WithDriver" && option != "WithStorageDriver" {
		return discovery.ResourceUse{}, false
	}
	if extension != "grove" && extension != "grove_kv" && extension != "cache" && extension != "queue" && extension != "trove" && extension != "conduit" {
		return discovery.ResourceUse{}, false
	}
	lit, ok := args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return discovery.ResourceUse{}, false
	}
	driver, err := strconv.Unquote(lit.Value)
	if err != nil {
		return discovery.ResourceUse{}, false
	}
	kinds := map[string]string{"postgres": "postgres", "pg": "postgres", "mysql": "mysql", "mongodb": "mongodb", "mongo": "mongodb", "redis": "redis", "redisstreams": "redis", "nats": "nats", "jetstream": "nats", "kafka": "kafka", "rabbitmq": "rabbitmq", "s3": "object-storage", "gcs": "object-storage", "azure": "object-storage"}
	kind := kinds[strings.ToLower(driver)]
	return discovery.ResourceUse{Kind: kind, Confidence: discovery.Certain}, kind != ""
}

func goReachEntrypoint(main *goPackage, mods []*goModule, pkgs map[string]*goPackage) (discovery.Entrypoint, goReach) {
	name := path.Base(main.dir)
	if main.dir == main.module.dir || name == "." {
		name = path.Base(main.module.path)
	}
	ep := discovery.Entrypoint{Name: name, Path: main.dir, Evidence: []discovery.Evidence{main.mainEv}}
	reach := goReach{origin: map[string]string{}, depth: map[string]int{}, packages: map[string]bool{}, registered: map[string]bool{}}
	type pending struct {
		pkg      *goPackage
		name     string
		depth    int
		receiver string
	}
	queue := []pending{{pkg: main, name: "main"}}
	seen := map[string]bool{}
	found := func(fw discovery.Framework, pkg string, depth int) {
		if _, ok := reach.origin[fw.Name]; !ok {
			reach.origin[fw.Name], reach.depth[fw.Name] = pkg, depth
		}
		ep.Frameworks = addFramework(ep.Frameworks, fw)
	}
	use := func(imp goImport, pkg string, depth int) {
		if fw, ok := goFramework(imp.path); ok {
			found(discovery.Framework{Name: fw.name, Kind: fw.kind, Confidence: discovery.Likely, Evidence: imp.ev}, pkg, depth)
		}
		if res, ok := goResource(imp); ok {
			ep.Resources = addResource(ep.Resources, res)
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		p := cur.pkg
		var bodies []goBody
		if !reach.packages[p.dir] {
			reach.packages[p.dir] = true
			bodies = append(bodies, p.initializers...)
			for _, imp := range p.imports {
				if dep := pkgs[resolve(imp.path, mods)]; dep != nil {
					queue = append(queue, pending{pkg: dep, depth: cur.depth + 1})
				} else if imp.alias == "_" || imp.alias == "." {
					use(imp, p.dir, cur.depth)
				}
			}
		}
		key := p.dir + "|" + cur.name + "|" + cur.receiver
		if cur.name != "" && !seen[key] {
			seen[key] = true
			for _, body := range p.functions[cur.name] {
				if body.receiver == cur.receiver {
					bodies = append(bodies, body)
				}
			}
		}
		for _, body := range bodies {
			if body.server != nil {
				found(discovery.Framework{Name: "net/http", Kind: discovery.HTTP, Confidence: discovery.Likely, Evidence: *body.server}, p.dir, cur.depth)
			}
			for _, imp := range body.imports {
				if resolve(imp.path, mods) == "" {
					use(imp, p.dir, cur.depth)
				}
			}
			for _, res := range body.resources {
				ep.Resources = addResource(ep.Resources, res)
			}
			for _, extension := range body.registered {
				reach.registered[extension] = true
			}
			for _, sym := range body.symbols {
				dep, depth := p, cur.depth
				if sym.imported != "" {
					dep, depth = pkgs[resolve(sym.imported, mods)], cur.depth+1
				}
				if dep == nil {
					continue
				}
				receivers := []goSymbol{{receiver: sym.receiver}}
				if sym.constructor != "" {
					receivers = nil
					for _, ctor := range dep.functions[sym.constructor] {
						receivers = append(receivers, ctor.returns...)
					}
				}
				for _, ref := range receivers {
					methodPkg := dep
					if ref.imported != "" {
						methodPkg = pkgs[resolve(ref.imported, mods)]
					}
					if methodPkg != nil && len(methodPkg.functions[sym.name]) > 0 && !seen[methodPkg.dir+"|"+sym.name+"|"+ref.receiver] {
						queue = append(queue, pending{pkg: methodPkg, name: sym.name, depth: depth, receiver: ref.receiver})
					}
				}
			}
		}
	}
	sortFrameworks(ep.Frameworks)
	sortResources(ep.Resources)
	// Stable ordering also keeps registration-driven configuration deterministic.
	sort.SliceStable(ep.Evidence, func(i, j int) bool { return ep.Evidence[i].Path < ep.Evidence[j].Path })
	return ep, reach
}
