package cloudprofiles

import (
	"errors"
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/security"
)

func TestNormalizeTags(t *testing.T) {
	got, err := normalizeTags([]string{" gpu ", "eu", "gpu"})
	if err != nil || !slices.Equal(got, []string{"eu", "gpu"}) {
		t.Fatalf("tags: %v %v", got, err)
	}
	if got, err = normalizeTags(nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("no tags should be an empty list: %#v %v", got, err)
	}
	many := make([]string, maxTags+1)
	for i := range many {
		many[i] = "t" + string(rune('a'+i))
	}
	for _, bad := range [][]string{{"EU"}, {"has space"}, {""}, {"-lead"}, {"a-very-long-tag-name-that-goes-past-32"}, many} {
		if _, err = normalizeTags(bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%v accepted: %v", bad, err)
		}
	}
}

func TestServiceTargetTags(t *testing.T) {
	s, st, _ := fixture()
	audit := &auditRecorder{}
	s.audit = audit
	p := security.Principal{OrgID: "org_1"}
	target, err := s.CreateTarget(t.Context(), p, "proj_1", "cloud_1", CreateTargetInput{Name: "primary", Provider: "cloud-run", ConnectionID: "conn_1", Tags: []string{"gpu", "eu"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(target.Tags, []string{"eu", "gpu"}) || !slices.Equal(st.target.Tags, target.Tags) {
		t.Fatalf("tags not normalized and stored: %+v", target)
	}
	if got := audit.events[len(audit.events)-1].Attributes["tags"]; !slices.Equal(got.([]string), target.Tags) {
		t.Fatalf("create audit omitted tags: %+v", audit.events)
	}
	if _, err = s.CreateTarget(t.Context(), p, "proj_1", "cloud_1", CreateTargetInput{Name: "other", Provider: "cloud-run", ConnectionID: "conn_1", Tags: []string{"Bad"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad tag accepted: %v", err)
	}

	// Leaving tags out of an update keeps them; an empty list clears them.
	renamed, err := s.UpdateTarget(t.Context(), p, "proj_1", "cloud_1", target.ID, UpdateTargetInput{DisplayName: "Renamed", Revision: 1})
	if err != nil || !slices.Equal(renamed.Tags, []string{"eu", "gpu"}) {
		t.Fatalf("rename dropped tags: %+v %v", renamed, err)
	}
	retagged, err := s.UpdateTarget(t.Context(), p, "proj_1", "cloud_1", target.ID, UpdateTargetInput{DisplayName: "Renamed", Tags: &[]string{"us"}, Revision: 2})
	if err != nil || !slices.Equal(retagged.Tags, []string{"us"}) || retagged.Revision != 3 {
		t.Fatalf("retag: %+v %v", retagged, err)
	}
	event := audit.events[len(audit.events)-1]
	if event.Action != "cloud_target.update" || !slices.Equal(event.Attributes["tags_before"].([]string), []string{"eu", "gpu"}) || !slices.Equal(event.Attributes["tags"].([]string), []string{"us"}) {
		t.Fatalf("update audit: %+v", event)
	}
	cleared, err := s.UpdateTarget(t.Context(), p, "proj_1", "cloud_1", target.ID, UpdateTargetInput{DisplayName: "Renamed", Tags: &[]string{}, Revision: 3})
	if err != nil || len(cleared.Tags) != 0 {
		t.Fatalf("clear: %+v %v", cleared, err)
	}
	if cleared.Provider != target.Provider || cleared.ConnectionID != target.ConnectionID || cleared.Region != target.Region {
		t.Fatalf("tag edit changed placement: %+v", cleared)
	}
}

func TestServiceCloudDefaultPlacement(t *testing.T) {
	s, st, _ := fixture()
	audit := &auditRecorder{}
	s.audit = audit
	p := security.Principal{OrgID: "org_1"}
	cloud, err := s.Create(t.Context(), p, "proj_1", CreateInput{Name: "Single", DefaultPlacement: &DefaultPlacement{Provider: " cloud-run ", ConnectionID: "conn_1", Region: "us-central1"}})
	if err != nil {
		t.Fatal(err)
	}
	if d := cloud.DefaultPlacement; d == nil || d.Provider != "cloud-run" || d.ConnectionID != "conn_1" || d.Region != "us-central1" || st.profile.DefaultPlacement == nil {
		t.Fatalf("default not kept: %+v", cloud)
	}
	if audit.events[0].Attributes["default_placement"] == nil {
		t.Fatalf("create audit omitted default: %+v", audit.events[0])
	}
	for name, in := range map[string]*DefaultPlacement{
		"no connection":  {Provider: "cloud-run"},
		"no provider":    {ConnectionID: "conn_1"},
		"other cloud":    {Provider: "vercel", ConnectionID: "conn_1"},
		"unknown":        {Provider: "cloud-run", ConnectionID: "conn_other"},
		"unknown vendor": {Provider: "unknown", ConnectionID: "conn_1"},
	} {
		if _, err = s.Create(t.Context(), p, "proj_1", CreateInput{Name: "Bad", DefaultPlacement: in}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}

	// Renaming keeps the default; the dedicated call replaces or clears it.
	renamed, err := s.Update(t.Context(), p, "proj_1", cloud.ID, UpdateInput{Name: "Renamed", Revision: 1})
	if err != nil || renamed.DefaultPlacement == nil {
		t.Fatalf("rename dropped default: %+v %v", renamed, err)
	}
	if _, err = s.SetDefaultPlacement(t.Context(), p, "proj_1", cloud.ID, SetDefaultPlacementInput{Revision: 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	cleared, err := s.SetDefaultPlacement(t.Context(), p, "proj_1", cloud.ID, SetDefaultPlacementInput{Revision: 2})
	if err != nil || cleared.DefaultPlacement != nil || cleared.Revision != 3 || st.profile.DefaultPlacement != nil {
		t.Fatalf("clear: %+v %v", cleared, err)
	}
	set, err := s.SetDefaultPlacement(t.Context(), p, "proj_1", cloud.ID, SetDefaultPlacementInput{DefaultPlacement: &DefaultPlacement{Provider: "cloud-run", ConnectionID: "conn_1"}, Revision: 3})
	if err != nil || set.DefaultPlacement == nil || set.Revision != 4 {
		t.Fatalf("set: %+v %v", set, err)
	}
	event := audit.events[len(audit.events)-1]
	if event.Action != "cloud.default_placement" || event.Attributes["before"] != nil || event.Attributes["after"] == nil {
		t.Fatalf("default audit: %+v", event)
	}
	if _, err = s.SetDefaultPlacement(t.Context(), p, "proj_1", cloud.ID, SetDefaultPlacementInput{DefaultPlacement: &DefaultPlacement{Provider: "vercel", ConnectionID: "conn_1"}, Revision: 4}); !errors.Is(err, ErrInvalid) || st.profile.Revision != 4 {
		t.Fatalf("mismatched default accepted: %v", err)
	}
}
