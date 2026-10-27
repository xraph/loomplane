package recipes_test

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

type goldenRecipe struct {
	Kind, Version, Architecture, Target string
	Recipe                              json.RawMessage
}

// builtinRecipes rebuilds testdata/builtin-recipes.json, which was captured
// from the recipe tables before the registry existed. lookup reads each
// recipe the way the caller under test does.
func builtinRecipes(t *testing.T, lookup func(kind, version, architecture, target string) (*recipes.Recipe, error)) []byte {
	t.Helper()
	var out []goldenRecipe
	add := func(kind, version, architecture, target, label string) {
		recipe, err := lookup(kind, version, architecture, target)
		if err != nil {
			t.Fatalf("%s %s %s %s: %v", kind, version, architecture, target, err)
		}
		b, err := json.Marshal(recipe)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, goldenRecipe{kind, version, architecture, label, b})
	}
	for _, kind := range []string{"postgres", "mysql", "mariadb", "redis", "valkey"} {
		for _, architecture := range []string{"amd64", "arm64"} {
			add(kind, recipes.DefaultVersion(kind), architecture, "", "lookup")
			add(kind, recipes.DefaultVersion(kind), architecture, "server", "server")
		}
	}
	for _, architecture := range []string{"amd64", "arm64"} {
		add("postgres", "17", architecture, "kubernetes", "kubernetes")
		add("postgres", "17", architecture, "kubernetes-postgres", "kubernetes-postgres")
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func TestBuiltInRecipesAreByteIdentical(t *testing.T) {
	golden, err := os.ReadFile("testdata/builtin-recipes.json")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(golden, []byte(`"family"`)) {
		t.Fatal("golden file has a family key")
	}
	today := func(kind, version, architecture, target string) (*recipes.Recipe, error) {
		switch target {
		case "":
			return recipes.Lookup(kind, version, architecture)
		case "kubernetes-postgres":
			return recipes.KubernetesPostgres(version, architecture)
		}
		return recipes.LookupForTarget(kind, version, architecture, target)
	}
	if got := builtinRecipes(t, today); !bytes.Equal(got, golden) {
		t.Fatalf("built-in recipes changed:\n%s", got)
	}
	registry := func(kind, version, architecture, target string) (*recipes.Recipe, error) {
		switch target {
		case "":
			target = "server"
		case "kubernetes-postgres":
			target = "kubernetes"
		}
		return recipes.Default().LookupEngine(kind, version, architecture, target)
	}
	if got := builtinRecipes(t, registry); !bytes.Equal(got, golden) {
		t.Fatalf("registry recipes differ from the built-in tables:\n%s", got)
	}
}

func TestRegisterAcceptsSample(t *testing.T) {
	r := recipestest.Registry(t)
	k, ok := r.Kind("sample")
	if !ok {
		t.Fatal("sample not registered")
	}
	if k.Components[0].Timeout != 10*time.Minute || k.Components[1].Resources != (recipes.Resources{CPUMillis: 500, MemoryMB: 256}) {
		t.Fatalf("defaults not applied: %+v", k.Components)
	}
	if k.Components[1].Protection != recipes.ProtectionNone || k.Secrets[0].Bytes != 32 || k.Secrets[0].Encoding != "base64url" {
		t.Fatalf("defaults not applied: %+v %+v", k.Components[1], k.Secrets)
	}
	if _, ok := r.Kind("postgres"); ok {
		t.Fatal("an engine is not a composed kind")
	}
	if !r.Composed("sample") || r.Composed("postgres") || r.Composed("nope") {
		t.Fatal("Composed is wrong")
	}
	if recipes.Composed("sample") {
		t.Fatal("the default registry has no sample kind")
	}
}

func TestRegisterCopiesTheKind(t *testing.T) {
	r := recipes.NewRegistry()
	k := recipestest.Sample()
	external := k.Dependencies[0].External
	external.Preload, external.TLS = []string{"timescaledb"}, []string{"disable"}
	if err := r.Register(k); err != nil {
		t.Fatal(err)
	}
	k.Components[0].Name = "changed"
	k.Releases[0].Images["api"].Digests["arm64"] = "sha256:changed"
	external.Preload[0], external.TLS[0] = "changed", "changed"
	got, _ := r.Kind("sample")
	if got.Components[0].Name != "migrate" || got.Releases[0].Images["api"].Digests["arm64"] == "sha256:changed" ||
		got.Dependencies[0].External.Preload[0] != "timescaledb" || got.Dependencies[0].External.TLS[0] != "disable" {
		t.Fatal("registry shares memory with the caller")
	}
	got.Components[1].Ports[0].Container = 1
	got.Dependencies[0].External.Preload[0] = "changed"
	again, _ := r.Kind("sample")
	if again.Components[1].Ports[0].Container != 8080 || again.Dependencies[0].External.Preload[0] != "timescaledb" {
		t.Fatal("registry shares memory with Kind's result")
	}
}

func TestRegisterRejectsInvalidKinds(t *testing.T) {
	component := func(k *recipes.Kind, name string) *recipes.Component {
		for i := range k.Components {
			if k.Components[i].Name == name {
				return &k.Components[i]
			}
		}
		t.Fatalf("no component %s", name)
		return nil
	}
	for _, test := range []struct {
		name, want string
		change     func(k *recipes.Kind)
	}{
		{"missing arm64 digest", "no arm64 digest", func(k *recipes.Kind) {
			k.Releases[0].Images["migrate"] = recipes.Image{Repository: "registry.example/sample-migrate", Digests: map[string]string{"amd64": k.Releases[0].Images["migrate"].Digests["amd64"]}}
		}},
		{"digest not sha256", "sha256:", func(k *recipes.Kind) {
			k.Releases[0].Images["migrate"] = recipes.Image{Repository: "registry.example/sample-migrate", Digests: map[string]string{"amd64": "sha256:abc", "arm64": k.Releases[0].Images["migrate"].Digests["arm64"]}}
		}},
		{"unknown architecture", "riscv64", func(k *recipes.Kind) {
			k.Releases[0].Images["migrate"].Digests["riscv64"] = k.Releases[0].Images["migrate"].Digests["arm64"]
		}},
		{"component without image", "no image", func(k *recipes.Kind) { delete(k.Releases[0].Images, "worker") }},
		{"process without readiness", "readiness", func(k *recipes.Kind) { component(k, "api").Readiness = nil }},
		{"readiness on undeclared port", `"metrics"`, func(k *recipes.Kind) {
			component(k, "api").Readiness = &recipes.HTTPProbe{Port: "metrics", Path: "/readyz"}
		}},
		{"job with ports", "job", func(k *recipes.Kind) {
			component(k, "migrate").Ports = []recipes.Port{{Name: "http", Container: 8080, Protocol: "http"}}
		}},
		{"job timeout over an hour", "hour", func(k *recipes.Kind) { component(k, "migrate").Timeout = 2 * time.Hour }},
		{"two components named alike", "more than once", func(k *recipes.Kind) { component(k, "worker").Name = "api" }},
		{"component name with double hyphen", "--", func(k *recipes.Kind) {
			k.Components = append(k.Components, recipes.Component{Name: "a--b", Role: recipes.RoleJob})
		}},
		{"after unknown component", `"cron"`, func(k *recipes.Kind) { component(k, "worker").After = []string{"cron"} }},
		{"after cycle", "cycle", func(k *recipes.Kind) { component(k, "migrate").After = []string{"worker"} }},
		{"ref to undeclared dependency", `"search"`, func(k *recipes.Kind) {
			component(k, "api").Env[0].From = &recipes.Ref{Dependency: "search", Field: recipes.FieldURL}
		}},
		{"secret env naming undeclared secret", `"session-key"`, func(k *recipes.Kind) { component(k, "api").Env[2].Secret = "session-key" }},
		{"output with from and per consumer", "per consumer", func(k *recipes.Kind) { k.Outputs[0].PerConsumer = true }},
		{"name clashes with a built-in engine", "already", func(k *recipes.Kind) { k.Name = "postgres" }},
		{"volume on a job", "volume", func(k *recipes.Kind) {
			c := component(k, "migrate")
			c.Volume, c.Protection = &recipes.Volume{MountPath: "/data"}, recipes.ProtectionRebuildable
		}},
		{"native protection on a component", "native", func(k *recipes.Kind) { component(k, "api").Protection = recipes.ProtectionNative }},
		{"port both routable and control", "both", func(k *recipes.Kind) { component(k, "api").Ports[1].Routable = true }},
		{"routable tcp port", "routable", func(k *recipes.Kind) {
			c := component(k, "api")
			c.Ports[0].Protocol = "tcp"
			c.Readiness = &recipes.HTTPProbe{Port: "admin", Path: "/readyz"}
		}},
		{"invalid kind name", "name", func(k *recipes.Kind) { k.Name = "Sample" }},
		{"unknown target", "vercel", func(k *recipes.Kind) { k.Targets = []string{"server", "vercel"} }},
		{"file outside the allowed paths", "/proc", func(k *recipes.Kind) {
			component(k, "api").Files = []recipes.File{{Path: "/proc/self/environ"}}
		}},
		{"unknown external TLS mode", `"prefer"`, func(k *recipes.Kind) { k.Dependencies[0].External.TLS = []string{"prefer"} }},
		{"preload on a redis endpoint", "postgres", func(k *recipes.Kind) {
			k.Dependencies[0].External = &recipes.Requirement{Engine: "redis", Preload: []string{"timescaledb"}}
		}},
		{"create role on a redis endpoint", "postgres", func(k *recipes.Kind) {
			k.Dependencies[0].External = &recipes.Requirement{Engine: "redis", CreateRole: true}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			k := recipestest.Sample()
			test.change(&k)
			err := recipes.NewRegistry().Register(k)
			if err == nil {
				t.Fatal("invalid kind accepted")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %q does not mention %q", err, test.want)
			}
		})
	}
}

func TestRegisterReportsEveryProblem(t *testing.T) {
	k := recipestest.Sample()
	k.Components[1].Readiness = nil
	k.Outputs[0].PerConsumer = true
	err := recipes.NewRegistry().Register(k)
	if err == nil || !strings.Contains(err.Error(), "readiness") || !strings.Contains(err.Error(), "per consumer") {
		t.Fatalf("want both problems, got %v", err)
	}
}

func TestMustRegisterPanicsOnInvalidKinds(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	k := recipestest.Sample()
	k.Name = "postgres"
	recipes.MustRegister(k)
}

func pgtest() recipes.Engine {
	return recipes.Engine{
		Kind: "pgtest", Family: "postgres", Title: "PG Test", EnvKey: "DATABASE_URL",
		Targets: []string{"server", "kubernetes"},
		Releases: []recipes.EngineRelease{{
			Version: "16", Product: "16.4", Revision: "1",
			Image: recipes.Image{Repository: "registry.example/pgtest", Digests: map[string]string{
				"amd64": "sha256:11538f20d0d489fad2dc7d30eeaafd731bd8405e25b1696d86571282ab6cdf25",
				"arm64": "sha256:61581a49b789716d4da7a45ce7fd3c944b60150492ccece97b11b0b497f49aa8",
			}},
			MountPath: "/var/lib/postgresql/data", BackupFormat: "pg-dump-custom-v1", KubernetesVolumeMB: 4096,
		}},
	}
}

func TestEngineRegistrationReusesFamily(t *testing.T) {
	r := recipes.NewRegistry()
	if err := r.RegisterEngine(pgtest()); err != nil {
		t.Fatal(err)
	}
	recipe, err := r.LookupEngine("pgtest", "16", "arm64", "server")
	if err != nil {
		t.Fatal(err)
	}
	want := recipes.Recipe{ID: "pgtest-16", Revision: "1", ProductVersion: "16.4", Image: "registry.example/pgtest@sha256:61581a49b789716d4da7a45ce7fd3c944b60150492ccece97b11b0b497f49aa8", Architecture: "arm64", MountPath: "/var/lib/postgresql/data", BackupFormat: "pg-dump-custom-v1", Family: "postgres"}
	if !reflect.DeepEqual(*recipe, want) {
		t.Fatalf("got %+v", recipe)
	}
	if recipe.Engine() != "postgres" || recipe.Port() != 5432 {
		t.Fatalf("family not reused: %q %d", recipe.Engine(), recipe.Port())
	}
	cluster, err := r.LookupEngine("pgtest", "", "amd64", "kubernetes")
	if err != nil {
		t.Fatal(err)
	}
	if cluster.ID != "pgtest-16-kubernetes" || cluster.VolumeSizeMB != 4096 {
		t.Fatalf("got %+v", cluster)
	}
	if r.FamilyOf("pgtest") != "postgres" || recipes.FamilyOf("mysql") != "mysql" || recipes.FamilyOf("nope") != "" || r.FamilyOf("pgtest") != "postgres" {
		t.Fatal("FamilyOf is wrong")
	}
	if _, err := r.LookupEngine("pgtest", "15", "arm64", "server"); err == nil {
		t.Fatal("unknown version accepted")
	}
	if _, err := r.LookupEngine("pgtest", "16", "riscv64", "server"); err == nil {
		t.Fatal("unknown architecture accepted")
	}
	if _, err := r.LookupEngine("pgtest", "16", "arm64", "vercel"); err == nil {
		t.Fatal("unknown target accepted")
	}
	if _, err := r.LookupEngine("nope", "16", "arm64", "server"); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestRegisterEngineRejectsInvalidEngines(t *testing.T) {
	for _, test := range []struct {
		name, want string
		change     func(e *recipes.Engine)
	}{
		{"unknown family", "family", func(e *recipes.Engine) { e.Family = "mongodb" }},
		{"kubernetes outside postgres", "kubernetes", func(e *recipes.Engine) {
			e.Family = "mysql"
			e.Releases[0].BackupFormat = "mysqldump-sql-v1"
		}},
		{"wrong backup format", "pg-dump-custom-v1", func(e *recipes.Engine) { e.Releases[0].BackupFormat = "pg-dump-plain-v1" }},
		{"missing arm64 digest", "no arm64 digest", func(e *recipes.Engine) { delete(e.Releases[0].Image.Digests, "arm64") }},
		{"name clashes with a built-in engine", "already", func(e *recipes.Engine) { e.Kind = "postgres" }},
		{"no releases", "release", func(e *recipes.Engine) { e.Releases = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := pgtest()
			test.change(&e)
			err := recipes.NewRegistry().RegisterEngine(e)
			if err == nil {
				t.Fatal("invalid engine accepted")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %q does not mention %q", err, test.want)
			}
		})
	}
	r := recipestest.Registry(t)
	e := pgtest()
	e.Kind = "sample"
	if err := r.RegisterEngine(e); err == nil {
		t.Fatal("engine named like a composed kind accepted")
	}
}

func TestEnvKeyComesFromTheRegistry(t *testing.T) {
	for kind, want := range map[string]string{"postgres": "DATABASE_URL", "mysql": "DATABASE_URL", "mariadb": "DATABASE_URL", "redis": "REDIS_URL", "valkey": "REDIS_URL", "nope": ""} {
		if got := recipes.EnvKey(kind); got != want {
			t.Fatalf("EnvKey(%s) = %q, want %q", kind, got, want)
		}
	}
	r := recipestest.Registry(t)
	if got := r.EnvKey("sample"); got != "SAMPLE_URL" {
		t.Fatalf("EnvKey(sample) = %q", got)
	}
	if err := r.RegisterEngine(pgtest()); err != nil {
		t.Fatal(err)
	}
	if got := r.EnvKey("pgtest"); got != "DATABASE_URL" {
		t.Fatalf("EnvKey(pgtest) = %q", got)
	}
}

func TestKindsListsBuiltInsFirst(t *testing.T) {
	r := recipestest.Registry(t)
	if err := r.RegisterEngine(pgtest()); err != nil {
		t.Fatal(err)
	}
	infos := r.Kinds()
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	if got := strings.Join(names, ","); got != "postgres,mysql,mariadb,redis,valkey,gpusample,pgtest,sample" {
		t.Fatalf("kinds in the wrong order: %s", got)
	}
	postgres, sample := infos[0], infos[7]
	if !postgres.Engine || strings.Join(postgres.Versions, ",") != "17" || strings.Join(postgres.Targets, ",") != "server,kubernetes" || postgres.Placement != recipes.PlacementServiceProject {
		t.Fatalf("postgres info: %+v", postgres)
	}
	if !infos[3].Engine || strings.Join(infos[3].Targets, ",") != "server" {
		t.Fatalf("redis info: %+v", infos[3])
	}
	if sample.Engine || sample.Placement != recipes.PlacementAppProject || len(sample.Components) != 3 || len(sample.Dependencies) != 2 || len(sample.Outputs) != 2 {
		t.Fatalf("sample info: %+v", sample)
	}
	if d := sample.Dependencies[0]; d.Name != "postgres" || !d.External || d.Optional {
		t.Fatalf("dependency info: %+v", d)
	}
	if c := sample.Components[1]; c.Name != "api" || c.Role != recipes.RoleProcess || c.Stateful || c.Protection != recipes.ProtectionNone {
		t.Fatalf("component info: %+v", c)
	}
	if o := sample.Outputs[0]; o.Name != "url" || o.EnvKey != "SAMPLE_URL" || o.PerConsumer {
		t.Fatalf("output info: %+v", o)
	}
}

func TestBuiltInEnginesAreValid(t *testing.T) {
	r := recipes.NewRegistry()
	for _, kind := range []string{"postgres", "mysql", "mariadb", "redis", "valkey"} {
		e, ok := r.Engine(kind)
		if !ok {
			t.Fatalf("no built-in %s", kind)
		}
		e.Kind = "copy-of-" + kind
		if err := r.RegisterEngine(e); err != nil {
			t.Fatalf("built-in %s does not pass validation: %v", kind, err)
		}
		if got := recipes.DefaultVersion(kind); got != e.Releases[0].Version {
			t.Fatalf("%s default version %s, descriptor %s", kind, got, e.Releases[0].Version)
		}
	}
}

// The default registry is shared by the whole test binary, and -count
// reruns tests in one process, so the engine goes in once.
var registerDefaultEngine sync.Once

func TestDefaultRegistryServesRegisteredEngines(t *testing.T) {
	registerDefaultEngine.Do(func() {
		e := pgtest()
		e.Kind = "pgdefault"
		recipes.MustRegisterEngine(e)
	})
	if got := recipes.DefaultVersion("pgdefault"); got != "16" {
		t.Fatalf("DefaultVersion(pgdefault) = %q", got)
	}
	if recipes.DefaultVersion("nope") != "" {
		t.Fatal("unknown kind has a default version")
	}
	recipe, err := recipes.LookupForTarget("pgdefault", "16", "amd64", "kubernetes")
	if err != nil {
		t.Fatal(err)
	}
	if recipe.ID != "pgdefault-16-kubernetes" || recipe.Engine() != "postgres" || recipe.VolumeSizeMB != 4096 {
		t.Fatalf("got %+v", recipe)
	}
	if recipes.FamilyOf("pgdefault") != "postgres" || recipes.EnvKey("pgdefault") != "DATABASE_URL" || recipes.Composed("pgdefault") {
		t.Fatal("package functions do not read the default registry")
	}
	if _, err := recipes.LookupForTarget("nope", "1", "amd64", "server"); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestFilesRejectUnsafePaths(t *testing.T) {
	register := func(p string) error {
		k := recipestest.Sample()
		k.Components[1].Files = []recipes.File{{Path: p}}
		return recipes.NewRegistry().Register(k)
	}
	for p, want := range map[string]string{
		"etc/x":     "clean absolute path",
		"/etc/../x": "clean absolute path",
		"/etc/x/":   "clean absolute path",
		"/":         "names no file",
		"/proc/x":   "/proc",
		"/sys/x":    "/proc, /sys or /dev",
		"/dev":      "/proc, /sys or /dev",
	} {
		err := register(p)
		if err == nil {
			t.Fatalf("file %q accepted", p)
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("file %q: error %q does not mention %q", p, err, want)
		}
	}
	if err := register("/etc/sample/config.yaml"); err != nil {
		t.Fatalf("a safe file path: %v", err)
	}
	if err := register("/processes/config.yaml"); err != nil {
		t.Fatalf("a path that only starts like /proc: %v", err)
	}
}

func TestFilesRejectNestedAndRepeatedPaths(t *testing.T) {
	for _, test := range []struct {
		files []recipes.File
		want  string
	}{
		{[]recipes.File{{Path: "/etc/a"}, {Path: "/etc/a/b"}}, `file "/etc/a/b" sits under file "/etc/a"`},
		{[]recipes.File{{Path: "/etc/a/b"}, {Path: "/etc/a"}}, `file "/etc/a/b" sits under file "/etc/a"`},
		{[]recipes.File{{Path: "/etc/a"}, {Path: "/etc/a"}}, `declares file "/etc/a" more than once`},
	} {
		k := recipestest.Sample()
		k.Components[1].Files = test.files
		err := recipes.NewRegistry().Register(k)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("files %v: %v", test.files, err)
		}
	}
	k := recipestest.Sample()
	k.Components[1].Files = []recipes.File{{Path: "/etc/a"}, {Path: "/etc/ab/c"}}
	if err := recipes.NewRegistry().Register(k); err != nil {
		t.Fatalf("siblings that share a prefix: %v", err)
	}
}

// oneFinding registers k and wants exactly one finding that mentions want.
func oneFinding(t *testing.T, r *recipes.Registry, k recipes.Kind, want string) {
	t.Helper()
	err := r.Register(k)
	if err == nil {
		t.Fatal("invalid kind accepted")
	}
	if strings.Count(err.Error(), "\n") != 0 {
		t.Fatalf("want one finding, got:\n%v", err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not mention %q", err, want)
	}
}

func TestRegisterRejectsUnsafeKubernetesRules(t *testing.T) {
	safe := func() recipes.KubernetesRule {
		return recipes.KubernetesRule{
			APIGroups: []string{"", "discovery.k8s.io"},
			Resources: []string{"services", "endpoints", "endpointslices"},
			Verbs:     []string{"get", "list", "watch"},
		}
	}
	k := recipestest.Sample()
	k.Components[1].KubernetesRules = []recipes.KubernetesRule{safe()}
	if err := recipes.NewRegistry().Register(k); err != nil {
		t.Fatalf("read-only discovery rules refused: %v", err)
	}
	for _, test := range []struct {
		name, want string
		change     func(k *recipes.Kind)
	}{
		{"verb create", `"create"`, func(k *recipes.Kind) {
			rule := safe()
			rule.Verbs = append(rule.Verbs, "create")
			k.Components[1].KubernetesRules = []recipes.KubernetesRule{rule}
		}},
		{"resource secrets", `"secrets"`, func(k *recipes.Kind) {
			rule := safe()
			rule.Resources = []string{"secrets"}
			k.Components[1].KubernetesRules = []recipes.KubernetesRule{rule}
		}},
		{"group rbac", `"rbac.authorization.k8s.io"`, func(k *recipes.Kind) {
			rule := safe()
			rule.APIGroups = []string{"rbac.authorization.k8s.io"}
			k.Components[1].KubernetesRules = []recipes.KubernetesRule{rule}
		}},
		{"rules on a job", "only processes", func(k *recipes.Kind) {
			k.Components[0].KubernetesRules = []recipes.KubernetesRule{safe()}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			k := recipestest.Sample()
			test.change(&k)
			oneFinding(t, recipes.NewRegistry(), k, test.want)
		})
	}
}

func TestRegisterValidatesFabriqAdditions(t *testing.T) {
	graph := recipestest.Graph()
	if err := recipes.NewRegistry().Register(graph); err != nil {
		t.Fatalf("dependency-only kind refused: %v", err)
	}
	withGraph := func(t *testing.T) *recipes.Registry {
		r := recipes.NewRegistry()
		if err := r.Register(recipestest.Graph()); err != nil {
			t.Fatal(err)
		}
		return r
	}
	plain := func(*testing.T) *recipes.Registry { return recipes.NewRegistry() }
	process := func(name string) recipes.Component {
		return recipes.Component{
			Name: name, Role: recipes.RoleProcess,
			Ports:     []recipes.Port{{Name: "resp", Container: 6380, Protocol: "redis"}},
			Readiness: &recipes.HTTPProbe{Port: "resp"},
		}
	}
	for _, test := range []struct {
		name, want string
		base       func() recipes.Kind
		registry   func(t *testing.T) *recipes.Registry
		change     func(k *recipes.Kind)
	}{
		{"dependency-only kind with two processes", "exactly one process", recipestest.Graph, plain, func(k *recipes.Kind) {
			k.Components = append(k.Components, process("replica"))
			k.Releases[0].Images["replica"] = k.Releases[0].Images["server"]
		}},
		{"dependency-only kind with a job", "exactly one process and no other components", recipestest.Graph, plain, func(k *recipes.Kind) {
			k.Components = append([]recipes.Component{{Name: "seed", Role: recipes.RoleJob}}, k.Components...)
			k.Releases[0].Images["seed"] = k.Releases[0].Images["server"]
		}},
		{"dependency-only kind with only a job", "exactly one process and no other components", recipestest.Graph, plain, func(k *recipes.Kind) {
			k.Components = []recipes.Component{{Name: "server", Role: recipes.RoleJob, Env: k.Components[0].Env}}
			k.Outputs[0].Template = "redis://:${secret:password}@graph"
		}},
		{"dependency-only kind with dependencies", "no dependencies", recipestest.Graph, plain, func(k *recipes.Kind) {
			k.Dependencies = []recipes.Dependency{{Name: "postgres", Title: "PostgreSQL", Kinds: []string{"postgres"}}}
		}},
		{"dependency-only kind with a per consumer output", "per consumer", recipestest.Graph, plain, func(k *recipes.Kind) {
			k.Outputs = append(k.Outputs, recipes.Output{Name: "tenant", EnvKey: "GRAPH_TENANT", PerConsumer: true})
		}},
		{"secret prefix in upper case", `"FQ_"`, recipestest.Sample, plain, func(k *recipes.Kind) { k.Secrets[0].Prefix = "FQ_" }},
		{"env with template and value", "exactly one of value, secret, from and template", recipestest.Sample, plain, func(k *recipes.Kind) {
			k.Components[1].Env = append(k.Components[1].Env, recipes.EnvVar{Name: "BOTH", Value: "x", Template: "${secret:admin-password}"})
		}},
		{"unknown placeholder", `"${env:X}"`, recipestest.Sample, plain, func(k *recipes.Kind) {
			k.Components[1].Env = append(k.Components[1].Env, recipes.EnvVar{Name: "X", Template: "a-${env:X}"})
		}},
		{"optional dependency without requires", `optional dependency "cache"`, recipestest.Sample, plain, func(k *recipes.Kind) {
			k.Components[1].Env[1].Requires = ""
		}},
		{"requires a required dependency", "isn't optional", recipestest.Sample, plain, func(k *recipes.Kind) {
			k.Components[2].Requires = "postgres"
		}},
		{"kinds mixing a dependency-only kind", "dependency-only", recipestest.Sample, withGraph, func(k *recipes.Kind) {
			k.Dependencies[1].Kinds = []string{"graph", "redis"}
		}},
		{"dependency-only kind with external", "external", recipestest.Sample, withGraph, func(k *recipes.Kind) {
			k.Dependencies = append(k.Dependencies, recipes.Dependency{Name: "graph", Title: "Graph", Kinds: []string{"graph"}, Optional: true, External: &recipes.Requirement{Engine: "redis"}})
		}},
		{"routable redis port", "routable", recipestest.Sample, plain, func(k *recipes.Kind) {
			k.Components[1].Ports = append(k.Components[1].Ports, recipes.Port{Name: "resp", Container: 6379, Protocol: "redis", Routable: true})
		}},
		{"redis readiness with a path", "path", recipestest.Graph, plain, func(k *recipes.Kind) {
			k.Components[0].Readiness.Path = "/ping"
		}},
		{"consumer option name in upper case", `"Tenant"`, recipestest.Sample, plain, func(k *recipes.Kind) {
			k.ConsumerOptions = []recipes.ConsumerOption{{Name: "Tenant", Title: "Tenant"}}
		}},
		{"consumer option pattern that doesn't compile", "pattern", recipestest.Sample, plain, func(k *recipes.Kind) {
			k.ConsumerOptions = []recipes.ConsumerOption{{Name: "tenant", Title: "Tenant", Pattern: "("}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			k := test.base()
			test.change(&k)
			oneFinding(t, test.registry(t), k, test.want)
		})
	}
}

func TestRegisterChecksDependencyFields(t *testing.T) {
	r := recipes.NewRegistry()
	if err := r.Register(recipestest.Graph()); err != nil {
		t.Fatal(err)
	}
	graphDependency := recipes.Dependency{Name: "graph", Title: "Graph", Kinds: []string{"graph"}, Optional: true}
	reads := func(dependency recipes.Dependency, template string) recipes.Kind {
		k := recipestest.Sample()
		if dependency.Name != "postgres" {
			k.Dependencies = append(k.Dependencies, dependency)
		}
		v := recipes.EnvVar{Name: "READS", Template: template}
		if dependency.Optional {
			v.Requires = dependency.Name
		}
		k.Components[1].Env = append(k.Components[1].Env, v)
		return k
	}
	for _, ok := range []struct {
		dependency recipes.Dependency
		template   string
	}{
		{graphDependency, "${dep:graph:url}"},
		{graphDependency, "${dep:graph:output:url}"},
		{graphDependency, "${dep:graph:secret:password}"},
		{recipestest.Sample().Dependencies[0], "${dep:postgres:admin-url}"},
		{recipestest.Sample().Dependencies[0], "${dep:postgres:hostport}/${dep:postgres:database}?${dep:postgres:query}"},
	} {
		if err := r.Register(reads(ok.dependency, ok.template)); err != nil {
			t.Fatalf("%s refused: %v", ok.template, err)
		}
		r = recipes.NewRegistry()
		if err := r.Register(recipestest.Graph()); err != nil {
			t.Fatal(err)
		}
	}
	for _, bad := range []struct {
		dependency recipes.Dependency
		template   string
		want       string
	}{
		{graphDependency, "${dep:graph:host}", `"host"`},
		{graphDependency, "${dep:graph:output:nope}", `"output:nope"`},
		{graphDependency, "${dep:graph:secret:nope}", `"secret:nope"`},
		{recipes.Dependency{Name: "cache2", Title: "Cache", Kinds: []string{"redis"}}, "${dep:cache2:admin-url}", "postgres"},
		{recipes.Dependency{Name: "cache2", Title: "Cache", Kinds: []string{"redis"}}, "${dep:cache2:output:x}", "dependency-only"},
		{recipestest.Sample().Dependencies[0], "${dep:postgres:url", "unterminated"},
		{recipestest.Sample().Dependencies[0], "${secret:nope}", `"nope"`},
		{recipestest.Sample().Dependencies[0], "${component:api:url:nope}", `"nope"`},
	} {
		oneFinding(t, r, reads(bad.dependency, bad.template), bad.want)
	}

	// Whichever registers first, the later one is refused.
	mixed := recipestest.Sample()
	mixed.Dependencies[1].Kinds = []string{"graph", "redis"}
	late := recipes.NewRegistry()
	if err := late.Register(mixed); err != nil {
		t.Fatalf("a kind accepting an unregistered kind: %v", err)
	}
	if err := late.Register(recipestest.Graph()); err == nil || !strings.Contains(err.Error(), "sample") {
		t.Fatalf("dependency-only kind registered under a mixed dependency: %v", err)
	}
	admin := reads(recipes.Dependency{Name: "store", Title: "Store", Kinds: []string{"pgtest"}}, "${dep:store:admin-url}")
	late = recipes.NewRegistry()
	if err := late.Register(admin); err != nil {
		t.Fatalf("a kind accepting an unregistered engine: %v", err)
	}
	notPostgres := pgtest()
	notPostgres.Family, notPostgres.Targets = "mysql", []string{"server"}
	notPostgres.Releases[0].BackupFormat = "mysqldump-sql-v1"
	if err := late.RegisterEngine(notPostgres); err == nil || !strings.Contains(err.Error(), "admin-url") {
		t.Fatalf("a mysql engine registered under an admin-url read: %v", err)
	}
	if err := late.RegisterEngine(pgtest()); err != nil {
		t.Fatalf("a postgres-family engine refused: %v", err)
	}
}

func TestOutputsMayReadDependenciesAndTemplates(t *testing.T) {
	k := recipestest.Sample()
	k.Outputs = append(k.Outputs,
		recipes.Output{Name: "database", EnvKey: "SAMPLE_DATABASE_URL", Secret: true, From: &recipes.Ref{Dependency: "postgres", Field: recipes.FieldURL}},
		recipes.Output{Name: "api", EnvKey: "SAMPLE_API", Template: "${component:api:url:http}/v1"},
		recipes.Output{Name: "cache", EnvKey: "SAMPLE_CACHE", Template: "${dep:cache:url}", Requires: "cache"},
	)
	if err := recipes.NewRegistry().Register(k); err != nil {
		t.Fatalf("outputs refused: %v", err)
	}
	k.Outputs[len(k.Outputs)-1].Requires = ""
	oneFinding(t, recipes.NewRegistry(), k, `optional dependency "cache"`)
	k = recipestest.Sample()
	k.Outputs[1] = recipes.Output{Name: "admin", EnvKey: "SAMPLE_ADMIN_URL", Template: "x", From: &recipes.Ref{Component: "api", Field: "url:admin"}}
	oneFinding(t, recipes.NewRegistry(), k, "exactly one of from, template and per consumer")
}

func TestEnvVarNamesAllowDots(t *testing.T) {
	register := func(name string) error {
		k := recipestest.Sample()
		k.Components[1].Env = append(k.Components[1].Env, recipes.EnvVar{Name: name, Value: "x"})
		return recipes.NewRegistry().Register(k)
	}
	if err := register("FABRIQ_ES.ADDRS"); err != nil {
		t.Fatalf("dotted name refused: %v", err)
	}
	for _, name := range []string{"1X", "A-B"} {
		if err := register(name); err == nil {
			t.Fatalf("env name %q accepted", name)
		}
	}
}

func TestEngineReleaseCarriesExtensionsAndFSGroup(t *testing.T) {
	r := recipes.NewRegistry()
	e := pgtest()
	e.Releases[0].Extensions = []string{"timescaledb", "vector", "postgis"}
	e.Releases[0].FSGroup = 1000
	if err := r.RegisterEngine(e); err != nil {
		t.Fatal(err)
	}
	e.Releases[0].Extensions[0] = "changed"
	for _, target := range []string{"server", "kubernetes"} {
		recipe, err := r.LookupEngine("pgtest", "16", "amd64", target)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(recipe.Extensions, ",") != "timescaledb,vector,postgis" || recipe.FSGroup != 1000 {
			t.Fatalf("%s recipe: %+v", target, recipe)
		}
		b, err := json.Marshal(recipe)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"extensions":["timescaledb","vector","postgis"]`) || !strings.Contains(string(b), `"fs_group":1000`) {
			t.Fatalf("%s recipe JSON: %s", target, b)
		}
	}
	for _, kind := range []string{"postgres", "mysql", "mariadb", "redis", "valkey"} {
		targets := []string{"server"}
		if kind == "postgres" {
			targets = append(targets, "kubernetes")
		}
		for _, target := range targets {
			recipe, err := recipes.LookupForTarget(kind, recipes.DefaultVersion(kind), "arm64", target)
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(recipe)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), `"extensions"`) || strings.Contains(string(b), `"fs_group"`) {
				t.Fatalf("built-in %s on %s: %s", kind, target, b)
			}
		}
	}
	bad := pgtest()
	bad.Releases[0].FSGroup = -1
	if err := recipes.NewRegistry().RegisterEngine(bad); err == nil || !strings.Contains(err.Error(), "fs group") {
		t.Fatalf("negative fs group: %v", err)
	}
	bad = pgtest()
	bad.Releases[0].Extensions = []string{"vector", "vector"}
	if err := recipes.NewRegistry().RegisterEngine(bad); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("repeated extension: %v", err)
	}
}

func TestKindInfoListsConsumerOptions(t *testing.T) {
	k := recipestest.Sample()
	k.ConsumerOptions = []recipes.ConsumerOption{{Name: "tenant", Title: "Tenant", Pattern: "^[a-z0-9-]{1,63}$", Required: true}}
	r := recipes.NewRegistry()
	if err := r.Register(k); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(recipestest.Graph()); err != nil {
		t.Fatal(err)
	}
	var sample, graph recipes.KindInfo
	for _, info := range r.Kinds() {
		switch info.Name {
		case "sample":
			sample = info
		case "graph":
			graph = info
		}
	}
	want := []recipes.ConsumerOption{{Name: "tenant", Title: "Tenant", Pattern: "^[a-z0-9-]{1,63}$", Required: true}}
	if !reflect.DeepEqual(sample.ConsumerOptions, want) {
		t.Fatalf("consumer options: %+v", sample.ConsumerOptions)
	}
	sample.ConsumerOptions[0].Name = "changed"
	if got, _ := r.Kind("sample"); got.ConsumerOptions[0].Name != "tenant" {
		t.Fatal("KindInfo shares memory with the registry")
	}
	if graph.Placement != recipes.PlacementDependency {
		t.Fatalf("dependency-only kind info: %+v", graph)
	}
	b, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "consumer_options") {
		t.Fatalf("empty consumer options are listed: %s", b)
	}
}
