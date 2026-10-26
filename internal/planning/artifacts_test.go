package planning

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/gateway"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes"
)

const (
	artifactCommit = "aaa1234567890123456789012345678901234567"
	artifactDigest = "sha256:" + "ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12"
	artifactImage  = "us-central1-docker.pkg.dev/shop/loomplane/api-23456789@" + artifactDigest
)

// fakeHistory is the release history of one environment: what each
// service ran, and the times it was asked.
type fakeHistory struct {
	images map[string]ReleasedImage
	asked  []string
}

func (h *fakeHistory) Releases(context.Context, string, string, time.Time, int) ([]ReleaseRecord, error) {
	return nil, nil
}

func (h *fakeHistory) ReleasedImages(_ context.Context, _, environmentID string, _ time.Time) (map[string]ReleasedImage, error) {
	h.asked = append(h.asked, environmentID)
	out := map[string]ReleasedImage{}
	for k, v := range h.images {
		out[k] = v
	}
	return out, nil
}

// stagingAPI is api as staging released it: on Cloud Run through conn_g
// in us-central1, at artifactCommit, by digest.
func stagingAPI() ReleasedImage {
	return ReleasedImage{
		Service: "api", Provider: "cloud-run", ConnectionID: "conn_g", Region: "us-central1",
		CommitSHA: artifactCommit, Image: artifactImage, Revision: artifactImage,
		DeploymentID: "d_s", PlanID: "plan_s",
	}
}

// productionAPI is api as production's promotion would deploy it.
func productionAPI() Action {
	return Action{
		ID: "service:api", Op: Update, Subject: ServiceSubject, Name: "api", Provider: "cloud-run", Region: "us-central1",
		Service: &ServiceConfig{Provider: "cloud-run", Region: "us-central1", CommitSHA: artifactCommit, Source: "services/api"},
	}
}

func artifactService() *Service {
	return NewService(Deps{Providers: providers.Registry()})
}

func promotionPlan() *Plan {
	return &Plan{Kind: KindPromotion, Environment: "production", From: "staging", FromEnvironmentID: envStaging}
}

func TestArtifacts_promotionReusesTheSourceImage(t *testing.T) {
	s := artifactService()
	result := &Result{Actions: []Action{productionAPI()}, Connections: map[string]string{"gcp": "conn_g"}}
	s.applyArtifacts(promotionPlan(), result, map[string]ReleasedImage{"api": stagingAPI()}, "staging")
	want := &ReusedArtifact{Reference: artifactImage, Digest: artifactDigest, CommitSHA: artifactCommit, FromEnvironment: "staging", FromDeploymentID: "d_s"}
	if got := result.Actions[0].Service.Artifact; !reflect.DeepEqual(got, want) {
		t.Fatalf("artifact %+v, want %+v", got, want)
	}
	choices := []ArtifactChoice{{Service: "api", Reused: true, Reference: artifactImage, From: "staging"}}
	if !reflect.DeepEqual(result.Artifacts, choices) {
		t.Fatalf("artifacts %+v", result.Artifacts)
	}
}

func TestArtifacts_rebuildsWithAReasonPerTarget(t *testing.T) {
	const again = "; production builds aaa1234 again"
	registry := func(id string) *BuildTarget { return &BuildTarget{Builder: "conn_builder", Registry: id} }
	for _, tc := range []struct {
		name     string
		provider string
		action   func(*Action)
		image    func(*ReleasedImage)
		reason   string
	}{
		{"ecs in another account", "ecs-fargate", func(a *Action) { a.ConnectionID = "conn_other" }, nil,
			"staging's image is in another AWS account or region, which production can't pull from" + again},
		{"cloud run in another region", "cloud-run", func(a *Action) { a.Region = "europe-west1" }, nil,
			"staging's image is in another Google Cloud project or region, which production can't pull from" + again},
		{"azure through another connection", "azure-container-apps", func(a *Action) { a.ConnectionID = "conn_other" }, nil,
			"staging's image is in another Azure container registry, which production can't pull from" + again},
		{"a server without a registry", "server", func(a *Action) { a.Service.BuildTarget = registry("conn_reg") },
			func(img *ReleasedImage) { img.Registry, img.Image = "", "loomplane/api:1234" },
			"staging built the image on its server without a registry" + again},
		{"a server pushing to another registry", "server", func(a *Action) { a.Service.BuildTarget = registry("conn_reg_prod") },
			func(img *ReleasedImage) { img.Registry = "conn_reg" },
			"production pushes to another registry" + again},
		{"render", "render", nil, nil, "Render builds each deployment from the commit itself"},
		{"vercel", "vercel", nil, nil, "Vercel builds each deployment from the commit itself"},
		{"digitalocean", "digitalocean", nil, nil,
			"App Platform builds the newest commit of its branch; this promotion works only while that is still aaa1234"},
		{"an image recorded by tag", "cloud-run", nil, func(img *ReleasedImage) { img.Image = "us-central1-docker.pkg.dev/shop/loomplane/api-23456789:aaa1234" },
			"staging recorded api's image by tag: its release predates digest-pinned images, or its connection can't read image digests" + again},
		{"another provider", "ecs-fargate", nil, func(img *ReleasedImage) { img.Provider = "cloud-run" },
			"production runs api on AWS ECS on Fargate, staging on Google Cloud Run"},
		{"another commit", "cloud-run", nil, func(img *ReleasedImage) { img.CommitSHA = "bbb1234567890123456789012345678901234567" },
			"staging ran api at bbb1234" + again},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := productionAPI()
			a.Provider, a.Service.Provider = tc.provider, tc.provider
			img := stagingAPI()
			img.Provider = tc.provider
			if tc.provider == "server" {
				img.Registry = "conn_reg"
			}
			if tc.action != nil {
				tc.action(&a)
			}
			if tc.image != nil {
				tc.image(&img)
			}
			cloud := map[string]string{"ecs-fargate": "aws", "cloud-run": "gcp", "azure-container-apps": "azure"}[tc.provider]
			result := &Result{Actions: []Action{a}, Connections: map[string]string{cloud: "conn_g"}}
			artifactService().applyArtifacts(promotionPlan(), result, map[string]ReleasedImage{"api": img}, "staging")
			want := []ArtifactChoice{{Service: "api", Reason: tc.reason}}
			if !reflect.DeepEqual(result.Artifacts, want) || result.Actions[0].Service.Artifact != nil {
				t.Fatalf("artifacts %+v, artifact %+v", result.Artifacts, result.Actions[0].Service.Artifact)
			}
		})
	}
}

// Every target that pulls from its connection's registry reuses an image
// it can pull, the server through the registry it pushes to.
func TestArtifacts_reusesOnEveryTargetThatPullsTheSameRegistry(t *testing.T) {
	for _, provider := range []string{"ecs-fargate", "cloud-run", "azure-container-apps", "server", "kubernetes"} {
		a := productionAPI()
		a.Provider, a.Service.Provider, a.ConnectionID = provider, provider, "conn_g"
		img := stagingAPI()
		img.Provider = provider
		if provider == "server" || provider == "kubernetes" {
			a.Service.BuildTarget, img.Registry = &BuildTarget{Builder: "conn_builder", Registry: "conn_reg"}, "conn_reg"
		}
		result := &Result{Actions: []Action{a}}
		artifactService().applyArtifacts(promotionPlan(), result, map[string]ReleasedImage{"api": img}, "staging")
		if len(result.Artifacts) != 1 || !result.Artifacts[0].Reused || result.Actions[0].Service.Artifact == nil {
			t.Fatalf("%s: %+v", provider, result.Artifacts)
		}
	}
}

func TestArtifacts_skipsWhatHasNothingToReuse(t *testing.T) {
	keep := productionAPI()
	keep.Op = Keep
	remove := productionAPI()
	remove.Op, remove.Service = Delete, nil
	gw := productionAPI()
	gw.Name, gw.Service.Gateway = "edge", []gateway.Route{{Path: "/api", To: "api"}}
	catalog := productionAPI()
	catalog.Name, catalog.Service.Catalog = "queue", &recipes.Recipe{}
	component := productionAPI()
	component.Name, component.Service.Component = "auth", &ComponentConfig{}
	pinned := productionAPI()
	pinned.Name, pinned.Service.BuildTarget = "proxy", &BuildTarget{Image: "nginx@" + artifactDigest}
	resource := Action{ID: "resource:db", Op: Create, Subject: ResourceSubject, Name: "db", Provider: "cloud-sql"}
	released := map[string]ReleasedImage{}
	for _, name := range []string{"api", "edge", "queue", "auth", "proxy", "db"} {
		img := stagingAPI()
		img.Service = name
		released[name] = img
	}
	for _, a := range []Action{keep, remove, gw, catalog, component, pinned, resource} {
		result := &Result{Actions: []Action{a}, Connections: map[string]string{"gcp": "conn_g"}}
		artifactService().applyArtifacts(promotionPlan(), result, released, "staging")
		if len(result.Artifacts) != 0 || (result.Actions[0].Service != nil && result.Actions[0].Service.Artifact != nil) {
			t.Fatalf("%s %s: artifacts %+v", a.Op, a.Name, result.Artifacts)
		}
	}
}

func TestArtifacts_listsChoicesByService(t *testing.T) {
	web := productionAPI()
	web.Name, web.Provider, web.Service.Provider = "web", "vercel", "vercel"
	result := &Result{Actions: []Action{web, productionAPI()}, Connections: map[string]string{"gcp": "conn_g"}}
	artifactService().applyArtifacts(promotionPlan(), result, map[string]ReleasedImage{"api": stagingAPI()}, "staging")
	if len(result.Artifacts) != 2 || result.Artifacts[0].Service != "api" || result.Artifacts[1].Service != "web" {
		t.Fatalf("artifacts %+v", result.Artifacts)
	}
}

// artifactWorld plans api on Cloud Run in us-central1 through conn_g,
// from commit artifactCommit, with history as the release history.
func artifactWorld(t *testing.T, history ReleaseHistory) (*Service, *Plan) {
	t.Helper()
	doc := "version: 1\nservices:\n  api: {source: cmd/api, provider: cloud-run, region: us-central1}\n"
	revision := &manifests.Revision{ID: "man_1", Revision: 1, Manifest: parse(t, doc), Digest: "digest"}
	options := targetOptions{connections: []*connections.Connection{{ID: "conn_g", Cloud: connections.GCP, Status: connections.StatusReady, Details: map[string]string{"project_id": "shop"}}}}
	s := NewService(Deps{
		Store: artifactStore{}, Analyses: targetAnalyses{}, Manifests: artifactManifests{revision: revision},
		Providers: providers.Registry(), Connections: options, History: history,
	})
	plan := promotionPlan()
	plan.OrgID, plan.ProjectID, plan.EnvironmentID, plan.ManifestID = "org", "project", envProduction, "man_1"
	plan.Sources = []PlanSource{{Alias: "app", AnalysisID: "analysis", CommitSHA: artifactCommit, InScope: true}}
	return s, plan
}

type artifactStore struct{ catalogStore }

func (artifactStore) DeployedState(context.Context, string, string) (*DeployedManifest, []*DeployedSource, error) {
	return &DeployedManifest{ManifestID: "man_1", ManifestRevision: 1}, nil, nil
}

type artifactManifests struct {
	Manifests
	revision *manifests.Revision
}

func (m artifactManifests) Get(context.Context, string, string) (*manifests.Revision, error) {
	return m.revision, nil
}

func (m artifactManifests) Latest(context.Context, string, string) (*manifests.Revision, error) {
	return m.revision, nil
}

func TestArtifacts_computedForAPromotion(t *testing.T) {
	history := &fakeHistory{images: map[string]ReleasedImage{"api": stagingAPI()}}
	s, plan := artifactWorld(t, history)
	result, _, err := s.compute(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	a := action(t, result, "service:api")
	if a.Service.Artifact == nil || a.Service.Artifact.Reference != artifactImage || len(result.Artifacts) != 1 || !result.Artifacts[0].Reused {
		t.Fatalf("api %+v, artifacts %+v, problems %+v", a, result.Artifacts, result.Problems)
	}
	if !reflect.DeepEqual(history.asked, []string{envStaging}) {
		t.Fatalf("history asked for %v", history.asked)
	}
}

func TestArtifacts_reviewedInTheRevision(t *testing.T) {
	with, plan := artifactWorld(t, &fakeHistory{images: map[string]ReleasedImage{"api": stagingAPI()}})
	_, reused, err := with.compute(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	without, plan := artifactWorld(t, &fakeHistory{})
	result, rebuilt, err := without.compute(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if reused == rebuilt {
		t.Fatal("the revision doesn't cover the image the plan reuses")
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Reused || !strings.Contains(result.Artifacts[0].Reason, "staging runs no release of api") {
		t.Fatalf("artifacts %+v", result.Artifacts)
	}
}

func TestArtifacts_onlyForPromotionsAndRollbacks(t *testing.T) {
	history := &fakeHistory{images: map[string]ReleasedImage{"api": stagingAPI()}}
	s, plan := artifactWorld(t, history)
	plan.Kind, plan.From, plan.FromEnvironmentID = KindRelease, "", ""
	result, _, err := s.compute(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.asked) != 0 || len(result.Artifacts) != 0 || action(t, result, "service:api").Service.Artifact != nil {
		t.Fatalf("a release consulted history %v: %+v", history.asked, result.Artifacts)
	}
}
