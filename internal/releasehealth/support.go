// Package releasehealth holds what a canary release needs to know about each
// target: whether Loomplane can split traffic there and which health signals
// the cloud reports. Release health is read from each cloud's own monitoring,
// never from the application's traffic, which Loomplane never sees.
package releasehealth

// Weighting mechanisms Loomplane can drive.
const (
	WeightingRevisions    = "revision-traffic"
	WeightingTargetGroups = "alb-target-groups"
)

// Support says what one provider can do for a canary.
type Support struct {
	Provider    string `json:"provider"`
	Weighting   string `json:"weighting"` // "" when Loomplane can't split traffic there
	Requests    bool   `json:"requests"`
	Errors      bool   `json:"errors"`
	Latency     string `json:"latency"` // "p95", "average" or ""
	PerRevision bool   `json:"per_revision"`
	Canary      bool   `json:"canary"`
	Note        string `json:"note"`
}

var matrix = []Support{
	{
		Provider: "cloud-run", Weighting: WeightingRevisions,
		Requests: true, Errors: true, Latency: "p95", PerRevision: true, Canary: true,
		Note: "Traffic is split with revision traffic percentages on the service.",
	},
	{
		Provider: "ecs-fargate", Weighting: WeightingTargetGroups,
		Requests: true, Errors: true, Latency: "p95", PerRevision: true, Canary: true,
		Note: "Traffic is split with weighted target groups on the ALB listener, for services behind the public or internal ALB. Service Connect calls stay on the baseline.",
	},
	{
		Provider: "azure-container-apps", Weighting: WeightingRevisions,
		Requests: true, Errors: true, Latency: "average", PerRevision: true, Canary: true,
		Note: "Traffic is split with revision traffic weights, in multiple revisions mode, for apps with ingress.",
	},
	{
		Provider: "kubernetes",
		Note:     "Services use a plain Ingress with no Gateway API, so there is no traffic weighting to drive.",
	},
	{
		Provider: "server",
		Note:     "Route releases are a DNS cutover, so traffic moves all at once.",
	},
	{
		Provider: "render", Requests: true, Errors: true, Latency: "p95",
		Note: "Render has no traffic weighting. Its metrics are per service, not per revision.",
	},
	{
		Provider: "vercel",
		Note:     "Vercel has no traffic weighting Loomplane drives.",
	},
	{
		Provider: "digitalocean",
		Note:     "DigitalOcean has no traffic weighting.",
	},
}

// Matrix returns the support matrix, one row per built-in provider.
func Matrix() []Support {
	return append([]Support(nil), matrix...)
}

// SupportFor returns the row for a provider, or a no-canary row for one the
// matrix doesn't list, such as a provider plugin.
func SupportFor(provider string) Support {
	for _, row := range matrix {
		if row.Provider == provider {
			return row
		}
	}
	return Support{
		Provider: provider,
		Note:     "Provider plugins have no traffic weighting Loomplane can drive.",
	}
}
