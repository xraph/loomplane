package recipes

const (
	// OctopusKind is the catalog kind name of the Octopus API gateway.
	OctopusKind = "octopus"
	// OctopusGateway is the name of the component that runs the gateway.
	OctopusGateway = "gateway"
	// OctopusConfigPath is where the gateway reads its configuration file.
	OctopusConfigPath = "/etc/octopus/config.yaml"
	// OctopusGatewayPort is the only Octopus port that is ever routed publicly.
	OctopusGatewayPort = 8080
	// OctopusAdminPort serves the admin API and FARP, over a private channel only.
	OctopusAdminPort = 9090
)

const octopusRepository = "ghcr.io/xraph/octopus"

// octopusKind is the Octopus API gateway, added into an app project. The
// gateway discovers routes over FARP and from Kubernetes Services.
//
// Image pin: ghcr.io/xraph/octopus:0.5.0, read from the v0.5.0 release
// asset octopus-image-digests.json (kept as testdata, which a test compares
// with the digests below). Its index is
// sha256:09228762bccac4be48a8d5c814b1d75ab0a7df9324c25d5fead7e8bd2473c12a.
// Refreshing a pin: bump Revision and replace both digests from the new
// release asset. The image's own entrypoint is
// "octopus serve --config /etc/octopus/config.yaml", so there is no Command.
func octopusKind() Kind {
	return Kind{
		Name: OctopusKind, Title: "Octopus", Placement: PlacementAppProject,
		Description: "API gateway that routes your services by domain and path, with routes discovered over FARP",
		Targets:     []string{"server", "kubernetes"},
		Releases: []Release{{Version: "0.5", Product: "0.5.0", Revision: "1", Images: map[string]Image{OctopusGateway: {
			Repository: octopusRepository, Digests: map[string]string{
				"amd64": "sha256:76e25d926c14f43260fe5a7b676e301dcdf4bbcab3ed6af12335a41da1e5a633",
				"arm64": "sha256:60d8b7421034de8059f514809aa30960f1d8b616ae08f70facadd42cde25487f",
			},
		}}}},
		Secrets: []GeneratedSecret{
			{Name: "admin-password", Bytes: 32, Encoding: "base64url"},
			{Name: "session-secret", Bytes: 32, Encoding: "base64url"},
			{Name: "admin-api-token", Bytes: 32, Encoding: "base64url"},
			{Name: "farp-token", Bytes: 32, Encoding: "base64url"},
		},
		Components: []Component{{
			Name: OctopusGateway, Role: RoleProcess,
			Ports: []Port{
				{Name: "http", Container: OctopusGatewayPort, Protocol: "http", Routable: true},
				{Name: "admin", Container: OctopusAdminPort, Protocol: "http", Control: true},
			},
			Readiness: &HTTPProbe{Port: "http", Path: "/readyz"},
			Env: []EnvVar{
				{Name: "OCTOPUS_ADMIN_USERNAME", Value: "admin"},
				{Name: "OCTOPUS_ADMIN_PASSWORD", Secret: "admin-password"},
				{Name: "OCTOPUS_ADMIN_SESSION_SECRET", Secret: "session-secret"},
				{Name: "OCTOPUS_ADMIN_API_TOKEN", Secret: "admin-api-token"},
				{Name: "OCTOPUS_FARP_TOKEN", Secret: "farp-token"},
				{Name: "RUST_LOG", Value: "info"},
			},
			Files:      []File{{Path: OctopusConfigPath, Mode: 0o444}},
			Protection: ProtectionNone,
			Resources:  Resources{CPUMillis: 500, MemoryMB: 256},
			KubernetesRules: []KubernetesRule{
				{APIGroups: []string{""}, Resources: []string{"services", "endpoints"}, Verbs: []string{"get", "list", "watch"}},
				{APIGroups: []string{"discovery.k8s.io"}, Resources: []string{"endpointslices"}, Verbs: []string{"get", "list", "watch"}},
			},
		}},
		Outputs: []Output{{Name: "url", EnvKey: "OCTOPUS_URL", From: &Ref{Component: OctopusGateway, Field: "url:http"}}},
	}
}

func init() { MustRegister(octopusKind()) }
