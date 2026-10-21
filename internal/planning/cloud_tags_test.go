package planning

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/providers"
)

// taggedFixture has two Vercel targets: frontend (web, eu) and preview (web, us).
func taggedFixture() (*cloudprofiles.Assignment, []ConnectionOption) {
	a, c := cloudTargetFixture()
	a.Targets[0].Tags = []string{"eu", "web"}
	a.Targets[1].Tags = []string{"us", "web"}
	return a, c
}

func placementError(t *testing.T, err error, code string) *CloudPlacementError {
	t.Helper()
	var placement *CloudPlacementError
	if !errors.As(err, &placement) || placement.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
	return placement
}

func TestCloudTagsResolveToTheOneMatchingTarget(t *testing.T) {
	a, c := taggedFixture()
	for _, tc := range []struct{ doc, want string }{
		{"cloud_tags: [eu]", "target-a"},
		{"cloud_tags: [us]", "target-b"},
		{"cloud_tags: [web, us]", "target-b"},
	} {
		m := parse(t, "version: 1\nservices:\n  web: {source: cmd/api, "+tc.doc+"}\n")
		resolved, pins, err := ResolveCloudTargets(m, "production", a, c)
		if err != nil {
			t.Fatalf("%s: %v", tc.doc, err)
		}
		pin := pins["web"]
		if pin.TargetID != tc.want || pin.Default || pin.TargetRevision < 1 {
			t.Fatalf("%s pinned %+v", tc.doc, pin)
		}
		if s := resolved.Services["web"]; len(s.CloudTags) != 0 || s.Provider != "vercel" {
			t.Fatalf("%s left unresolved settings: %+v", tc.doc, s.Settings)
		}
	}
}

func TestCloudTagsFailInsteadOfGuessing(t *testing.T) {
	a, c := taggedFixture()
	m := parse(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_tags: [gpu]}\n")
	_, _, err := ResolveCloudTargets(m, "production", a, c)
	missing := placementError(t, err, "cloud-tag-unmatched")
	if missing.Path != "services.web.cloud_tags" || !strings.Contains(missing.Message, "gpu") {
		t.Fatalf("unclear finding: %+v", missing)
	}

	m = parse(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_tags: [web]}\n")
	_, _, err = ResolveCloudTargets(m, "production", a, c)
	ambiguous := placementError(t, err, "cloud-tag-ambiguous")
	if !strings.Contains(ambiguous.Message, "frontend") || !strings.Contains(ambiguous.Message, "preview") {
		t.Fatalf("ambiguous finding should name every match: %+v", ambiguous)
	}

	// The assignment only lists targets the grant allows. A tag carried
	// only by a target outside the grant matches nothing.
	a.Targets = a.Targets[:1]
	m = parse(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_tags: [us]}\n")
	_, _, err = ResolveCloudTargets(m, "production", a, c)
	placementError(t, err, "cloud-tag-unmatched")

	m = parse(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_tags: [eu], provider: vercel}\n")
	if _, _, err = ResolveCloudTargets(m, "production", a, c); err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("tags with direct placement accepted: %v", err)
	}
	if _, _, err = ResolveCloudTargets(parse(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_tags: [eu]}\n"), "production", nil, c); err == nil {
		t.Fatal("tags without an assigned Cloud accepted")
	}
}

func TestCloudTagsOverridesAndManifestDefaults(t *testing.T) {
	a, c := taggedFixture()
	doc := "version: 1\nservices:\n  web:\n    source: cmd/api\n    cloud_target: frontend\n    environments:\n      staging: {cloud_tags: [us]}\n  api:\n    source: cmd/api\n    cloud_tags: [us]\n    environments:\n      staging: {cloud_target: frontend}\n"
	_, pins, err := ResolveCloudTargets(parse(t, doc), "staging", a, c)
	if err != nil {
		t.Fatal(err)
	}
	if pins["web"].TargetID != "target-b" || pins["api"].TargetID != "target-a" {
		t.Fatalf("environment override did not replace the service's choice: %+v", pins)
	}

	doc = "version: 1\ndefaults: {cloud_tags: [eu]}\nservices:\n  web: {source: cmd/api}\n  api: {source: cmd/api, cloud_tags: [us]}\n  direct: {source: cmd/api, provider: vercel}\n"
	resolved, pins, err := ResolveCloudTargets(parse(t, doc), "production", a, c)
	if err != nil {
		t.Fatal(err)
	}
	if pins["web"].TargetID != "target-a" || pins["api"].TargetID != "target-b" {
		t.Fatalf("manifest default tags misapplied: %+v", pins)
	}
	if _, ok := pins["direct"]; ok {
		t.Fatal("default tags overrode a service's own provider")
	}
	if len(resolved.Defaults.CloudTags) != 0 {
		t.Fatal("resolved manifest kept default tags for the planner")
	}
}

func TestCloudTagsSkipHeldServices(t *testing.T) {
	a, c := taggedFixture()
	m := parse(t, "version: 1\nservices:\n  web: {source: cmd/api, cloud_tags: [gpu]}\n")
	_, pins, err := resolveCloudPlacements(m, "production", a, c, nil, map[string]bool{"web": true})
	if err != nil || len(pins) != 0 {
		t.Fatalf("held service was resolved: %+v %v", pins, err)
	}
}

func cloudDefaultFixture() (*cloudprofiles.Assignment, []ConnectionOption) {
	a, c := cloudTargetFixture()
	a.Targets = nil
	a.DefaultPlacement = &cloudprofiles.DefaultPlacement{Provider: "vercel", ConnectionID: "b", Region: "iad1"}
	return a, c
}

func TestCloudDefaultPlacesServicesWithoutPlacement(t *testing.T) {
	a, c := cloudDefaultFixture()
	doc := "version: 1\nservices:\n  web: {source: cmd/api}\n  other: {source: cmd/api}\n"
	resolved, pins, err := ResolveCloudTargets(parse(t, doc), "production", a, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web", "other"} {
		pin := pins[name]
		if !pin.Default || pin.TargetID != "" || pin.TargetRevision != 0 || pin.Provider != "vercel" || pin.ConnectionID != "b" || pin.Region != "iad1" || pin.RegionSource != "default" || pin.CloudRevision != 3 {
			t.Fatalf("%s default pin: %+v", name, pin)
		}
	}
	r := Compute(Input{Environment: "production", Graph: cloudTargetGraph(), Manifest: resolved, Providers: providers.Registry(), Connections: c, CloudPlacements: pins})
	if !r.Executable {
		t.Fatalf("default placement blocked: %+v", r.Problems)
	}
	if got := action(t, r, "service:web"); got.ConnectionID != "b" || got.Service.CloudPlacement == nil || !got.Service.CloudPlacement.Default {
		t.Fatalf("default not pinned on the action: %+v", got)
	}
}

func TestCloudDefaultKeepsApplyingOnceTargetsExist(t *testing.T) {
	a, c := cloudDefaultFixture()
	targets, _ := taggedFixture()
	a.Targets = targets.Targets
	doc := "version: 1\nservices:\n  web: {source: cmd/api}\n  tagged: {source: cmd/api, cloud_tags: [eu]}\n  named: {source: cmd/api, cloud_target: frontend}\n  direct: {source: cmd/api, provider: vercel}\n"
	_, pins, err := ResolveCloudTargets(parse(t, doc), "production", a, c)
	if err != nil {
		t.Fatal(err)
	}
	if !pins["web"].Default || pins["web"].ConnectionID != "b" {
		t.Fatalf("untagged service left the default: %+v", pins["web"])
	}
	if pins["tagged"].Default || pins["tagged"].TargetID != "target-a" || pins["named"].TargetID != "target-a" {
		t.Fatalf("targets misresolved beside the default: %+v", pins)
	}
	if _, ok := pins["direct"]; ok {
		t.Fatal("the default overrode a service's own provider")
	}
}

func TestCloudDefaultNeedsAWholeCloudGrantAndAReadyAccount(t *testing.T) {
	a, c := cloudDefaultFixture()
	// Resolve leaves out the default when the grant names chosen targets.
	a.DefaultPlacement = nil
	_, pins, err := ResolveCloudTargets(parse(t, "version: 1\nservices:\n  web: {source: cmd/api}\n"), "production", a, c)
	if err != nil || len(pins) != 0 {
		t.Fatalf("service placed without a reachable default: %+v %v", pins, err)
	}
	a, c = cloudDefaultFixture()
	c[1].Ready = false
	if _, _, err = ResolveCloudTargets(parse(t, "version: 1\nservices:\n  web: {source: cmd/api}\n"), "production", a, c); err == nil || !strings.Contains(err.Error(), "ready") {
		t.Fatalf("unready default account accepted: %v", err)
	}
}

func TestCloudResolutionManifestReportsWhyItNeedsTheCloud(t *testing.T) {
	for _, tc := range []struct {
		doc                string
		explicit, fallback bool
	}{
		{"web: {source: cmd/api, provider: vercel}", false, false},
		{"web: {source: cmd/api}", false, true},
		{"web: {source: cmd/api, cloud_tags: [eu]}", true, false},
		{"web: {source: cmd/api, cloud_target: frontend}", true, false},
		{"web: {source: cmd/api, enabled: false}", false, false},
	} {
		_, needs, err := cloudResolutionManifest(parse(t, "version: 1\nservices:\n  "+tc.doc+"\n"), Input{Environment: "production", Graph: cloudTargetGraph()})
		if err != nil || needs.Explicit != tc.explicit || needs.Fallback != tc.fallback {
			t.Fatalf("%s: %+v %v", tc.doc, needs, err)
		}
	}
}

func TestAppliedCloudPlanMovesDefaultPlacedServicesToTheNewDefault(t *testing.T) {
	registry := providers.Registry()
	b := appliedTestBinding(t, "web", "commit")
	var cfg ServiceConfig
	_ = json.Unmarshal(b.Config, &cfg)
	cfg.CloudPlacement = &CloudPlacement{Default: true, CloudID: "cloud", CloudRevision: 1, AssignmentRevision: 1, Provider: "vercel", ConnectionID: "old", AccountIdentity: "vercel:old"}
	b.Config, _ = json.Marshal(cfg)
	options := []ConnectionOption{{ID: "new", Cloud: "vercel", Ready: true, Account: "new", Details: map[string]string{"team_id": "new"}}}

	next := appliedAssignment("unused")
	next.Targets = nil
	next.DefaultPlacement = &cloudprofiles.DefaultPlacement{Provider: "vercel", ConnectionID: "new"}
	result := AppliedCloudPlan([]*Binding{b}, next, "", options, registry)
	if !result.Executable {
		t.Fatalf("blocked: %+v", result.Problems)
	}
	a := action(t, result, "service:web")
	if a.Op != Replace || a.ConnectionID != "new" || a.Service.CloudPlacement == nil || !a.Service.CloudPlacement.Default || a.Service.CloudPlacement.TargetID != "" {
		t.Fatalf("default-placed service not moved to the new default: %+v", a)
	}

	// A target switch elsewhere in the Cloud leaves default-placed services alone.
	if switched := AppliedCloudPlan([]*Binding{b}, next, "target", options, registry); action(t, switched, "service:web").Op != Keep {
		t.Fatal("target switch touched a default-placed service")
	}

	next.DefaultPlacement = nil
	if blocked := AppliedCloudPlan([]*Binding{b}, next, "", options, registry); blocked.Executable {
		t.Fatal("reassignment to a Cloud without a default kept a default-placed service")
	}
}
