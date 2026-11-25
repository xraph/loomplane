package planning

import (
	"context"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/providers"
)

var (
	doAccount  = ConnectionOption{ID: "conn_00000000000000000000000001", Name: "DigitalOcean", Cloud: "digitalocean", Ready: true}
	doRegistry = ConnectionOption{ID: "conn_00000000000000000000000003", Name: "DOCR", Cloud: "registry", Ready: true,
		Details: map[string]string{"endpoint": "https://registry.digitalocean.com", "repository": "acme", "push": "ready"}}
	doGHCR = ConnectionOption{ID: "conn_00000000000000000000000004", Name: "GHCR", Cloud: "registry", Ready: true,
		Details: map[string]string{"endpoint": "https://ghcr.io", "repository": "acme", "push": "ready"}}
	doRunners = ConnectionOption{ID: "conn_00000000000000000000000002", Name: "Pool", Cloud: "runner", Ready: true,
		Details: doRunnerDetails("amd64")}
)

// doRunnerDetails are a runner pool's whose runners passed the microVM
// check, as a pool must before it builds.
func doRunnerDetails(arch string) map[string]string {
	return map[string]string{"architecture": arch, "isolation": "firecracker", "build_isolation": "ready",
		"runtime_identity": strings.Repeat("a", 64), "host_id": "host-1"}
}

// planAppPlatform plans one service on App Platform: a Go module, which
// Loomplane can build, or the same module made a static site, which it
// can't.
func planAppPlatform(t *testing.T, static bool, settings string, def *BuildDefault, conns ...ConnectionOption) *Result {
	t.Helper()
	fs := fstest.MapFS{
		"go.mod":  {Data: []byte("module example.com/api\n\ngo 1.24\n")},
		"main.go": {Data: []byte("package main\n\nimport \"net/http\"\n\nfunc main() { http.ListenAndServe(\":8080\", nil) }\n")},
	}
	if static {
		settings = "    kind: static-site\n" + settings
	}
	report, err := discovery.Analyze(context.Background(), fs)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.Build(report)
	if len(g.Services) != 1 {
		t.Fatalf("services %+v", g.Services)
	}
	name := g.Services[0].Name
	m := parse(t, "version: 1\nservices:\n  "+name+":\n    source: .\n    provider: digitalocean\n"+settings)
	r := Compute(Input{Environment: "production", Graph: g, Providers: providers.Registry(), Connections: append([]ConnectionOption{doAccount}, conns...), Manifest: m, BuildDefault: def})
	r.Actions = slices.DeleteFunc(r.Actions, func(a Action) bool { return a.Service == nil })
	if len(r.Actions) != 1 {
		t.Fatalf("actions %+v, problems %+v", r.Actions, r.Problems)
	}
	return r
}

func TestAppPlatform_buildsTheImageWithTheDefaultPool(t *testing.T) {
	r := planAppPlatform(t, false, "", &BuildDefault{Builder: doRunners.ID, Source: "the project's runner pool"}, doRunners, doRegistry)
	if !r.Executable {
		t.Fatalf("problems %+v", r.Problems)
	}
	build := r.Actions[0].Service.BuildTarget
	if build == nil || build.Builder != doRunners.ID || build.BuilderCloud != "runner" || build.Registry != doRegistry.ID || build.RegistrySettings["endpoint"] != "https://registry.digitalocean.com" {
		t.Fatalf("build target %+v", build)
	}
	if hasCode(r.Warnings, "app-platform-repository") {
		t.Fatalf("warned about the repository for an image build: %+v", r.Warnings)
	}
}

// A service that deployed through DigitalOcean's GitHub connection keeps
// doing so until there's somewhere to build and push, and the plan says
// what's missing.
func TestAppPlatform_fallsBackToTheRepositoryUntilItCanBuild(t *testing.T) {
	for name, conns := range map[string][]ConnectionOption{
		"no registry":      {doRunners},
		"no builder":       {doRegistry},
		"two registries":   {doRunners, doRegistry, {ID: "conn_00000000000000000000000005", Name: "DOCR 2", Cloud: "registry", Ready: true, Details: doRegistry.Details}},
		"nothing set up":   nil,
		"another registry": {doRunners, doGHCR},
	} {
		t.Run(name, func(t *testing.T) {
			var def *BuildDefault
			if slices.ContainsFunc(conns, func(c ConnectionOption) bool { return c.ID == doRunners.ID }) {
				def = &BuildDefault{Builder: doRunners.ID, Source: "the project's runner pool"}
			}
			r := planAppPlatform(t, false, "", def, conns...)
			if !r.Executable {
				t.Fatalf("problems %+v", r.Problems)
			}
			if r.Actions[0].Service.BuildTarget != nil {
				t.Fatalf("build target %+v", r.Actions[0].Service.BuildTarget)
			}
			if !hasCode(r.Warnings, "app-platform-repository") {
				t.Fatalf("warnings %+v", r.Warnings)
			}
		})
	}
}

func TestAppPlatform_staticSitesBuildFromTheRepository(t *testing.T) {
	r := planAppPlatform(t, true, "", &BuildDefault{Builder: doRunners.ID}, doRunners, doRegistry)
	if !r.Executable || r.Actions[0].Service.BuildTarget != nil || !hasCode(r.Warnings, "app-platform-repository") {
		t.Fatalf("executable %v, build %+v, warnings %+v, problems %+v", r.Executable, r.Actions[0].Service.BuildTarget, r.Warnings, r.Problems)
	}
	r = planAppPlatform(t, true, "    build:\n      registry: conn_00000000000000000000000003\n", nil, doRegistry)
	if r.Executable || !hasCode(r.Problems, "app-platform-build") {
		t.Fatalf("a static site took build settings: %+v", r.Problems)
	}
}

func TestAppPlatform_refusesARegistryItCantPullFrom(t *testing.T) {
	r := planAppPlatform(t, false, "    build:\n      builder: conn_00000000000000000000000002\n      registry: conn_00000000000000000000000004\n", nil, doRunners, doGHCR)
	if r.Executable || !hasCode(r.Problems, "app-platform-registry") {
		t.Fatalf("problems %+v", r.Problems)
	}
}

func TestAppPlatform_refusesAnArmBuilder(t *testing.T) {
	arm := doRunners
	arm.Details = doRunnerDetails("arm64")
	r := planAppPlatform(t, false, "    build:\n      builder: conn_00000000000000000000000002\n      registry: conn_00000000000000000000000003\n", nil, arm, doRegistry)
	if r.Executable || !hasCode(r.Problems, "build-architecture") {
		t.Fatalf("problems %+v", r.Problems)
	}
}

// appPlatformImageAction is api on App Platform, built as an image pushed
// to conn_reg.
func appPlatformImageAction() Action {
	a := productionAPI()
	a.Provider, a.Service.Provider, a.Region, a.Service.Region = "digitalocean", "digitalocean", "ams", "ams"
	a.Service.BuildTarget = &BuildTarget{Builder: "conn_builder", Registry: "conn_reg", BuilderSettings: map[string]string{"architecture": "amd64"}}
	return a
}

func TestAppPlatform_promotionReusesTheImageInTheSameRegistry(t *testing.T) {
	img := stagingAPI()
	img.Provider, img.Registry, img.Architecture = "digitalocean", "conn_reg", "amd64"
	result := &Result{Actions: []Action{appPlatformImageAction()}}
	artifactService().applyArtifacts(promotionPlan(), result, map[string]ReleasedImage{"api": img}, "staging")
	if len(result.Artifacts) != 1 || !result.Artifacts[0].Reused || result.Actions[0].Service.Artifact.Reference != img.Image {
		t.Fatalf("artifacts %+v", result.Artifacts)
	}

	img.Registry = "conn_reg_other"
	result = &Result{Actions: []Action{appPlatformImageAction()}}
	artifactService().applyArtifacts(promotionPlan(), result, map[string]ReleasedImage{"api": img}, "staging")
	if result.Artifacts[0].Reused || result.Artifacts[0].Reason != "production pushes to another registry; production builds aaa1234 again" {
		t.Fatalf("artifacts %+v", result.Artifacts)
	}
}

// Loomplane builds the image from the code it fetched, so App Platform
// never needs to read the repository's host.
func TestAppPlatform_imageBuildsTakeAnyRepositoryHost(t *testing.T) {
	plan := &Plan{Sources: []PlanSource{{Alias: "api", Repository: "gitlab.example.com/team/api"}}}
	a := appPlatformImageAction()
	a.Op, a.Service.Repo = Create, "api"
	result := &Result{Executable: true, Actions: []Action{a}}
	checkGitBuilds(plan, result)
	if !result.Executable || len(result.Problems) != 0 {
		t.Fatalf("problems %+v", result.Problems)
	}
}

func TestAppPlatform_rollbackRunsTheRecordedImage(t *testing.T) {
	img := ReleasedImage{Service: "api", Provider: "digitalocean", Revision: "historical", CommitSHA: artifactCommit, Image: artifactImage, Architecture: "amd64"}
	a := appPlatformImageAction()
	choice, artifact, finding := appPlatformRollback(a, img, "previous")
	if finding != nil || artifact.Revision != "historical" || artifact.Reference != artifactImage || artifact.Digest != artifactDigest || choice.Reference != artifactImage {
		t.Fatalf("choice %+v, artifact %+v, finding %+v", choice, artifact, finding)
	}

	// Across the switch between App Platform's builds and Loomplane's,
	// there's no matching deployment to go back to.
	built := img
	built.Image = ""
	if _, _, finding := appPlatformRollback(a, built, "previous"); finding == nil || !strings.Contains(finding.Message, "from the repository") {
		t.Fatalf("finding %+v", finding)
	}
	a.Service.BuildTarget = nil
	if _, _, finding := appPlatformRollback(a, img, "previous"); finding == nil || !strings.Contains(finding.Message, "ran an image Loomplane built") {
		t.Fatalf("finding %+v", finding)
	}
}
