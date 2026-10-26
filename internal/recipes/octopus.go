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
