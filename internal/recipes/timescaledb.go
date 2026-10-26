package recipes

// TimescaleDB runs the Timescale HA image as the postgres family: it backs
// up, restores and is configured exactly as the PostgreSQL 17 recipe is.
//
// The image runs as uid 1000 with PGDATA=/home/postgres/pgdata/data, so the
// volume mounts at /home/postgres/pgdata and Kubernetes gives it group 1000.
// Extensions are the ones a Fabriq stack creates as postgres. Fabriq creates
// timescaledb_toolkit when the image ships it, which the -all image does, so
// a restore must create it before pg_restore replays the dump.
//
// Image pins belong to index
// sha256:bdd7ce991710781cec6eefb74eca56ebbc591fd43ae370806492232fea2afc68
// (docker.io/timescale/timescaledb-ha:pg16.15-ts2.30.2-all, resolved on
// 2026-10-21). TimescaleDB 2.30.2 is used rather than 2.31.0, which was
// published the day the plan was written. Refreshing a pin: bump Revision,
// replace both digests and this index, and rerun TestQualifyNewEngineImages.
func init() {
	MustRegisterEngine(Engine{
		Kind: "timescaledb", Family: "postgres", Title: "PostgreSQL 16 with TimescaleDB", EnvKey: "DATABASE_URL",
		Targets: []string{"server", "kubernetes"},
		Releases: []EngineRelease{{
			Version: "16", Product: "16.15-ts2.30.2", Revision: "1",
			Image: Image{Repository: "docker.io/timescale/timescaledb-ha", Digests: map[string]string{
				"amd64": "sha256:10f7c9dfb1e860596be5a355a47cc508ee12daf26dd97d647a75b5731a80538c",
				"arm64": "sha256:f6e5b3fb4e839f116109675b5d1be56dfa7baa67f5272ad907117490694c4288",
			}},
			MountPath: "/home/postgres/pgdata", BackupFormat: "pg-dump-custom-v1", KubernetesVolumeMB: 10240,
			Extensions: []string{"timescaledb", "vector", "postgis", "timescaledb_toolkit"},
			FSGroup:    1000,
		}},
	})
}
