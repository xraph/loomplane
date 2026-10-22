package planning

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/providers"
)

// dbgwManifest opts one DigitalOcean Postgres into the gateway, with an
// app on Cloud Run and a site on Vercel that both need it.
const dbgwManifest = `version: 1
services:
  api: {source: services/api, provider: cloud-run, needs: [db]}
  web: {source: apps/web, provider: vercel, needs: [db]}
resources:
  db: {kind: postgres, provider: digitalocean, gateway: true}
database_gateway: {provider: cloud-run, region: us-east1}
`

var dbgwConnections = append(slices.Clone(connected), ConnectionOption{ID: "conn_do", Name: "DO", Cloud: "digitalocean", Ready: true})

var exampleZone = []cloudinfra.ProjectZone{{ID: "zone_a", Name: "Example.com", Verified: true}}

func dbgwInput(t *testing.T, doc string, zones []cloudinfra.ProjectZone, bindings ...*Binding) Input {
	t.Helper()
	return Input{
		Environment: "production", Graph: polyglot(t), Manifest: parse(t, doc),
		Providers: providers.Registry(), Connections: dbgwConnections, Bindings: bindings, Zones: zones,
	}
}

func dbgwCompute(t *testing.T, doc string, zones []cloudinfra.ProjectZone, bindings ...*Binding) *Result {
	t.Helper()
	return Compute(dbgwInput(t, doc, zones, bindings...))
}

func finding(t *testing.T, fs []Finding, code string) Finding {
	t.Helper()
	for _, f := range fs {
		if f.Code == code {
			return f
		}
	}
	t.Fatalf("no %s in %v", code, codes(fs))
	return Finding{}
}

func TestGatewayReachesMatrix(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		gateway, conn, where string
		backend              ResourceConfig
		ok                   bool
		why                  string
	}{
		{"do from cloud run", cloudRun, "", "us-east1", ResourceConfig{Provider: "digitalocean", Region: "nyc"}, true, ""},
		{"do from ecs", ecsFargate, "conn_aws", "us-east-1", ResourceConfig{Provider: "digitalocean", Region: "nyc"}, true, ""},
		{"rds from ecs, same connection and region", ecsFargate, "conn_aws", "us-east-1", ResourceConfig{Provider: "rds", Region: "us-east-1", Target: "conn_aws"}, true, ""},
		{"rds from cloud run", cloudRun, "", "us-east1", ResourceConfig{Provider: "rds", Region: "us-east-1", Target: "conn_aws"}, false, "RDS is private to its VPC"},
		{"rds from ecs, other connection", ecsFargate, "conn_aws", "us-east-1", ResourceConfig{Provider: "rds", Region: "us-east-1", Target: "conn_other"}, false, "RDS is private to its VPC"},
		{"rds from ecs, other region", ecsFargate, "conn_aws", "us-east-1", ResourceConfig{Provider: "rds", Region: "eu-west-1", Target: "conn_aws"}, false, "RDS is private to its VPC"},
		{"managed rds from ecs", ecsFargate, "conn_aws", "us-east-1", ResourceConfig{Provider: "rds", Region: "us-east-1", Managed: &ManagedDatabaseConfig{ConnectionID: "conn_aws"}}, true, ""},
		{"cloud sql", cloudRun, "", "us-east1", ResourceConfig{Provider: "cloud-sql"}, false, "not supported by the database gateway yet"},
		{"server", ecsFargate, "conn_aws", "us-east-1", ResourceConfig{Provider: "server"}, false, "not supported by the database gateway yet"},
		{"kubernetes", cloudRun, "", "us-east1", ResourceConfig{Provider: "kubernetes"}, false, "not supported by the database gateway yet"},
		{"azure", cloudRun, "", "us-east1", ResourceConfig{Provider: "azure-database"}, false, "not supported by the database gateway yet"},
		{"render", cloudRun, "", "us-east1", ResourceConfig{Provider: "render"}, false, "not supported by the database gateway yet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, why := gatewayReaches(tc.gateway, tc.conn, tc.where, &tc.backend)
			if ok != tc.ok || !strings.Contains(why, tc.why) {
				t.Fatalf("gatewayReaches = %v %q, want %v %q", ok, why, tc.ok, tc.why)
			}
			if !ok && strings.Contains(why, "\u2014") {
				t.Fatalf("reason has an em dash: %q", why)
			}
		})
	}
}

func TestPlanDatabaseGateway(t *testing.T) {
	r := dbgwCompute(t, dbgwManifest, exampleZone)
	if len(r.Problems) > 0 {
		t.Fatalf("problems: %+v", r.Problems)
	}
	gw := action(t, r, "service:db-gateway")
	cfg := gw.Service
	if gw.Op != Create || gw.Provider != cloudRun || gw.Region != "us-east1" {
		t.Fatalf("db-gateway action %+v", gw)
	}
	if cfg.Source != "." || cfg.Ecosystem != "go" || cfg.Image != dbgateway.Dockerfile || cfg.Port != dbgateway.Port || !cfg.Public {
		t.Fatalf("db-gateway config %+v", cfg)
	}
	// Two instances at least, so one draining or cold never leaves apps
	// without their database.
	if cfg.Scale == nil || cfg.Scale.Min != 2 || cfg.Scale.Max != 0 {
		t.Fatalf("db-gateway scale %+v, want at least 2 instances", cfg.Scale)
	}
	want := DatabaseGatewayConfig{Name: "db-gateway", Suffix: "us-east1.db.example.com", Resources: []string{"db"}}
	if d := cfg.DatabaseGateway; d == nil || d.Name != want.Name || d.Suffix != want.Suffix || !slices.Equal(d.Resources, want.Resources) {
		t.Fatalf("DatabaseGateway = %+v, want %+v", cfg.DatabaseGateway, want)
	}
	for _, name := range []string{"api", "web"} {
		app := action(t, r, "service:"+name)
		if !slices.Contains(app.DependsOn, "service:db-gateway") {
			t.Fatalf("%s doesn't depend on the gateway: %v", name, app.DependsOn)
		}
		if app.Step <= gw.Step {
			t.Fatalf("%s runs at step %d, the gateway at %d", name, app.Step, gw.Step)
		}
	}
}

func TestPlanDatabaseGatewaySortsResources(t *testing.T) {
	doc := strings.Replace(dbgwManifest, "resources:\n", "resources:\n  orders: {kind: postgres, provider: digitalocean, gateway: true}\n", 1)
	r := dbgwCompute(t, doc, exampleZone)
	if got := action(t, r, "service:db-gateway").Service.DatabaseGateway.Resources; !slices.Equal(got, []string{"db", "orders"}) {
		t.Fatalf("resources = %v", got)
	}
}

func TestPlanDatabaseGatewayOnlyWhenOptedIn(t *testing.T) {
	doc := strings.Replace(dbgwManifest, ", gateway: true", "", 1)
	r := dbgwCompute(t, doc, nil)
	for _, a := range r.Actions {
		if a.Name == "db-gateway" {
			t.Fatalf("planned a gateway nobody opted into: %+v", a)
		}
	}
	doc = strings.Replace(dbgwManifest, "database_gateway: {provider: cloud-run, region: us-east1}", "database_gateway:\n  provider: cloud-run\n  region: us-east1\n", 1)
	doc = strings.Replace(doc, ", gateway: true}", ", gateway: true, environments: {production: {gateway: false}}}", 1)
	// Without the gateway, the services can't reach DigitalOcean.
	r = dbgwCompute(t, doc, nil)
	if slices.ContainsFunc(r.Actions, func(a Action) bool { return a.Name == "db-gateway" }) || !slices.Equal(codes(r.Problems), []string{"unreachable-resource:api", "unreachable-resource:web"}) {
		t.Fatalf("an environment's gateway: false still planned it: %v", codes(r.Problems))
	}
}

func TestPlanDatabaseGatewayReplanIsNoOp(t *testing.T) {
	first := dbgwCompute(t, dbgwManifest, exampleZone)
	if len(first.Problems) > 0 {
		t.Fatalf("problems: %+v", first.Problems)
	}
	again := dbgwCompute(t, dbgwManifest, exampleZone, deployed(t, first)...)
	if len(again.Problems) > 0 {
		t.Fatalf("problems: %+v", again.Problems)
	}
	for _, a := range again.Actions {
		if a.Op != Keep {
			t.Fatalf("%s is %s (%s) on an unchanged re-plan: %+v", a.ID, a.Op, a.Reason, a.Changes)
		}
	}
	// A new zone name is a new suffix, which redeploys the gateway.
	moved := dbgwCompute(t, dbgwManifest, []cloudinfra.ProjectZone{{ID: "zone_a", Name: "other.dev", Verified: true}}, deployed(t, first)...)
	if gw := action(t, moved, "service:db-gateway"); gw.Op != Update {
		t.Fatalf("a changed suffix planned %s", gw.Op)
	}
}

// A deployed gateway the plan can't plan this time, here for want of a
// zone, isn't deleted: the problem says why.
func TestPlanDatabaseGatewayProblemKeepsDeployedGateway(t *testing.T) {
	first := dbgwCompute(t, dbgwManifest, exampleZone)
	r := dbgwCompute(t, dbgwManifest, nil, deployed(t, first)...)
	finding(t, r.Problems, "db-gateway-domain-missing")
	for _, a := range r.Actions {
		if a.Name == "db-gateway" && a.Op == Delete {
			t.Fatal("a problem deleted the deployed gateway")
		}
	}
}

func TestPlanDatabaseGatewayRemovedWhenNoLongerWanted(t *testing.T) {
	first := dbgwCompute(t, dbgwManifest, exampleZone)
	doc := strings.Replace(dbgwManifest, ", gateway: true", "", 1)
	r := dbgwCompute(t, doc, exampleZone, deployed(t, first)...)
	if gw := action(t, r, "service:db-gateway"); gw.Op != Delete {
		t.Fatalf("db-gateway is %s, want delete", gw.Op)
	}
}

func TestDatabaseGatewayFindings(t *testing.T) {
	two := []cloudinfra.ProjectZone{{ID: "zone_a", Name: "a.com", Verified: true}, {ID: "zone_b", Name: "b.com", Verified: true}}
	for _, tc := range []struct {
		name, doc string
		zones     []cloudinfra.ProjectZone
		code      string
		path      string
		message   string
	}{
		{"missing", strings.Replace(dbgwManifest, "database_gateway: {provider: cloud-run, region: us-east1}\n", "", 1), exampleZone,
			"db-gateway-missing", "resources.db.gateway", "database_gateway"},
		{"domain missing", dbgwManifest, nil, "db-gateway-domain-missing", "database_gateway", "DNS zone"},
		{"unverified zone", dbgwManifest, []cloudinfra.ProjectZone{{ID: "zone_a", Name: "example.com"}}, "db-gateway-domain-missing", "database_gateway", "DNS zone"},
		{"domain ambiguous", dbgwManifest, two, "db-gateway-domain-ambiguous", "database_gateway", "a.com, b.com"},
		{"backend unsupported", strings.Replace(dbgwManifest, "provider: digitalocean", "provider: cloud-sql", 1), exampleZone,
			"db-gateway-backend-unsupported", "resources.db.provider", "not supported by the database gateway yet"},
		{"rds from cloud run", strings.Replace(dbgwManifest, "provider: digitalocean", "provider: rds", 1), exampleZone,
			"db-gateway-backend-unsupported", "resources.db.provider", "RDS is private to its VPC"},
		{"name invalid", strings.ReplaceAll(dbgwManifest, "db", "db-"), exampleZone, "db-gateway-name-invalid", "resources.db-", "db-"},
		{"name taken", strings.Replace(dbgwManifest, "services:\n", "services:\n  db-gateway: {source: services/api, provider: cloud-run}\n", 1), exampleZone,
			"db-gateway-name-taken", "services.db-gateway", "db-gateway"},
		{"unsupported", strings.Replace(dbgwManifest, "{provider: cloud-run, region: us-east1}", "{provider: vercel}", 1), exampleZone,
			"db-gateway-unsupported", "database_gateway.provider", "Cloud Run and ECS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := dbgwCompute(t, tc.doc, tc.zones)
			f := finding(t, r.Problems, tc.code)
			if f.Path != tc.path || !strings.Contains(f.Message, tc.message) || strings.Contains(f.Message, "\u2014") {
				t.Fatalf("%s = %+v, want path %s and a message with %q", tc.code, f, tc.path, tc.message)
			}
			if r.Executable {
				t.Fatal("a plan with a problem is executable")
			}
		})
	}
}

func TestDatabaseGatewayRegionMismatch(t *testing.T) {
	r := dbgwCompute(t, dbgwManifest, exampleZone)
	f := finding(t, r.Warnings, "db-gateway-region-mismatch")
	want := "The gateway runs in us-east1 and db runs in nyc, so every query crosses regions."
	if f.Message != want || f.Path != "database_gateway.region" {
		t.Fatalf("warning = %+v, want %q", f, want)
	}
	if !r.Executable {
		t.Fatalf("a region mismatch blocked the plan: %+v", r.Problems)
	}
}

// RDS behind a gateway on ECS, with the same connection and region, is
// reachable, and nothing warns.
func TestDatabaseGatewayRDSOnECS(t *testing.T) {
	doc := strings.Replace(dbgwManifest, "provider: digitalocean", "provider: rds", 1)
	doc = strings.Replace(doc, "{provider: cloud-run, region: us-east1}", "{provider: ecs-fargate}", 1)
	in := dbgwInput(t, doc, exampleZone)
	in.Connections = httpsAWS(in.Connections)
	r := Compute(in)
	if len(r.Problems) > 0 {
		t.Fatalf("problems: %+v", r.Problems)
	}
	if gw := action(t, r, "service:db-gateway"); gw.Service.DatabaseGateway.Suffix != "us-east-1.db.example.com" || gw.Service.Scale == nil || gw.Service.Scale.Min != 2 {
		t.Fatalf("suffix = %s, scale %+v", gw.Service.DatabaseGateway.Suffix, gw.Service.Scale)
	}
	if slices.ContainsFunc(r.Warnings, func(f Finding) bool { return f.Code == "db-gateway-region-mismatch" }) {
		t.Fatalf("warned about a region match: %+v", r.Warnings)
	}
}

func TestDatabaseGatewayDomainNarrowsToRouteZones(t *testing.T) {
	in := dbgwInput(t, dbgwManifest, []cloudinfra.ProjectZone{{ID: "zone_a", Name: "a.com", Verified: true}, {ID: "zone_b", Name: "B.com", Verified: true}})
	in.Routes = []cloudinfra.Route{{ID: "route", Input: cloudinfra.RouteInput{ZoneID: "zone_b", Service: "api", Hostname: "api.b.com"}}}
	r := Compute(in)
	if gw := action(t, r, "service:db-gateway"); gw.Service.DatabaseGateway.Suffix != "us-east1.db.b.com" {
		t.Fatalf("suffix = %s, problems %+v", gw.Service.DatabaseGateway.Suffix, r.Problems)
	}
	in.Routes = append(in.Routes, cloudinfra.Route{ID: "route2", Input: cloudinfra.RouteInput{ZoneID: "zone_a", Service: "web", Hostname: "a.com"}})
	f := finding(t, Compute(in).Problems, "db-gateway-domain-ambiguous")
	if !strings.Contains(f.Message, "a.com, b.com") {
		t.Fatalf("ambiguous finding doesn't name the zones: %+v", f)
	}
}

func TestDatabaseGatewayLabels(t *testing.T) {
	for name, ok := range map[string]bool{"db": true, "orders-2": true, "my_db": false, "db-": false, "a--b": false, "Orders": false, strings.Repeat("a", 41): false} {
		if err := dbGatewayLabel(name); (err == nil) != ok {
			t.Errorf("dbGatewayLabel(%q) = %v", name, err)
		}
	}
}

// dbgwCloudFixture is a Cloud with a Cloud Run target tagged db, a Vercel
// target tagged web, and a default placement on ECS.
func dbgwCloudFixture() (*cloudprofiles.Assignment, []ConnectionOption) {
	return &cloudprofiles.Assignment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", CloudID: "cloud", CloudRevision: 3, Revision: 4,
			Targets: []cloudprofiles.Target{
				{ID: "target-run", OrgID: "org", ProjectID: "project", CloudID: "cloud", Name: "run", Provider: cloudRun, ConnectionID: "g", Region: "us-east4", Revision: 5, Tags: []string{"db"}},
				{ID: "target-web", OrgID: "org", ProjectID: "project", CloudID: "cloud", Name: "web", Provider: "vercel", ConnectionID: "v", Revision: 6, Tags: []string{"web"}},
			},
			DefaultPlacement: &cloudprofiles.DefaultPlacement{Provider: ecsFargate, ConnectionID: "aws", Region: "us-east-2"},
		}, []ConnectionOption{
			{ID: "g", Cloud: "gcp", Ready: true, Details: map[string]string{"project_id": "proj"}},
			{ID: "v", Cloud: "vercel", Ready: true, Details: map[string]string{"team_id": "team"}},
			{ID: "aws", Cloud: "aws", Ready: true, Account: "123456789012", Region: "us-east-2", Details: map[string]string{"region": "us-east-2"}},
		}
}

const dbgwPlacementBase = "version: 1\nresources:\n  db: {kind: postgres, provider: digitalocean, gateway: true}\n"

func TestDatabaseGatewayPlacementPrecedence(t *testing.T) {
	a, c := dbgwCloudFixture()
	for _, tc := range []struct {
		name, doc, env string
		provider       string
		target         string // the pinned target's ID, "default", or "" for no pin
	}{
		{"block cloud_tags", "database_gateway: {cloud_tags: [db]}\n", "production", cloudRun, "target-run"},
		{"block provider", "database_gateway: {provider: ecs-fargate}\n", "production", ecsFargate, ""},
		{"environment provider over block tags", "database_gateway:\n  cloud_tags: [db]\n  environments: {staging: {provider: ecs-fargate}}\n", "staging", ecsFargate, ""},
		{"block provider over default tags", "defaults: {cloud_tags: [web]}\ndatabase_gateway: {provider: cloud-run}\n", "production", cloudRun, ""},
		{"default cloud_tags", "defaults: {cloud_tags: [db]}\ndatabase_gateway: {region: us-east1}\n", "production", cloudRun, "target-run"},
		{"default provider", "defaults: {provider: cloud-run}\ndatabase_gateway: {}\n", "production", cloudRun, ""},
		{"cloud default", "database_gateway: {}\n", "production", ecsFargate, "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved, pins, err := ResolveCloudTargets(parse(t, dbgwPlacementBase+tc.doc), tc.env, a, c)
			if err != nil {
				t.Fatal(err)
			}
			pin, pinned := pins[dbGatewayName]
			switch tc.target {
			case "":
				if pinned {
					t.Fatalf("pinned %+v", pin)
				}
			case "default":
				if !pinned || !pin.Default || pin.ConnectionID != "aws" {
					t.Fatalf("pin = %+v", pin)
				}
			default:
				if !pinned || pin.TargetID != tc.target {
					t.Fatalf("pin = %+v", pin)
				}
			}
			if got := dbGatewaySettings(resolved, tc.env).Provider; got != tc.provider {
				t.Fatalf("provider = %q, want %q", got, tc.provider)
			}
		})
	}
}

func TestDatabaseGatewayPlacementTagFindings(t *testing.T) {
	a, c := dbgwCloudFixture()
	_, _, err := ResolveCloudTargets(parse(t, dbgwPlacementBase+"database_gateway: {cloud_tags: [gpu]}\n"), "production", a, c)
	if f := placementError(t, err, "cloud-tag-unmatched"); f.Path != "database_gateway.cloud_tags" {
		t.Fatalf("path = %s", f.Path)
	}
	a.Targets[1].Tags = []string{"db"}
	_, _, err = ResolveCloudTargets(parse(t, dbgwPlacementBase+"database_gateway: {cloud_tags: [db]}\n"), "production", a, c)
	if f := placementError(t, err, "cloud-tag-ambiguous"); f.Path != "database_gateway.cloud_tags" {
		t.Fatalf("path = %s", f.Path)
	}
	if _, _, err = ResolveCloudTargets(parse(t, dbgwPlacementBase+"database_gateway: {cloud_tags: [db]}\n"), "production", nil, c); err == nil {
		t.Fatal("tags without an assigned Cloud accepted")
	}
	// Nothing opts in, so the gateway's tags aren't read.
	if _, pins, err := ResolveCloudTargets(parse(t, "version: 1\ndatabase_gateway: {cloud_tags: [gpu]}\n"), "production", a, c); err != nil || len(pins) != 0 {
		t.Fatalf("resolved an unused gateway: %+v %v", pins, err)
	}
}

func TestDatabaseGatewayCloudTagsPlacement(t *testing.T) {
	a, c := dbgwCloudFixture()
	c = append(c, ConnectionOption{ID: "conn_do", Cloud: "digitalocean", Ready: true})
	m := parse(t, dbgwPlacementBase+"services:\n  api: {source: services/api, provider: cloud-run, needs: [db]}\ndatabase_gateway: {cloud_tags: [db]}\n")
	resolved, pins, err := ResolveCloudTargets(m, "production", a, c)
	if err != nil {
		t.Fatal(err)
	}
	r := Compute(Input{Environment: "production", Graph: polyglot(t), Manifest: resolved, Providers: providers.Registry(), Connections: c, CloudPlacements: pins, Zones: exampleZone})
	if len(r.Problems) > 0 {
		t.Fatalf("problems: %+v", r.Problems)
	}
	gw := action(t, r, "service:db-gateway")
	if gw.Provider != cloudRun || gw.Region != "us-east4" || gw.ConnectionID != "g" || gw.Service.CloudPlacement == nil || gw.Service.CloudPlacement.TargetID != "target-run" {
		t.Fatalf("db-gateway not placed on the tagged target: %+v", gw)
	}
	if gw.Service.DatabaseGateway.Suffix != "us-east4.db.example.com" {
		t.Fatalf("suffix = %s", gw.Service.DatabaseGateway.Suffix)
	}

	// Tags that pick a target Loomplane can't run the gateway on.
	resolved, pins, err = ResolveCloudTargets(parse(t, dbgwPlacementBase+"database_gateway: {cloud_tags: [web]}\n"), "production", a, c)
	if err != nil {
		t.Fatal(err)
	}
	r = Compute(Input{Environment: "production", Graph: polyglot(t), Manifest: resolved, Providers: providers.Registry(), Connections: c, CloudPlacements: pins, Zones: exampleZone})
	if f := finding(t, r.Problems, "db-gateway-unsupported"); !strings.Contains(f.Message, "Cloud Run and ECS") {
		t.Fatalf("finding = %+v", f)
	}
}

// The database gateway comes from no repository, so holding the
// application's repository back doesn't hold it.
func TestDatabaseGatewayIsNeverHeld(t *testing.T) {
	first := dbgwCompute(t, dbgwManifest, exampleZone)
	in := dbgwInput(t, dbgwManifest, exampleZone, deployed(t, first)...)
	in.Held = map[string]bool{"other": true}
	r := Compute(in)
	if gw := action(t, r, "service:db-gateway"); gw.Op != Keep || gw.Reason != "Unchanged." || gw.Service.DatabaseGateway == nil {
		t.Fatalf("db-gateway = %s %q", gw.Op, gw.Reason)
	}
}

func TestCloudResolutionManifestCountsDatabaseGateway(t *testing.T) {
	for _, tc := range []struct {
		doc                string
		explicit, fallback bool
	}{
		{dbgwPlacementBase + "database_gateway: {cloud_tags: [db]}\n", true, false},
		{dbgwPlacementBase + "database_gateway: {}\n", false, true},
		{dbgwPlacementBase + "database_gateway: {provider: cloud-run}\n", false, false},
		{"version: 1\ndatabase_gateway: {cloud_tags: [db]}\n", false, false},
	} {
		_, needs, err := cloudResolutionManifest(parse(t, tc.doc), Input{Environment: "production", Graph: cloudTargetGraph()})
		if err != nil || needs.Explicit != tc.explicit || needs.Fallback != tc.fallback {
			t.Errorf("%q: needs %+v %v", tc.doc, needs, err)
		}
	}
}

// httpsAWS gives the AWS connection a certificate, so ECS serves HTTPS.
func httpsAWS(cs []ConnectionOption) []ConnectionOption {
	out := slices.Clone(cs)
	for i := range out {
		if out[i].Cloud == "aws" {
			out[i].HTTPS = true
		}
	}
	return out
}

// dbgwSameCloud has an app on DigitalOcean that reaches its database
// directly, so the resource can opt in and out without a problem.
const dbgwSameCloud = `version: 1
services:
  api: {source: services/api, provider: digitalocean, needs: [db]}
resources:
  db: {kind: postgres, provider: digitalocean}
database_gateway: {provider: cloud-run, region: us-east1}
`

func TestDatabaseGatewayOptInAndOutRedeploysApps(t *testing.T) {
	optedIn := strings.Replace(dbgwSameCloud, "provider: digitalocean}", "provider: digitalocean, gateway: true}", 1)
	direct := dbgwCompute(t, dbgwSameCloud, exampleZone)
	if len(direct.Problems) > 0 {
		t.Fatalf("problems: %+v", direct.Problems)
	}
	if link := action(t, direct, "service:api").Service.Env[0]; link.Gateway != "" {
		t.Fatalf("a direct link is marked as through the gateway: %+v", link)
	}
	// A link that isn't through the gateway is written as it always was,
	// so bindings from before the field compare as unchanged.
	if raw, _ := json.Marshal(action(t, direct, "service:api").Service.Env); strings.Contains(string(raw), "gateway") {
		t.Fatalf("a direct link records the gateway: %s", raw)
	}
	if again := dbgwCompute(t, dbgwSameCloud, exampleZone, deployed(t, direct)...); action(t, again, "service:api").Op != Keep {
		t.Fatal("an unchanged direct link redeployed the app")
	}

	in := dbgwCompute(t, optedIn, exampleZone, deployed(t, direct)...)
	api := action(t, in, "service:api")
	if api.Op != Update || api.Service.Env[0].Gateway != "us-east1.db.example.com" || !slices.Contains(api.DependsOn, "service:db-gateway") {
		t.Fatalf("opting in left api %s (%s) with %+v", api.Op, api.Reason, api.Service.Env)
	}
	if again := dbgwCompute(t, optedIn, exampleZone, deployed(t, in)...); action(t, again, "service:api").Op != Keep {
		t.Fatal("re-planning an opted-in app redeployed it")
	}

	out := dbgwCompute(t, dbgwSameCloud, exampleZone, deployed(t, in)...)
	if api := action(t, out, "service:api"); api.Op != Update || api.Service.Env[0].Gateway != "" {
		t.Fatalf("opting out left api %s with %+v", api.Op, api.Service.Env)
	}
	if gw := action(t, out, "service:db-gateway"); gw.Op != Delete {
		t.Fatalf("db-gateway is %s after the last resource opted out", gw.Op)
	}
}

func TestDatabaseGatewayNewAddressRedeploysApps(t *testing.T) {
	first := dbgwCompute(t, dbgwManifest, exampleZone)
	live := deployed(t, first)

	// A new region replaces the gateway, at a new address.
	moved := dbgwCompute(t, strings.Replace(dbgwManifest, "region: us-east1", "region: us-east4", 1), exampleZone, live...)
	if gw := action(t, moved, "service:db-gateway"); gw.Op != Replace {
		t.Fatalf("db-gateway is %s", gw.Op)
	}
	// Its hosts move with it, so the links change too.
	for _, name := range []string{"api", "web"} {
		if a := action(t, moved, "service:"+name); a.Op != Update || a.Service.Env[0].Gateway != "us-east4.db.example.com" {
			t.Fatalf("%s is %s (%s) with %+v while the gateway moves", name, a.Op, a.Reason, a.Service.Env)
		}
	}

	// One deleted outside Loomplane is created again, at a new address.
	for _, b := range live {
		if b.Name == dbGatewayName {
			b.Drift = DriftMissing
		}
	}
	again := dbgwCompute(t, dbgwManifest, exampleZone, live...)
	if gw := action(t, again, "service:db-gateway"); gw.Op != Create {
		t.Fatalf("db-gateway is %s", gw.Op)
	}
	// Its hosts keep their names, so only the new address redeploys api.
	if a := action(t, again, "service:api"); a.Op != Update || !strings.Contains(a.Reason, "db-gateway") {
		t.Fatalf("api is %s (%s) while the gateway is created again", a.Op, a.Reason)
	}
}

// The links record the suffix the gateway names hosts under, so a new DNS
// zone, which renames every host and leaves the gateway's own address
// alone, still redeploys each app at its new host. A held app can't
// follow, so it's a problem.
func TestDatabaseGatewayZoneChangeRedeploysApps(t *testing.T) {
	otherZone := []cloudinfra.ProjectZone{{ID: "zone_b", Name: "other.org", Verified: true}}
	first := dbgwCompute(t, dbgwManifest, exampleZone)
	for _, name := range []string{"api", "web"} {
		if link := action(t, first, "service:"+name).Service.Env[0]; link.Gateway != "us-east1.db.example.com" {
			t.Fatalf("%s's link %+v doesn't record the gateway's suffix", name, link)
		}
	}
	live := deployed(t, first)

	moved := dbgwCompute(t, dbgwManifest, otherZone, live...)
	if len(moved.Problems) > 0 {
		t.Fatalf("problems: %+v", moved.Problems)
	}
	if gw := action(t, moved, "service:db-gateway"); gw.Op != Update || gw.Service.DatabaseGateway.Suffix != "us-east1.db.other.org" {
		t.Fatalf("db-gateway is %s with %+v", gw.Op, gw.Service.DatabaseGateway)
	}
	for _, name := range []string{"api", "web"} {
		a := action(t, moved, "service:"+name)
		if a.Op != Update || a.Service.Env[0].Gateway != "us-east1.db.other.org" || !slices.Contains(a.DependsOn, "service:db-gateway") {
			t.Fatalf("%s is %s (%s) with %+v after the zone changed", name, a.Op, a.Reason, a.Service.Env)
		}
	}
	if again := dbgwCompute(t, dbgwManifest, otherZone, deployed(t, moved)...); action(t, again, "service:api").Op != Keep {
		t.Fatal("re-planning after the zone change redeployed api")
	}

	in := dbgwInput(t, dbgwManifest, otherZone, live...)
	in.Held = map[string]bool{"other": true}
	r := Compute(in)
	if got := heldUsers(r); !slices.Equal(got, []string{"api", "web"}) || r.Executable {
		t.Fatalf("held users %v, executable %v, problems %+v", got, r.Executable, r.Problems)
	}
	if f := finding(t, r.Problems, "db-gateway-held-user"); !strings.Contains(f.Message, "moves the gateway's hosts from us-east1.db.example.com to us-east1.db.other.org") {
		t.Fatalf("finding = %+v", f)
	}
}

// A held app keeps the gateway address it was deployed with, so a plan
// that moves the gateway is a problem.
func TestDatabaseGatewayMoveUnderAHeldApp(t *testing.T) {
	live := deployed(t, dbgwCompute(t, dbgwManifest, exampleZone))
	in := dbgwInput(t, strings.Replace(dbgwManifest, "region: us-east1", "region: us-east4", 1), exampleZone, live...)
	in.Held = map[string]bool{"other": true}
	r := Compute(in)
	f := finding(t, r.Problems, "held-address")
	if !strings.Contains(f.Message, "uses db-gateway's address") {
		t.Fatalf("held-address doesn't name the gateway: %+v", f)
	}
}

func TestDatabaseGatewayOnECSNeedsHTTPS(t *testing.T) {
	doc := strings.Replace(dbgwManifest, "provider: digitalocean", "provider: rds", 1)
	doc = strings.Replace(doc, "{provider: cloud-run, region: us-east1}", "{provider: ecs-fargate}", 1)
	f := finding(t, dbgwCompute(t, doc, exampleZone).Problems, "db-gateway-needs-https")
	if f.Path != "database_gateway.provider" || !strings.Contains(f.Message, "certificate") || strings.Contains(f.Message, "—") {
		t.Fatalf("finding = %+v", f)
	}

	in := dbgwInput(t, doc, exampleZone)
	in.Connections = httpsAWS(in.Connections)
	first := Compute(in)
	if len(first.Problems) > 0 {
		t.Fatalf("problems: %+v", first.Problems)
	}
	// A gateway deployed before the certificate keeps its plain HTTP port.
	live := deployed(t, first)
	for _, b := range live {
		if b.Name == dbGatewayName {
			b.URL = "http://lb.example.com:8000"
		}
	}
	in.Bindings = live
	f = finding(t, Compute(in).Problems, "db-gateway-needs-https")
	if !strings.Contains(f.Message, "http://lb.example.com:8000") {
		t.Fatalf("finding = %+v", f)
	}
}

// dbgwAppliedGateway is a deployed database gateway on a Cloud Run
// target, as a Cloud switch reads it.
func dbgwAppliedGateway(t *testing.T) *Binding {
	t.Helper()
	cfg := ServiceConfig{Provider: cloudRun, Region: "us-east1", Workload: "http-service", Execution: "container", Source: ".", Ecosystem: "go", Image: dbgateway.Dockerfile, Port: dbgateway.Port, Public: true,
		DatabaseGateway: &DatabaseGatewayConfig{Name: dbGatewayName, Suffix: "us-east1.db.example.com", Resources: []string{"db"}},
		CloudPlacement:  &CloudPlacement{CloudID: "cloud", CloudRevision: 1, AssignmentRevision: 1, TargetID: "target", TargetRevision: 1, TargetName: "run", Provider: cloudRun, ConnectionID: "old", AccountIdentity: "gcp:old"}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &Binding{ID: "bind_gw", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Name: dbGatewayName, Subject: ServiceSubject, Provider: cloudRun, Region: "us-east1", ConnectionID: "old", Config: raw, ExternalID: "ext_gw"}
}

func TestDatabaseGatewayInCloudSync(t *testing.T) {
	assignment := func(connection string) *cloudprofiles.Assignment {
		return &cloudprofiles.Assignment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", CloudID: "cloud", CloudRevision: 1, Revision: 1, Targets: []cloudprofiles.Target{{ID: "target", OrgID: "org", ProjectID: "project", CloudID: "cloud", Name: "run", Provider: cloudRun, ConnectionID: connection, Revision: 2}}}
	}
	gcp := func(id string) ConnectionOption {
		return ConnectionOption{ID: id, Cloud: "gcp", Ready: true, Details: map[string]string{"project_id": id}}
	}
	// Staying put, it keeps the configuration it was applied with.
	r := AppliedCloudPlan([]*Binding{dbgwAppliedGateway(t)}, assignment("old"), "target", []ConnectionOption{gcp("old")}, providers.Registry())
	if !r.Executable || r.Actions[0].Op != Keep || !slices.Equal(r.Actions[0].Service.DatabaseGateway.Resources, []string{"db"}) {
		t.Fatalf("adopting the gateway's placement: %+v %+v", r.Problems, r.Actions)
	}
	// Moving, it would get a new address its apps never hear of.
	r = AppliedCloudPlan([]*Binding{dbgwAppliedGateway(t)}, assignment("new"), "target", []ConnectionOption{gcp("new")}, providers.Registry())
	f := finding(t, r.Problems, "cloud-sync-blocked")
	if r.Executable || f.Name != dbGatewayName || !strings.Contains(f.Message, "database gateway") {
		t.Fatalf("a Cloud switch moved the gateway: %+v", r.Problems)
	}
}

// heldUsers are the held-user problems, by the held service and the
// resource each names.
func heldUsers(r *Result) []string {
	var out []string
	for _, f := range r.Problems {
		if f.Code == "db-gateway-held-user" {
			out = append(out, f.Name)
		}
	}
	return out
}

// A held app keeps the gateway address it was deployed with, so a plan
// that removes the gateway takes its database away.
func TestDatabaseGatewayHeldUserWhenTheGatewayGoes(t *testing.T) {
	live := deployed(t, dbgwCompute(t, dbgwManifest, exampleZone))
	in := dbgwInput(t, strings.Replace(dbgwManifest, ", gateway: true", "", 1), exampleZone, live...)
	in.Held = map[string]bool{"other": true}
	r := Compute(in)
	if gw := action(t, r, "service:db-gateway"); gw.Op != Delete {
		t.Fatalf("db-gateway is %s", gw.Op)
	}
	if got := heldUsers(r); !slices.Equal(got, []string{"api", "web"}) || r.Executable {
		t.Fatalf("held users %v, executable %v, problems %+v", got, r.Executable, r.Problems)
	}
	f := finding(t, r.Problems, "db-gateway-held-user")
	if f.Path != "services.api" || !strings.Contains(f.Message, "reaches db through the database gateway") || !strings.Contains(f.Message, "removes the database gateway") || strings.Contains(f.Message, "—") {
		t.Fatalf("finding = %+v", f)
	}

	// Nothing held, nothing to report: the apps are deployed again.
	in.Held = nil
	if got := heldUsers(Compute(in)); len(got) != 0 {
		t.Fatalf("held users %v with nothing held", got)
	}
}

func TestDatabaseGatewayHeldUserWhenItsResourceLeaves(t *testing.T) {
	both := strings.Replace(dbgwManifest, "resources:\n", "resources:\n  orders: {kind: postgres, provider: digitalocean, gateway: true}\n", 1)
	live := deployed(t, dbgwCompute(t, both, exampleZone))
	in := dbgwInput(t, strings.Replace(both, "provider: digitalocean, gateway: true}\ndatabase_gateway", "provider: digitalocean}\ndatabase_gateway", 1), exampleZone, live...)
	in.Held = map[string]bool{"other": true}
	r := Compute(in)
	if gw := action(t, r, "service:db-gateway"); gw.Op != Update || !slices.Equal(gw.Service.DatabaseGateway.Resources, []string{"orders"}) {
		t.Fatalf("db-gateway is %s with %+v", gw.Op, gw.Service.DatabaseGateway)
	}
	if got := heldUsers(r); !slices.Equal(got, []string{"api", "web"}) {
		t.Fatalf("held users %v, problems %+v", got, r.Problems)
	}
	if f := finding(t, r.Problems, "db-gateway-held-user"); !strings.Contains(f.Message, "takes db off the database gateway") {
		t.Fatalf("finding = %+v", f)
	}
}

// An app the gateway marking turns from a Keep into an Update is deployed,
// not adopted, so it carries no adoption.
func TestDatabaseGatewayMarkingClearsAdoption(t *testing.T) {
	live := deployed(t, dbgwCompute(t, dbgwSameCloud, exampleZone))
	in := dbgwInput(t, strings.Replace(dbgwSameCloud, "provider: digitalocean}", "provider: digitalocean, gateway: true}", 1), exampleZone, live...)
	in.CloudPlacements = map[string]CloudPlacement{"api": {CloudID: "cloud", CloudRevision: 1, AssignmentRevision: 1, TargetID: "target", TargetRevision: 1, TargetName: "apps", Provider: "digitalocean", ConnectionID: "conn_do", AccountIdentity: "digitalocean:team"}}
	api := action(t, Compute(in), "service:api")
	if api.Op != Update || api.AdoptCloudPlacement || api.CloudAdoption != nil {
		t.Fatalf("api is %s, adopt %v %+v", api.Op, api.AdoptCloudPlacement, api.CloudAdoption)
	}
	// Without the marking, the same placement is adopted.
	in.Manifest = parse(t, dbgwSameCloud)
	if api := action(t, Compute(in), "service:api"); api.Op != Keep || !api.AdoptCloudPlacement {
		t.Fatalf("control: api is %s, adopt %v", api.Op, api.AdoptCloudPlacement)
	}
}
