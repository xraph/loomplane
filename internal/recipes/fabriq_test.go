package recipes_test

import (
	"crypto/rand"
	"encoding/hex"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/fabriqloom"
	"github.com/xraph/loomplane/internal/recipes"
)

func TestTimescaleDBEnginePins(t *testing.T) {
	r := recipes.Default()
	extensions := []string{"timescaledb", "vector", "postgis", "timescaledb_toolkit"}
	want := recipes.Recipe{
		ID: "timescaledb-16", Family: "postgres", Revision: "1", ProductVersion: "16.15-ts2.30.2",
		Image:        "docker.io/timescale/timescaledb-ha@sha256:10f7c9dfb1e860596be5a355a47cc508ee12daf26dd97d647a75b5731a80538c",
		Architecture: "amd64", MountPath: "/home/postgres/pgdata", BackupFormat: "pg-dump-custom-v1",
		Extensions: extensions, FSGroup: 1000,
	}
	got, err := r.LookupEngine("timescaledb", "16", "amd64", "server")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("got %+v", got)
	}
	if got.Engine() != "postgres" || got.Port() != 5432 {
		t.Fatalf("not run as the postgres family: %q %d", got.Engine(), got.Port())
	}

	arm, err := r.LookupEngine("timescaledb", "", "arm64", "server")
	if err != nil {
		t.Fatal(err)
	}
	if arm.Image != "docker.io/timescale/timescaledb-ha@sha256:f6e5b3fb4e839f116109675b5d1be56dfa7baa67f5272ad907117490694c4288" || arm.Architecture != "arm64" {
		t.Fatalf("got %+v", arm)
	}

	cluster, err := r.LookupEngine("timescaledb", "16", "amd64", "kubernetes")
	if err != nil {
		t.Fatal(err)
	}
	if cluster.ID != "timescaledb-16-kubernetes" || cluster.VolumeSizeMB != 10240 || cluster.FSGroup != 1000 {
		t.Fatalf("got %+v", cluster)
	}

	if _, err := r.LookupEngine("timescaledb", "16", "riscv64", "server"); err == nil {
		t.Fatal("unknown architecture accepted")
	}
	if _, err := r.LookupEngine("timescaledb", "15", "amd64", "server"); err == nil {
		t.Fatal("unknown version accepted")
	}
	if recipes.EnvKey("timescaledb") != "DATABASE_URL" || recipes.FamilyOf("timescaledb") != "postgres" {
		t.Fatalf("env key %q family %q", recipes.EnvKey("timescaledb"), recipes.FamilyOf("timescaledb"))
	}
	if recipes.DefaultVersion("timescaledb") != "16" {
		t.Fatalf("default version %q", recipes.DefaultVersion("timescaledb"))
	}
}

func TestFalkorDBKind(t *testing.T) {
	kind, ok := recipes.Default().Kind("falkordb")
	if !ok {
		t.Fatal("FalkorDB is not registered")
	}
	if kind.Title != "FalkorDB" || kind.Placement != recipes.PlacementDependency || !slices.Equal(kind.Targets, []string{"server", "kubernetes"}) {
		t.Fatalf("got %+v", kind)
	}
	if len(kind.Components) != 1 || len(kind.Dependencies) != 0 {
		t.Fatalf("components %d dependencies %d", len(kind.Components), len(kind.Dependencies))
	}
	c := kind.Components[0]
	if c.Name != "falkordb" || c.Role != recipes.RoleProcess || !slices.Equal(c.Command, []string{"sh", "-c"}) || len(c.Args) != 1 {
		t.Fatalf("got %+v", c)
	}
	if !reflect.DeepEqual(c.Ports, []recipes.Port{{Name: "redis", Container: 6379, Protocol: "redis"}}) || c.Readiness == nil || *c.Readiness != (recipes.HTTPProbe{Port: "redis"}) {
		t.Fatalf("ports %+v readiness %+v", c.Ports, c.Readiness)
	}
	if c.Volume == nil || *c.Volume != (recipes.Volume{MountPath: "/data", KubernetesSizeMB: 4096}) || c.Protection != recipes.ProtectionRebuildable {
		t.Fatalf("volume %+v protection %s", c.Volume, c.Protection)
	}
	if c.Resources != (recipes.Resources{CPUMillis: 500, MemoryMB: 512}) {
		t.Fatalf("resources %+v", c.Resources)
	}
	wantEnv := []recipes.EnvVar{{Name: "FALKORDB_PASSWORD", Secret: "password"}, {Name: "BROWSER", Value: "0"}}
	if !reflect.DeepEqual(c.Env, wantEnv) {
		t.Fatalf("env %+v", c.Env)
	}
	if len(kind.Secrets) != 1 || kind.Secrets[0].Name != "password" || kind.Secrets[0].Bytes != 32 || kind.Secrets[0].Encoding != "hex" {
		t.Fatalf("secrets %+v", kind.Secrets)
	}
	if len(kind.Outputs) != 1 || kind.Outputs[0].Name != "url" || kind.Outputs[0].EnvKey != "FALKORDB_ADDR" || kind.Outputs[0].From == nil || *kind.Outputs[0].From != (recipes.Ref{Component: "falkordb", Field: "address:redis"}) {
		t.Fatalf("outputs %+v", kind.Outputs)
	}

	// The password reaches the script through the environment and is hashed
	// before it is written anywhere. It never appears in argv or the config.
	script := c.Args[0]
	if strings.Count(script, "$FALKORDB_PASSWORD") != 1 || !strings.Contains(script, `printf '%s' "$FALKORDB_PASSWORD" | sha256sum`) {
		t.Fatalf("the password is used outside the sha256 line:\n%s", script)
	}
	for _, line := range []string{
		"bind 0.0.0.0", "port 6379", "protected-mode yes", "dir /data", "appendonly no", "save 60 1000",
		"user default on #${hash} ~* &* +@all",
		"loadmodule /FalkorDB/bin/src/falkordb.so MAX_QUEUED_QUERIES 25 TIMEOUT 1000 RESULTSET_SIZE 10000",
	} {
		if !strings.Contains(script, line+"\n") {
			t.Errorf("script lacks %q:\n%s", line, script)
		}
	}
	if !strings.Contains(script, "/tmp/falkordb.conf") || !strings.HasSuffix(script, "cd /data && exec docker-entrypoint.sh redis-server /tmp/falkordb.conf") {
		t.Fatalf("script does not end by starting redis-server:\n%s", script)
	}

	release := kind.Releases[0]
	image := release.Images["falkordb"]
	if release.Version != "4.2" || release.Product != "4.2.2" || image.Repository != "docker.io/falkordb/falkordb" {
		t.Fatalf("release %+v", release)
	}
	if image.Digests["amd64"] != "sha256:fe2cd0ed30c1df273b8bc1f7b467ff3ca006dee64e453de571563a9932c3aebb" || image.Digests["arm64"] != "sha256:c2a716eccab1b81f68343e44f6539a31beb5991b7523d8aa4e60551b2de18e51" {
		t.Fatalf("digests %+v", image.Digests)
	}
}

func TestElasticsearchKind(t *testing.T) {
	kind, ok := recipes.Default().Kind("elasticsearch")
	if !ok {
		t.Fatal("Elasticsearch is not registered")
	}
	if kind.Title != "Elasticsearch" || kind.Placement != recipes.PlacementDependency || len(kind.Components) != 1 || len(kind.Dependencies) != 0 {
		t.Fatalf("got %+v", kind)
	}
	c := kind.Components[0]
	if c.Name != "elasticsearch" || c.Role != recipes.RoleProcess {
		t.Fatalf("got %+v", c)
	}
	if !reflect.DeepEqual(c.Ports, []recipes.Port{{Name: "http", Container: 9200, Protocol: "http"}}) || c.Readiness == nil || *c.Readiness != (recipes.HTTPProbe{Port: "http", Path: "/_cluster/health?wait_for_status=yellow&timeout=5s"}) {
		t.Fatalf("ports %+v readiness %+v", c.Ports, c.Readiness)
	}
	if c.Volume == nil || *c.Volume != (recipes.Volume{MountPath: "/usr/share/elasticsearch/data", KubernetesSizeMB: 10240, FSGroup: 1000}) || c.Protection != recipes.ProtectionRebuildable {
		t.Fatalf("volume %+v protection %s", c.Volume, c.Protection)
	}
	if !reflect.DeepEqual(c.Files, []recipes.File{{Path: "/usr/share/elasticsearch/config/roles.yml", Mode: 0o444}}) {
		t.Fatalf("files %+v", c.Files)
	}
	if c.Resources != (recipes.Resources{CPUMillis: 1000, MemoryMB: 1536}) {
		t.Fatalf("resources %+v", c.Resources)
	}
	wantEnv := []recipes.EnvVar{
		{Name: "discovery.type", Value: "single-node"},
		{Name: "xpack.security.enabled", Value: "true"},
		{Name: "xpack.security.http.ssl.enabled", Value: "false"},
		{Name: "xpack.security.transport.ssl.enabled", Value: "false"},
		{Name: "xpack.security.authc.anonymous.roles", Value: "loomplane_probe"},
		{Name: "xpack.security.authc.anonymous.authz_exception", Value: "false"},
		{Name: "node.store.allow_mmap", Value: "false"},
		{Name: "ES_JAVA_OPTS", Value: "-Xms512m -Xmx512m"},
		{Name: "ELASTIC_PASSWORD", Secret: "password"},
	}
	if !reflect.DeepEqual(c.Env, wantEnv) {
		t.Fatalf("env %+v", c.Env)
	}
	if len(kind.Secrets) != 1 || kind.Secrets[0].Name != "password" || kind.Secrets[0].Bytes != 32 || kind.Secrets[0].Encoding != "hex" {
		t.Fatalf("secrets %+v", kind.Secrets)
	}
	if len(kind.Outputs) != 1 || kind.Outputs[0].Name != "url" || kind.Outputs[0].EnvKey != "ELASTICSEARCH_URL" || kind.Outputs[0].From == nil || *kind.Outputs[0].From != (recipes.Ref{Component: "elasticsearch", Field: "url:http"}) {
		t.Fatalf("outputs %+v", kind.Outputs)
	}
	release := kind.Releases[0]
	image := release.Images["elasticsearch"]
	if release.Version != "9.4" || release.Product != "9.4.1" || image.Repository != "docker.io/library/elasticsearch" {
		t.Fatalf("release %+v", release)
	}
	if image.Digests["amd64"] != "sha256:8a232d1f9fa617d2b3c9478746ef7b295e15eee963b2dd5a40e0488df6ab25b4" || image.Digests["arm64"] != "sha256:d0c208251c5d06f93ae800f43ca9a8a0958959cc1d01df716a08892e009a750a" {
		t.Fatalf("digests %+v", image.Digests)
	}

	var info *recipes.KindInfo
	for _, k := range recipes.Default().Kinds() {
		if k.Name == "elasticsearch" {
			info = &k
		}
	}
	if info == nil || info.Placement != "dependency" {
		t.Fatalf("info %+v", info)
	}
}

func TestNewKindsAreListedAndBuiltinsUnchanged(t *testing.T) {
	names := map[string]bool{}
	for _, k := range recipes.Default().Kinds() {
		names[k.Name] = true
	}
	for _, name := range []string{"postgres", "mysql", "mariadb", "redis", "valkey", "timescaledb", "falkordb", "elasticsearch"} {
		if !names[name] {
			t.Errorf("%s is not listed", name)
		}
	}
}

const (
	fabriqImage      = "ghcr.io/xraph/fabriq@sha256:0dd5dc887c567748e95c42d355adb4aa4598cb0cb9aea5fea4b786de0fb18f9e"
	fabriqAdminImage = "ghcr.io/xraph/fabriq-admin@sha256:c2a59e503c7ff886a8302038461ca71cfba44ea3250c52bf0926c9f3af57f364"
)

func fabriqComposition(t *testing.T, arch string, off map[string]bool) *recipes.Composition {
	t.Helper()
	c, err := recipes.Default().Resolve("fabriq", "1.9", arch, "server")
	if err != nil {
		t.Fatal(err)
	}
	return c.Without(off)
}

func fabriqComponent(t *testing.T, c *recipes.Composition, name string) recipes.ResolvedComponent {
	t.Helper()
	for _, rc := range c.Components {
		if rc.Name == name {
			return rc
		}
	}
	t.Fatalf("no component %q in %v", name, componentNames(c))
	return recipes.ResolvedComponent{}
}

func componentNames(c *recipes.Composition) []string {
	names := make([]string, 0, len(c.Components))
	for _, rc := range c.Components {
		names = append(names, rc.Name)
	}
	return names
}

// envOf maps a component's env by name, failing on a repeat.
func envOf(t *testing.T, c recipes.Component) map[string]recipes.EnvVar {
	t.Helper()
	out := map[string]recipes.EnvVar{}
	for _, e := range c.Env {
		if _, dup := out[e.Name]; dup {
			t.Fatalf("%s sets %s twice", c.Name, e.Name)
		}
		out[e.Name] = e
	}
	return out
}

func TestFabriqKindResolvesInOrder(t *testing.T) {
	c := fabriqComposition(t, "arm64", map[string]bool{"falkordb": true, "elasticsearch": true})
	want := []string{"preflight", "migrate", "app-role", "server", "admin"}
	if got := componentNames(c); !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	for i, rc := range c.Components {
		if rc.Step != i {
			t.Errorf("%s is step %d, want %d", rc.Name, rc.Step, i)
		}
		wantImage := fabriqImage
		if rc.Name == "admin" {
			wantImage = fabriqAdminImage
		}
		if rc.Image != wantImage {
			t.Errorf("%s image %q, want %q", rc.Name, rc.Image, wantImage)
		}
	}
	if c.Version != "1.9" || c.Product != "1.9.0" || c.Revision != "1" {
		t.Fatalf("release %s %s %s", c.Version, c.Product, c.Revision)
	}
	amd := fabriqComposition(t, "amd64", map[string]bool{"falkordb": true, "elasticsearch": true})
	if got := fabriqComponent(t, amd, "server").Image; got != "ghcr.io/xraph/fabriq@sha256:be0d467ad641fe87b0909285b087fa40f3ea313e4f594245c5eea3e3754b79c6" {
		t.Errorf("amd64 server image %q", got)
	}
	if got := fabriqComponent(t, amd, "admin").Image; got != "ghcr.io/xraph/fabriq-admin@sha256:0d2223ee3f21d7a2acad33b6cf09f48e6b202ba49ec3ad4a9b6b08c2e4354d9e" {
		t.Errorf("amd64 admin image %q", got)
	}
	if _, err := recipes.Default().Resolve("fabriq", "1.9", "riscv64", "server"); err == nil {
		t.Fatal("unknown architecture accepted")
	}

	kind, _ := recipes.Default().Kind("fabriq")
	if kind.Title != "Fabriq" || kind.Placement != recipes.PlacementAppProject || !slices.Equal(kind.Targets, []string{"server", "kubernetes"}) {
		t.Fatalf("kind %+v", kind)
	}
	if kind.Description != "Hosted Fabriq server with tenants, per-service keys and the admin console" {
		t.Fatalf("description %q", kind.Description)
	}
	wantOption := recipes.ConsumerOption{Name: "tenant", Title: "Default tenant", Pattern: "^[a-z0-9][a-z0-9-]{0,62}$", Required: true}
	if !reflect.DeepEqual(kind.ConsumerOptions, []recipes.ConsumerOption{wantOption}) {
		t.Fatalf("consumer options %+v", kind.ConsumerOptions)
	}
	wantOutput := recipes.Output{Name: "url", EnvKey: "FABRIQ_URL", PerConsumer: true, Secret: true}
	if !reflect.DeepEqual(kind.Outputs, []recipes.Output{wantOutput}) {
		t.Fatalf("outputs %+v", kind.Outputs)
	}
}

func TestFabriqKindDependencies(t *testing.T) {
	kind, _ := recipes.Default().Kind("fabriq")
	want := []recipes.Dependency{
		{Name: "postgres", Title: "Postgres 16", Kinds: []string{"timescaledb"}, Version: "16", External: &recipes.Requirement{
			Engine: "postgres", MinVersion: "16", Extensions: []string{"timescaledb", "vector", "postgis"},
			Preload: []string{"timescaledb"}, CreateRole: true, TLS: []string{"disable", "require", "verify-full"},
		}},
		{Name: "redis", Title: "Redis", Kinds: []string{"redis", "valkey"}, Version: "8.2", External: &recipes.Requirement{Engine: "redis", MinVersion: "7", TLS: []string{"disable"}}},
		{Name: "falkordb", Title: "FalkorDB", Kinds: []string{"falkordb"}, Optional: true},
		{Name: "elasticsearch", Title: "Elasticsearch", Kinds: []string{"elasticsearch"}, Optional: true},
	}
	if !reflect.DeepEqual(kind.Dependencies, want) {
		t.Fatalf("dependencies %+v", kind.Dependencies)
	}
	if kind.Releases[0].Images["admin"].Repository != "ghcr.io/xraph/fabriq-admin" || kind.Releases[0].Images["server"].Repository != "ghcr.io/xraph/fabriq" {
		t.Fatalf("images %+v", kind.Releases[0].Images)
	}
}

func TestFabriqKindWithProjections(t *testing.T) {
	c := fabriqComposition(t, "amd64", nil)
	want := []string{"preflight", "migrate", "app-role", "server", "rebuild-graph", "rebuild-search", "admin"}
	if got := componentNames(c); !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	for _, name := range []string{"rebuild-graph", "rebuild-search", "admin"} {
		if step := fabriqComponent(t, c, name).Step; step != 4 {
			t.Errorf("%s is step %d, want 4", name, step)
		}
	}
	// Within step 4 the jobs come before the process.
	if c.Components[4].Role != recipes.RoleJob || c.Components[5].Role != recipes.RoleJob || c.Components[6].Name != "admin" {
		t.Fatalf("order %v", componentNames(c))
	}
	graph := fabriqComponent(t, c, "rebuild-graph")
	search := fabriqComponent(t, c, "rebuild-search")
	if graph.Requires != "falkordb" || search.Requires != "elasticsearch" || graph.Timeout != time.Hour || search.Timeout != time.Hour {
		t.Fatalf("graph %q %s, search %q %s", graph.Requires, graph.Timeout, search.Requires, search.Timeout)
	}
	if !slices.Equal(graph.After, []string{"server"}) || !slices.Equal(search.After, []string{"server"}) {
		t.Fatalf("after %v %v", graph.After, search.After)
	}

	graphOnly := fabriqComposition(t, "amd64", map[string]bool{"elasticsearch": true})
	if got := componentNames(graphOnly); !slices.Equal(got, []string{"preflight", "migrate", "app-role", "server", "rebuild-graph", "admin"}) {
		t.Fatalf("graph only %v", got)
	}
}

func TestFabriqJobsRunTheBinary(t *testing.T) {
	c := fabriqComposition(t, "amd64", nil)
	binary := []string{"/usr/local/bin/fabriq"}
	privileged := recipes.EnvVar{Name: "FABRIQ_POSTGRES_DSN", From: &recipes.Ref{Dependency: "postgres", Field: "admin-url"}}
	cases := []struct {
		name    string
		args    []string
		after   []string
		timeout time.Duration
	}{
		{"preflight", []string{"migrate", "preflight", "--json"}, nil, 2 * time.Minute},
		{"migrate", []string{"migrate", "up"}, []string{"preflight"}, 10 * time.Minute},
		{"app-role", []string{"migrate", "app-role", "--role", "fabriq_app"}, []string{"migrate"}, 2 * time.Minute},
	}
	for _, tc := range cases {
		j := fabriqComponent(t, c, tc.name)
		if j.Role != recipes.RoleJob || !slices.Equal(j.Command, binary) || !slices.Equal(j.Args, tc.args) || !slices.Equal(j.After, tc.after) || j.Timeout != tc.timeout {
			t.Errorf("%s: %+v", tc.name, j.Component)
		}
		if env := envOf(t, j.Component); !reflect.DeepEqual(env["FABRIQ_POSTGRES_DSN"], privileged) {
			t.Errorf("%s DSN %+v", tc.name, env["FABRIQ_POSTGRES_DSN"])
		}
	}
	role := envOf(t, fabriqComponent(t, c, "app-role").Component)
	if len(role) != 2 || role["FABRIQ_APP_ROLE_PASSWORD"].Secret != "app-password" {
		t.Fatalf("app-role env %+v", role)
	}
	server := fabriqComponent(t, c, "server")
	if !slices.Equal(server.Command, binary) || !slices.Equal(server.Args, []string{"serve"}) || !slices.Equal(server.After, []string{"app-role"}) {
		t.Fatalf("server %+v", server.Component)
	}
	if admin := fabriqComponent(t, c, "admin"); len(admin.Command) != 0 || len(admin.Args) != 0 || !slices.Equal(admin.After, []string{"server"}) {
		t.Fatalf("admin must keep the image entrypoint: %+v", admin.Component)
	}
}

func TestFabriqHasNoRoutablePort(t *testing.T) {
	kind, _ := recipes.Default().Kind("fabriq")
	ports := 0
	for _, c := range kind.Components {
		for _, p := range c.Ports {
			ports++
			if p.Routable || !p.Control {
				t.Errorf("%s port %+v must be a control port", c.Name, p)
			}
		}
	}
	if ports != 2 {
		t.Fatalf("%d ports, want the server's and the console's", ports)
	}
	c := fabriqComposition(t, "amd64", nil)
	server, admin := fabriqComponent(t, c, "server"), fabriqComponent(t, c, "admin")
	if !reflect.DeepEqual(server.Ports, []recipes.Port{{Name: "api", Container: 8081, Protocol: "http", Control: true}}) || *server.Readiness != (recipes.HTTPProbe{Port: "api", Path: "/_/readyz"}) {
		t.Errorf("server ports %+v readiness %+v", server.Ports, server.Readiness)
	}
	if !reflect.DeepEqual(admin.Ports, []recipes.Port{{Name: "http", Container: 8080, Protocol: "http", Control: true}}) || *admin.Readiness != (recipes.HTTPProbe{Port: "http", Path: "/healthz"}) {
		t.Errorf("admin ports %+v readiness %+v", admin.Ports, admin.Readiness)
	}
	if server.Resources != (recipes.Resources{CPUMillis: 500, MemoryMB: 512}) || admin.Resources != (recipes.Resources{CPUMillis: 100, MemoryMB: 64}) {
		t.Errorf("resources %+v %+v", server.Resources, admin.Resources)
	}
}

func TestFabriqAdminKeyFormat(t *testing.T) {
	kind, _ := recipes.Default().Kind("fabriq")
	var key recipes.GeneratedSecret
	for _, s := range kind.Secrets {
		if s.Name == "admin-key" {
			key = s
		}
	}
	if key.Bytes != 20 || key.Encoding != "hex" || key.Prefix != "fq_" {
		t.Fatalf("admin-key %+v", key)
	}
	if i := slices.IndexFunc(kind.Secrets, func(s recipes.GeneratedSecret) bool { return s.Name == "app-password" }); i < 0 ||
		kind.Secrets[i].Bytes != 32 || kind.Secrets[i].Encoding != "hex" || kind.Secrets[i].Prefix != "" {
		t.Fatalf("secrets %+v", kind.Secrets)
	}
	// Generated the way the owned target does it: the prefix verbatim, then
	// the random bytes in the declared encoding.
	pattern := regexp.MustCompile(`^fq_[0-9A-Za-z]{32,}$`)
	for range 100 {
		raw := make([]byte, key.Bytes)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		value := key.Prefix + hex.EncodeToString(raw)
		if !pattern.MatchString(value) || len(value) != 43 {
			t.Fatalf("%q is not an operator key", value)
		}
	}
}

func TestFabriqServeEnvMatchesPlan2a(t *testing.T) {
	c := fabriqComposition(t, "amd64", nil)
	server := fabriqComponent(t, c, "server")
	env := envOf(t, server.Component)

	if want := "postgres://fabriq_app:${secret:app-password}@${dep:postgres:hostport}/${dep:postgres:database}?${dep:postgres:query}"; env["FABRIQ_POSTGRES_DSN"].Template != want {
		t.Errorf("DSN %+v", env["FABRIQ_POSTGRES_DSN"])
	}
	refs := map[string]recipes.Ref{
		"FABRIQ_REDIS_ADDR":             {Dependency: "redis", Field: "hostport"},
		"FABRIQ_REDIS_USERNAME":         {Dependency: "redis", Field: "username"},
		"FABRIQ_REDIS_PASSWORD":         {Dependency: "redis", Field: "password"},
		"FABRIQ_FALKORDB_ADDR":          {Dependency: "falkordb", Field: "url"},
		"FABRIQ_FALKORDB_PASSWORD":      {Dependency: "falkordb", Field: "secret:password"},
		"FABRIQ_ELASTICSEARCH_ADDRS":    {Dependency: "elasticsearch", Field: "url"},
		"FABRIQ_ELASTICSEARCH_PASSWORD": {Dependency: "elasticsearch", Field: "secret:password"},
	}
	for name, ref := range refs {
		if env[name].From == nil || *env[name].From != ref {
			t.Errorf("%s from %+v, want %+v", name, env[name].From, ref)
		}
	}
	values := map[string]string{
		"FABRIQ_FALKORDB_USERNAME": "default", "FABRIQ_ELASTICSEARCH_USERNAME": "elastic", "FABRIQ_HTTP_ADDR": ":8081",
		"FABRIQ_ADMIN_API": "true", "FABRIQ_DOMAIN_PACK": "none", "FABRIQ_DYNAMIC_PERSIST": "true",
	}
	for name, value := range values {
		if env[name].Value != value {
			t.Errorf("%s = %q, want %q", name, env[name].Value, value)
		}
	}
	if env["FABRIQ_ADMIN_KEY"].Secret != "admin-key" {
		t.Errorf("FABRIQ_ADMIN_KEY %+v", env["FABRIQ_ADMIN_KEY"])
	}
	for _, name := range []string{"FABRIQ_FALKORDB_ADDR", "FABRIQ_FALKORDB_USERNAME", "FABRIQ_FALKORDB_PASSWORD"} {
		if env[name].Requires != "falkordb" {
			t.Errorf("%s requires %q", name, env[name].Requires)
		}
	}
	for _, name := range []string{"FABRIQ_ELASTICSEARCH_ADDRS", "FABRIQ_ELASTICSEARCH_USERNAME", "FABRIQ_ELASTICSEARCH_PASSWORD"} {
		if env[name].Requires != "elasticsearch" {
			t.Errorf("%s requires %q", name, env[name].Requires)
		}
	}
	// The console's variables belong to the admin component, not the server.
	for _, name := range []string{"FABRIQ_ADMIN_UPSTREAM", "FABRIQ_ADMIN_UPSTREAM_KEY", "FABRIQ_ADMIN_BASE_PATH", "FABRIQ_ADMIN_DEFAULT_TENANT", "FABRIQ_ADMIN_API_URL", "FABRIQ_ADMIN_MAP_STYLE_URL"} {
		if _, ok := env[name]; ok {
			t.Errorf("the server sets console variable %s", name)
		}
	}
	for name := range env {
		if strings.HasPrefix(name, "FABRIQ_ADMIN_") && name != "FABRIQ_ADMIN_API" && name != "FABRIQ_ADMIN_KEY" {
			t.Errorf("the server sets %s", name)
		}
	}

	admin := envOf(t, fabriqComponent(t, c, "admin").Component)
	if got := admin["FABRIQ_ADMIN_BASE_PATH"].Value; got != fabriqloom.ConsoleBasePath {
		t.Errorf("base path %+v, want %q", admin["FABRIQ_ADMIN_BASE_PATH"], fabriqloom.ConsoleBasePath)
	}
	if up := admin["FABRIQ_ADMIN_UPSTREAM"]; up.From == nil || *up.From != (recipes.Ref{Component: "server", Field: "url:api"}) {
		t.Errorf("upstream %+v", up)
	}
	if admin["FABRIQ_ADMIN_UPSTREAM_KEY"].Secret != "admin-key" {
		t.Errorf("upstream key %+v", admin["FABRIQ_ADMIN_UPSTREAM_KEY"])
	}
}

// fabriq-admin's nginx resolves the server's address once, when it starts,
// so whatever recreates the server must recreate the admin component too.
func TestFabriqAdminFollowsTheServer(t *testing.T) {
	c := fabriqComposition(t, "amd64", nil)
	admin := envOf(t, fabriqComponent(t, c, "admin").Component)
	// The marker is built from the server image's own digests, so refreshing
	// either one changes it.
	kind, _ := recipes.Default().Kind("fabriq")
	server := kind.Releases[0].Images["server"]
	wantImage := server.Repository + "@amd64=" + server.Digests["amd64"] + ",arm64=" + server.Digests["arm64"]
	if len(server.Digests) != 2 {
		t.Fatalf("server digests %v", server.Digests)
	}
	want := map[string]recipes.EnvVar{
		"LOOMPLANE_FABRIQ_SERVER_IMAGE":  {Name: "LOOMPLANE_FABRIQ_SERVER_IMAGE", Value: wantImage},
		"LOOMPLANE_FABRIQ_FALKORDB":      {Name: "LOOMPLANE_FABRIQ_FALKORDB", Value: "on", Requires: "falkordb"},
		"LOOMPLANE_FABRIQ_ELASTICSEARCH": {Name: "LOOMPLANE_FABRIQ_ELASTICSEARCH", Value: "on", Requires: "elasticsearch"},
	}
	for name, e := range want {
		if !reflect.DeepEqual(admin[name], e) {
			t.Errorf("%s: %+v, want %+v", name, admin[name], e)
		}
	}
	for arch, digest := range server.Digests {
		if !strings.Contains(admin["LOOMPLANE_FABRIQ_SERVER_IMAGE"].Value, arch+"="+digest) {
			t.Errorf("the marker %q omits the %s digest", admin["LOOMPLANE_FABRIQ_SERVER_IMAGE"].Value, arch)
		}
	}
}

func TestFabriqRebuildJobsMatchPlan2a(t *testing.T) {
	c := fabriqComposition(t, "amd64", nil)
	dsn := recipes.EnvVar{Name: "FABRIQ_POSTGRES_DSN", From: &recipes.Ref{Dependency: "postgres", Field: "admin-url"}}
	cases := []struct {
		job, projection string
		extra           []recipes.EnvVar
	}{
		{"rebuild-graph", "graph", []recipes.EnvVar{
			{Name: "FABRIQ_FALKORDB_ADDR", From: &recipes.Ref{Dependency: "falkordb", Field: "url"}},
			{Name: "FABRIQ_FALKORDB_USERNAME", Value: "default"},
			{Name: "FABRIQ_FALKORDB_PASSWORD", From: &recipes.Ref{Dependency: "falkordb", Field: "secret:password"}},
		}},
		{"rebuild-search", "search", []recipes.EnvVar{
			{Name: "FABRIQ_ELASTICSEARCH_ADDRS", From: &recipes.Ref{Dependency: "elasticsearch", Field: "url"}},
			{Name: "FABRIQ_ELASTICSEARCH_USERNAME", Value: "elastic"},
			{Name: "FABRIQ_ELASTICSEARCH_PASSWORD", From: &recipes.Ref{Dependency: "elasticsearch", Field: "secret:password"}},
		}},
	}
	for _, tc := range cases {
		j := fabriqComponent(t, c, tc.job)
		wantArgs := []string{"rebuild", "--projection", tc.projection, "--all-tenants", "--drop-old"}
		if got := append(slices.Clone(j.Command), j.Args...); !slices.Equal(got, append([]string{"/usr/local/bin/fabriq"}, wantArgs...)) {
			t.Errorf("%s runs %v", tc.job, got)
		}
		want := append([]recipes.EnvVar{dsn}, tc.extra...)
		want = append(want, recipes.EnvVar{Name: "FABRIQ_DOMAIN_PACK", Value: "none"}, recipes.EnvVar{Name: "FABRIQ_DYNAMIC_PERSIST", Value: "true"})
		if !reflect.DeepEqual(j.Env, want) {
			t.Errorf("%s env %+v, want %+v", tc.job, j.Env, want)
		}
	}
}
