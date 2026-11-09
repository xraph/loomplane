package planning

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/platformcloud"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestSelectsPlacement(t *testing.T) {
	onTarget := &CloudPlacement{TargetID: "target"}
	onDefault := &CloudPlacement{Default: true}
	for _, tc := range []struct {
		selector string
		pin      *CloudPlacement
		want     bool
	}{
		{"", onTarget, true},
		{"", onDefault, true},
		{"target", onTarget, true},
		{"target", onDefault, false},
		{"other", onTarget, false},
		{defaultPlacementSelector, onDefault, true},
		{defaultPlacementSelector, onTarget, false},
	} {
		if got := selectsPlacement(tc.selector, tc.pin); got != tc.want {
			t.Errorf("selector %q on %+v = %v", tc.selector, tc.pin, got)
		}
	}
}

func TestRepinCloudPlacement(t *testing.T) {
	a := appliedAssignment("old")
	a.CloudRevision, a.Revision, a.GrantRevision = 7, 3, 2
	a.DefaultPlacement = &cloudprofiles.DefaultPlacement{Provider: "vercel", ConnectionID: "default"}
	pin, ok := repinCloudPlacement(CloudPlacement{CloudID: "cloud", CloudRevision: 1, TargetID: "target", TargetRevision: 1, Provider: "vercel", ConnectionID: "old"}, a)
	if !ok || pin.CloudRevision != 7 || pin.AssignmentRevision != 3 || pin.GrantRevision != 2 || pin.TargetRevision != 2 || pin.TargetName != "frontend" {
		t.Fatalf("target pin: %+v %v", pin, ok)
	}
	if pin, ok = repinCloudPlacement(CloudPlacement{Default: true, CloudID: "cloud", Provider: "vercel", ConnectionID: "default"}, a); !ok || pin.CloudRevision != 7 {
		t.Fatalf("default pin: %+v %v", pin, ok)
	}
	for name, stale := range map[string]CloudPlacement{
		"moved target":   {CloudID: "cloud", TargetID: "target", Provider: "vercel", ConnectionID: "elsewhere"},
		"missing target": {CloudID: "cloud", TargetID: "gone", Provider: "vercel", ConnectionID: "old"},
		"moved default":  {Default: true, CloudID: "cloud", Provider: "vercel", ConnectionID: "elsewhere"},
		"another Cloud":  {CloudID: "other", TargetID: "target", Provider: "vercel", ConnectionID: "old"},
		"other provider": {CloudID: "cloud", TargetID: "target", Provider: "netlify", ConnectionID: "old"},
	} {
		if _, ok = repinCloudPlacement(stale, a); ok {
			t.Errorf("%s re-pinned", name)
		}
	}
}

func TestAppliedCloudPlanDefaultSwitchRepinsDependents(t *testing.T) {
	registry := providers.Registry()
	api := appliedTestBinding(t, "api", "commit")
	var cfg ServiceConfig
	_ = json.Unmarshal(api.Config, &cfg)
	cfg.CloudPlacement = &CloudPlacement{Default: true, CloudID: "cloud", CloudRevision: 1, AssignmentRevision: 1, Provider: "vercel", ConnectionID: "old", AccountIdentity: "vercel:old"}
	api.Config, _ = json.Marshal(cfg)
	web := appliedTestBinding(t, "web", "commit")
	var webCfg ServiceConfig
	_ = json.Unmarshal(web.Config, &webCfg)
	webCfg.Env = []EnvLink{{Key: "API_URL", From: "api", Subject: ServiceSubject}}
	web.Config, _ = json.Marshal(webCfg)

	after := appliedAssignment("old")
	after.CloudRevision = 2
	after.DefaultPlacement = &cloudprofiles.DefaultPlacement{Provider: "vercel", ConnectionID: "new"}
	options := []ConnectionOption{
		{ID: "new", Cloud: "vercel", Ready: true, Account: "new", Details: map[string]string{"team_id": "new"}},
		{ID: "old", Cloud: "vercel", Ready: true, Account: "old", Details: map[string]string{"team_id": "old"}},
	}
	result := AppliedCloudPlan([]*Binding{web, api}, after, defaultPlacementSelector, options, registry)
	if !result.Executable {
		t.Fatalf("blocked: %+v", result.Problems)
	}
	moved, dependent := action(t, result, "service:api"), action(t, result, "service:web")
	if moved.Op != Replace || moved.ConnectionID != "new" || !moved.Service.CloudPlacement.Default || moved.Service.CloudPlacement.CloudRevision != 2 {
		t.Fatalf("default-placed api: %+v", moved)
	}
	if dependent.Op != Update || dependent.ConnectionID != "old" || dependent.Service.CloudPlacement.CloudRevision != 2 || dependent.Service.CloudPlacement.TargetRevision != 2 {
		t.Fatalf("dependent web should keep its target on the reviewed Cloud revision: %+v", dependent.Service.CloudPlacement)
	}
}

// platformBinding is web running on the Loomplane Cloud target fra.
func platformBinding(t *testing.T) *Binding {
	t.Helper()
	cfg := ServiceConfig{Provider: "platform", Region: "fra", Repo: "app", CommitSHA: "commit", Service: "web", Ecosystem: "go", Image: "Dockerfile", Workload: provider.WebApp, Execution: provider.Container, Public: true,
		BuildTarget:    &BuildTarget{BuilderCloud: platformBuilder, Builder: platformBuilder},
		CloudPlacement: &CloudPlacement{CloudID: "cloud_platform", CloudRevision: 1, AssignmentRevision: 1, GrantRevision: 1, TargetID: "target_fra", TargetRevision: 1, TargetName: "fra", Provider: "platform", Region: "fra", RegionSource: "default", Platform: &platformcloud.Ref{Region: "fra", VCPUs: 1, MemoryMiB: 512, DiskMiB: platformServiceDiskMiB}},
		AppliedInputs:  &AppliedInputs{Variables: "org/env/applied-inputs/dep/web", Source: &PlanSource{SourceID: "src", Alias: "app", Repository: "owner/app", CommitSHA: "commit"}, ArtifactKey: "sources/org/project/app/commit.tar.gz"}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &Binding{ID: "bind_web", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Name: "web", Subject: ServiceSubject, Provider: "platform", Region: "fra", CommitSHA: "commit", Config: raw, ExternalID: "ext_web", PlanID: "plan_web"}
}

// Leaving Loomplane Cloud is never blocked. An environment on the projection
// Cloud moves to a connected Cloud whose default is a managed provider: web,
// whose target that Cloud doesn't have, goes to the default. Moving back
// places it on the Loomplane Cloud target again, with no connection.
func TestDefaultSwitchOffPlatformCloud(t *testing.T) {
	registry := providers.Registry()
	options := []ConnectionOption{{ID: "new", Cloud: "vercel", Ready: true, Account: "new", Details: map[string]string{"team_id": "new"}}}
	mine := &cloudprofiles.Assignment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", CloudID: "cloud", CloudRevision: 1, Revision: 2, DefaultPlacement: &cloudprofiles.DefaultPlacement{Provider: "vercel", ConnectionID: "new"}}

	off := AppliedCloudPlan([]*Binding{platformBinding(t)}, mine, "", options, registry)
	if !off.Executable {
		t.Fatalf("moving off Loomplane Cloud blocked: %+v", off.Problems)
	}
	moved := action(t, off, "service:web")
	if moved.Op != Replace || moved.Provider != "vercel" || moved.ConnectionID != "new" || moved.Service.CloudPlacement == nil || !moved.Service.CloudPlacement.Default || moved.Service.CloudPlacement.Platform != nil || moved.Service.BuildTarget != nil && moved.Service.BuildTarget.Builder == platformBuilder {
		t.Fatalf("off Loomplane Cloud: %+v %+v", moved, moved.Service.CloudPlacement)
	}

	// Back: web now runs on the connected Cloud's fra target.
	var cfg ServiceConfig
	_ = json.Unmarshal(platformBinding(t).Config, &cfg)
	cfg.Provider, cfg.Execution, cfg.BuildTarget = "vercel", provider.Serverless, nil
	cfg.CloudPlacement = &CloudPlacement{CloudID: "cloud", CloudRevision: 1, AssignmentRevision: 2, TargetID: "target_mine", TargetRevision: 1, TargetName: "fra", Provider: "vercel", ConnectionID: "new", AccountIdentity: "vercel:new"}
	back := platformBinding(t)
	back.Provider, back.ConnectionID, back.Region = "vercel", "new", ""
	back.Config, _ = json.Marshal(cfg)
	onto := platformAssignment()
	pin, err := platformCloudPin(onto, onto.Targets[0], &platformcloud.Ref{Region: "fra", VCPUs: 1, MemoryMiB: 512, DiskMiB: platformServiceDiskMiB}, "")
	if err != nil {
		t.Fatal(err)
	}
	result := appliedCloudPlan([]*Binding{back}, onto, "", options, registry, map[string]CloudPlacement{"web": pin})
	if !result.Executable {
		t.Fatalf("moving onto Loomplane Cloud blocked: %+v", result.Problems)
	}
	returned := action(t, result, "service:web")
	if returned.Op != Replace || returned.Provider != "platform" || returned.ConnectionID != "" || returned.Region != "fra" || returned.Service.CloudPlacement == nil || returned.Service.CloudPlacement.Platform == nil || returned.Service.CloudPlacement.ConnectionID != "" || returned.Service.BuildTarget == nil || returned.Service.BuildTarget.Builder != platformBuilder {
		t.Fatalf("onto Loomplane Cloud: %+v %+v", returned, returned.Service.CloudPlacement)
	}

	reviewed := &Plan{}
	reviewed.setResult(result)
	if refs := snapshotConnections([]*Binding{back}, reviewed); slices.Contains(refs, platformBuilder) || !slices.Contains(refs, "new") {
		t.Fatalf("Cloud sync locks %v", refs)
	}

	// Without a reviewed preview the move onto Loomplane Cloud waits.
	if blocked := AppliedCloudPlan([]*Binding{back}, onto, "", options, registry); blocked.Executable {
		t.Fatal("moved onto Loomplane Cloud without a capacity preview")
	}
}

// A Cloud default is one managed provider and connection. Loomplane Cloud
// isn't one, and says so in its own branch.
func TestDefaultSwitchRefusesPlatformRuntime(t *testing.T) {
	s := NewService(Deps{Providers: providers.Registry()})
	for _, in := range []cloudprofiles.ChangeInput{
		{CloudID: "cloud", Default: true, Runtime: "platform", Provider: "platform"},
		{CloudID: "cloud", Default: true, Provider: "platform", ConnectionID: "conn"},
	} {
		err := s.previewDefaultSwitch(t.Context(), nil, security.Principal{OrgID: "org"}, in, &cloudprofiles.Preview{}, &cloudprofiles.Snapshot{}, nil)
		if !errors.Is(err, cloudprofiles.ErrInvalid) || !strings.Contains(err.Error(), "Loomplane Cloud") {
			t.Fatalf("%+v: %v", in, err)
		}
	}
}
