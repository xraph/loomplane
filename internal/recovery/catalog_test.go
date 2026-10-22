package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
)

func TestRecoveryProfilesRequireMatchingEngineAndSupportedTarget(t *testing.T) {
	for _, kind := range []string{"postgres", "mysql", "mariadb", "redis", "valkey"} {
		recipe, err := recipes.Lookup(kind, recipes.DefaultVersion(kind), "arm64")
		if err != nil {
			t.Fatal(err)
		}
		cfg := planning.ResourceConfig{Provider: "server", Kind: kind, Catalog: recipe}
		if !supportsRecovery(&cfg) {
			t.Fatalf("qualified Docker profile refused: %s", kind)
		}
		cfg.Provider = "kubernetes"
		if supportsRecovery(&cfg) != (kind == "postgres") {
			t.Fatalf("unqualified Kubernetes profile accepted: %s", kind)
		}
		cfg.Provider = "server"
		cfg.Kind = "unreviewed-engine"
		if supportsRecovery(&cfg) {
			t.Fatal("recipe accepted for a different engine")
		}
	}
}

// pgFamilyRegistry registers an engine kind of the postgres family, on a
// registry of its own so Default stays untouched.
func pgFamilyRegistry(t *testing.T) *recipes.Registry {
	t.Helper()
	return engineRegistry(t, "pgtest", "PG Test")
}

// timescaleRegistry registers a TimescaleDB engine of the postgres family.
func timescaleRegistry(t *testing.T) *recipes.Registry {
	t.Helper()
	return engineRegistry(t, "timescaledb", "PostgreSQL 16 with TimescaleDB")
}

// engineRegistry registers one postgres-family engine on a registry of its
// own and points the package's registry seams at it.
func engineRegistry(t *testing.T, kind, title string) *recipes.Registry {
	t.Helper()
	r := recipes.NewRegistry()
	err := r.RegisterEngine(recipes.Engine{
		Kind: kind, Family: "postgres", Title: title, EnvKey: "DATABASE_URL",
		Targets: []string{"server", "kubernetes"},
		Releases: []recipes.EngineRelease{{
			Version: "16", Product: "16.4", Revision: "1",
			Image: recipes.Image{Repository: "registry.example/" + kind, Digests: map[string]string{
				"amd64": "sha256:11538f20d0d489fad2dc7d30eeaafd731bd8405e25b1696d86571282ab6cdf25",
				"arm64": "sha256:61581a49b789716d4da7a45ce7fd3c944b60150492ccece97b11b0b497f49aa8",
			}},
			MountPath: "/var/lib/postgresql/data", BackupFormat: "pg-dump-custom-v1", KubernetesVolumeMB: 4096,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	oldFamily, oldTitle := familyOf, titleOf
	familyOf, titleOf = r.FamilyOf, func(kind string) string { return engineTitle(r, kind) }
	t.Cleanup(func() { familyOf, titleOf = oldFamily, oldTitle })
	return r
}

func engineConfig(t *testing.T, provider, kind string) planning.ResourceConfig {
	t.Helper()
	recipe, err := recipes.Lookup(kind, recipes.DefaultVersion(kind), "arm64")
	if err != nil {
		t.Fatal(err)
	}
	return planning.ResourceConfig{Provider: provider, Kind: kind, Catalog: recipe}
}

func TestProtectionOfEachConfig(t *testing.T) {
	for _, kind := range []string{"postgres", "mysql", "mariadb", "redis", "valkey"} {
		cfg := engineConfig(t, "server", kind)
		if got := Protection(&cfg); got != recipes.ProtectionNative {
			t.Fatalf("%s on a server: %q", kind, got)
		}
	}
	pg := engineConfig(t, "kubernetes", "postgres")
	if got := Protection(&pg); got != recipes.ProtectionNative {
		t.Fatalf("postgres on kubernetes: %q", got)
	}
	redis := engineConfig(t, "kubernetes", "redis")
	if got := Protection(&redis); got != "" {
		t.Fatalf("redis on kubernetes has no qualified adapter: %q", got)
	}

	component := func(p recipes.Protection) *planning.ResourceConfig {
		return &planning.ResourceConfig{Provider: "server", Kind: "sample", Component: &planning.ComponentConfig{Kind: "sample", Resource: "app", Protection: p}}
	}
	cases := []struct {
		name string
		cfg  *planning.ResourceConfig
		want recipes.Protection
	}{
		{"stateless component", component(recipes.ProtectionNone), recipes.ProtectionNone},
		{"component with a volume", component(recipes.ProtectionRebuildable), recipes.ProtectionRebuildable},
		{"component without a recorded protection", component(""), recipes.ProtectionNone},
		{"composition parent", &planning.ResourceConfig{Provider: "server", Kind: "sample", Composition: &planning.CompositionConfig{Kind: "sample"}}, recipes.ProtectionNone},
		{"external dependency", &planning.ResourceConfig{Provider: planning.DependencyExternal, Kind: "postgres"}, recipes.ProtectionExternal},
	}
	for _, c := range cases {
		if got := Protection(c.cfg); got != c.want {
			t.Fatalf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRegisteredPostgresFamilyEngineGetsRecovery(t *testing.T) {
	r := pgFamilyRegistry(t)
	recipe, err := r.LookupEngine("pgtest", "16", "arm64", "server")
	if err != nil {
		t.Fatal(err)
	}
	cfg := planning.ResourceConfig{Provider: "server", Kind: "pgtest", Target: "connection", Catalog: recipe}
	if !supportsRecovery(&cfg) || Protection(&cfg) != recipes.ProtectionNative {
		t.Fatal("a postgres-family engine kind is refused native recovery")
	}
	cluster, err := r.LookupEngine("pgtest", "16", "arm64", "kubernetes")
	if err != nil {
		t.Fatal(err)
	}
	cfg = planning.ResourceConfig{Provider: "kubernetes", Kind: "pgtest", Target: "connection", Catalog: cluster}
	if supportsRecovery(&cfg) {
		t.Fatal("a registered postgres-family engine kind has no qualified Kubernetes adapter yet")
	}
	// The recipe has to be the family's, not another engine's.
	mysql := engineConfig(t, "server", "mysql")
	cfg = planning.ResourceConfig{Provider: "server", Kind: "pgtest", Catalog: mysql.Catalog}
	if supportsRecovery(&cfg) {
		t.Fatal("a recipe of another family was accepted")
	}

	b := retentionBackup(t, "backup", 1, false)
	b.Source.Config, err = json.Marshal(planning.ResourceConfig{Provider: "server", Kind: "pgtest", Target: "connection", Catalog: recipe})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := retentionScope(&b)
	if err != nil || scope.format != "pg-dump-custom-v1" {
		t.Fatalf("retention scope: %+v, %v", scope, err)
	}
}

func TestBuiltInRecoveryUnchanged(t *testing.T) {
	formats := map[string]string{"postgres": "pg-dump-custom-v1", "mysql": "mysqldump-sql-v1", "mariadb": "mariadb-dump-sql-v1", "redis": "redis-rdb-v1", "valkey": "valkey-rdb-v1"}
	for kind, format := range formats {
		cfg := engineConfig(t, "server", kind)
		cfg.Target = "connection"
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		b := retentionBackup(t, "backup", 1, false)
		b.Source.Config = raw
		scope, err := retentionScope(&b)
		if err != nil || scope.format != format {
			t.Fatalf("%s retention scope: %+v, %v", kind, scope, err)
		}
		if !supportsRecovery(&cfg) {
			t.Fatalf("%s lost recovery", kind)
		}
		cfg.Catalog.BackupFormat = "wal-v1"
		raw, _ = json.Marshal(cfg)
		b.Source.Config = raw
		if _, err := retentionScope(&b); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s accepted a chained backup format: %v", kind, err)
		}
	}
}

type allowAll struct{}

func (allowAll) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

type fixedProjects struct{}

func (fixedProjects) GetProject(context.Context, string, string) (*projects.Project, error) {
	return &projects.Project{}, nil
}
func (fixedProjects) ListEnvironments(context.Context, string, string) ([]*projects.Environment, error) {
	return []*projects.Environment{{ID: "environment", Name: "production"}}, nil
}

type fixedBindings []*planning.Binding

func (f fixedBindings) ListBindings(context.Context, string, string) ([]*planning.Binding, error) {
	return f, nil
}

func bindingFor(t *testing.T, name string, cfg planning.ResourceConfig) *planning.Binding {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &planning.Binding{ID: "binding-" + name, ProjectID: "project", EnvironmentID: "environment", Name: name, Subject: planning.ResourceSubject, Config: raw}
}

func TestCreateBackupForStatelessReportsNothingToRestore(t *testing.T) {
	service := Service{Authz: allowAll{}, Projects: fixedProjects{}, Bindings: fixedBindings{
		bindingFor(t, "app--api", planning.ResourceConfig{Provider: "server", Kind: "sample", Component: &planning.ComponentConfig{Kind: "sample", Resource: "app", Protection: recipes.ProtectionNone}}),
		bindingFor(t, "app--data", planning.ResourceConfig{Provider: "server", Kind: "sample", Component: &planning.ComponentConfig{Kind: "sample", Resource: "app", Protection: recipes.ProtectionRebuildable}}),
		bindingFor(t, "app", planning.ResourceConfig{Provider: "server", Kind: "sample", Composition: &planning.CompositionConfig{Kind: "sample"}}),
		bindingFor(t, "ext", planning.ResourceConfig{Provider: planning.DependencyExternal, Kind: "postgres"}),
	}}
	p := security.Principal{OrgID: "org", UserID: "user"}
	create := func(resource string) error {
		_, err := service.Create(t.Context(), p, "project", BackupCreateInput{Environment: "production", Resource: resource, RequestID: "request-123"})
		return err
	}

	err := create("app--api")
	if !errors.Is(err, ErrNothingToRestore) || !errors.Is(err, ErrInvalid) {
		t.Fatalf("stateless component: %v", err)
	}
	if want := "app--api keeps no data: it isn't backed up and there is nothing to restore"; err.Error() != want {
		t.Fatalf("message %q, want %q", err, want)
	}
	if err = create("app"); !errors.Is(err, ErrNothingToRestore) || err.Error() != "app keeps no data: it isn't backed up and there is nothing to restore" {
		t.Fatalf("composition parent: %v", err)
	}
	err = create("app--data")
	if !errors.Is(err, ErrRebuildable) || !errors.Is(err, ErrInvalid) || err.Error() != "app--data is rebuilt from its source, not backed up" {
		t.Fatalf("rebuildable component: %v", err)
	}
	err = create("ext")
	if !errors.Is(err, ErrNotManaged) || !errors.Is(err, ErrInvalid) || err.Error() != "Not managed by Loomplane: no backups or restores" {
		t.Fatalf("external dependency: %v", err)
	}

	policy := func(resource string) error {
		_, setErr := service.SetPolicy(t.Context(), p, "project", PolicyInput{Environment: "production", Resource: resource, EveryHours: 24, Enabled: true})
		return setErr
	}
	if err = policy("app--api"); !errors.Is(err, ErrNothingToRestore) {
		t.Fatalf("policy for a stateless component: %v", err)
	}
	if err = policy("app--data"); !errors.Is(err, ErrRebuildable) {
		t.Fatalf("policy for a rebuildable component: %v", err)
	}
	if err = policy("ext"); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("policy for an external dependency: %v", err)
	}
}

func TestProtectionOnKubernetesForNewEngines(t *testing.T) {
	r := timescaleRegistry(t)
	onServer, err := r.LookupEngine("timescaledb", "16", "arm64", "server")
	if err != nil {
		t.Fatal(err)
	}
	onCluster, err := r.LookupEngine("timescaledb", "16", "arm64", "kubernetes")
	if err != nil {
		t.Fatal(err)
	}
	ts := planning.ResourceConfig{Provider: "server", Kind: "timescaledb", Target: "connection", Catalog: onServer}
	if got := Protection(&ts); got != recipes.ProtectionNative {
		t.Fatalf("timescaledb on a server: %q", got)
	}
	ts = planning.ResourceConfig{Provider: "kubernetes", Kind: "timescaledb", Target: "connection", Catalog: onCluster}
	if got := Protection(&ts); got != "" {
		t.Fatalf("timescaledb on kubernetes: %q", got)
	}
	redis := engineConfig(t, "kubernetes", "redis")
	if got := Protection(&redis); got != "" {
		t.Fatalf("redis on kubernetes: %q", got)
	}
	pg := engineConfig(t, "kubernetes", "postgres")
	if got := Protection(&pg); got != recipes.ProtectionNative {
		t.Fatalf("postgres on kubernetes: %q", got)
	}
}

func TestCreateBackupOnKubernetesSaysNotYet(t *testing.T) {
	r := timescaleRegistry(t)
	onCluster, err := r.LookupEngine("timescaledb", "16", "arm64", "kubernetes")
	if err != nil {
		t.Fatal(err)
	}
	redis := engineConfig(t, "kubernetes", "redis")
	redis.Target = "connection"
	service := Service{Authz: allowAll{}, Projects: fixedProjects{}, Bindings: fixedBindings{
		bindingFor(t, "metrics", planning.ResourceConfig{Provider: "kubernetes", Kind: "timescaledb", Target: "connection", Catalog: onCluster}),
		bindingFor(t, "cache", redis),
	}}
	p := security.Principal{OrgID: "org", UserID: "user"}

	_, err = service.Create(t.Context(), p, "project", BackupCreateInput{Environment: "production", Resource: "metrics", RequestID: "request-123"})
	if !errors.Is(err, ErrNotOnKubernetes) || !errors.Is(err, ErrInvalid) {
		t.Fatalf("timescaledb on kubernetes: %v", err)
	}
	if want := "Backups for PostgreSQL 16 with TimescaleDB aren't available on Kubernetes yet."; err.Error() != want {
		t.Fatalf("message %q, want %q", err, want)
	}
	_, err = service.SetPolicy(t.Context(), p, "project", PolicyInput{Environment: "production", Resource: "metrics", EveryHours: 24, Enabled: true})
	if !errors.Is(err, ErrNotOnKubernetes) || err.Error() != "Backups for PostgreSQL 16 with TimescaleDB aren't available on Kubernetes yet." {
		t.Fatalf("policy for timescaledb on kubernetes: %v", err)
	}

	// A built-in that reaches the same path is named by its own title.
	titleOf = func(kind string) string { return engineTitle(recipes.Default(), kind) }
	_, err = service.Create(t.Context(), p, "project", BackupCreateInput{Environment: "production", Resource: "cache", RequestID: "request-123"})
	if !errors.Is(err, ErrNotOnKubernetes) || err.Error() != "Backups for Redis aren't available on Kubernetes yet." {
		t.Fatalf("redis on kubernetes: %v", err)
	}
}

// A combination that is not a Kubernetes gap keeps the plain refusal, on
// both the backup and the schedule path.
func TestUnsupportedRecoveryKeepsItsPlainRefusal(t *testing.T) {
	mysql := engineConfig(t, "server", "mysql")
	mysql.Kind = "unreviewed-engine"
	service := Service{Authz: allowAll{}, Projects: fixedProjects{}, Bindings: fixedBindings{bindingFor(t, "data", mysql)}}
	p := security.Principal{OrgID: "org", UserID: "user"}
	const want = "invalid service backup request: the resource has no supported database backup adapter"

	_, err := service.Create(t.Context(), p, "project", BackupCreateInput{Environment: "production", Resource: "data", RequestID: "request-123"})
	if !errors.Is(err, ErrInvalid) || errors.Is(err, ErrNotOnKubernetes) || err.Error() != want {
		t.Fatalf("backup: %v", err)
	}
	_, err = service.SetPolicy(t.Context(), p, "project", PolicyInput{Environment: "production", Resource: "data", EveryHours: 24, Enabled: true})
	if !errors.Is(err, ErrInvalid) || errors.Is(err, ErrNotOnKubernetes) || err.Error() != want {
		t.Fatalf("policy: %v", err)
	}
}
