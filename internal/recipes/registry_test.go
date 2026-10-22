package recipes_test

import (
	"bytes"
	"encoding/json"
	"os"
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
	if err := r.Register(k); err != nil {
		t.Fatal(err)
	}
	k.Components[0].Name = "changed"
	k.Releases[0].Images["api"].Digests["arm64"] = "sha256:changed"
	got, _ := r.Kind("sample")
	if got.Components[0].Name != "migrate" || got.Releases[0].Images["api"].Digests["arm64"] == "sha256:changed" {
		t.Fatal("registry shares memory with the caller")
	}
	got.Components[1].Ports[0].Container = 1
	again, _ := r.Kind("sample")
	if again.Components[1].Ports[0].Container != 8080 {
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
	if *recipe != want {
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
	if got := strings.Join(names, ","); got != "postgres,mysql,mariadb,redis,valkey,pgtest,sample" {
		t.Fatalf("kinds in the wrong order: %s", got)
	}
	postgres, sample := infos[0], infos[6]
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
