//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

type locationMoveGraphs struct{ planning.Analyses }

func (locationMoveGraphs) Graph(context.Context, string, string) (*graph.Graph, error) {
	return &graph.Graph{Services: []graph.Service{{ID: "api", Name: "api", Kind: graph.Worker, Deployable: true, Path: "cmd/api", Entrypoint: "cmd/api", Image: "Dockerfile", Context: "."}}}, nil
}

type locationMoveManifest struct{ revision *manifests.Revision }

func (m locationMoveManifest) Get(context.Context, string, string) (*manifests.Revision, error) {
	return m.revision, nil
}
func (m locationMoveManifest) Latest(context.Context, string, string) (*manifests.Revision, error) {
	return m.revision, nil
}

func locationServingPool(t *testing.T, f *poolFixture, name string, cpu int64) *planning.Binding {
	t.Helper()
	for _, arch := range []string{"amd64", "arm64"} {
		if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,details,created_by,created_at,updated_at) VALUES($1,'org','server',$1,$1,'ready',$2,'usr',now(),now()) ON CONFLICT DO NOTHING`, map[string]string{"amd64": "conn_01m4vewksnea2bwdxwdy5aekhn", "arm64": "conn_01m4vewksnea2bwdxwdy5aekhp"}[arch], map[string]string{"docker_id": "builder-" + arch, "architecture": arch, "build_workers": "dedicated"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,details,created_by,created_at,updated_at) VALUES('conn_01m4vem4sde8ab4yfr1abnj3wf','org','registry','Registry','registry','ready','{"endpoint":"registry.example.com","repository":"app"}','usr',now(),now()) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	req := f.request("env", name)
	req.CPUMillis = cpu
	p, err := f.store.PreviewPlacement(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	pin := f.plan(t, "serving-"+name, req, p)
	f.accept(t, "serving-"+name)
	cfg := planning.ServiceConfig{Provider: "server", Target: p.ConnectionID, TargetSettings: map[string]string{"docker_id": p.ConnectionID, "architecture": "amd64", "build_workers": "dedicated"}, BuildTarget: &planning.BuildTarget{Builder: "conn_01m4vewksnea2bwdxwdy5aekhn", Registry: "conn_01m4vem4sde8ab4yfr1abnj3wf"}, Workload: provider.Worker, Execution: provider.Container, Source: "cmd/api", Image: "Dockerfile", Context: ".", Repo: "app", CommitSHA: "old", Size: &manifest.Size{CPU: float64(cpu) / 1000, Memory: 512}, CloudPlacement: pin, AppliedInputs: &planning.AppliedInputs{Variables: "vars", ArtifactKey: "artifact", Source: &planning.PlanSource{Alias: "app", CommitSHA: "old"}}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b := &planning.Binding{ID: "binding-" + name, OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: name, Provider: "server", ConnectionID: p.ConnectionID, Engine: provider.Loomplane, ExternalID: name, PlanID: "serving-" + name, Revision: "source-review", CommitSHA: "old", Config: raw}
	if err = f.plans.PutBinding(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	return b
}
func locationNormalPlan(t *testing.T, f *poolFixture, id, doc string) *planning.Plan {
	t.Helper()
	m, err := manifest.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	for name, service := range m.Services {
		if service.Build == nil {
			service.Build = &manifest.Build{Builder: "conn_01m4vewksnea2bwdxwdy5aekhn", Registry: "conn_01m4vem4sde8ab4yfr1abnj3wf"}
			m.Services[name] = service
		}
	}
	for _, query := range []string{`INSERT INTO loomplane_project_sources(id,org_id,project_id,alias,repository,ref,created_by,created_at,updated_at) VALUES('src','org','proj','app','repo','main','usr',now(),now()) ON CONFLICT DO NOTHING`, `INSERT INTO loomplane_analyses(id,org_id,project_id,source_id,alias,status,repository,ref,commit_sha,graph,created_by,created_at) VALUES('analysis','org','proj','src','app','succeeded','repo','main','new','{}','usr',now()) ON CONFLICT DO NOTHING`} {
		if _, err = f.sql.Exec(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,manifest_id,manifest_revision,created_by,created_at) VALUES($1,'org','proj','env','production','planning','pool-manifest-proj',1,'usr',now())`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_plan_sources(plan_id,source_id,alias,repository,root,analysis_id,commit_sha,in_scope) VALUES($1,'src','app','repo','','analysis','new',true)`, id); err != nil {
		t.Fatal(err)
	}
	svc := planning.NewService(planning.Deps{Store: f.plans, Projects: projects.NewPgStore(f.db), CloudProfiles: f.clouds, Connections: &syncAccounts{store: connections.NewPgStore(f.db)}, Providers: providers.Registry(), Authz: &syncAuth{}, Analyses: locationMoveGraphs{}, Manifests: locationMoveManifest{&manifests.Revision{ID: "pool-manifest-proj", Revision: 1, Manifest: m}}})
	if _, err = svc.Plan(t.Context(), "org", id); err != nil {
		t.Fatal(err)
	}
	p, err := f.plans.GetPlan(t.Context(), "org", id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func locationAcceptMove(t *testing.T, f *poolFixture, name, arch string) {
	t.Helper()
	svc := cloudinfra.NewService(cloudinfra.Deps{Store: f.store, Clouds: f.clouds, Authz: &syncAuth{}})
	in := cloudinfra.MoveInput{ProjectID: "proj", EnvironmentID: "env", Service: name, PoolID: "pool", DestinationServerID: "server-2"}
	in.Architecture = arch
	p := security.Principal{OrgID: "org", UserID: "usr"}
	review, err := svc.PreviewMove(t.Context(), p, "cloud", in)
	if err != nil {
		t.Fatal(err)
	}
	in.Review = review.Review
	if _, err = svc.AcceptMove(t.Context(), p, "cloud", in); err != nil {
		t.Fatal(err)
	}
}
func TestLocationNormalMixedMoveApproval(t *testing.T) {
	f := newPoolFixture(t)
	locationServingPool(t, f, "api", 1000)
	locationServingPool(t, f, "sibling", 250)
	locationAcceptMove(t, f, "api", "")
	p := locationNormalPlan(t, f, "mixed", "version: 1\nservices:\n  api: {source: cmd/api, kind: worker, cloud_target: shared, size: {cpu: 1.5, memory: 768}}\n  sibling: {source: cmd/api, kind: worker, cloud_target: shared, size: {cpu: 0.25, memory: 512}, placement: {}}\n")
	if !p.Executable {
		t.Fatalf("mixed blocked: %+v", p.Problems)
	}
	// Approval must compare the same applied demand the runtime receives.
	f.accept(t, p.ID)
	for _, a := range p.Actions {
		if a.Name == "api" {
			if a.Op != planning.Replace || a.Service.Size.CPU != 1 || a.Service.Size.Memory != 512 || a.Service.CommitSHA != "old" || a.Service.AppliedInputs == nil || a.Service.CloudPlacement.ServerID != "server-2" {
				t.Fatalf("unconstrained migration lost applied inputs: %+v", a)
			}
		}
	}
}
func TestLocationNormalArchitectureMoveApproval(t *testing.T) {
	for _, stale := range []string{"", "source", "review"} {
		t.Run(stale, func(t *testing.T) {
			f := newPoolFixture(t)
			b := locationServingPool(t, f, "api", 1000)
			server, err := f.store.GetServer(t.Context(), "org", "cloud", "server-2")
			if err != nil {
				t.Fatal(err)
			}
			server.Health.Architecture = "arm64"
			if err = f.store.SaveServer(t.Context(), server); err != nil {
				t.Fatal(err)
			}
			if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET details=jsonb_set(details,'{architecture}','"arm64"') WHERE id='runtime-2'`); err != nil {
				t.Fatal(err)
			}
			doc := "version: 1\nservices:\n  api: {source: cmd/api, kind: worker, cloud_target: shared, build: {builder: conn_01m4vewksnea2bwdxwdy5aekhp, registry: conn_01m4vem4sde8ab4yfr1abnj3wf}, placement: {architecture: arm64}}\n"
			blocked := locationNormalPlan(t, f, "before-review", doc)
			if blocked.Executable {
				t.Fatal("unreviewed host move advertised executable")
			}
			raw, _ := json.Marshal(blocked.Problems)
			if !strings.Contains(string(raw), "location-movement-required") {
				t.Fatalf("missing actionable move review: %s", raw)
			}
			locationAcceptMove(t, f, "api", "arm64")
			legacy := locationNormalPlan(t, f, "legacy-after-review", "version: 1\nservices:\n  api: {source: cmd/api, kind: worker, cloud_target: shared}\n")
			if legacy.Executable {
				t.Fatal("legacy applied amd64 intent consumed reviewed arm64 destination")
			}
			applied := f.request("env", "api")
			applied.StickyServerID = "server-1"
			if _, previewErr := f.store.PreviewPlacement(t.Context(), applied); previewErr == nil {
				t.Fatal("applied architecture preview accepted incompatible move")
			}

			p := locationNormalPlan(t, f, "after-review", doc)
			if !p.Executable {
				t.Fatalf("reviewed architecture move blocked: %+v", p.Problems)
			}
			if len(p.Actions) != 1 || p.Actions[0].Service.CommitSHA != "new" || p.Actions[0].Service.Placement.Architecture != "arm64" || p.Actions[0].Service.CloudPlacement.ServerID != "server-2" {
				t.Fatalf("requested settings lost: %+v", p.Actions)
			}
			if stale == "source" {
				if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_bindings SET config=jsonb_set(config,'{commit_sha}','"changed"') WHERE id=$1`, b.ID); err != nil {
					t.Fatal(err)
				}
			}
			if stale == "review" {
				if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_placement_moves SET expires_at=now()-interval '1 second'`); err != nil {
					t.Fatal(err)
				}
			}
			ok, err := f.plans.Decide(t.Context(), "org", p.ID, "approved", "usr", "", time.Now())
			if stale == "" && (err != nil || !ok) {
				t.Fatalf("review-then-approve: %v %v", ok, err)
			}
			if stale != "" && err == nil && ok {
				t.Fatal("stale move authorized")
			}
			if stale == "" {
				var reserved int
				if err = f.sql.QueryRow(t.Context(), `SELECT count(DISTINCT server_id) FROM loomplane_capacity_reservations WHERE org_id='org' AND environment_id='env' AND service='api' AND server_id IN ('server-1','server-2')`).Scan(&reserved); err != nil || reserved != 2 {
					t.Fatalf("replacement lost source capacity: %d %v", reserved, err)
				}
			}
			var serving string
			if err = f.sql.QueryRow(t.Context(), `SELECT connection_id FROM loomplane_bindings WHERE id=$1`, b.ID).Scan(&serving); err != nil || serving != "runtime-1" {
				t.Fatalf("old serving binding lost: %s %v", serving, err)
			}
		})
	}
}

func TestLocationMoveReviewRevalidatesSourceAndArchitecture(t *testing.T) {
	for _, change := range []string{"source", "destination", "requested-architecture"} {
		t.Run(change, func(t *testing.T) {
			f := newPoolFixture(t)
			b := locationServingPool(t, f, "api", 1000)
			server, err := f.store.GetServer(t.Context(), "org", "cloud", "server-2")
			if err != nil {
				t.Fatal(err)
			}
			server.Health.Architecture = "arm64"
			if err = f.store.SaveServer(t.Context(), server); err != nil {
				t.Fatal(err)
			}
			svc := cloudinfra.NewService(cloudinfra.Deps{Store: f.store, Clouds: f.clouds, Authz: &syncAuth{}})
			actor := security.Principal{OrgID: "org", UserID: "usr"}
			in := cloudinfra.MoveInput{ProjectID: "proj", EnvironmentID: "env", Service: "api", PoolID: "pool", DestinationServerID: "server-2", Architecture: "arm64"}
			invalid := in
			invalid.Architecture = "unknown"
			if _, err = svc.PreviewMove(t.Context(), actor, "cloud", invalid); err == nil {
				t.Fatal("invalid architecture accepted")
			}
			reviewed, err := svc.PreviewMove(t.Context(), actor, "cloud", in)
			if err != nil {
				t.Fatal(err)
			}
			in.Review = reviewed.Review
			switch change {
			case "source":
				_, err = f.sql.Exec(t.Context(), `UPDATE loomplane_bindings SET config=jsonb_set(config,'{commit_sha}','"changed"') WHERE id=$1`, b.ID)
			case "destination":
				server.Health.Architecture = "amd64"
				err = f.store.SaveServer(t.Context(), server)
			case "requested-architecture":
				in.Architecture = "amd64"
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.AcceptMove(t.Context(), actor, "cloud", in); err == nil {
				t.Fatal("stale move review accepted")
			}
			var count int
			if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_placement_moves`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("refused review persisted authority: %d %v", count, err)
			}
		})
	}
}
