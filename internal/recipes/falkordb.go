package recipes

// falkordbScript mirrors cacheArgs: the password is hashed into an ephemeral
// config, so it never reaches argv or the data volume. The module path is
// what the image's own /FalkorDB/build/docker/run.sh loads. The entrypoint
// drops to the redis user, which must be able to read the config.
const falkordbScript = `set -eu
: "${FALKORDB_PASSWORD:?}"
hash=$(printf '%s' "$FALKORDB_PASSWORD" | sha256sum); hash=${hash%% *}
umask 077
cat > /tmp/falkordb.conf <<EOF
bind 0.0.0.0
port 6379
protected-mode yes
dir /data
appendonly no
save 60 1000
user default on #${hash} ~* &* +@all
loadmodule /FalkorDB/bin/src/falkordb.so MAX_QUEUED_QUERIES 25 TIMEOUT 1000 RESULTSET_SIZE 10000
EOF
chmod 644 /tmp/falkordb.conf
cd /data && exec docker-entrypoint.sh redis-server /tmp/falkordb.conf`

// FalkorDB is provisioned only as a dependency. It needs a password: without
// one, any container on the environment network could read every tenant's
// projections and bypass row-level security.
//
// Image pins belong to index
// sha256:428464b8ed08b7289e35ece482109d8d54b2cb1d77ff8a7a66a9bfbeaa0f145b
// (docker.io/falkordb/falkordb:v4.2.2, resolved on 2026-10-21), the version
// Fabriq's own integration harness tests against. Refreshing a pin: bump
// Revision, replace both digests and this index, and rerun
// TestQualifyNewEngineImages.
func init() {
	MustRegister(Kind{
		Name: "falkordb", Title: "FalkorDB", Placement: PlacementDependency,
		Description: "Graph database for a Fabriq stack. Reached only on the environment network, with a generated password. Its data is rebuilt from Fabriq's source of truth, not backed up.",
		Targets:     []string{"server", "kubernetes"},
		Releases: []Release{{Version: "4.2", Product: "4.2.2", Revision: "1", Images: map[string]Image{"falkordb": {
			Repository: "docker.io/falkordb/falkordb", Digests: map[string]string{
				"amd64": "sha256:fe2cd0ed30c1df273b8bc1f7b467ff3ca006dee64e453de571563a9932c3aebb",
				"arm64": "sha256:c2a716eccab1b81f68343e44f6539a31beb5991b7523d8aa4e60551b2de18e51",
			},
		}}}},
		Secrets: []GeneratedSecret{{Name: "password", Bytes: 32, Encoding: "hex"}},
		Components: []Component{{
			Name: "falkordb", Role: RoleProcess, Command: []string{"sh", "-c"}, Args: []string{falkordbScript},
			Ports:     []Port{{Name: "redis", Container: 6379, Protocol: "redis"}},
			Readiness: &HTTPProbe{Port: "redis"},
			Volume:    &Volume{MountPath: "/data", KubernetesSizeMB: 4096}, Protection: ProtectionRebuildable,
			Env:       []EnvVar{{Name: "FALKORDB_PASSWORD", Secret: "password"}, {Name: "BROWSER", Value: "0"}},
			Resources: Resources{CPUMillis: 500, MemoryMB: 512},
		}},
		Outputs: []Output{{Name: "url", EnvKey: "FALKORDB_ADDR", From: &Ref{Component: "falkordb", Field: "address:redis"}}},
	})
}
