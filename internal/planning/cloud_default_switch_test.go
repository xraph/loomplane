package planning

import (
	"encoding/json"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/providers"
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
