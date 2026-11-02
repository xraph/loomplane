package recipes

import (
	"maps"
	"slices"
	"strings"
	"time"
)

const (
	fabriqRepository      = "ghcr.io/xraph/fabriq"
	fabriqAdminRepository = "ghcr.io/xraph/fabriq-admin"
)

// imageStamp names an image by its repository and every architecture's
// digest, sorted by architecture. It is derived from the pin itself, so a
// refreshed digest changes it with no second place to remember.
func imageStamp(image Image) string {
	parts := make([]string, 0, len(image.Digests))
	for _, arch := range slices.Sorted(maps.Keys(image.Digests)) {
		parts = append(parts, arch+"="+image.Digests[arch])
	}
	return image.Repository + "@" + strings.Join(parts, ",")
}

// fabriqPrivileged is the environment every Fabriq job shares: the
// superuser DSN, which migrations and rebuilds need and the server never
// gets. The server runs as fabriq_app, a role row-level security applies to.
func fabriqPrivileged() []EnvVar {
	return []EnvVar{{Name: "FABRIQ_POSTGRES_DSN", From: &Ref{Dependency: "postgres", Field: FieldAdminURL}}}
}

// fabriqGraphEnv and fabriqSearchEnv are how Fabriq reads FalkorDB and
// Elasticsearch, on the server and on the rebuild jobs alike. The users are
// the ones the two dependency recipes define.
func fabriqGraphEnv(requires string) []EnvVar {
	return []EnvVar{
		{Name: "FABRIQ_FALKORDB_ADDR", From: &Ref{Dependency: "falkordb", Field: FieldURL}, Requires: requires},
		{Name: "FABRIQ_FALKORDB_USERNAME", Value: "default", Requires: requires},
		{Name: "FABRIQ_FALKORDB_PASSWORD", From: &Ref{Dependency: "falkordb", Field: "secret:password"}, Requires: requires},
	}
}

func fabriqSearchEnv(requires string) []EnvVar {
	return []EnvVar{
		{Name: "FABRIQ_ELASTICSEARCH_ADDRS", From: &Ref{Dependency: "elasticsearch", Field: FieldURL}, Requires: requires},
		{Name: "FABRIQ_ELASTICSEARCH_USERNAME", Value: "elastic", Requires: requires},
		{Name: "FABRIQ_ELASTICSEARCH_PASSWORD", From: &Ref{Dependency: "elasticsearch", Field: "secret:password"}, Requires: requires},
	}
}

// fabriqJob is a one-shot subcommand. The image's entrypoint is
// "fabriq serve", so every job names the binary as its Command.
func fabriqJob(name string, after, args []string, env []EnvVar, timeout time.Duration, requires string) Component {
	return Component{
		Name: name, Role: RoleJob, Command: []string{"/usr/local/bin/fabriq"}, Args: args,
		Env: env, After: after, Timeout: timeout, Requires: requires,
	}
}

func concatEnv(lists ...[]EnvVar) []EnvVar {
	var out []EnvVar
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// Fabriq is the hosted Fabriq stack: migrations, the app role, the server
// and the admin console, on Postgres 16 with TimescaleDB, pgvector and
// PostGIS, plus Redis, and optionally FalkorDB and Elasticsearch
// projections that the rebuild jobs back-fill when first turned on.
//
// Image pins:
//   - ghcr.io/xraph/fabriq:1.9.0 belongs to index
//     sha256:5a3f9391bbafaeb2e10e9e45ee92090ca9b79d0d575d5f435f5742e95e27e221
//     (amd64 sha256:be0d467a..., arm64 sha256:0dd5dc88...).
//   - ghcr.io/xraph/fabriq-admin:0.1.0 belongs to index
//     sha256:e145587f6e50f5a1e9d627c0e4ddedef9aeb39787a00c8d2e22e09a93a8307ec
//     (amd64 sha256:0d2223ee..., arm64 sha256:c2a59e50...).
//
// Both were verified with docker buildx imagetools inspect. Refreshing a
// pin: bump Revision and replace both digests and this index. The admin
// component's server-image marker is derived from the digests.
func init() {
	fabriq := Image{Repository: fabriqRepository, Digests: map[string]string{
		"amd64": "sha256:be0d467ad641fe87b0909285b087fa40f3ea313e4f594245c5eea3e3754b79c6",
		"arm64": "sha256:0dd5dc887c567748e95c42d355adb4aa4598cb0cb9aea5fea4b786de0fb18f9e",
	}}
	admin := Image{Repository: fabriqAdminRepository, Digests: map[string]string{
		"amd64": "sha256:0d2223ee3f21d7a2acad33b6cf09f48e6b202ba49ec3ad4a9b6b08c2e4354d9e",
		"arm64": "sha256:c2a59e503c7ff886a8302038461ca71cfba44ea3250c52bf0926c9f3af57f364",
	}}
	dropOld := func(projection string) []string {
		return []string{"rebuild", "--projection", projection, "--all-tenants", "--drop-old"}
	}
	// rebuild reads the domain pack and dynamic persistence as serve does,
	// and its own default pack is "example".
	rebuildEnv := []EnvVar{{Name: "FABRIQ_DOMAIN_PACK", Value: "none"}, {Name: "FABRIQ_DYNAMIC_PERSIST", Value: "true"}}

	hosted := Kind{
		Name: "fabriq", Title: "Fabriq", Placement: PlacementAppProject,
		Description: "Hosted Fabriq server with tenants, per-service keys and the admin console",
		Targets:     []string{"server", "kubernetes"},
		Releases: []Release{{Version: "1.9", Product: "1.9.0", Revision: "1", Images: map[string]Image{
			"preflight": fabriq, "migrate": fabriq, "app-role": fabriq, "server": fabriq,
			"rebuild-graph": fabriq, "rebuild-search": fabriq, "admin": admin,
		}}},
		Dependencies: []Dependency{
			{Name: "postgres", Title: "Postgres 16", Kinds: []string{"timescaledb"}, Version: "16", BackupEveryHours: map[string]int{"server": 24}, External: &Requirement{
				Engine: "postgres", MinVersion: "16", Extensions: []string{"timescaledb", "vector", "postgis"},
				Preload: []string{"timescaledb"}, CreateRole: true, TLS: []string{"disable", "require", "verify-full"},
			}},
			{Name: "redis", Title: "Redis", Kinds: []string{"redis", "valkey"}, Version: "8.2", External: &Requirement{Engine: "redis", MinVersion: "7", TLS: []string{"disable"}}},
			{Name: "falkordb", Title: "FalkorDB", Kinds: []string{"falkordb"}, Optional: true},
			{Name: "elasticsearch", Title: "Elasticsearch", Kinds: []string{"elasticsearch"}, Optional: true},
		},
		// 20 random bytes in hex are 40 characters, 43 with the prefix, which
		// is what ^fq_[0-9A-Za-z]{32,}$ asks of an operator key.
		Secrets: []GeneratedSecret{
			{Name: "admin-key", Bytes: 20, Encoding: "hex", Prefix: "fq_"},
			{Name: "app-password", Bytes: 32, Encoding: "hex"},
		},
		Components: []Component{
			fabriqJob("preflight", nil, []string{"migrate", "preflight", "--json"}, fabriqPrivileged(), 2*time.Minute, ""),
			fabriqJob("migrate", []string{"preflight"}, []string{"migrate", "up"}, fabriqPrivileged(), 10*time.Minute, ""),
			fabriqJob("app-role", []string{"migrate"}, []string{"migrate", "app-role", "--role", "fabriq_app"},
				concatEnv(fabriqPrivileged(), []EnvVar{{Name: "FABRIQ_APP_ROLE_PASSWORD", Secret: "app-password"}}), 2*time.Minute, ""),
			{
				Name: "server", Role: RoleProcess, Command: []string{"/usr/local/bin/fabriq"}, Args: []string{"serve"}, After: []string{"app-role"},
				Ports:     []Port{{Name: "api", Container: 8081, Protocol: "http", Control: true}},
				Readiness: &HTTPProbe{Port: "api", Path: "/_/readyz"},
				Resources: Resources{CPUMillis: 500, MemoryMB: 512},
				Env: concatEnv(
					[]EnvVar{
						{Name: "FABRIQ_POSTGRES_DSN", Template: "postgres://fabriq_app:${secret:app-password}@${dep:postgres:hostport}/${dep:postgres:database}?${dep:postgres:query}"},
						{Name: "FABRIQ_REDIS_ADDR", From: &Ref{Dependency: "redis", Field: FieldHostPort}},
						{Name: "FABRIQ_REDIS_USERNAME", From: &Ref{Dependency: "redis", Field: FieldUsername}},
						{Name: "FABRIQ_REDIS_PASSWORD", From: &Ref{Dependency: "redis", Field: FieldPassword}},
					},
					fabriqGraphEnv("falkordb"),
					fabriqSearchEnv("elasticsearch"),
					[]EnvVar{
						{Name: "FABRIQ_HTTP_ADDR", Value: ":8081"},
						{Name: "FABRIQ_ADMIN_API", Value: "true"},
						{Name: "FABRIQ_ADMIN_KEY", Secret: "admin-key"},
						{Name: "FABRIQ_DOMAIN_PACK", Value: "none"},
						{Name: "FABRIQ_DYNAMIC_PERSIST", Value: "true"},
					},
				),
			},
			{
				// The image's entrypoint runs nginx, so there is no Command.
				Name: "admin", Role: RoleProcess, After: []string{"server"},
				Ports:     []Port{{Name: "http", Container: 8080, Protocol: "http", Control: true}},
				Readiness: &HTTPProbe{Port: "http", Path: "/healthz"},
				Resources: Resources{CPUMillis: 100, MemoryMB: 64},
				Env: []EnvVar{
					{Name: "FABRIQ_ADMIN_UPSTREAM", From: &Ref{Component: "server", Field: "url:api"}},
					{Name: "FABRIQ_ADMIN_UPSTREAM_KEY", Secret: "admin-key"},
					// A literal because fabriqloom imports recipes, not the other way
					// round; TestFabriqServeEnvMatchesPlan2a ties it to fabriqloom.ConsoleBasePath.
					{Name: "FABRIQ_ADMIN_BASE_PATH", Value: "/fabriq/"},
					// fabriq-admin's nginx resolves its upstream once, when it
					// starts, so a release that recreates server must recreate
					// admin after it. These mirror what recreates server: its
					// image, and each projection being on. An env Value can't be
					// resolved per architecture, so the marker names the server
					// image's repository and every architecture's digest. It is
					// built from the same Image the server uses, so it changes
					// whenever either digest does.
					{Name: "LOOMPLANE_FABRIQ_SERVER_IMAGE", Value: imageStamp(fabriq)},
					{Name: "LOOMPLANE_FABRIQ_FALKORDB", Value: "on", Requires: "falkordb"},
					{Name: "LOOMPLANE_FABRIQ_ELASTICSEARCH", Value: "on", Requires: "elasticsearch"},
				},
			},
			fabriqJob("rebuild-graph", []string{"server"}, dropOld("graph"),
				concatEnv(fabriqPrivileged(), fabriqGraphEnv(""), rebuildEnv), time.Hour, "falkordb"),
			fabriqJob("rebuild-search", []string{"server"}, dropOld("search"),
				concatEnv(fabriqPrivileged(), fabriqSearchEnv(""), rebuildEnv), time.Hour, "elasticsearch"),
		},
		ConsumerOptions: []ConsumerOption{{Name: "tenant", Title: "Default tenant", Pattern: "^[a-z0-9][a-z0-9-]{0,62}$", Required: true}},
		Outputs:         []Output{{Name: "url", EnvKey: "FABRIQ_URL", PerConsumer: true, Secret: true}},
	}
	MustRegister(hosted)
	MustRegister(fabriqLibrary(hosted))
}
