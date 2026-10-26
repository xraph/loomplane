package recipes

// Elasticsearch is provisioned only as a dependency. Security is on, so
// every index needs the elastic password; the anonymous user holds one role,
// loomplane_probe, which fabriqloom.SearchHooks renders into roles.yml and
// which can read cluster health and nothing else. That lets the
// unauthenticated readiness probe pass without opening any data. Without a
// password, any container on the environment network could read every
// tenant's projections and bypass row-level security.
//
// Image pins belong to index
// sha256:43b9eb68c147dd82aa0933812103631135aa95d283e46cf6d86f4c5c959ee3b7
// (docker.io/library/elasticsearch:9.4.1, resolved on 2026-10-21), the
// version Fabriq's own integration harness tests against. Refreshing a pin:
// bump Revision, replace both digests and this index, and rerun
// TestQualifyNewEngineImages.
func init() {
	MustRegister(Kind{
		Name: "elasticsearch", Title: "Elasticsearch", Placement: PlacementDependency,
		Description: "Search index for a Fabriq stack. Reached only on the environment network, with a generated password. Its data is rebuilt from Fabriq's source of truth, not backed up.",
		Targets:     []string{"server", "kubernetes"},
		Releases: []Release{{Version: "9.4", Product: "9.4.1", Revision: "1", Images: map[string]Image{"elasticsearch": {
			Repository: "docker.io/library/elasticsearch", Digests: map[string]string{
				"amd64": "sha256:8a232d1f9fa617d2b3c9478746ef7b295e15eee963b2dd5a40e0488df6ab25b4",
				"arm64": "sha256:d0c208251c5d06f93ae800f43ca9a8a0958959cc1d01df716a08892e009a750a",
			},
		}}}},
		Secrets: []GeneratedSecret{{Name: "password", Bytes: 32, Encoding: "hex"}},
		Components: []Component{{
			Name: "elasticsearch", Role: RoleProcess,
			Ports:     []Port{{Name: "http", Container: 9200, Protocol: "http"}},
			Readiness: &HTTPProbe{Port: "http", Path: "/_cluster/health?wait_for_status=yellow&timeout=5s"},
			Volume:    &Volume{MountPath: "/usr/share/elasticsearch/data", KubernetesSizeMB: 10240, FSGroup: 1000}, Protection: ProtectionRebuildable,
			Files: []File{{Path: "/usr/share/elasticsearch/config/roles.yml"}},
			Env: []EnvVar{
				{Name: "discovery.type", Value: "single-node"},
				{Name: "xpack.security.enabled", Value: "true"},
				{Name: "xpack.security.http.ssl.enabled", Value: "false"},
				{Name: "xpack.security.transport.ssl.enabled", Value: "false"},
				{Name: "xpack.security.authc.anonymous.roles", Value: "loomplane_probe"},
				{Name: "xpack.security.authc.anonymous.authz_exception", Value: "false"},
				{Name: "node.store.allow_mmap", Value: "false"},
				{Name: "ES_JAVA_OPTS", Value: "-Xms512m -Xmx512m"},
				{Name: "ELASTIC_PASSWORD", Secret: "password"},
			},
			Resources: Resources{CPUMillis: 1000, MemoryMB: 1536},
		}},
		Outputs: []Output{{Name: "url", EnvKey: "ELASTICSEARCH_URL", From: &Ref{Component: "elasticsearch", Field: "url:http"}}},
	})
}
