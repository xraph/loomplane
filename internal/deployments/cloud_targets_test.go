package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

type cloudResolver struct{ assignment *cloudprofiles.Assignment }

func (c *cloudResolver) Resolve(context.Context, string, string, string) (*cloudprofiles.Assignment, error) {
	return c.assignment, nil
}

func cloudRunner(t *testing.T) (*Runner, *planning.Plan, *fakePlans, *recorder, *cloudResolver) {
	t.Helper()
	plan := webAndAPI()
	plan.Actions = plan.Actions[1:]
	a := &plan.Actions[0]
	a.Service.Public = false
	a.ConnectionID = "cloud-account"
	a.Service.CloudPlacement = &planning.CloudPlacement{CloudID: "cloud", CloudRevision: 2, AssignmentRevision: 3, TargetID: "target", TargetRevision: 4, TargetName: "frontend", Provider: a.Provider, ConnectionID: a.ConnectionID}
	c := &connections.Connection{ID: a.ConnectionID, Cloud: "vercel", Status: connections.StatusReady, Details: map[string]string{"team_id": "team-a"}}
	a.Service.CloudPlacement.AccountIdentity = connections.AccountIdentity(c.Cloud, c.Account, c.Details)
	exec := &recorder{}
	r, _, plans := newRunner(t, plan, exec, nil)
	r.credentials = targetCredentials{a.ConnectionID: c}
	resolved := &cloudResolver{assignment: &cloudprofiles.Assignment{OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, CloudID: "cloud", CloudRevision: 2, Revision: 3, Targets: []cloudprofiles.Target{{ID: "target", OrgID: plan.OrgID, ProjectID: plan.ProjectID, CloudID: "cloud", Name: "frontend", Provider: a.Provider, ConnectionID: a.ConnectionID, Revision: 4}}}}
	r.cloudProfiles = resolved
	return r, plan, plans, exec, resolved
}

func TestRunnerCloudTargetRevalidatesBeforeStartAndRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Runner, *cloudResolver)
	}{
		{"assignment", func(_ *Runner, c *cloudResolver) { c.assignment.Revision++ }},
		{"cloud", func(_ *Runner, c *cloudResolver) { c.assignment.CloudRevision++ }},
		{"target", func(_ *Runner, c *cloudResolver) { c.assignment.Targets[0].Revision++ }},
		{"project scope", func(_ *Runner, c *cloudResolver) { c.assignment.ProjectID = "other" }},
		{"environment scope", func(_ *Runner, c *cloudResolver) { c.assignment.EnvironmentID = "other" }},
		{"target scope", func(_ *Runner, c *cloudResolver) { c.assignment.Targets[0].OrgID = "other" }},
		{"account identity", func(r *Runner, _ *cloudResolver) {
			r.credentials.(targetCredentials)["cloud-account"].Details["team_id"] = "team-b"
		}},
		{"unverified identity", func(r *Runner, _ *cloudResolver) { r.credentials.(targetCredentials)["cloud-account"].Details = nil }},
		{"missing resolver", func(r *Runner, _ *cloudResolver) { r.cloudProfiles = nil }},
	} {
		for _, atStart := range []bool{true, false} {
			t.Run(tc.name+map[bool]string{true: " start", false: " run"}[atStart], func(t *testing.T) {
				r, plan, plans, exec, c := cloudRunner(t)
				if atStart {
					tc.change(r, c)
					if _, _, err := r.Start(t.Context(), plan.OrgID, plan.ID); err == nil {
						t.Fatal("stale placement started")
					}
				} else {
					id, _, err := r.Start(t.Context(), plan.OrgID, plan.ID)
					if err != nil {
						t.Fatal(err)
					}
					tc.change(r, c)
					err = r.Run(t.Context(), plan.OrgID, id, plan.Actions[0].ID, nil, func([]byte) error { return nil })
					if err == nil {
						t.Fatal("stale placement ran")
					}
				}
				if len(exec.jobs) != 0 || len(plans.bindings) != 0 {
					t.Fatal("stale placement mutated provider or binding")
				}
			})
		}
	}
}

func TestRunnerCloudTargetAdoptsMetadataWithoutProviderMutation(t *testing.T) {
	r, plan, plans, exec, _ := cloudRunner(t)
	a := &plan.Actions[0]
	a.Op = planning.Keep
	a.AdoptCloudPlacement = true
	cfg := *a.Service
	cfg.CloudPlacement = nil
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	plans.bindings[a.ID] = &planning.Binding{ID: "existing", OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, Name: a.Name, Subject: a.Subject, Provider: a.Provider, Region: a.Region, ConnectionID: a.ConnectionID, ExternalID: "serving", Revision: "provider-revision", URL: "https://serving", Config: raw}
	a.CloudAdoption = planning.ReviewCloudAdoption(plans.bindings[a.ID])
	id, steps, err := r.Start(t.Context(), plan.OrgID, plan.ID)
	if err != nil || len(steps) != 1 {
		t.Fatalf("metadata step omitted: %v %v", steps, err)
	}
	if strings.Contains(string(plans.bindings[a.ID].Config), "cloud_placement") {
		t.Fatal("Start adopted before approved action execution")
	}
	if err = r.Run(t.Context(), plan.OrgID, id, a.ID, nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	b := plans.bindings[a.ID]
	if len(exec.jobs) != 0 || b.ExternalID != "serving" || b.Revision != "provider-revision" || !strings.Contains(string(b.Config), "cloud_placement") {
		t.Fatalf("adoption changed serving resource: %+v, provider calls=%d", b, len(exec.jobs))
	}
}

func TestRunnerCloudAdoptionRejectsChangedServingBinding(t *testing.T) {
	for _, change := range []struct {
		name  string
		apply func(*planning.Binding)
	}{
		{"external resource", func(b *planning.Binding) { b.ExternalID = "concurrent-serving" }},
		{"provider revision", func(b *planning.Binding) { b.Revision = "new-revision" }},
		{"binding ID", func(b *planning.Binding) { b.ID = "another-binding" }},
		{"serving plan", func(b *planning.Binding) { b.PlanID = "concurrent-plan" }},
		{"configuration", func(b *planning.Binding) { b.Config = []byte(`{"provider":"vercel","port":9090}`) }},
	} {
		t.Run(change.name, func(t *testing.T) {
			r, plan, plans, exec, _ := cloudRunner(t)
			a := &plan.Actions[0]
			a.Op = planning.Keep
			a.AdoptCloudPlacement = true
			cfg := *a.Service
			cfg.CloudPlacement = nil
			raw, _ := json.Marshal(cfg)
			b := &planning.Binding{ID: "existing", OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, Subject: a.Subject, Name: a.Name, Provider: a.Provider, Region: a.Region, ConnectionID: a.ConnectionID, ExternalID: "serving", Revision: "reviewed", Config: raw}
			a.CloudAdoption = planning.ReviewCloudAdoption(b)
			plans.bindings[a.ID] = b
			id, _, err := r.Start(t.Context(), plan.OrgID, plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			change.apply(b)
			if err = r.Run(t.Context(), plan.OrgID, id, a.ID, nil, func([]byte) error { return nil }); err == nil {
				t.Fatal("changed binding relabeled")
			}
			if strings.Contains(string(b.Config), "cloud_placement") || len(exec.jobs) > 0 {
				t.Fatal("changed binding mutated")
			}
		})
	}
}

func TestRunnerCloudTargetAllowsEffectiveRegionOverride(t *testing.T) {
	r, plan, _, _, resolver := cloudRunner(t)
	a := &plan.Actions[0]
	a.Provider = "cloud-run"
	a.Service.Provider = "cloud-run"
	a.Region = "us-east1"
	a.Service.Region = a.Region
	a.Service.CloudPlacement.Provider = a.Provider
	a.Service.CloudPlacement.Region = a.Region
	resolver.assignment.Targets[0].Provider = a.Provider
	resolver.assignment.Targets[0].Region = "us-central1"
	c := r.credentials.(targetCredentials)[a.ConnectionID]
	c.Cloud = "gcp"
	c.Account = "project-a"
	c.Details = map[string]string{"project_id": "project-a"}
	a.Service.CloudPlacement.AccountIdentity = connections.AccountIdentity(c.Cloud, c.Account, c.Details)
	if _, _, err := r.Start(t.Context(), plan.OrgID, plan.ID); err != nil {
		t.Fatalf("effective region rejected: %v", err)
	}
}

func TestRunnerCloudAdoptionRetriesAfterBindingWrite(t *testing.T) {
	r, plan, plans, exec, _ := cloudRunner(t)
	a := &plan.Actions[0]
	a.Op = planning.Keep
	a.AdoptCloudPlacement = true
	cfg := *a.Service
	cfg.CloudPlacement = nil
	raw, _ := json.Marshal(cfg)
	b := &planning.Binding{ID: "existing", OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, Subject: a.Subject, Name: a.Name, Provider: a.Provider, Region: a.Region, ConnectionID: a.ConnectionID, ExternalID: "serving", Revision: "reviewed", Config: raw}
	a.CloudAdoption = planning.ReviewCloudAdoption(b)
	plans.bindings[a.ID] = b
	id, _, err := r.Start(t.Context(), plan.OrgID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Run(t.Context(), plan.OrgID, id, a.ID, nil, func([]byte) error { return errors.New("checkpoint unavailable") }); err == nil {
		t.Fatal("expected checkpoint failure")
	}
	if err = r.Run(t.Context(), plan.OrgID, id, a.ID, nil, func([]byte) error { return nil }); err != nil {
		t.Fatalf("adoption could not resume after persisted metadata: %v", err)
	}
	if len(exec.jobs) > 0 {
		t.Fatal("retry called provider")
	}
}

type adoptionRacePlans struct{ *fakePlans }

func (p adoptionRacePlans) AdoptCloudPlacement(ctx context.Context, b *planning.Binding, placement *planning.CloudPlacement, at time.Time) (bool, error) {
	p.mu.Lock()
	changed := *p.bindings["service:web"]
	changed.ExternalID = "racing-serving"
	p.bindings["service:web"] = &changed
	p.mu.Unlock()
	return p.fakePlans.AdoptCloudPlacement(ctx, b, placement, at)
}

func TestRunnerCloudAdoptionCASRejectsRace(t *testing.T) {
	r, plan, plans, exec, _ := cloudRunner(t)
	a := &plan.Actions[0]
	a.Op = planning.Keep
	a.AdoptCloudPlacement = true
	cfg := *a.Service
	cfg.CloudPlacement = nil
	raw, _ := json.Marshal(cfg)
	b := &planning.Binding{ID: "existing", OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, Subject: a.Subject, Name: a.Name, Provider: a.Provider, ConnectionID: a.ConnectionID, ExternalID: "serving", Config: raw}
	a.CloudAdoption = planning.ReviewCloudAdoption(b)
	plans.bindings[a.ID] = b
	r.plans = adoptionRacePlans{plans}
	id, _, err := r.Start(t.Context(), plan.OrgID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Run(t.Context(), plan.OrgID, id, a.ID, nil, func([]byte) error { return nil }); err == nil {
		t.Fatal("adoption overwrote concurrent resource")
	}
	if plans.bindings[a.ID].ExternalID != "racing-serving" || strings.Contains(string(plans.bindings[a.ID].Config), "cloud_placement") || len(exec.jobs) > 0 {
		t.Fatal("concurrent resource changed")
	}
}

func (f *fakePlans) AdoptCloudPlacement(_ context.Context, expected *planning.Binding, placement *planning.CloudPlacement, at time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := string(expected.Subject) + ":" + expected.Name
	b := f.bindings[key]
	if b == nil || !reflect.DeepEqual(b, expected) {
		return false, nil
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(b.Config, &config); err != nil {
		return false, err
	}
	raw, err := json.Marshal(placement)
	if err != nil {
		return false, err
	}
	config["cloud_placement"] = raw
	updated := *b
	updated.Config, err = json.Marshal(config)
	if err != nil {
		return false, err
	}
	updated.UpdatedAt = at
	f.bindings[key] = &updated
	return true, nil
}

func TestRunnerCloudTargetsTwoVercelAccountsIgnoreDefault(t *testing.T) {
	r, plan, plans, exec, resolver := cloudRunner(t)
	plan.Connections["vercel"] = "wrong-default"
	first := &plan.Actions[0]
	first.Service.Env = nil
	first.DependsOn = nil
	second := *first
	second.ID = "service:preview"
	second.Name = "preview"
	cfg := *first.Service
	pin := *cfg.CloudPlacement
	pin.TargetID = "preview-target"
	pin.TargetName = "preview"
	pin.ConnectionID = "preview-account"
	c := &connections.Connection{ID: pin.ConnectionID, Cloud: "vercel", Status: connections.StatusReady, Details: map[string]string{"team_id": "team-b"}}
	pin.AccountIdentity = connections.AccountIdentity(c.Cloud, c.Account, c.Details)
	cfg.CloudPlacement = &pin
	second.Service = &cfg
	second.ConnectionID = c.ID
	plan.Actions = append(plan.Actions, second)
	r.credentials.(targetCredentials)[c.ID] = c
	target := resolver.assignment.Targets[0]
	target.ID = pin.TargetID
	target.Name = pin.TargetName
	target.ConnectionID = c.ID
	resolver.assignment.Targets = append(resolver.assignment.Targets, target)
	if _, err := runAll(t, r, plan); err != nil {
		t.Fatal(err)
	}
	if len(exec.jobs) != 2 {
		t.Fatalf("provider calls=%d", len(exec.jobs))
	}
	for _, a := range plan.Actions {
		b := plans.bindings[a.ID]
		if b == nil || b.ConnectionID != a.ConnectionID {
			t.Fatalf("wrong managed account for %s: %+v", a.Name, b)
		}
	}
}
