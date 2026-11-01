package recipes

import "github.com/xraph/loomplane/internal/enginetls"

// DuckDBKind builds an unregistered runtime definition. Supply manifest digests
// for the qualified packaged CLI image, including bundled extensions and the
// supervisor. The upstream bare CLI image cannot run this definition.
func DuckDBKind(image Image) Kind {
	return Kind{Name: "duckdb", Title: "DuckDB", Description: "Private DuckDB with verified TLS and checkpointed backups. Backups briefly stop the service. Recovery copies are read-only.", Placement: PlacementServiceProject, Targets: []string{"server", "kubernetes"},
		Service:    &ServiceDefinition{Connection: map[string]string{FieldURL: "url", FieldHost: "host", FieldHostPort: "hostport", FieldPort: "port", FieldPassword: "token", FieldCACert: "ca-cert"}, Recovery: "duckdb", BackupFormat: "duckdb-checkpoint-v1"},
		Releases:   []Release{{Version: "2.0", Product: "2.0.0", Revision: "2", Images: map[string]Image{"duckdb": image}}},
		Secrets:    []GeneratedSecret{{Name: "token", Bytes: 32, Encoding: "hex"}, {Name: "tls", Generator: enginetls.Generator}},
		Components: []Component{{Name: "duckdb", Role: RoleProcess, Command: []string{"/loomplane-duckdb"}, Ports: []Port{{Name: "quack", Container: 9494, Protocol: "tcp"}}, Readiness: &HTTPProbe{Port: "quack", Checker: "duckdb-v1"}, Volume: &Volume{MountPath: "/data", KubernetesSizeMB: 4096}, Protection: ProtectionNative, Files: []File{{Path: "/etc/loomplane/token"}, {Path: "/etc/loomplane/ca.pem"}, {Path: "/etc/loomplane/node-key.pem"}, {Path: "/etc/loomplane/node.pem", CertificateSecret: "tls"}}, Resources: Resources{CPUMillis: 1000, MemoryMB: 1024}}},
		Outputs:    []Output{{Name: "url", EnvKey: "DUCKDB_URL", Template: "quack://${component:duckdb:address:quack}"}, {Name: "host", EnvKey: "DUCKDB_HOST", From: &Ref{Component: "duckdb", Field: "host:quack"}}, {Name: "hostport", EnvKey: "DUCKDB_HOSTPORT", From: &Ref{Component: "duckdb", Field: "address:quack"}}, {Name: "port", EnvKey: "DUCKDB_PORT", Template: "9494"}, {Name: "token", EnvKey: "DUCKDB_TOKEN", Secret: true, Template: "${secret:token}"}, {Name: "ca-cert", EnvKey: "DUCKDB_CA_CERT", PublicCA: "tls"}}}
}
