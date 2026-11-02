package recipes

func init() {
	MustRegisterEngine(Engine{Kind: "rabbitmq", Family: "rabbitmq", Title: "RabbitMQ", EnvKey: "AMQP_URL", Targets: []string{"server"},
		Releases: []EngineRelease{{Version: "4.3", Product: "4.3.6", Revision: "1", MountPath: "/var/lib/rabbitmq", BackupFormat: "rabbitmq-node-v1", Image: Image{Repository: "docker.io/library/rabbitmq", Digests: map[string]string{
			"amd64": "sha256:266f3807c975a22ffce3274b042b42ac6500b88496c460ad8468ee8a197992be",
			"arm64": "sha256:f84b8c4d86bb76f3cf09719db979336be482ca4237a72d06a6e3a9321f720117",
		}}}},
	})
}

func rabbitmqArgs(recovery bool) []string {
	listener := "0.0.0.0:5672"
	if recovery {
		listener = "127.0.0.1:5672"
	}
	return []string{"sh", "-c", `set -eu
umask 077
cat > /tmp/loomplane-rabbitmq.conf <<'CONFIG'
listeners.tcp.1 = ` + listener + `
vm_memory_high_watermark.absolute = 256MiB
disk_free_limit.absolute = 128MiB
CONFIG
printf '[] .\n' > /tmp/loomplane-rabbitmq-plugins
chown rabbitmq:rabbitmq /tmp/loomplane-rabbitmq.conf /tmp/loomplane-rabbitmq-plugins
export RABBITMQ_ENABLED_PLUGINS_FILE=/tmp/loomplane-rabbitmq-plugins
export RABBITMQ_CONFIG_FILE=/tmp/loomplane-rabbitmq.conf
exec docker-entrypoint.sh rabbitmq-server`}
}
