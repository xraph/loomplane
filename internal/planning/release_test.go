package planning

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/pkg/provider"
)

// releaseYAML declares a release block for production and nothing else.
func releaseYAML(release string) string {
	return "version: 1\nenvironments:\n  production:\n    release:\n" + release
}

const canaryRelease = "      strategy: canary\n"

// releaseInput is what planRelease reads: the manifest and the environment.
func releaseInput(t *testing.T, yaml string) Input {
	t.Helper()
	return Input{Environment: "production", Manifest: parse(t, yaml), Providers: providers.Registry()}
}

// serviceAction is a service action on providerName, made from cfg.
func serviceAction(name, providerName string, op Op, cfg ServiceConfig) Action {
	cfg.Provider = providerName
	if cfg.Workload == "" {
		cfg.Workload = provider.HTTPService
	}
	return Action{ID: actionID(ServiceSubject, name), Op: op, Subject: ServiceSubject, Name: name, Provider: providerName, Service: &cfg}
}

// A manifest without a release block plans exactly what it did before
// releases existed, so a plan waiting for approval doesn't go stale on
// upgrade. The golden file was captured before plans pinned a release.
func TestComputeWithoutReleaseBlockKeepsResultJSON(t *testing.T) {
	first := compute(t, acme, "production")
	r := compute(t, strings.Replace(acme, "max: 3", "max: 5", 1), "production", deployed(t, first)...)
	if r.Release != nil {
		t.Fatalf("release without a release block: %+v", r.Release)
	}
	got, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/result-no-release.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(got, '\n'), want) {
		t.Fatalf("result JSON changed:\n%s", got)
	}
	if bytes.Contains(got, []byte(`"release"`)) {
		t.Fatal(`result has a "release" key`)
	}
}

func TestCanaryPlanListsRampingServices(t *testing.T) {
	r := &Result{Actions: []Action{
		serviceAction("api", "cloud-run", Update, ServiceConfig{Port: 8080, Public: true}),
		serviceAction("web", "ecs-fargate", Update, ServiceConfig{Public: true, Port: 8080}),
		serviceAction("worker", "cloud-run", Update, ServiceConfig{Workload: provider.Worker}),
	}}
	planRelease(releaseInput(t, releaseYAML(canaryRelease)), r)
	want := &Release{
		Strategy: "canary", Steps: []int{5, 25, 50, 100}, HoldSeconds: 600,
		Thresholds: releasehealth.Thresholds{ErrorRate: 0.01, ErrorRateDelta: 0.005, P95LatencyRatio: 1.3, MinRequests: 200},
		Canary:     []string{"service:api", "service:web"},
	}
	if !reflect.DeepEqual(r.Release, want) {
		t.Fatalf("release = %+v, want %+v", r.Release, want)
	}
	if len(r.Warnings) != 1 {
		t.Fatalf("warnings: %+v", r.Warnings)
	}
	w := r.Warnings[0]
	if w.Code != "canary-unavailable" || w.Subject != ServiceSubject || w.Name != "worker" ||
		w.Message != "worker can't run a canary: it's a worker, so it takes no HTTP traffic. It deploys all at once." {
		t.Fatalf("warning: %+v", w)
	}
	if r.Release.Hold() != 10*time.Minute || !r.Release.Ramps("service:web") || r.Release.Ramps("service:worker") {
		t.Fatalf("hold %v, ramps web %v, ramps worker %v", r.Release.Hold(), r.Release.Ramps("service:web"), r.Release.Ramps("service:worker"))
	}
}

func TestCanaryUnavailableReasons(t *testing.T) {
	routed := serviceAction("api", "cloud-run", Update, ServiceConfig{Port: 8080, Public: true})
	routed.Routes = []cloudinfra.Route{{ID: "route_1"}}
	for _, tc := range []struct {
		name      string
		action    Action
		cloudSync string
		// reason is "" for an action that ramps.
		reason string
	}{
		{name: "cloud run update ramps", action: serviceAction("api", "cloud-run", Update, ServiceConfig{})},
		{name: "new", action: serviceAction("api", "cloud-run", Create, ServiceConfig{}), reason: "it's new, so there's nothing to compare it with"},
		{name: "moving", action: serviceAction("api", "cloud-run", Replace, ServiceConfig{}), reason: "it's moving"},
		{name: "render", action: serviceAction("api", "render", Update, ServiceConfig{Port: 8080, Public: true}), reason: "Render has no traffic weighting Loomplane can drive"},
		{name: "provider plugin", action: serviceAction("api", "acme-cloud", Update, ServiceConfig{Port: 8080}), reason: "acme-cloud has no traffic weighting Loomplane can drive"},
		{name: "worker", action: serviceAction("api", "ecs-fargate", Update, ServiceConfig{Workload: provider.Worker}), reason: "it's a worker, so it takes no HTTP traffic"},
		{name: "ecs public", action: serviceAction("api", "ecs-fargate", Update, ServiceConfig{Port: 8080, Public: true})},
		{name: "ecs internal", action: serviceAction("api", "ecs-fargate", Update, ServiceConfig{Port: 8080, Internal: true})},
		{name: "ecs private", action: serviceAction("api", "ecs-fargate", Update, ServiceConfig{Port: 8080}), reason: "it isn't behind a load balancer"},
		{name: "ecs without a port", action: serviceAction("api", "ecs-fargate", Update, ServiceConfig{Public: true}), reason: "it takes no HTTP traffic"},
		{name: "azure with ingress", action: serviceAction("api", "azure-container-apps", Update, ServiceConfig{Port: 8080})},
		{name: "azure static site", action: serviceAction("api", "azure-container-apps", Update, ServiceConfig{Workload: provider.StaticSite})},
		{name: "azure without a port", action: serviceAction("api", "azure-container-apps", Update, ServiceConfig{}), reason: "it takes no HTTP traffic"},
		{name: "routes", action: routed, reason: "its domain routes switch over by DNS"},
		{name: "cloud placement", action: serviceAction("api", "cloud-run", Update, ServiceConfig{CloudPlacement: &CloudPlacement{CloudID: "cloud_1"}}), reason: "it runs on a Cloud target"},
		{name: "cloud sync", action: serviceAction("api", "cloud-run", Update, ServiceConfig{}), cloudSync: "sync_1", reason: "a Cloud sync moves services as they are"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Result{CloudSyncID: tc.cloudSync, Actions: []Action{tc.action}}
			planRelease(releaseInput(t, releaseYAML(canaryRelease)), r)
			if r.Release == nil {
				t.Fatal("no release")
			}
			if tc.reason == "" {
				if !r.Release.Ramps("service:api") || len(r.Warnings) != 0 {
					t.Fatalf("doesn't ramp: %+v, warnings %+v", r.Release, r.Warnings)
				}
				return
			}
			if r.Release.Ramps("service:api") || len(r.Release.Canary) != 0 {
				t.Fatalf("ramps: %+v", r.Release)
			}
			want := "api can't run a canary: " + tc.reason + ". It deploys all at once."
			if len(r.Warnings) != 1 || r.Warnings[0].Code != "canary-unavailable" || r.Warnings[0].Name != "api" || r.Warnings[0].Message != want {
				t.Fatalf("warnings %+v, want %q", r.Warnings, want)
			}
		})
	}

	// Nothing deploys for a kept or deleted service, or for a resource, so
	// none of them is warned about.
	r := &Result{Actions: []Action{
		serviceAction("kept", "render", Keep, ServiceConfig{}),
		{ID: "service:gone", Op: Delete, Subject: ServiceSubject, Name: "gone", Provider: "render"},
		{ID: "resource:db", Op: Update, Subject: ResourceSubject, Name: "db", Provider: "cloud-sql", Resource: &ResourceConfig{Kind: "postgres"}},
	}}
	planRelease(releaseInput(t, releaseYAML(canaryRelease)), r)
	if r.Release == nil || len(r.Release.Canary) != 0 || len(r.Warnings) != 0 {
		t.Fatalf("release %+v, warnings %+v", r.Release, r.Warnings)
	}
}

func TestBlueGreenWarnsWithoutRoutes(t *testing.T) {
	routed := serviceAction("edge", "server", Update, ServiceConfig{Port: 8080, Public: true})
	routed.Routes = []cloudinfra.Route{{ID: "route_1"}}
	r := &Result{Actions: []Action{
		serviceAction("api", "cloud-run", Update, ServiceConfig{Port: 8080, Public: true}),
		routed,
	}}
	planRelease(releaseInput(t, releaseYAML("      strategy: blue_green\n")), r)
	if r.Release == nil || r.Release.Strategy != "blue_green" || len(r.Release.Canary) != 0 || len(r.Release.Steps) != 0 || r.Release.HoldSeconds != 0 {
		t.Fatalf("release: %+v", r.Release)
	}
	if r.Release.Thresholds != releasehealth.DefaultThresholds() {
		t.Fatalf("thresholds: %+v", r.Release.Thresholds)
	}
	want := "api has no route release to switch over, so it can't release blue/green. It deploys all at once."
	if len(r.Warnings) != 1 || r.Warnings[0].Code != "blue-green-unavailable" || r.Warnings[0].Name != "api" || r.Warnings[0].Message != want {
		t.Fatalf("warnings: %+v", r.Warnings)
	}

	// A direct release pins its thresholds and warns about nothing.
	r = &Result{Actions: []Action{serviceAction("api", "render", Update, ServiceConfig{})}}
	planRelease(releaseInput(t, releaseYAML("      strategy: direct\n      thresholds: {error_rate: 2%}\n      min_requests: 50\n")), r)
	if r.Release == nil || r.Release.Strategy != "direct" || len(r.Warnings) != 0 ||
		r.Release.Thresholds != (releasehealth.Thresholds{ErrorRate: 0.02, ErrorRateDelta: 0.005, P95LatencyRatio: 1.3, MinRequests: 50}) {
		t.Fatalf("direct: %+v, warnings %+v", r.Release, r.Warnings)
	}
}

// A release pins on the plan through what storage writes and reads back,
// including a canary with no service that ramps.
func TestReleaseSurvivesStorage(t *testing.T) {
	for name, release := range map[string]*Release{
		"canary": {
			Strategy: "canary", Steps: []int{10, 100}, HoldSeconds: 900,
			Thresholds: releasehealth.Thresholds{ErrorRate: 0.02, ErrorRateDelta: 0.01, P95LatencyRatio: 1.5, MinRequests: 50},
			Canary:     []string{"service:api"},
		},
		"canary without ramping services": {Strategy: "canary", Steps: []int{5, 25, 50, 100}, HoldSeconds: 600, Thresholds: releasehealth.DefaultThresholds()},
		"none":                            nil,
	} {
		t.Run(name, func(t *testing.T) {
			p := &Plan{}
			p.setResult(&Result{Actions: []Action{}, Release: release})
			raw, err := json.Marshal(p.result())
			if err != nil {
				t.Fatal(err)
			}
			var stored Result
			if err = json.Unmarshal(raw, &stored); err != nil {
				t.Fatal(err)
			}
			read := &Plan{}
			read.setResult(&stored)
			if !reflect.DeepEqual(read.Release, release) {
				t.Fatalf("read %+v, want %+v", read.Release, release)
			}
			if release != nil && len(release.Canary) == 0 && (read.Release.Ramps("service:api") || len(read.Release.Canary) != 0) {
				t.Fatalf("an empty canary ramps: %+v", read.Release)
			}
		})
	}
}

func TestPlanThresholdsDefault(t *testing.T) {
	if got := (&Plan{}).Thresholds(); got != releasehealth.DefaultThresholds() {
		t.Fatalf("no release: %+v", got)
	}
	mine := releasehealth.Thresholds{ErrorRate: 0.05, ErrorRateDelta: 0.01, P95LatencyRatio: 2, MinRequests: 10}
	if got := (&Plan{Release: &Release{Strategy: "direct", Thresholds: mine}}).Thresholds(); got != mine {
		t.Fatalf("with a release: %+v", got)
	}
	var none *Release
	if none.Hold() != 0 || none.Ramps("service:api") {
		t.Fatal("a nil release holds or ramps")
	}
}

// A canary on ECS whose connection's key can't split a listener is
// refused at plan time, and says how to release instead.
func TestCanaryOnECSNeedsTheCanaryPermissions(t *testing.T) {
	in := releaseInput(t, releaseYAML(canaryRelease))
	in.Connections = []ConnectionOption{{ID: "conn_1", Name: "Prod", Cloud: "aws", Ready: true, Details: map[string]string{
		connections.DetailCanaryMissing: "elasticloadbalancing:DescribeTargetHealth,elasticloadbalancing:ModifyListener",
	}}}
	web := serviceAction("web", "ecs-fargate", Update, ServiceConfig{Public: true, Port: 8080})
	r := &Result{Actions: []Action{web}, Connections: map[string]string{"aws": "conn_1"}, Executable: true}
	planRelease(in, r)
	if r.Executable || r.Release.Ramps(web.ID) || len(r.Problems) != 1 {
		t.Fatalf("executable %v, release %+v, problems %+v", r.Executable, r.Release, r.Problems)
	}
	p := r.Problems[0]
	for _, want := range []string{"Prod", "elasticloadbalancing:DescribeTargetHealth, elasticloadbalancing:ModifyListener", "direct or blue_green"} {
		if p.Code != codeCanaryPermissions || p.Name != "web" || !strings.Contains(p.Message, want) {
			t.Fatalf("problem %+v doesn't say %q", p, want)
		}
	}

	// With the permissions, or with a direct release, nothing is refused.
	in.Connections[0].Details = map[string]string{}
	r = &Result{Actions: []Action{web}, Connections: map[string]string{"aws": "conn_1"}, Executable: true}
	planRelease(in, r)
	if !r.Executable || !r.Release.Ramps(web.ID) || len(r.Problems) != 0 {
		t.Fatalf("executable %v, release %+v, problems %+v", r.Executable, r.Release, r.Problems)
	}
	in = releaseInput(t, releaseYAML("      strategy: direct\n"))
	in.Connections = []ConnectionOption{{ID: "conn_1", Name: "Prod", Cloud: "aws", Ready: true, Details: map[string]string{
		connections.DetailCanaryMissing: "elasticloadbalancing:ModifyListener",
	}}}
	r = &Result{Actions: []Action{web}, Connections: map[string]string{"aws": "conn_1"}, Executable: true}
	planRelease(in, r)
	if !r.Executable || len(r.Problems) != 0 {
		t.Fatalf("a direct release: executable %v, problems %+v", r.Executable, r.Problems)
	}
}

func TestCanaryOnCloudRunNeedsTheCanaryPermissions(t *testing.T) {
	in := releaseInput(t, releaseYAML(canaryRelease))
	in.Connections = []ConnectionOption{{ID: "conn_g", Name: "Prod", Cloud: "gcp", Ready: true, Details: map[string]string{
		connections.DetailCanaryMissing: "run.revisions.delete,run.revisions.get",
	}}}
	api := serviceAction("api", "cloud-run", Update, ServiceConfig{Public: true, Port: 8080})
	r := &Result{Actions: []Action{api}, Connections: map[string]string{"gcp": "conn_g"}, Executable: true}
	planRelease(in, r)
	if r.Executable || r.Release.Ramps(api.ID) || len(r.Problems) != 1 {
		t.Fatalf("executable %v, release %+v, problems %+v", r.Executable, r.Release, r.Problems)
	}
	p := r.Problems[0]
	for _, want := range []string{"the service account of Prod", "run.revisions.delete, run.revisions.get", "direct or blue_green"} {
		if p.Code != codeCanaryPermissions || p.Name != "api" || p.Path != "connections.gcp" || !strings.Contains(p.Message, want) {
			t.Fatalf("problem %+v doesn't say %q", p, want)
		}
	}

	in.Connections[0].Details = map[string]string{}
	r = &Result{Actions: []Action{api}, Connections: map[string]string{"gcp": "conn_g"}, Executable: true}
	planRelease(in, r)
	if !r.Executable || !r.Release.Ramps(api.ID) || len(r.Problems) != 0 {
		t.Fatalf("executable %v, release %+v, problems %+v", r.Executable, r.Release, r.Problems)
	}
}

func TestCanaryOnContainerAppsNeedsTheCanaryActions(t *testing.T) {
	in := releaseInput(t, releaseYAML(canaryRelease))
	in.Connections = []ConnectionOption{{ID: "conn_az", Name: "Prod", Cloud: "azure", Ready: true, Details: map[string]string{
		connections.DetailCanaryMissing: "Microsoft.App/containerApps/revisions/deactivate/action,Microsoft.App/containerApps/listSecrets/action",
	}}}
	api := serviceAction("api", "azure-container-apps", Update, ServiceConfig{Public: true, Port: 8080})
	r := &Result{Actions: []Action{api}, Connections: map[string]string{"azure": "conn_az"}, Executable: true}
	planRelease(in, r)
	if r.Executable || r.Release.Ramps(api.ID) || len(r.Problems) != 1 {
		t.Fatalf("executable %v, release %+v, problems %+v", r.Executable, r.Release, r.Problems)
	}
	p := r.Problems[0]
	for _, want := range []string{"the service principal of Prod", "Microsoft.App/containerApps/revisions/deactivate/action, Microsoft.App/containerApps/listSecrets/action", "direct or blue_green"} {
		if p.Code != codeCanaryPermissions || p.Name != "api" || p.Path != "connections.azure" || !strings.Contains(p.Message, want) {
			t.Fatalf("problem %+v doesn't say %q", p, want)
		}
	}

	in.Connections[0].Details = map[string]string{}
	r = &Result{Actions: []Action{api}, Connections: map[string]string{"azure": "conn_az"}, Executable: true}
	planRelease(in, r)
	if !r.Executable || !r.Release.Ramps(api.ID) || len(r.Problems) != 0 {
		t.Fatalf("executable %v, release %+v, problems %+v", r.Executable, r.Release, r.Problems)
	}
}

// Container Apps applies ingress to every revision at once, so a release
// that changes it can't canary: the baseline would take the change too.
func TestCanaryOnContainerAppsRefusesAnAppWideChange(t *testing.T) {
	in := releaseInput(t, releaseYAML(canaryRelease))
	for what, change := range map[string]Change{
		"port":   {Field: "port", From: "8080", To: "9090"},
		"public": {Field: "public", From: "true", To: "false"},
	} {
		t.Run(what, func(t *testing.T) {
			api := serviceAction("api", "azure-container-apps", Update, ServiceConfig{Public: true, Port: 8080})
			api.Changes = []Change{{Field: "commit", From: "abc", To: "def"}, change}
			r := &Result{Actions: []Action{api}, Executable: true}
			planRelease(in, r)
			if r.Executable || r.Release.Ramps(api.ID) || len(r.Problems) != 1 {
				t.Fatalf("executable %v, release %+v, problems %+v", r.Executable, r.Release, r.Problems)
			}
			p := r.Problems[0]
			for _, want := range []string{what + " " + change.From + " to " + change.To, "app-wide", "direct or blue_green"} {
				if p.Code != codeCanaryAppWide || p.Name != "api" || !strings.Contains(p.Message, want) {
					t.Fatalf("problem %+v doesn't say %q", p, want)
				}
			}
		})
	}
	// A release that keeps the ingress canaries; so does one on Cloud Run
	// that changes its port, since each revision there has its own.
	api := serviceAction("api", "azure-container-apps", Update, ServiceConfig{Public: true, Port: 8080})
	api.Changes = []Change{{Field: "commit", From: "abc", To: "def"}}
	run := serviceAction("web", "cloud-run", Update, ServiceConfig{Public: true, Port: 9090})
	run.Changes = []Change{{Field: "port", From: "8080", To: "9090"}}
	r := &Result{Actions: []Action{api, run}, Executable: true}
	planRelease(in, r)
	if !r.Executable || !r.Release.Ramps(api.ID) || !r.Release.Ramps(run.ID) || len(r.Problems) != 0 {
		t.Fatalf("executable %v, release %+v, problems %+v", r.Executable, r.Release, r.Problems)
	}
}

// A service that reaches Cloud SQL through a PSC route can't canary: its
// candidate wouldn't carry the route. The plan says so and blocks, since a
// canary environment promised a ramp.
func TestCanaryRefusesACloudSQLPSCRoute(t *testing.T) {
	in := releaseInput(t, releaseYAML(canaryRelease))
	api := serviceAction("api", "cloud-run", Update, ServiceConfig{Public: true, Port: 8080, CloudSQLNetwork: &provider.CloudSQLApplicationNetwork{ConsumerProject: "shop", Network: "default", Subnetwork: "projects/shop/regions/europe-west1/subnetworks/default"}})
	web := serviceAction("web", "cloud-run", Update, ServiceConfig{Public: true, Port: 8080})
	r := &Result{Actions: []Action{api, web}, Executable: true}
	planRelease(in, r)
	if r.Executable || r.Release.Ramps(api.ID) || !r.Release.Ramps(web.ID) || len(r.Problems) != 1 {
		t.Fatalf("executable %v, release %+v, problems %+v", r.Executable, r.Release, r.Problems)
	}
	p := r.Problems[0]
	for _, want := range []string{"api can't run a canary", "Private Service Connect", "direct or blue_green"} {
		if p.Code != "canary-unsupported-database-route" || p.Name != "api" || p.Path != "services.api" || !strings.Contains(p.Message, want) {
			t.Fatalf("problem %+v doesn't say %q", p, want)
		}
	}
	// A new one deploys all at once anyway, so nothing blocks.
	api.Op = Create
	r = &Result{Actions: []Action{api}, Executable: true}
	planRelease(in, r)
	if !r.Executable || len(r.Problems) != 0 || len(r.Warnings) != 1 {
		t.Fatalf("executable %v, problems %+v, warnings %+v", r.Executable, r.Problems, r.Warnings)
	}
}

// The database gateway never ramps: its record, keys and routes follow a
// release in one go.
func TestCanaryRefusesTheDatabaseGateway(t *testing.T) {
	in := releaseInput(t, releaseYAML(canaryRelease))
	gw := serviceAction(dbGatewayName, "cloud-run", Update, ServiceConfig{Port: 5432, Public: true, DatabaseGateway: &DatabaseGatewayConfig{Name: dbGatewayName, Suffix: "europe-west1.db.example.com", Resources: []string{"db"}}})
	r := &Result{Actions: []Action{gw}, Executable: true}
	planRelease(in, r)
	if r.Executable || r.Release.Ramps(gw.ID) || len(r.Problems) != 1 {
		t.Fatalf("executable %v, release %+v, problems %+v", r.Executable, r.Release, r.Problems)
	}
	p := r.Problems[0]
	for _, want := range []string{"can't run a canary", "database gateway", "direct or blue_green"} {
		if p.Code != "canary-unsupported-database-gateway" || p.Name != dbGatewayName || !strings.Contains(p.Message, want) {
			t.Fatalf("problem %+v doesn't say %q", p, want)
		}
	}
}

// Who may call a Cloud Run service is the service's, not a revision's, so
// a release that flips public would switch access for all of the traffic
// at Promote with no step judging it. The plan refuses it as Container
// Apps refuses an ingress change.
func TestCanaryOnCloudRunRefusesFlippingPublic(t *testing.T) {
	in := releaseInput(t, releaseYAML(canaryRelease))
	api := serviceAction("api", "cloud-run", Update, ServiceConfig{Port: 8080})
	api.Changes = []Change{{Field: "commit", From: "abc", To: "def"}, {Field: "public", From: "true", To: "false"}}
	r := &Result{Actions: []Action{api}, Executable: true}
	planRelease(in, r)
	if r.Executable || r.Release.Ramps(api.ID) || len(r.Problems) != 1 {
		t.Fatalf("executable %v, release %+v, problems %+v", r.Executable, r.Release, r.Problems)
	}
	p := r.Problems[0]
	for _, want := range []string{"public true to false", "Cloud Run applies to the whole service", "direct or blue_green"} {
		if p.Code != codeCanaryAppWide || p.Name != "api" || !strings.Contains(p.Message, want) {
			t.Fatalf("problem %+v doesn't say %q", p, want)
		}
	}
}
