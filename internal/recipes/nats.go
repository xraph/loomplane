package recipes

func init() {
	MustRegisterEngine(Engine{Kind: "nats", Family: "nats", Title: "NATS JetStream", EnvKey: "NATS_URL", Targets: []string{"server"},
		Releases: []EngineRelease{{Version: "2.11", Product: "2.11.17", Revision: "1", MountPath: "/data", BackupFormat: "nats-jetstream-v1", Image: Image{Repository: "docker.io/library/nats", Digests: map[string]string{
			"amd64": "sha256:a7d440bf0240e664ed74fc17c70d616e6ff4bb9890dc26f7276589daedd1e196",
			"arm64": "sha256:69fa60167708111e9318f327bb7b3609ad9d4d3e7a0d16243231c5a6e9a8fb4c",
		}}}},
	})
}

// NATS keeps server credentials outside the retained JetStream store. Recovery
// readers can inspect messages but cannot publish, consume or acknowledge them.
func natsArgs(recovery bool) []string {
	permissions := `publish: {allow: [">"], deny: ["$SYS.>", "$JS.API.STREAM.SNAPSHOT.>", "$JS.API.STREAM.RESTORE.>", "$JS.SNAPSHOT.>", "$JS.RESTORE.>"]}, subscribe: {allow: [">"], deny: ["$SYS.>"]}`
	if recovery {
		permissions = `publish: {allow: ["$JS.API.INFO", "$JS.API.STREAM.INFO.*", "$JS.API.STREAM.LIST", "$JS.API.STREAM.NAMES", "$JS.API.STREAM.MSG.GET.*", "$JS.API.CONSUMER.INFO.*.*", "$JS.API.CONSUMER.LIST.*", "$JS.API.CONSUMER.NAMES.*"]}, subscribe: {allow: ["_INBOX.>"]}`
	}
	return []string{"sh", "-c", `set -eu
umask 077
cat > /tmp/loomplane-nats.conf <<'CONFIG'
port: 4222
jetstream { store_dir: /data, max_memory_store: 0, max_file_store: 1GB }
authorization { users: [
 {user: admin, password: $LOOMPLANE_NATS_ADMIN_PASSWORD},
 {user: app, password: $LOOMPLANE_APP_PASSWORD, permissions: {` + permissions + `}}
] }
CONFIG
exec nats-server -c /tmp/loomplane-nats.conf`}
}
