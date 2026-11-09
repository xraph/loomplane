package planning

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/security"
)

func TestCloudCompensationUsesSavedSourceAndRetainedIdentity(t *testing.T) {
	source := Binding{ID: "binding", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Subject: ServiceSubject, Name: "web", Provider: "server", ConnectionID: "source", ExternalID: "original", PlanID: "source-plan", Config: json.RawMessage(`{"commit_sha":"saved","applied_inputs":{"variables":"saved-vars"}}`)}
	source.PhysicalID = "native-original"
	source.OwnedResources = map[string]json.RawMessage{"zone_id": json.RawMessage(`"123"`)}
	candidate := source
	candidate.ExternalID, candidate.ConnectionID, candidate.PlanID = "candidate", "destination", "sync.env"
	candidate.Config = json.RawMessage(`{"commit_sha":"newer"}`)
	candidate.Replaced = []Binding{source}
	original := Plan{ID: "sync.env", Actions: []Action{{ID: "service:web", Name: "web", Provider: "server", Subject: ServiceSubject, Op: Replace}}}
	raw, _ := json.Marshal([]Binding{source})
	planRaw, _ := json.Marshal(original)
	snapshot := &cloudprofiles.Snapshot{Environments: []cloudprofiles.EnvironmentSnapshot{{EnvironmentID: "env", Bindings: raw, Plan: planRaw}}}
	for _, retained := range []bool{true, false} {
		current := candidate
		if !retained {
			current.Replaced = nil
		}
		bindings, compensation, err := cloudCompensationBindings([]*Binding{&current}, snapshot, "sync", "env", false)
		if err != nil {
			t.Fatal(err)
		}
		c := compensation["service:web"]
		if c == nil || c.Retained != retained || c.Source.ExternalID != "original" || c.Candidate.ExternalID != "candidate" || c.PlanID != "sync.env" {
			t.Fatalf("wrong compensation identity: %+v", c)
		}
		var cfg ServiceConfig
		if err := json.Unmarshal(bindings[0].Config, &cfg); err != nil || cfg.CommitSHA != "saved" {
			t.Fatal("recreation refreshed source snapshot", err, cfg)
		}
		if retained && bindings[0].ExternalID != "original" {
			t.Fatal("retained source would be recreated")
		}
		if !retained && bindings[0].ExternalID != "candidate" {
			t.Fatal("released source appears live")
		}
	}
	for _, ownership := range []map[string]json.RawMessage{nil, {"zone_id": json.RawMessage(`"foreign"`)}} {
		candidate.Replaced[0].OwnedResources = ownership
		if _, _, err := cloudCompensationBindings([]*Binding{&candidate}, snapshot, "sync", "env", false); err == nil {
			t.Error("changed retained source ownership accepted")
		}
	}
	candidate.Replaced[0].OwnedResources = source.OwnedResources
	candidate.Replaced[0].Config = json.RawMessage(`{"commit_sha":"other"}`)
	if _, _, err := cloudCompensationBindings([]*Binding{&candidate}, snapshot, "sync", "env", false); err == nil {
		t.Fatal("different retained source config accepted")
	}
}

func TestCloudCompensationPublicModesExcludeSavedInputs(t *testing.T) {
	for _, tc := range []struct {
		modes []bool
		want  string
	}{{nil, ""}, {[]bool{true}, "restore_retained"}, {[]bool{false}, "recreate"}, {[]bool{true, false}, "mixed"}} {
		var actions []Action
		for _, retained := range tc.modes {
			actions = append(actions, Action{Compensation: &CloudCompensation{Retained: retained, Source: Binding{Config: json.RawMessage(`{"applied_inputs":{"variables":"private-variable-ref","artifact_key":"private-artifact-ref"}}`)}}})
		}
		mode, message := cloudCompensationDescription(actions)
		if mode != tc.want || mode != "" && message == "" {
			t.Fatal(mode, message)
		}
		preview := cloudprofiles.Preview{Environments: []cloudprofiles.EnvironmentChange{{CompensationMode: mode, CompensationMessage: message}}}
		raw, err := json.Marshal(preview)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("private-")) || bytes.Contains(raw, []byte("applied_inputs")) || mode == "" && bytes.Contains(raw, []byte("compensation_")) {
			t.Fatal(string(raw))
		}
	}
}

func TestCloudCompensationMixedRuntimeAlwaysReviewsRecreation(t *testing.T) {
	for _, tc := range []struct{ source, original string }{{"server", "render"}, {"render", "server"}, {"render", "vercel"}} {
		source := Binding{ID: "binding", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Name: "web", Subject: ServiceSubject, Provider: tc.source, ConnectionID: "source", ExternalID: "source", PlanID: "source-plan", Config: json.RawMessage(`{"commit_sha":"saved"}`)}
		current := source
		current.Provider = tc.original
		current.ConnectionID = "destination"
		current.ExternalID = "candidate"
		current.Replaced = []Binding{source}
		original := Plan{Actions: []Action{{ID: "service:web", Name: "web", Subject: ServiceSubject, Provider: tc.original, Op: Replace}}}
		bindings, _ := json.Marshal([]Binding{source})
		plan, _ := json.Marshal(original)
		_, metadata, err := cloudCompensationBindings([]*Binding{&current}, &cloudprofiles.Snapshot{Environments: []cloudprofiles.EnvironmentSnapshot{{EnvironmentID: "env", Bindings: bindings, Plan: plan}}}, "sync", "env", false)
		if err != nil {
			t.Fatal(err)
		}
		c := metadata["service:web"]
		if c == nil || c.Retained || c.ServerAttempt != (tc.original == "server") {
			t.Fatal(tc, c)
		}
	}
}

// A revert onto the Loomplane Cloud an environment left is the same
// environment move back. A target change on a Loomplane Cloud target never
// happened, so it has nothing to revert, and says so in its own branch.
func TestCloudRevertPlatformTarget(t *testing.T) {
	st := &platformSyncStore{}
	p := security.Principal{OrgID: "org", UserID: "user"}
	moved := &cloudprofiles.Preview{Input: cloudprofiles.ChangeInput{CloudID: "cloud_user", EnvironmentID: "env"}, BeforeAssignment: &cloudprofiles.Assignment{CloudID: "cloud_platform"}}
	in, err := revertChangeInput(t.Context(), st, p, "proj", &cloudprofiles.Sync{}, moved)
	if err != nil || in.CloudID != "cloud_platform" || in.EnvironmentID != "env" || in.Revision != 1 {
		t.Fatalf("revert onto Loomplane Cloud: %+v %v", in, err)
	}
	before, after := *platformTarget, *platformTarget
	sync := &cloudprofiles.Sync{CloudID: "cloud_platform", TargetID: platformTarget.ID, Before: &before, After: &after}
	if _, err = revertChangeInput(t.Context(), st, p, "proj", sync, &cloudprofiles.Preview{}); !errors.Is(err, cloudprofiles.ErrPlatformOwned) {
		t.Fatalf("revert of a Loomplane Cloud target change: %v", err)
	}
}
