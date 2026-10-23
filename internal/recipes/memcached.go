package recipes

func init() {
	MustRegister(Kind{
		Name: "memcached", Title: "Memcached", Placement: PlacementAppProject,
		Description: "Private disposable cache for trusted applications. Restarting empties the cache; applications must rebuild it from their source. No authentication, backups or historical restores.",
		Targets:     []string{"server"},
		Releases: []Release{{Version: "1.6", Product: "1.6.45", Revision: "1", Images: map[string]Image{"cache": {
			Repository: "docker.io/library/memcached", Digests: map[string]string{
				"amd64": "sha256:9057793717e87e054636eaf06cf8ec2792a3ad164ad9e8bcae1e90241ea73fb0",
				"arm64": "sha256:e09dcc0bc98221ed468081adaa4a78c61d2161f475b1aa0cd282e18573a8ede7",
			},
		}}}},
		Components: []Component{{Name: "cache", Role: RoleProcess, Args: []string{"memcached", "-m", "128", "-U", "0", "-c", "1024", "-t", "2"},
			Ports: []Port{{Name: "cache", Container: 11211, Protocol: "memcached"}}, Readiness: &HTTPProbe{Port: "cache"},
			Protection: ProtectionNone, Resources: Resources{CPUMillis: 500, MemoryMB: 256}}},
		Outputs: []Output{{Name: "servers", EnvKey: "MEMCACHED_SERVERS", From: &Ref{Component: "cache", Field: "address:cache"}}},
	})
}
