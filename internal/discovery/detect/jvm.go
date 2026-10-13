package detect

import (
	"context"
	"encoding/xml"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/xraph/loomplane/pkg/discovery"
)

// JVM finds Maven and Gradle projects, the multi-module builds that tie
// them together, which modules run (an application plugin, or a main in
// their sources), and what their dependencies say they serve and talk to.
// Java and Kotlin are both the JVM to Loomplane: they build and run alike.
// Build files are read as text and XML. Gradle scripts are programs, and
// nothing here runs them, so only the plain declarations are understood.
type JVM struct{}

// Name implements discovery.Detector.
func (JVM) Name() string { return "jvm" }

// maxJVMSources caps how many source files one module's main is looked for
// in. A main is near the top of src/main; a module with thousands of files
// that hasn't shown one yet isn't going to.
const maxJVMSources = 2000

type pom struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Packaging  string `xml:"packaging"`
	Parent     struct {
		GroupID string `xml:"groupId"`
	} `xml:"parent"`
	Properties struct {
		JavaVersion string `xml:"java.version"`
		Release     string `xml:"maven.compiler.release"`
		Source      string `xml:"maven.compiler.source"`
	} `xml:"properties"`
	Modules      []string `xml:"modules>module"`
	Dependencies []struct {
		GroupID    string `xml:"groupId"`
		ArtifactID string `xml:"artifactId"`
		Version    string `xml:"version"`
		Scope      string `xml:"scope"`
	} `xml:"dependencies>dependency"`
	Plugins []struct {
		GroupID    string `xml:"groupId"`
		ArtifactID string `xml:"artifactId"`
	} `xml:"build>plugins>plugin"`
}

type jvmDep struct {
	coord, version string
	project        string // a module of this build, by directory
	ev             discovery.Evidence
}

type jvmModule struct {
	dir, file, tool string
	name, runtime   string
	coord           string // group:artifact, for Maven
	deps            []jvmDep
	app             *discovery.Evidence
	plugins         []jvmPlugin
	// shared is what a root build declares for its projects, in a
	// subprojects or allprojects block.
	shared     *jvmModule
	members    []string
	ev         discovery.Evidence
	aggregator bool
}

// jvmPlugin is a plugin a Gradle build applies, by id.
type jvmPlugin struct {
	id string
	ev discovery.Evidence
}

// jvmBuildLogic are the plugins of a build that makes Gradle plugins, not
// an application: buildSrc, or an included build-logic build.
var jvmBuildLogic = map[string]bool{"kotlin-dsl": true, "groovy-gradle-plugin": true, "java-gradle-plugin": true}

type jvmMatch struct {
	prefix, name string
	kind         discovery.FrameworkKind
}

// jvmFrameworks maps group:artifact prefixes to frameworks.
var jvmFrameworks = []jvmMatch{
	{"org.springframework.boot:spring-boot-starter-web", "spring-boot", discovery.HTTP},
	{"org.springframework.boot:spring-boot-starter-graphql", "spring-boot", discovery.HTTP},
	{"io.quarkus:quarkus-rest", "quarkus", discovery.HTTP},
	{"io.quarkus:quarkus-resteasy", "quarkus", discovery.HTTP},
	{"io.quarkus:quarkus-vertx-http", "quarkus", discovery.HTTP},
	{"io.micronaut:micronaut-http-server", "micronaut", discovery.HTTP},
	{"io.ktor:ktor-server", "ktor", discovery.HTTP},
	{"io.javalin:javalin", "javalin", discovery.HTTP},
	{"com.sparkjava:spark-core", "spark", discovery.HTTP},
	{"io.dropwizard:dropwizard-core", "dropwizard", discovery.HTTP},
	{"io.helidon.webserver:helidon-webserver", "helidon", discovery.HTTP},
	{"io.vertx:vertx-web", "vert.x", discovery.HTTP},
	{"io.grpc:grpc-netty", "grpc", discovery.GRPC},
	{"net.devh:grpc-server-spring-boot-starter", "grpc", discovery.GRPC},
	{"info.picocli:picocli", "picocli", discovery.CLI},
	{"com.github.ajalt.clikt:clikt", "clikt", discovery.CLI},
}

// jvmResources maps group:artifact prefixes to backing services.
var jvmResources = []struct{ prefix, kind string }{
	{"org.postgresql:postgresql", "postgres"},
	{"org.postgresql:r2dbc-postgresql", "postgres"},
	{"com.mysql:mysql-connector-j", "mysql"},
	{"mysql:mysql-connector-java", "mysql"},
	{"org.mariadb.jdbc:mariadb-java-client", "mysql"},
	{"redis.clients:jedis", "redis"},
	{"io.lettuce:lettuce-core", "redis"},
	{"org.redisson:redisson", "redis"},
	{"org.springframework.boot:spring-boot-starter-data-redis", "redis"},
	{"org.mongodb:", "mongodb"},
	{"org.springframework.boot:spring-boot-starter-data-mongodb", "mongodb"},
	{"com.rabbitmq:amqp-client", "rabbitmq"},
	{"org.springframework.boot:spring-boot-starter-amqp", "rabbitmq"},
	{"org.apache.kafka:kafka-clients", "kafka"},
	{"org.springframework.kafka:spring-kafka", "kafka"},
	{"io.nats:jnats", "nats"},
	{"software.amazon.awssdk:s3", "object-storage"},
	{"com.google.cloud:google-cloud-storage", "object-storage"},
	{"io.minio:minio", "object-storage"},
	{"co.elastic.clients:elasticsearch-java", "search"},
	{"org.opensearch.client:", "search"},
	{"org.springframework.boot:spring-boot-starter-data-elasticsearch", "search"},
	{"com.clickhouse:", "clickhouse"},
}

// jvmAppPlugins are the build plugins that make a module something that
// runs, rather than a library.
var jvmAppPlugins = map[string]bool{
	"org.springframework.boot": true, "application": true, "io.quarkus": true,
	"io.micronaut.application": true, "io.ktor.plugin": true,
	"org.springframework.boot:spring-boot-maven-plugin": true,
	"io.quarkus:quarkus-maven-plugin":                   true,
	"io.quarkus.platform:quarkus-maven-plugin":          true,
	"io.micronaut.maven:micronaut-maven-plugin":         true,
	"org.codehaus.mojo:exec-maven-plugin":               true,
}

var (
	gradleDepRE     = regexp.MustCompile(`\b(?:implementation|api|compileOnly|runtimeOnly|compile|runtime)\s*\(?\s*["']([^:"'\s]+):([^:"'\s]+)(?::([^"'\s]+))?["']`)
	gradleProjectRE = regexp.MustCompile(`\b(?:implementation|api|compileOnly|runtimeOnly|compile|runtime)\s*\(?\s*project\s*\(\s*(?:path\s*[:=]\s*)?["']:?([^"']+)["']`)
	gradleCatalogRE = regexp.MustCompile(`\b(?:implementation|api|compileOnly|runtimeOnly)\s*\(?\s*libs\.([A-Za-z0-9_.]+)`)
	gradlePluginRE  = regexp.MustCompile(`^\s*(?:id\s*\(?\s*["']([A-Za-z0-9_.-]+)["']|apply\s+plugin\s*:\s*["']([A-Za-z0-9_.-]+)["']|(application)\s*$|` + "`" + `([A-Za-z0-9_.-]+)` + "`" + `)`)
	gradleIncludeRE = regexp.MustCompile(`^\s*include\b(.*)$`)
	gradleMapDepRE  = regexp.MustCompile(`\b(?:implementation|api|compileOnly|runtimeOnly|compile|runtime)\s*\(?\s*group\s*[:=]\s*["']([^"']+)["']\s*,\s*name\s*[:=]\s*["']([^"']+)["'](?:\s*,\s*version\s*[:=]\s*["']([^"']+)["'])?`)
	gradleSharedRE  = regexp.MustCompile(`^\s*(subprojects|allprojects)\s*\{`)
	gradleScriptRE  = regexp.MustCompile(`/src/main/(?:kotlin|groovy)/(.+)\.gradle(?:\.kts)?$`)
	quotedRE        = regexp.MustCompile(`["']([^"']+)["']`)
	gradleJavaRE    = regexp.MustCompile(`JavaVersion\.VERSION_(\d+)|jvmToolchain\s*\(\s*(\d+)\s*\)|JavaLanguageVersion\.of\s*\(\s*(\d+)\s*\)`)
	jvmMainRE       = regexp.MustCompile(`@SpringBootApplication|@QuarkusMain|\bpublic\s+static\s+void\s+main\s*\(|^\s*fun\s+main\s*\(`)
)

// Detect implements discovery.Detector.
func (JVM) Detect(ctx context.Context, repo discovery.Repo, r *discovery.Report) error {
	byDir := map[string]*jvmModule{}
	catalogs := map[string]map[string]string{} // by build root
	for _, f := range repo.Named("libs.versions.toml") {
		if path.Base(dir(f)) == "gradle" {
			catalogs[dir(dir(f))] = readCatalog(repo, f)
		}
	}
	for _, f := range repo.Named("pom.xml") {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m := readPom(repo, r, f); m != nil {
			byDir[m.dir] = m
		}
	}
	var settings []string
	for _, f := range repo.Named("build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts") {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.HasPrefix(path.Base(f), "settings.") {
			settings = append(settings, f)
			continue
		}
		if _, ok := byDir[dir(f)]; ok {
			continue // a Maven build is already here
		}
		if m := readGradle(repo, r, f, catalogFor(catalogs, dir(f))); m != nil {
			byDir[m.dir] = m
		}
	}
	for _, f := range settings {
		readSettings(repo, r, f, byDir)
	}
	conventions := gradleConventions(repo, r, catalogs)
	ordered := make([]string, 0, len(byDir))
	for d := range byDir {
		ordered = append(ordered, d)
	}
	sort.Strings(ordered)
	for _, d := range ordered {
		m := byDir[d]
		if m.shared == nil {
			continue
		}
		for _, x := range m.members {
			if member := byDir[x]; member != nil && member != m {
				member.deps = append(member.deps, m.shared.deps...)
				member.plugins = append(member.plugins, m.shared.plugins...)
			}
		}
	}
	for _, d := range ordered {
		if m := byDir[d]; m.tool == "gradle" {
			gradleApply(m, conventions)
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
	coords := map[string]string{}
	for _, d := range dirs {
		if c := byDir[d].coord; c != "" {
			coords[c] = d
		}
	}
	for _, d := range dirs {
		m := byDir[d]
		if len(m.members) > 0 {
			var members []string
			for _, x := range m.members {
				if _, ok := byDir[x]; ok {
					members = append(members, x)
				}
			}
			sort.Strings(members)
			r.Workspaces = append(r.Workspaces, discovery.Workspace{
				Ecosystem: discovery.JVM, Tool: m.tool, Path: d, Members: members, Evidence: []discovery.Evidence{m.ev},
			})
		}
		if m.aggregator {
			continue // a parent build only gathers modules
		}
		r.Units = append(r.Units, jvmUnit(repo, m, byDir, coords))
	}
	return nil
}

// catalogFor is the version catalog of the build root nearest d.
func catalogFor(catalogs map[string]map[string]string, d string) map[string]string {
	roots := make([]string, 0, len(catalogs))
	for root := range catalogs {
		roots = append(roots, root)
	}
	if root := owner(d, roots); root != "" {
		return catalogs[root]
	}
	return nil
}

// readCatalog maps a version catalog's library accessors (libs.spring.web)
// to their group:artifact.
func readCatalog(repo discovery.Repo, f string) map[string]string {
	data, n := read(repo, f)
	if n != nil {
		return nil
	}
	var c struct {
		Libraries map[string]any `toml:"libraries"`
	}
	if _, err := toml.Decode(string(data), &c); err != nil {
		return nil
	}
	out := map[string]string{}
	for alias, spec := range c.Libraries {
		key := pyNormalizeRE.ReplaceAllString(alias, ".")
		switch s := spec.(type) {
		case string:
			parts := strings.Split(s, ":")
			if len(parts) >= 2 {
				out[key] = parts[0] + ":" + parts[1]
			}
		case map[string]any:
			if mod, ok := s["module"].(string); ok {
				out[key] = mod
			} else if g, ok := s["group"].(string); ok {
				if name, ok := s["name"].(string); ok {
					out[key] = g + ":" + name
				}
			}
		}
	}
	return out
}

func readPom(repo discovery.Repo, r *discovery.Report, f string) *jvmModule {
	data, n := read(repo, f)
	if n != nil {
		note(r, n)
		return nil
	}
	var p pom
	if err := xml.Unmarshal(data, &p); err != nil {
		note(r, &discovery.Note{Path: f, Message: "not a valid pom.xml"})
		return nil
	}
	group := p.GroupID
	if group == "" {
		group = p.Parent.GroupID
	}
	m := &jvmModule{
		dir: dir(f), file: f, tool: "maven", name: p.ArtifactID, coord: group + ":" + p.ArtifactID,
		ev:         evidence(data, f, lineOf(data, regexp.MustCompile(`<artifactId>`), 1)),
		aggregator: p.Packaging == "pom",
	}
	for _, v := range []string{p.Properties.JavaVersion, p.Properties.Release, p.Properties.Source} {
		if v != "" {
			m.runtime = "java " + v
			break
		}
	}
	for _, mod := range p.Modules {
		m.members = append(m.members, join(m.dir, strings.TrimSpace(mod)))
	}
	for _, d := range p.Dependencies {
		if d.Scope == "test" || d.ArtifactID == "" {
			continue
		}
		line := lineOf(data, regexp.MustCompile(`<artifactId>\s*`+regexp.QuoteMeta(d.ArtifactID)+`\s*</artifactId>`), 1)
		m.deps = append(m.deps, jvmDep{coord: d.GroupID + ":" + d.ArtifactID, version: d.Version, ev: evidence(data, f, line)})
	}
	for _, pl := range p.Plugins {
		if jvmAppPlugins[pl.GroupID+":"+pl.ArtifactID] {
			ev := evidence(data, f, lineOf(data, regexp.MustCompile(`<artifactId>\s*`+regexp.QuoteMeta(pl.ArtifactID)+`\s*</artifactId>`), 1))
			m.app = &ev
		}
	}
	return m
}

func readGradle(repo discovery.Repo, r *discovery.Report, f string, catalog map[string]string) *jvmModule {
	data, n := read(repo, f)
	if n != nil {
		note(r, n)
		return nil
	}
	m := &jvmModule{dir: dir(f), file: f, tool: "gradle", name: path.Base(dir(f)), ev: discovery.Evidence{Path: f, Detail: path.Base(f)}}
	if m.dir == "." {
		m.name = "app"
	}
	// Braces say when a subprojects or allprojects block ends. What's in
	// one is for the projects under this build; allprojects is for this
	// one too.
	depth, sharedAt, all := 0, -1, false
	for i, line := range strings.Split(string(data), "\n") {
		code, _, _ := strings.Cut(line, "//")
		ev := discovery.Evidence{Path: f, Line: i + 1, Detail: strings.TrimSpace(line)}
		if s := gradleSharedRE.FindStringSubmatch(code); s != nil && sharedAt < 0 {
			sharedAt, all = depth, s[1] == "allprojects"
			if m.shared == nil {
				m.shared = &jvmModule{}
			}
		}
		targets := []*jvmModule{m}
		if sharedAt >= 0 {
			targets = []*jvmModule{m.shared}
			if all {
				targets = append(targets, m)
			}
		}
		for _, t := range targets {
			gradleLine(t, code, ev, catalog)
		}
		if s := gradleJavaRE.FindStringSubmatch(code); s != nil && m.runtime == "" {
			for _, v := range s[1:] {
				if v != "" {
					m.runtime = "java " + v
				}
			}
		}
		depth += strings.Count(code, "{") - strings.Count(code, "}")
		if sharedAt >= 0 && depth <= sharedAt {
			sharedAt = -1
		}
	}
	return m
}

// gradleLine reads one line of a build script's plugins and dependencies
// into m.
func gradleLine(m *jvmModule, code string, ev discovery.Evidence, catalog map[string]string) {
	if s := gradlePluginRE.FindStringSubmatch(code); s != nil {
		for _, id := range s[1:] {
			if id != "" {
				m.plugins = append(m.plugins, jvmPlugin{id: id, ev: ev})
			}
		}
	}
	for _, s := range gradleProjectRE.FindAllStringSubmatch(code, -1) {
		m.deps = append(m.deps, jvmDep{project: gradleProjectDir(s[1]), ev: ev})
	}
	for _, s := range gradleDepRE.FindAllStringSubmatch(code, -1) {
		m.deps = append(m.deps, jvmDep{coord: s[1] + ":" + s[2], version: s[3], ev: ev})
	}
	for _, s := range gradleMapDepRE.FindAllStringSubmatch(code, -1) {
		m.deps = append(m.deps, jvmDep{coord: s[1] + ":" + s[2], version: s[3], ev: ev})
	}
	for _, s := range gradleCatalogRE.FindAllStringSubmatch(code, -1) {
		if coord, ok := catalog[s[1]]; ok {
			m.deps = append(m.deps, jvmDep{coord: coord, ev: ev})
		}
	}
}

// gradleConventions reads the repository's precompiled script plugins
// (buildSrc/src/main/kotlin/shop.spring.gradle.kts, or the same in an
// included build-logic build), by the id a build applies them with.
func gradleConventions(repo discovery.Repo, r *discovery.Report, catalogs map[string]map[string]string) map[string]*jvmModule {
	out := map[string]*jvmModule{}
	for _, f := range repo.Files {
		s := gradleScriptRE.FindStringSubmatch(f)
		if s == nil {
			continue
		}
		if m := readGradle(repo, r, f, catalogFor(catalogs, dir(f))); m != nil {
			out[path.Base(s[1])] = m
			out[strings.ReplaceAll(s[1], "/", ".")] = m
		}
	}
	return out
}

// gradleApply adds what the convention plugins a module applies declare,
// and what those apply in turn, to the module.
func gradleApply(m *jvmModule, conventions map[string]*jvmModule) {
	seen := map[string]bool{}
	for i := 0; i < len(m.plugins); i++ {
		id := m.plugins[i].id
		c, ok := conventions[id]
		if !ok || seen[id] || c == m {
			continue
		}
		seen[id] = true
		m.deps = append(m.deps, c.deps...)
		m.plugins = append(m.plugins, c.plugins...)
		if m.runtime == "" {
			m.runtime = c.runtime
		}
	}
	for _, p := range m.plugins {
		if jvmAppPlugins[p.id] && m.app == nil {
			ev := p.ev
			m.app = &ev
		}
		if jvmBuildLogic[p.id] || path.Base(m.dir) == "buildSrc" {
			m.aggregator = true // it builds plugins, and runs nothing
		}
	}
}

// gradleProjectDir is a Gradle project path (:services:api) as the
// directory it is by default (services/api).
func gradleProjectDir(p string) string {
	return path.Clean(strings.ReplaceAll(strings.Trim(p, ":"), ":", "/"))
}

// readSettings reads a settings file's includes into a workspace at its
// directory. A root build that only gathers its projects isn't one itself.
func readSettings(repo discovery.Repo, r *discovery.Report, f string, byDir map[string]*jvmModule) {
	data, n := read(repo, f)
	if n != nil {
		note(r, n)
		return
	}
	root := dir(f)
	var members []string
	first := 0
	for i, line := range strings.Split(string(data), "\n") {
		code, _, _ := strings.Cut(line, "//")
		s := gradleIncludeRE.FindStringSubmatch(code)
		if s == nil {
			continue
		}
		if first == 0 {
			first = i + 1
		}
		for _, q := range quotedRE.FindAllStringSubmatch(s[1], -1) {
			members = append(members, join(root, gradleProjectDir(q[1])))
		}
	}
	if len(members) == 0 {
		return
	}
	m := byDir[root]
	if m == nil {
		m = &jvmModule{dir: root, file: f, tool: "gradle", aggregator: true}
		byDir[root] = m
	} else if !hasSources(repo, root) {
		m.aggregator = true
	}
	m.members = members
	m.ev = evidence(data, f, first)
}

// hasSources reports whether a module has code of its own in src/main.
func hasSources(repo discovery.Repo, d string) bool {
	src := join(d, "src/main")
	for _, f := range repo.Files {
		if within(f, src) {
			return true
		}
	}
	return false
}

// jvmMain finds a main in a module's sources: an application class, or a
// main method or function.
func jvmMain(repo discovery.Repo, d string) *discovery.Evidence {
	src := join(d, "src/main")
	checked := 0
	for _, f := range repo.Files {
		if !within(f, src) || (!strings.HasSuffix(f, ".java") && !strings.HasSuffix(f, ".kt")) {
			continue
		}
		checked++
		if checked > maxJVMSources {
			return nil
		}
		data, n := read(repo, f)
		if n != nil {
			continue
		}
		if l := lineOf(data, jvmMainRE, 1); l > 0 {
			ev := evidence(data, f, l)
			return &ev
		}
	}
	return nil
}

func jvmUnit(repo discovery.Repo, m *jvmModule, byDir map[string]*jvmModule, coords map[string]string) discovery.Unit {
	u := discovery.Unit{
		ID: discovery.UnitID(discovery.JVM, m.dir), Ecosystem: discovery.JVM,
		Name: m.name, Path: m.dir, Runtime: m.runtime, Evidence: []discovery.Evidence{m.ev},
	}
	seen := map[string]bool{}
	for _, d := range m.deps {
		target := d.project
		if target != "" {
			target = join(gradleRoot(m.dir, byDir), target)
		} else {
			target = coords[d.coord]
		}
		if target != "" {
			if _, ok := byDir[target]; ok && target != m.dir && !seen["unit:"+target] {
				seen["unit:"+target] = true
				u.DependsOn = append(u.DependsOn, discovery.Dependency{Unit: discovery.UnitID(discovery.JVM, target), Evidence: d.ev})
			}
			continue
		}
		if d.coord == "" || seen[d.coord] {
			continue
		}
		seen[d.coord] = true
		u.Requires = append(u.Requires, discovery.Requirement{Name: d.coord, Version: d.version, Evidence: d.ev})
		if fw, ok := jvmFramework(d.coord); ok {
			u.Frameworks = addFramework(u.Frameworks, discovery.Framework{Name: fw.name, Kind: fw.kind, Confidence: discovery.Likely, Evidence: d.ev})
		}
		for _, res := range jvmResources {
			if strings.HasPrefix(d.coord, res.prefix) {
				u.Resources = addResource(u.Resources, discovery.ResourceUse{Kind: res.kind, Confidence: discovery.Possible, Evidence: d.ev})
				break
			}
		}
	}
	sort.Slice(u.DependsOn, func(i, j int) bool { return u.DependsOn[i].Unit < u.DependsOn[j].Unit })
	sortFrameworks(u.Frameworks)
	sortResources(u.Resources)

	ep := discovery.Entrypoint{Name: m.name, Path: m.dir}
	if m.app != nil {
		ep.Evidence = append(ep.Evidence, *m.app)
	}
	if main := jvmMain(repo, m.dir); main != nil {
		ep.Evidence = append(ep.Evidence, *main)
	}
	if len(ep.Evidence) == 0 {
		return u // a library
	}
	ep.Frameworks = u.Frameworks
	ep.Resources = u.Resources
	u.Entrypoints = []discovery.Entrypoint{ep}
	return u
}

// jvmFramework is the framework a dependency is from, by the longest
// matching prefix.
func jvmFramework(coord string) (jvmMatch, bool) {
	var best jvmMatch
	for _, f := range jvmFrameworks {
		if strings.HasPrefix(coord, f.prefix) && len(f.prefix) > len(best.prefix) {
			best = f
		}
	}
	return best, best.prefix != ""
}

// gradleRoot is the directory of the Gradle build a module belongs to: the
// nearest one with a settings file, which is what project paths are from.
func gradleRoot(d string, byDir map[string]*jvmModule) string {
	var roots []string
	for root, m := range byDir {
		if m.tool == "gradle" && len(m.members) > 0 {
			roots = append(roots, root)
		}
	}
	if root := owner(d, roots); root != "" {
		return root
	}
	return "."
}
