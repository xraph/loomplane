package planning

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/platformcloud"
	"github.com/xraph/loomplane/internal/providers"
)

// fakePlacer answers platform previews with a fixed error, or with a
// reference to the request's region and size.
type fakePlacer struct {
	err      error
	requests []platformcloud.Request
}

func (f *fakePlacer) Preview(_ context.Context, req platformcloud.Request) (*platformcloud.Ref, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return nil, f.err
	}
	return &platformcloud.Ref{Region: req.Region, VCPUs: req.VCPUs, MemoryMiB: req.MemoryMiB, DiskMiB: req.DiskMiB}, nil
}

// platformAssignment is an environment assigned to the platform-owned
// Loomplane Cloud with one region target, fra.
func platformAssignment() *cloudprofiles.Assignment {
	return &cloudprofiles.Assignment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", CloudID: "cloud_platform", CloudRevision: 1, Revision: 1, GrantRevision: 1, Targets: []cloudprofiles.Target{
		{ID: "target_fra", OrgID: "org", CloudID: "cloud_platform", Name: "fra", DisplayName: "Frankfurt", Runtime: platformcloud.RuntimePlatform, Provider: platformcloud.RuntimePlatform, Region: "fra", Revision: 1},
	}}
}

func platformPlanningService(t *testing.T, doc string, placer *fakePlacer) (*Service, *Plan) {
	t.Helper()
	deps := Deps{Store: catalogStore{}, Analyses: targetAnalyses{}, Manifests: catalogManifest{revision: &manifests.Revision{Manifest: parse(t, doc)}}, Providers: providers.Registry(), Connections: targetOptions{}, CloudProfiles: &targetResolver{assignment: platformAssignment()}}
	if placer != nil {
		deps.Platform = placer
	}
	plan := &Plan{OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production", Sources: []PlanSource{{Alias: "app", AnalysisID: "analysis", CommitSHA: "abc", InScope: true}}}
	return NewService(deps), plan
}

func TestPlanPlatformTargetStoresRegionOnly(t *testing.T) {
	placer := &fakePlacer{}
	s, p := platformPlanningService(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: fra, size: {cpu: 1.5, memory: 1024}}\n", placer)
	result, _, err := s.compute(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Executable {
		t.Fatalf("platform service blocked: %+v", result.Problems)
	}
	if len(placer.requests) != 1 {
		t.Fatalf("previews = %+v", placer.requests)
	}
	req := placer.requests[0]
	if req.OrgID != "org" || req.ProjectID != "project" || req.EnvironmentID != "env" || req.Resource != "web" || req.Region != "fra" || req.Kind != "service" || req.VCPUs != 2 || req.MemoryMiB != 1024 || req.DiskMiB != platformServiceDiskMiB {
		t.Fatalf("preview request = %+v", req)
	}
	a := action(t, result, "service:web")
	if a.Provider != "platform" || a.ConnectionID != "" || a.Region != "fra" {
		t.Fatalf("action = %+v", a)
	}
	if a.Service.BuildTarget == nil || a.Service.BuildTarget.Builder != "platform" || a.Service.Target != "" || len(a.Service.TargetSettings) != 0 {
		t.Fatalf("platform service config = %+v build %+v", a.Service, a.Service.BuildTarget)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Actions []struct {
			Name    string `json:"name"`
			Service struct {
				CloudPlacement map[string]any `json:"cloud_placement"`
			} `json:"service"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	var pin map[string]any
	for _, action := range decoded.Actions {
		if action.Name == "web" {
			pin = action.Service.CloudPlacement
		}
	}
	platform, _ := pin["platform"].(map[string]any)
	if platform["region"] != "fra" || platform["vcpus"] != float64(2) || platform["memory_mib"] != float64(1024) || platform["disk_mib"] != float64(platformServiceDiskMiB) || len(platform) != 4 {
		t.Fatalf("cloud_placement.platform = %v", pin["platform"])
	}
	for _, key := range []string{"server_id", "connection_id", "pool_id", "account_identity", "allocation", "server_revision", "connection_generation"} {
		if v, ok := pin[key]; ok && v != "" && v != float64(0) {
			t.Fatalf("cloud_placement.%s = %v", key, v)
		}
	}
	if strings.Contains(string(raw), "host") {
		t.Fatalf("plan names a host: %s", raw)
	}
}

func TestPlanPlatformTargetRefusedOverLimit(t *testing.T) {
	limit := errors.Join(platformcloud.ErrLimit, errors.New("this needs 2 vCPUs but the workspace has 1 vCPU left of its 4 vCPU limit"))
	s, p := platformPlanningService(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: fra, size: {cpu: 2, memory: 512}}\n", &fakePlacer{err: limit})
	result, _, err := s.compute(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if result.Executable || len(result.Problems) != 1 {
		t.Fatalf("over-limit plan not refused: %+v", result)
	}
	finding := result.Problems[0]
	if finding.Path != "services.web" || finding.Name != "web" || finding.Subject != ServiceSubject || !strings.Contains(finding.Message, "1 vCPU left of its 4 vCPU limit") {
		t.Fatalf("finding = %+v", finding)
	}
}

func TestPlanPlatformTargetWithoutPlacer(t *testing.T) {
	s, p := platformPlanningService(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: fra}\n", nil)
	result, _, err := s.compute(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if result.Executable || len(result.Problems) != 1 || result.Problems[0].Path != "services.web.cloud_target" || result.Problems[0].Message != "Loomplane Cloud isn't available on this instance." {
		t.Fatalf("missing placer: %+v", result.Problems)
	}
}

func TestPlanPlatformTargetRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ doc, path, message string }{
		"builder": {
			doc:  "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: fra, build: {builder: conn_01h455vb4pex5vsknk084sn02q}}\n",
			path: "services.web.build.builder", message: "Loomplane Cloud builds",
		},
		"location policy": {
			doc:  "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: fra, placement: {architecture: amd64}}\n",
			path: "services.web.placement", message: "Location constraints don't apply to Loomplane Cloud",
		},
		"region": {
			doc:  "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: fra, region: ams}\n",
			path: "services.web.cloud_target", message: "runs in fra",
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, p := platformPlanningService(t, tc.doc, &fakePlacer{})
			result, _, err := s.compute(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			if result.Executable {
				t.Fatalf("refusal missing: %+v", result)
			}
			found := false
			for _, f := range result.Problems {
				found = found || f.Path == tc.path && strings.Contains(f.Message, tc.message)
			}
			if !found {
				t.Fatalf("problems = %+v", result.Problems)
			}
		})
	}
}

// A service can't name the platform provider directly: Loomplane Cloud is
// reached only through its targets.
func TestPlanPlatformProviderNeedsTarget(t *testing.T) {
	s, p := platformPlanningService(t, "version: 1\nservices:\n  web: {source: cmd/api, provider: platform}\n", &fakePlacer{})
	result, _, err := s.compute(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if result.Executable || !strings.Contains(findingMessages(result.Problems), "Loomplane Cloud runs services only through its Cloud targets") {
		t.Fatalf("direct platform provider: %+v", result.Problems)
	}
}

func findingMessages(findings []Finding) string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Path+": "+f.Message)
	}
	return strings.Join(out, "\n")
}

// The next plan of an applied Loomplane Cloud service previews it again and
// keeps it where it runs, with no connection anywhere.
func TestPlanPlatformTargetAppliedService(t *testing.T) {
	placer := &fakePlacer{}
	s, p := platformPlanningService(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_target: fra}\n", placer)
	first, _, err := s.compute(t.Context(), p)
	if err != nil || !first.Executable {
		t.Fatalf("first plan: %+v %v", first, err)
	}
	a := action(t, first, "service:web")
	raw, _ := json.Marshal(a.Service)
	s.store = catalogStore{bindings: []*Binding{{ID: "binding", Subject: ServiceSubject, Name: "web", Provider: a.Provider, Region: a.Region, Config: raw, PlanID: "plan"}}}
	next, _, err := s.compute(t.Context(), p)
	if err != nil || !next.Executable {
		t.Fatalf("next plan: %+v %v", next.Problems, err)
	}
	if b := action(t, next, "service:web"); b.Op == Replace || b.ConnectionID != "" || b.Service.CloudPlacement.Platform == nil {
		t.Fatalf("applied platform service: %+v", b)
	}
	if len(placer.requests) != 2 {
		t.Fatalf("previews = %d", len(placer.requests))
	}
}
