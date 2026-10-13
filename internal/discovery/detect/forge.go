package detect

import (
	"context"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/pkg/discovery"
)

// Forge reads project layouts, declared build targets and app roles.
type Forge struct{}

// Name implements discovery.Detector.
func (Forge) Name() string { return "forge" }

// Detect implements discovery.Detector. Go runs first and owns executable discovery.
func (Forge) Detect(ctx context.Context, repo discovery.Repo, report *discovery.Report) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, file := range repo.Named(".forge.yaml", ".forge.yml") {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.HasSuffix(file, ".yml") && repo.Has(strings.TrimSuffix(file, ".yml")+".yaml") {
			continue
		}
		doc := forgeReadYAML(repo, report, file)
		if doc == nil {
			continue
		}
		root := dir(file)
		project := forgeNode(doc, "project")
		if project != nil && project.Kind != yaml.MappingNode {
			report.Notes = append(report.Notes, discovery.Note{Path: file, Message: "Forge project must be a mapping"})
			continue
		}
		if project != nil {
			forgeProject(repo, report, file, root, project, forgeNode(doc, "build", "apps"))
		}
		app := forgeNode(doc, "app")
		if app == nil {
			continue
		}
		if app.Kind != yaml.MappingNode {
			report.Notes = append(report.Notes, discovery.Note{Path: file, Message: "Forge app must be a mapping"})
			continue
		}
		for i := range report.Units {
			u := &report.Units[i]
			if u.Ecosystem != discovery.Go {
				continue
			}
			for j := range u.Entrypoints {
				ep := &u.Entrypoints[j]
				if !within(ep.Path, root) || forgeNestedManifest(repo, root, ep.Path) {
					continue
				}
				if name := forgeScalar(app, "name"); name != "" {
					ep.Name = name
				}
				ev := forgeEvidence(file, app, "Forge app declaration")
				ep.Evidence = append(ep.Evidence, ev)
				var kind discovery.FrameworkKind
				switch forgeScalar(app, "type") {
				case "worker":
					kind = discovery.Worker
				case "web", "server", "api":
					kind = discovery.HTTP
				case "cli":
					kind = discovery.CLI
				default:
					if forgeScalar(app, "type") != "" {
						report.Notes = append(report.Notes, discovery.Note{Path: file, Message: "Forge app type is unsupported"})
					}
				}
				if kind != "" {
					forgeSetFramework(ep, discovery.Framework{Name: "forge", Kind: kind, Confidence: discovery.Certain, Evidence: forgeEvidence(file, forgeNode(app, "type"), "Forge app type: "+string(kind))})
				}
				if err := applyForgeConfig(ctx, repo, report, ep, ep.Path, nil); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func forgeNestedManifest(repo discovery.Repo, root, ep string) bool {
	for d := ep; d != root && within(d, root); d = dir(d) {
		if repo.Has(join(d, ".forge.yaml")) || repo.Has(join(d, ".forge.yml")) {
			return true
		}
	}
	return false
}

func forgeSetFramework(ep *discovery.Entrypoint, f discovery.Framework) {
	for i := range ep.Frameworks {
		if ep.Frameworks[i].Name == f.Name {
			ep.Frameworks[i] = f
			return
		}
	}
	ep.Frameworks = append(ep.Frameworks, f)
	sortFrameworks(ep.Frameworks)
}

func forgeProject(repo discovery.Repo, report *discovery.Report, file, root string, project, targets *yaml.Node) {
	ev := forgeEvidence(file, project, "Forge project layout")
	report.BuildSystems = append(report.BuildSystems, discovery.BuildSystem{Name: "forge", Path: root, Evidence: ev})
	ws := discovery.Workspace{Ecosystem: discovery.Go, Tool: "forge", Path: root, Evidence: []discovery.Evidence{ev}}
	layout := forgeScalar(project, "layout")
	for i := range report.Units {
		u := &report.Units[i]
		if u.Ecosystem != discovery.Go || !within(u.Path, root) {
			continue
		}
		member := layout != "multi-module" && u.Path == root
		if layout == "multi-module" {
			for _, key := range []string{"apps", "services", "extensions", "pkg"} {
				pattern := forgeScalar(forgeNode(project, "workspace"), key)
				if pattern != "" && matchGlob(join(root, pattern), u.Path) {
					member = true
				}
			}
		}
		if member {
			forgeLayoutTargets(u, file, root, project)
			ws.Members = append(ws.Members, u.Path)
			u.Evidence = append(u.Evidence, ev)
		}
	}
	sort.Strings(ws.Members)
	report.Workspaces = append(report.Workspaces, ws)
	if targets == nil {
		return
	}
	if targets.Kind != yaml.SequenceNode {
		report.Notes = append(report.Notes, discovery.Note{Path: file, Message: "Forge build.apps must be a list"})
		return
	}
	for _, target := range targets.Content {
		if target.Kind != yaml.MappingNode {
			report.Notes = append(report.Notes, discovery.Note{Path: file, Message: "Forge build target must be a mapping"})
			continue
		}
		targetPath, valid := forgeBuildTarget(repo, root, target)
		if !valid {
			report.Notes = append(report.Notes, discovery.Note{Path: file, Message: "Forge build target needs a repository-local cmd or module path"})
			continue
		}
		found := false
		for i := range report.Units {
			u := &report.Units[i]
			if u.Ecosystem != discovery.Go {
				continue
			}
			for j := range u.Entrypoints {
				ep := &u.Entrypoints[j]
				if ep.Path != targetPath {
					continue
				}
				if name := forgeScalar(target, "name"); name != "" {
					ep.Name = name
				}
				ep.Evidence = append(ep.Evidence, forgeEvidence(file, target, "Forge build target declaration"))
				found = true
			}
		}
		if !found {
			report.Notes = append(report.Notes, discovery.Note{Path: file, Message: "Forge build target has no discovered Go main package"})
		}
	}
}

func forgeRelative(root, value string) (string, bool) {
	if strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return "", false
	}
	cleaned := path.Clean(value)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return join(root, cleaned), true
}

// forgeReadYAML avoids including YAML parser errors, which can quote secrets.
func forgeReadYAML(repo discovery.Repo, report *discovery.Report, file string) *yaml.Node {
	data, n := read(repo, file)
	note(report, n)
	if n != nil {
		return nil
	}
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		report.Notes = append(report.Notes, discovery.Note{Path: file, Message: "invalid Forge YAML mapping"})
		return nil
	}
	// Decode validates duplicate keys without retaining or reporting scalar values.
	var mapping map[string]any
	if doc.Decode(&mapping) != nil {
		report.Notes = append(report.Notes, discovery.Note{Path: file, Message: "invalid Forge YAML mapping"})
		return nil
	}
	return doc.Content[0]
}

// forgeNode follows YAML aliases and merge keys with a bounded traversal.
func forgeNode(node *yaml.Node, keys ...string) *yaml.Node {
	for _, key := range keys {
		node = forgeMappingValue(node, key, 0)
	}
	return forgeDereference(node, 0)
}

const forgeYAMLDepth = 32

func forgeDereference(node *yaml.Node, depth int) *yaml.Node {
	for node != nil && node.Kind == yaml.AliasNode {
		if depth >= forgeYAMLDepth {
			return nil
		}
		node = node.Alias
		depth++
	}
	return node
}

func forgeMappingValue(node *yaml.Node, key string, depth int) *yaml.Node {
	if depth >= forgeYAMLDepth {
		return nil
	}
	node = forgeDereference(node, depth)
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	var merged *yaml.Node
	for i := 0; i+1 < len(node.Content); i += 2 {
		name, value := node.Content[i], node.Content[i+1]
		if name.Tag == "!!merge" {
			merged = value
			continue
		}
		if name.Value == key {
			return forgeDereference(value, depth+1)
		}
	}
	merged = forgeDereference(merged, depth+1)
	if merged == nil {
		return nil
	}
	if merged.Kind != yaml.SequenceNode {
		return forgeMappingValue(merged, key, depth+1)
	}
	// Earlier maps in a YAML merge sequence override later maps.
	for _, item := range merged.Content {
		if value := forgeMappingValue(item, key, depth+1); value != nil {
			return value
		}
	}
	return nil
}

func forgeScalar(node *yaml.Node, keys ...string) string {
	node = forgeNode(node, keys...)
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return ""
	}
	return strings.TrimSpace(node.Value)
}

func forgeEvidence(file string, node *yaml.Node, detail string) discovery.Evidence {
	line := 0
	if node != nil {
		line = node.Line
	}
	return discovery.Evidence{Path: file, Line: line, Detail: detail}
}

type forgeBackend struct {
	extension, list string
	drivers         []string
	kinds           map[string]string
}

var forgeBackends = []forgeBackend{
	{"grove", "databases", []string{"driver", "type"}, map[string]string{"postgres": "postgres", "pg": "postgres", "postgresql": "postgres", "mysql": "mysql", "mongo": "mongodb", "mongodb": "mongodb", "clickhouse": "clickhouse", "elasticsearch": "search", "sqlite": "", "sqlite3": "", "turso": ""}},
	{"grove_kv", "stores", []string{"driver", "type"}, map[string]string{"redis": "redis", "memcached": "memcached", "badger": "", "boltdb": "", "bbolt": "", "dynamodb": ""}},
	{"cache", "", []string{"driver"}, map[string]string{"inmemory": "", "memory": "", "redis": "redis", "memcached": "memcached"}},
	{"queue", "", []string{"driver"}, map[string]string{"inmemory": "", "memory": "", "redis": "redis", "rabbitmq": "rabbitmq", "nats": "nats"}},
	{"trove", "stores", []string{"storage_driver", "driver", "type"}, map[string]string{"s3": "object-storage", "gcs": "object-storage", "azure": "object-storage", "sftp": "", "file": "", "local": "", "mem": "", "memory": "", "inmemory": ""}},
	{"events", "brokers", []string{"type", "driver"}, map[string]string{"memory": "", "inmemory": "", "redis": "redis", "nats": "nats"}},
	{"search", "", []string{"driver"}, map[string]string{"inmemory": "", "elasticsearch": "search", "meilisearch": "search", "typesense": "search"}},
}

// applyForgeConfig reads app config first, then shared defaults for registered extensions.
// A nil registration set permits app-specific config only, as selected by an app manifest.
func applyForgeConfig(ctx context.Context, repo discovery.Repo, report *discovery.Report, ep *discovery.Entrypoint, appDir string, registered map[string]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if registered != nil && len(registered) == 0 {
		return nil
	}
	root := forgeConfigRoot(repo, report, appDir)
	configDir := "config"
	for _, manifest := range []string{join(root, ".forge.yaml"), join(root, ".forge.yml")} {
		if !repo.Has(manifest) {
			continue
		}
		if doc := forgeReadYAML(repo, report, manifest); doc != nil {
			if custom := forgeScalar(doc, "project", "structure", "config"); custom != "" {
				if rel, ok := forgeRelative(".", custom); ok {
					configDir = rel
				}
			}
		}
		break
	}
	var candidates []string
	configName := forgeConfigName(repo, report, ep, root)
	if configName != "" && path.Base(configName) == configName && configName != "." && configName != ".." {
		candidates = append(candidates, join(root, configDir, configName+".yaml"), join(root, configDir, configName+".yml"))
	}
	for _, configAppDir := range forgeAppConfigDirs(repo, report, appDir, root) {
		candidates = append(candidates, join(configAppDir, "config.yaml"), join(configAppDir, "config.yml"))
	}
	if root != appDir && registered != nil {
		candidates = append(candidates, join(root, "config.yaml"), join(root, "config.yml"))
	}
	seen := map[string]bool{}
	files := map[string]bool{}
	for _, file := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if files[file] || !repo.Has(file) {
			continue
		}
		files[file] = true
		doc := forgeReadYAML(repo, report, file)
		if doc == nil {
			continue
		}
		for _, backend := range forgeBackends {
			if seen[backend.extension] || registered != nil && !registered[backend.extension] {
				continue
			}
			ext := forgeNode(doc, "extensions", backend.extension)
			if ext == nil {
				continue
			}
			seen[backend.extension] = true
			if forgeDisabled(ext) {
				continue
			}
			if ext.Kind != yaml.MappingNode {
				report.Notes = append(report.Notes, discovery.Note{Path: file, Message: "Forge extension " + backend.extension + " must be a mapping"})
				continue
			}
			items := []*yaml.Node{ext}
			namedInstances := false
			if list := forgeNode(ext, backend.list); backend.list != "" && list != nil {
				if list.Kind != yaml.SequenceNode {
					report.Notes = append(report.Notes, discovery.Note{Path: file, Message: "Forge extension " + backend.extension + " instances must be a list"})
					continue
				}
				items = list.Content
				namedInstances = true
			}
			for _, item := range items {
				item = forgeNode(item)
				if item == nil || item.Kind != yaml.MappingNode {
					report.Notes = append(report.Notes, discovery.Note{Path: file, Message: "Forge extension " + backend.extension + " instances must be mappings"})
					continue
				}
				if forgeDisabled(item) {
					continue
				}
				if namedInstances && forgeScalar(item, "name") == "" {
					report.Notes = append(report.Notes, discovery.Note{Path: file, Message: "Forge extension " + backend.extension + " instance requires a name"})
					continue
				}
				driver, driverNode, configured := forgeConfigDriver(backend, item)
				if driver == "" {
					if configured && registered != nil && registered[backend.extension] {
						report.Notes = append(report.Notes, discovery.Note{Path: file, Message: "Forge extension " + backend.extension + " backend is unresolved"})
					}
					continue
				}
				kind, known := backend.kinds[driver]
				if !known {
					report.Notes = append(report.Notes, discovery.Note{Path: file, Message: "Forge extension " + backend.extension + " uses an unsupported driver"})
					continue
				}
				if kind == "" {
					continue
				}
				ep.Resources = addResource(ep.Resources, discovery.ResourceUse{Kind: kind, Confidence: discovery.Certain, Evidence: forgeEvidence(file, driverNode, "Forge extensions."+backend.extension+" backend: "+driver)})
			}
		}
	}
	sortResources(ep.Resources)
	return nil
}

func forgeConfigRoot(repo discovery.Repo, report *discovery.Report, appDir string) string {
	moduleRoot := ""
	for d := appDir; ; d = dir(d) {
		if moduleRoot == "" && repo.Has(join(d, "go.mod")) {
			moduleRoot = d
		}
		for _, file := range []string{join(d, ".forge.yaml"), join(d, ".forge.yml")} {
			if !repo.Has(file) {
				continue
			}
			if doc := forgeReadYAML(repo, report, file); doc != nil && forgeNode(doc, "project") != nil {
				return d
			}
			break
		}
		if d == "." {
			break
		}
	}
	if moduleRoot != "" {
		return moduleRoot
	}
	return "."
}

func forgeLayoutTargets(unit *discovery.Unit, file, root string, project *yaml.Node) {
	for i := range unit.Entrypoints {
		ep := &unit.Entrypoints[i]
		var selected *yaml.Node
		if forgeScalar(project, "layout") == "multi-module" {
			for _, key := range []string{"apps", "services"} {
				n := forgeNode(project, "workspace", key)
				if pattern := forgeScalar(n); pattern != "" && matchGlob(join(root, pattern), unit.Path) {
					selected = n
					break
				}
			}
		} else {
			for _, key := range []string{"cmd", "apps"} {
				n := forgeNode(project, "structure", key)
				value := forgeScalar(n)
				base, valid := forgeRelative(root, value)
				if value == "" || !valid {
					continue
				}
				if dir(ep.Path) == base || key == "apps" && path.Base(ep.Path) == "cmd" && dir(dir(ep.Path)) == base {
					selected = n
					break
				}
			}
		}
		if selected != nil {
			ep.Evidence = append(ep.Evidence, forgeEvidence(file, selected, "Forge project application path"))
		}
	}
}

func forgeConfigName(repo discovery.Repo, report *discovery.Report, ep *discovery.Entrypoint, root string) string {
	for d := ep.Path; within(d, root); d = dir(d) {
		for _, file := range []string{join(d, ".forge.yaml"), join(d, ".forge.yml")} {
			if !repo.Has(file) {
				continue
			}
			doc := forgeReadYAML(repo, report, file)
			if doc != nil {
				if name := forgeScalar(doc, "app", "name"); name != "" {
					return name
				}
				if targets := forgeNode(doc, "build", "apps"); targets != nil && targets.Kind == yaml.SequenceNode {
					for _, target := range targets.Content {
						targetPath, valid := forgeBuildTarget(repo, d, target)
						if valid && ep.Path == targetPath {
							if name := forgeScalar(target, "name"); name != "" {
								return name
							}
						}
					}
				}
			}
			break
		}
		if d == root {
			break
		}
	}
	return ep.Name
}

func forgeBuildTarget(repo discovery.Repo, root string, target *yaml.Node) (string, bool) {
	cmd := forgeScalar(target, "cmd")
	module := forgeScalar(target, "module")
	if cmd == "" && module == "" {
		return "", false
	}
	value := cmd
	if value == "" {
		value = module
	}
	targetPath, valid := forgeRelative(root, value)
	if !valid {
		return "", false
	}
	if strings.HasSuffix(targetPath, ".go") {
		targetPath = dir(targetPath)
	}
	if cmd == "" && !repo.Has(join(targetPath, "main.go")) && repo.Has(join(targetPath, "cmd", "main.go")) {
		targetPath = join(targetPath, "cmd")
	}
	if module != "" && cmd != "" && !repo.Has(join(targetPath, "main.go")) {
		if moduleRoot, ok := forgeRelative(root, module); ok {
			relCmd, cmdOK := forgeRelative(moduleRoot, cmd)
			if strings.HasSuffix(relCmd, ".go") {
				relCmd = dir(relCmd)
			}
			if cmdOK && repo.Has(join(relCmd, "main.go")) {
				targetPath = relCmd
			}
		}
	}
	return targetPath, true
}

// forgeAppConfigDirs keeps a declared app's config ahead of executable and module fallbacks.
func forgeAppConfigDirs(repo discovery.Repo, report *discovery.Report, executable, root string) []string {
	var appDir, moduleDir string
	for d := executable; within(d, root); d = dir(d) {
		if moduleDir == "" && repo.Has(join(d, "go.mod")) {
			moduleDir = d
		}
		if appDir == "" {
			for _, file := range []string{join(d, ".forge.yaml"), join(d, ".forge.yml")} {
				if !repo.Has(file) {
					continue
				}
				if doc := forgeReadYAML(repo, report, file); doc != nil {
					if app := forgeNode(doc, "app"); app != nil && app.Kind == yaml.MappingNode {
						appDir = d
					}
				}
				break
			}
		}
		if d == root {
			break
		}
	}
	var dirs []string
	if appDir != "" {
		dirs = append(dirs, appDir)
	}
	dirs = append(dirs, executable)
	if moduleDir != "" && moduleDir != root && moduleDir != appDir && moduleDir != executable {
		dirs = append(dirs, moduleDir)
	}
	return dirs
}

func forgeDisabled(node *yaml.Node) bool {
	for _, key := range []string{"enabled", "disabled"} {
		value := forgeNode(node, key)
		if value == nil || value.Kind != yaml.ScalarNode || value.Tag != "!!bool" {
			continue
		}
		var flag bool
		if value.Decode(&flag) == nil && (key == "enabled" && !flag || key == "disabled" && flag) {
			return true
		}
	}
	return false
}

// forgeConfigDriver returns only a driver name or a DSN scheme, never a full endpoint.
func forgeConfigDriver(backend forgeBackend, item *yaml.Node) (string, *yaml.Node, bool) {
	configured := false
	for _, key := range backend.drivers {
		node := forgeNode(item, key)
		if node != nil {
			configured = true
		}
		if value := forgeScalar(node); value != "" {
			return strings.ToLower(value), node, true
		}
	}
	var endpointKeys [][]string
	switch backend.extension {
	case "grove", "grove_kv":
		endpointKeys = [][]string{{"dsn"}}
	case "cache", "queue", "search":
		endpointKeys = [][]string{{"url"}}
	case "events":
		endpointKeys = [][]string{{"config", "url"}, {"url"}}
	}
	for _, keys := range endpointKeys {
		node := forgeNode(item, keys...)
		if node == nil {
			continue
		}
		configured = true
		value := forgeScalar(node)
		scheme, _, ok := strings.Cut(value, ":")
		if !ok || scheme == "" {
			continue
		}
		valid := true
		for _, ch := range scheme {
			if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '+' || ch == '-' || ch == '.' {
				continue
			}
			valid = false
			break
		}
		if valid {
			return strings.ToLower(scheme), node, true
		}
	}
	return "", nil, configured
}
