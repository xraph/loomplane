package planning

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// Release is how a plan's services go live: the environment's release
// block with its defaults filled in, and which services ramp. It's pinned
// when the plan is made, so it's part of the revision you approve. A
// release with a canary is still one decision.
type Release struct {
	Strategy    string                   `json:"strategy"`
	Steps       []int                    `json:"steps,omitempty"`
	HoldSeconds int64                    `json:"hold_seconds,omitempty"`
	Thresholds  releasehealth.Thresholds `json:"thresholds"`
	// Canary are the action IDs that ramp. Every other service action deploys all at once.
	Canary []string `json:"canary,omitempty"`
}

// Hold is how long a canary watches each step. It's zero without a release.
func (r *Release) Hold() time.Duration {
	if r == nil {
		return 0
	}
	return time.Duration(r.HoldSeconds) * time.Second
}

// Ramps reports whether the action ramps as a canary. Nothing ramps
// without a release.
func (r *Release) Ramps(actionID string) bool {
	return r != nil && slices.Contains(r.Canary, actionID)
}

// Thresholds are what the plan's release is judged against: its own, or
// the defaults when the environment declares no release block.
func (p *Plan) Thresholds() releasehealth.Thresholds {
	if p.Release == nil {
		return releasehealth.DefaultThresholds()
	}
	return p.Release.Thresholds
}

// Finding codes for releases.
const (
	// codeCanaryUnavailable is a service that deploys all at once in an
	// environment whose releases are canaries.
	codeCanaryUnavailable = "canary-unavailable"
	// codeBlueGreenUnavailable is a service with no route release to
	// switch over in an environment whose releases are blue/green.
	codeBlueGreenUnavailable = "blue-green-unavailable"
	// codeCanaryInProgress blocks a plan while a canary ramps in its
	// environment.
	codeCanaryInProgress = "canary-in-progress"
	// codeCanaryPermissions blocks a canary on ECS, Cloud Run or Container
	// Apps whose connection's credential can't split the service's traffic.
	codeCanaryPermissions = "canary-permissions"
	// codeCanaryAppWide blocks a canary on Container Apps or Cloud Run
	// whose release changes what the target applies to every revision at
	// once.
	codeCanaryAppWide = "canary-app-wide-change" //nolint:gosec // G101: a finding code, not a credential
	// codeCanaryDatabaseRoute blocks a canary for a service that reaches
	// Cloud SQL through a Private Service Connect route, which a candidate
	// doesn't carry.
	codeCanaryDatabaseRoute = "canary-unsupported-database-route"
	// codeCanaryDatabaseGateway blocks a canary for the environment's
	// database gateway, which is replaced in one go.
	codeCanaryDatabaseGateway = "canary-unsupported-database-gateway"
)

// planRelease pins the environment's release on r, and says which service
// can't take part in it. It does nothing when the environment declares no
// release block, so those results are what they were before releases.
func planRelease(in Input, r *Result) {
	if in.Manifest == nil {
		return
	}
	resolved, ok := in.Manifest.ReleaseFor(in.Environment)
	if !ok {
		return
	}
	release := &Release{
		Strategy: resolved.Strategy, Steps: resolved.Steps, HoldSeconds: int64(resolved.Hold / time.Second),
		Thresholds: releasehealth.Thresholds{
			ErrorRate: resolved.ErrorRate, ErrorRateDelta: resolved.ErrorRateDelta,
			P95LatencyRatio: resolved.P95LatencyRatio, MinRequests: float64(resolved.MinRequests),
		},
	}
	r.Release = release
	for _, a := range r.Actions {
		// A kept or deleted service deploys nothing.
		if a.Subject != ServiceSubject || (a.Op != Create && a.Op != Update && a.Op != Replace) {
			continue
		}
		switch resolved.Strategy {
		case manifest.StrategyCanary:
			reason := noCanary(in, r, a)
			if reason == "" {
				if code, message := unsupportedCanary(a); code != "" {
					r.Problems = append(r.Problems, Finding{Code: code, Subject: ServiceSubject, Name: a.Name, Path: "services." + a.Name, Message: message})
					r.Executable = false
					continue
				}
				if changed := appWideChanges(a); len(changed) > 0 {
					r.Problems = append(r.Problems, Finding{
						Code: codeCanaryAppWide, Subject: ServiceSubject, Name: a.Name, Path: "services." + a.Name,
						Message: a.Name + " can't run a canary: this release changes its " + strings.Join(changed, ", ") +
							", which " + appWideTargets[a.Provider] + ", to every revision at once, the one serving now included. Release this environment direct or blue_green once to make that change.",
					})
					r.Executable = false
					continue
				}
				if conn, cloud, missing := canaryMissing(in, r, a); missing != "" {
					credential := map[string]string{"gcp": "the service account of ", "azure": "the service principal of "}[cloud]
					if credential == "" {
						credential = "the key of "
					}
					r.Problems = append(r.Problems, Finding{
						Code: codeCanaryPermissions, Subject: ServiceSubject, Name: a.Name, Path: "connections." + cloud,
						Message: a.Name + " can't run a canary: " + credential + conn + " isn't allowed " + strings.ReplaceAll(missing, ",", ", ") +
							". Grant them and check the connection again under Integrations, or release this environment direct or blue_green, which don't need them.",
					})
					r.Executable = false
					continue
				}
				release.Canary = append(release.Canary, a.ID)
				continue
			}
			r.Warnings = append(r.Warnings, Finding{
				Code: codeCanaryUnavailable, Subject: ServiceSubject, Name: a.Name,
				Message: a.Name + " can't run a canary: " + reason + ". It deploys all at once.",
			})
		case manifest.StrategyBlueGreen:
			if len(a.Routes) == 0 {
				r.Warnings = append(r.Warnings, Finding{
					Code: codeBlueGreenUnavailable, Subject: ServiceSubject, Name: a.Name,
					Message: a.Name + " has no route release to switch over, so it can't release blue/green. It deploys all at once.",
				})
			}
		}
	}
}

// noCanary says why a service action can't ramp as a canary, or "" when
// it can: an update of an HTTP service on a target whose traffic weighting
// Loomplane drives, outside a Cloud sync, Cloud placement and route
// releases.
func noCanary(in Input, r *Result, a Action) string {
	cfg := a.Service
	switch {
	// Defensive: Cloud sync plans are made without planRelease and store
	// no release, but one that ever carries a release must not ramp.
	case r.CloudSyncID != "":
		return "a Cloud sync moves services as they are"
	case a.Op == Create:
		return "it's new, so there's nothing to compare it with"
	case a.Op == Replace:
		return "it's moving"
	case !releasehealth.SupportFor(a.Provider).Canary:
		return providerTitle(in, a.Provider) + " has no traffic weighting Loomplane can drive"
	case cfg == nil:
		return "the plan has no configuration for it"
	case cfg.Workload == provider.Worker:
		return "it's a worker, so it takes no HTTP traffic"
	case len(a.Routes) > 0:
		return "its domain routes switch over by DNS"
	case cfg.CloudPlacement != nil:
		return "it runs on a Cloud target"
	}
	switch a.Provider {
	case "ecs-fargate":
		if cfg.Port == 0 {
			return "it takes no HTTP traffic"
		}
		if !cfg.Public && !cfg.Internal {
			return "it isn't behind a load balancer"
		}
	case "azure-container-apps":
		if cfg.Port == 0 && cfg.Workload != provider.StaticSite {
			return "it takes no HTTP traffic"
		}
	}
	return ""
}

// unsupportedCanary blocks a canary for a service that could otherwise
// ramp but whose candidate Loomplane can't start beside what runs: the
// database gateway, whose record, keys and routes follow each release in
// one go, and a service that reaches Cloud SQL through a Private Service
// Connect route, whose sidecar and checks a candidate doesn't carry. It
// returns the finding's code and message, or "" when nothing blocks it.
func unsupportedCanary(a Action) (code, message string) {
	switch cfg := a.Service; {
	case cfg.DatabaseGateway != nil:
		return codeCanaryDatabaseGateway, a.Name + " can't run a canary: it's the environment's database gateway, which Loomplane replaces in one go. Release this environment direct or blue_green."
	case cfg.CloudSQLNetwork != nil:
		return codeCanaryDatabaseRoute, a.Name + " can't run a canary: it reaches Cloud SQL through a Private Service Connect route, which a canary candidate doesn't carry. Release this environment direct or blue_green."
	}
	return "", ""
}

// appWideFields are the service settings, by provider, that apply to
// every revision at once, so a canary's baseline would take a change to
// them too. On Container Apps that's the app's ingress: its port, whether
// it's public, and its workload, which decides both for a static site and
// whether it has ingress at all. On Cloud Run it's who may call the
// service, which public decides.
var appWideFields = map[string]map[string]string{
	"azure-container-apps": {"port": "port", "public": "public", "workload": "workload"},
	"cloud-run":            {"public": "public"},
}

// appWideTargets say, by provider, where an app-wide setting applies.
var appWideTargets = map[string]string{
	"azure-container-apps": "Azure Container Apps applies app-wide",
	"cloud-run":            "Cloud Run applies to the whole service",
}

// appWideChanges are the changes an action makes that a canary on its
// provider can't, as "port 8080 to 9090".
func appWideChanges(a Action) []string {
	fields := appWideFields[a.Provider]
	var out []string
	for _, c := range a.Changes {
		if name, ok := fields[c.Field]; ok {
			out = append(out, name+" "+cmp.Or(c.From, "unset")+" to "+cmp.Or(c.To, "unset"))
		}
	}
	return out
}

// canaryClouds are the clouds whose canaries need more than an ordinary
// deploy, by provider.
var canaryClouds = map[string]string{"ecs-fargate": "aws", "cloud-run": "gcp", "azure-container-apps": "azure"}

// canaryMissing names the connection an ECS, Cloud Run or Container Apps
// action deploys with and its cloud, and lists, comma separated, what its
// credential lacks to ramp a canary, as its last check found. A
// connection checked before canaries existed lists nothing, and the
// canary checks again when it starts.
func canaryMissing(in Input, r *Result, a Action) (connection, cloud, missing string) {
	cloud, ok := canaryClouds[a.Provider]
	if !ok {
		return "", "", ""
	}
	id := a.ConnectionID
	if id == "" {
		id = r.Connections[cloud]
	}
	i := slices.IndexFunc(in.Connections, func(c ConnectionOption) bool { return c.ID == id })
	if id == "" || i < 0 {
		return "", "", ""
	}
	return in.Connections[i].Name, cloud, in.Connections[i].Details[connections.DetailCanaryMissing]
}

// providerTitle names a provider for people, or by its name when the
// registry doesn't know it.
func providerTitle(in Input, name string) string {
	if in.Providers != nil {
		if p, err := in.Providers.Get(name); err == nil && p.Title != "" {
			return p.Title
		}
	}
	return name
}

// canaryReader is a store that can tell whether a canary still holds an
// environment. PgStore can; a store that can't never blocks a plan for it.
type canaryReader interface {
	RampingCanary(ctx context.Context, orgID, environmentID string) (deploymentID string, err error)
}

var _ canaryReader = (*PgStore)(nil)

// rampingCanary blocks result while a canary ramps in the plan's
// environment, when the store can tell.
func (s *Service) rampingCanary(ctx context.Context, plan *Plan, result *Result) error {
	reader, ok := s.store.(canaryReader)
	if !ok {
		return nil
	}
	deployment, err := reader.RampingCanary(ctx, plan.OrgID, plan.EnvironmentID)
	if err != nil || deployment == "" {
		return err
	}
	result.Problems = append(result.Problems, Finding{
		Code:    codeCanaryInProgress,
		Message: "A canary release is ramping in " + plan.Environment + " (deployment " + deployment + "). Plan again once it finishes.",
	})
	result.Executable = false
	return nil
}
