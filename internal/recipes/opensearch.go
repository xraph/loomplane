package recipes

import "github.com/xraph/loomplane/internal/enginetls"

func init() { MustRegister(openSearch()) }

func openSearch() Kind {
	files := make([]File, 0, 14)
	files = append(files, []File{{Path: "/usr/share/opensearch/config/opensearch.yml"}, {Path: "/usr/share/opensearch/config/loomplane-probe.conf"}, {Path: "/usr/share/opensearch/config/ca.pem"}, {Path: "/usr/share/opensearch/config/node-key.pem"}, {Path: "/usr/share/opensearch/config/node.pem", CertificateSecret: "tls"}}...)
	for _, name := range []string{"action_groups", "allowlist", "audit", "config", "internal_users", "nodes_dn", "roles", "roles_mapping", "tenants"} {
		files = append(files, File{Path: "/usr/share/opensearch/config/opensearch-security/" + name + ".yml"})
	}
	outputs := make([]Output, 0, 7)
	outputs = append(outputs, Output{Name: "url", EnvKey: "OPENSEARCH_URL", Template: "https://${component:opensearch:address:http}"})
	outputs = append(outputs, engineConnectionOutputs("opensearch", "OPENSEARCH", "http", "9200", "loomplane")...)
	outputs = append(outputs, Output{Name: "ca-cert", EnvKey: "OPENSEARCH_CA_CERT", PublicCA: "tls"})
	return Kind{Name: "opensearch", Title: "OpenSearch", Description: "Private authenticated search with verified TLS. Recovery is not yet available.", Placement: PlacementDependency, Targets: []string{"server", "kubernetes"}, Service: engineService("opensearch", "opensearch-snapshot-v1", true),
		Releases:   []Release{{Version: "3.9", Product: "3.9.0", Revision: "1", Images: map[string]Image{"opensearch": {Repository: "docker.io/opensearchproject/opensearch", Digests: map[string]string{"amd64": "sha256:5c20af6b5cb472a74ba99f663bee3dd03afbeced14b3897b220a87fc75278445", "arm64": "sha256:bbdf65cb3d35c61a8ba495f22c1a42d7d7f0df23412e3419bf1e6cfca3ba4774"}}}}},
		Secrets:    []GeneratedSecret{{Name: "password", Bytes: 32, Encoding: "hex"}, {Name: "tls", Generator: enginetls.Generator}},
		Components: []Component{{Name: "opensearch", Role: RoleProcess, Ports: []Port{{Name: "http", Container: 9200, Protocol: "http"}}, Readiness: &HTTPProbe{Port: "http", Checker: "opensearch-v1"}, Volume: &Volume{MountPath: "/usr/share/opensearch/data", KubernetesSizeMB: 10240, FSGroup: 1000}, Protection: ProtectionRebuildable, Files: files, Env: []EnvVar{{Name: "DISABLE_INSTALL_DEMO_CONFIG", Value: "true"}, {Name: "DISABLE_PERFORMANCE_ANALYZER_AGENT_CLI", Value: "true"}, {Name: "OPENSEARCH_JAVA_OPTS", Value: "-Xms512m -Xmx512m"}}, Resources: Resources{CPUMillis: 1000, MemoryMB: 1536}}}, Outputs: outputs}
}
