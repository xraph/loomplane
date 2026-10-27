package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providers"
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
	a.Service.CloudPlacement = &planning.CloudPlacement{GrantRevision: 1, CloudID: "cloud", CloudRevision: 2, AssignmentRevision: 3, TargetID: "target", TargetRevision: 4, TargetName: "frontend", Provider: a.Provider, ConnectionID: a.ConnectionID}
	c := &connections.Connection{ID: a.ConnectionID, Cloud: "vercel", Status: connections.StatusReady, Details: map[string]string{"team_id": "team-a"}}
	a.Service.CloudPlacement.AccountIdentity = connections.AccountIdentity(c.Cloud, c.Account, c.Details)
	exec := &recorder{}
	r, _, plans := newRunner(t, plan, exec, nil)
	r.credentials = targetCredentials{a.ConnectionID: c}
	resolved := &cloudResolver{assignment: &cloudprofiles.Assignment{OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, GrantRevision: 1, CloudID: "cloud", CloudRevision: 2, Revision: 3, Targets: []cloudprofiles.Target{{ID: "target", OrgID: plan.OrgID, ProjectID: plan.ProjectID, CloudID: "cloud", Name: "frontend", Provider: a.Provider, ConnectionID: a.ConnectionID, Revision: 4}}}}
	r.cloudProfiles = resolved
	return r, plan, plans, exec, resolved
}

func TestRunnerCloudTargetRevalidatesBeforeStartAndRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Runner, *cloudResolver)
	}{
		{"grant", func(_ *Runner, c *cloudResolver) { c.assignment.GrantRevision++ }},
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

func TestRunnerCloudAdoptionRevalidatesOwnedConnectionSettings(t *testing.T) {
	for _, field := range []string{"ingress_domain", "tls_secret", "storage_class"} {
		for _, atStart := range []bool{true, false} {
			t.Run(field+map[bool]string{true: " start", false: " run"}[atStart], func(t *testing.T) {
				r, plan, plans, exec, resolver := cloudRunner(t)
				r.providers = providers.Registry()
				a := &plan.Actions[0]
				a.Provider = "kubernetes"
				a.Service.Provider = a.Provider
				a.Op = planning.Keep
				a.AdoptCloudPlacement = true
				pin := a.Service.CloudPlacement
				pin.Provider = a.Provider
				resolver.assignment.Targets[0].Provider = a.Provider
				c := r.credentials.(targetCredentials)[a.ConnectionID]
				c.Cloud = "kubernetes"
				c.Details = map[string]string{"endpoint": "https://cluster", "namespace": "default", "namespace_uid": "namespace-a", "ingress_domain": "apps.example.com", "tls_secret": "tls-original", "storage_class": "fast"}
				pin.AccountIdentity = connections.AccountIdentity(c.Cloud, c.Account, c.Details)
				a.Service.Target = a.ConnectionID
				a.Service.TargetSettings = planning.ReviewedConnectionSettings(c.Cloud, c.Details)
				cfg := *a.Service
				cfg.CloudPlacement = nil
				raw, _ := json.Marshal(cfg)
				b := &planning.Binding{ID: "existing", OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, Subject: a.Subject, Name: a.Name, Provider: a.Provider, ConnectionID: a.ConnectionID, ExternalID: "serving", Config: raw}
				a.CloudAdoption = planning.ReviewCloudAdoption(b)
				plans.bindings[a.ID] = b
				original := string(raw)
				if atStart {
					c.Details[field] = "changed"
					if _, _, err := r.Start(t.Context(), plan.OrgID, plan.ID); err == nil {
						t.Fatal("stale owned settings started adoption")
					}
				} else {
					id, _, err := r.Start(t.Context(), plan.OrgID, plan.ID)
					if err != nil {
						t.Fatal(err)
					}
					c.Details[field] = "changed"
					if err = r.Run(t.Context(), plan.OrgID, id, a.ID, nil, func([]byte) error { return nil }); err == nil {
						t.Fatal("stale owned settings were adopted")
					}
				}
				if string(plans.bindings[a.ID].Config) != original || len(exec.jobs) > 0 {
					t.Fatal("stale settings changed binding or provider")
				}
				if got := connections.AccountIdentity(c.Cloud, c.Account, c.Details); got != pin.AccountIdentity {
					t.Fatal("fixture changed resource identity instead of reviewed settings")
				}
			})
		}
	}
}

func TestLegacyCloudPlacementCannotReuseChangedGrant(t *testing.T) {
	runner, plan, _, _, resolver := cloudRunner(t)
	plan.Actions[0].Service.CloudPlacement.GrantRevision = 0
	resolver.assignment.GrantRevision = 2
	if err := runner.validateCloudPlacement(t.Context(), plan, plan.Actions[0], nil); err == nil {
		t.Fatal("legacy placement reused a changed project grant")
	}
}

// defaultCloudRunner pins the action to the Cloud's default instead of a target.
func defaultCloudRunner(t *testing.T) (*Runner, *planning.Plan, *recorder, *cloudResolver) {
	t.Helper()
	r, plan, _, exec, c := cloudRunner(t)
	a := &plan.Actions[0]
	pin := a.Service.CloudPlacement
	pin.Default, pin.TargetID, pin.TargetName, pin.TargetRevision = true, "", "", 0
	c.assignment.Targets = nil
	c.assignment.DefaultPlacement = &cloudprofiles.DefaultPlacement{Provider: a.Provider, ConnectionID: a.ConnectionID, Region: a.Region}
	return r, plan, exec, c
}

func TestRunnerCloudDefaultRevalidatesBeforeStart(t *testing.T) {
	r, plan, _, _ := defaultCloudRunner(t)
	if _, _, err := r.Start(t.Context(), plan.OrgID, plan.ID); err != nil {
		t.Fatalf("reviewed default placement refused: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*cloudResolver)
	}{
		{"cleared", func(c *cloudResolver) { c.assignment.DefaultPlacement = nil }},
		{"other account", func(c *cloudResolver) { c.assignment.DefaultPlacement.ConnectionID = "other" }},
		{"other provider", func(c *cloudResolver) { c.assignment.DefaultPlacement.Provider = "netlify" }},
		{"cloud revision", func(c *cloudResolver) { c.assignment.CloudRevision++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, plan, exec, c := defaultCloudRunner(t)
			tc.change(c)
			if _, _, err := r.Start(t.Context(), plan.OrgID, plan.ID); err == nil {
				t.Fatal("stale default placement started")
			}
			if len(exec.jobs) != 0 {
				t.Fatal("stale default placement reached the provider")
			}
		})
	}
}

// componentAdoptionRunner has one Keep that records a composed component's
// reviewed Cloud placement on its running binding.
func componentAdoptionRunner(t *testing.T) (*Runner, *planning.Plan, *fakePlans, *recorder) {
	t.Helper()
	plan := webAndAPI()
	c := &connections.Connection{ID: "conn_s", Cloud: "server", Status: connections.StatusReady, Details: map[string]string{"host": "edge.example.com", "user": "deploy", "docker_socket": "/var/run/docker.sock", "docker_id": "daemon-a"}}
	pin := &planning.CloudPlacement{GrantRevision: 1, CloudID: "cloud", CloudRevision: 2, AssignmentRevision: 3, TargetID: "target", TargetRevision: 4, TargetName: "edge-host", Provider: "server", ConnectionID: c.ID, AccountIdentity: connections.AccountIdentity(c.Cloud, c.Account, c.Details), RegionSource: "default"}
	cfg := &planning.ResourceConfig{Provider: "server", Target: c.ID, TargetSettings: planning.ReviewedConnectionSettings(c.Cloud, c.Details), Kind: "sample", CloudPlacement: pin}
	plan.Actions = []planning.Action{{ID: "resource:edge--api", Op: planning.Keep, Subject: planning.ResourceSubject, Name: "edge--api", Provider: "server", ConnectionID: c.ID, Engine: "loomplane", Step: 1, Resource: cfg, AdoptCloudPlacement: true}}
	exec := &recorder{}
	r, _, plans := newRunner(t, plan, exec, nil)
	r.providers = providers.Registry()
	r.credentials = targetCredentials{c.ID: c}
	r.cloudProfiles = &cloudResolver{assignment: &cloudprofiles.Assignment{OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, GrantRevision: 1, CloudID: "cloud", CloudRevision: 2, Revision: 3, Targets: []cloudprofiles.Target{{ID: "target", OrgID: plan.OrgID, ProjectID: plan.ProjectID, CloudID: "cloud", Name: "edge-host", Provider: "server", ConnectionID: c.ID, Revision: 4}}}}
	applied := *cfg
	applied.CloudPlacement = nil
	raw, err := json.Marshal(applied)
	if err != nil {
		t.Fatal(err)
	}
	b := &planning.Binding{ID: "existing", OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, Subject: planning.ResourceSubject, Name: "edge--api", Provider: "server", ConnectionID: c.ID, ExternalID: "container-a", Revision: "running", Config: raw}
	plans.bindings["resource:edge--api"] = b
	plan.Actions[0].CloudAdoption = planning.ReviewCloudAdoption(b)
	return r, plan, plans, exec
}

func TestRunnerComposedComponentAdoptsItsCloudPlacement(t *testing.T) {
	r, plan, plans, exec := componentAdoptionRunner(t)
	a := plan.Actions[0]
	id, steps, err := r.Start(t.Context(), plan.OrgID, plan.ID)
	if err != nil || len(steps) != 1 {
		t.Fatalf("adoption step omitted: %v %v", steps, err)
	}
	if err = r.Run(t.Context(), plan.OrgID, id, a.ID, nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	b := plans.bindings[a.ID]
	var recorded planning.ResourceConfig
	if err = json.Unmarshal(b.Config, &recorded); err != nil {
		t.Fatal(err)
	}
	if len(exec.jobs) != 0 || b.ExternalID != "container-a" || b.Revision != "running" || recorded.CloudPlacement == nil || *recorded.CloudPlacement != *a.Resource.CloudPlacement {
		t.Fatalf("adoption redeployed or didn't record the placement: %+v, provider calls=%d", b, len(exec.jobs))
	}
}

func TestRunnerComposedComponentAdoptionRejectsAChangedBinding(t *testing.T) {
	r, plan, plans, exec := componentAdoptionRunner(t)
	a := plan.Actions[0]
	b := plans.bindings[a.ID]
	var applied planning.ResourceConfig
	if err := json.Unmarshal(b.Config, &applied); err != nil {
		t.Fatal(err)
	}
	applied.Version = "2"
	raw, err := json.Marshal(applied)
	if err != nil {
		t.Fatal(err)
	}
	b.Config = raw
	id, _, err := r.Start(t.Context(), plan.OrgID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Run(t.Context(), plan.OrgID, id, a.ID, nil, func([]byte) error { return nil }); err == nil {
		t.Fatal("changed component binding adopted")
	}
	if string(plans.bindings[a.ID].Config) != string(raw) || len(exec.jobs) != 0 {
		t.Fatal("rejected adoption changed the binding or the provider")
	}
}

// reservationPlans counts reservation checks and fails every one.
type reservationPlans struct {
	*fakePlans
	checks *int
}

func (p reservationPlans) ValidatePlacement(context.Context, string, string, string, string, string, cloudinfra.Placement) error {
	*p.checks++
	return errors.New("no capacity reservation")
}

func (reservationPlans) PooledConnections(context.Context, string) (map[string]bool, error) {
	return map[string]bool{}, nil
}

// The Cloud server a direct target records is metadata for domain
// routes, not a pool reservation: neither adopting it nor deploying with
// it checks capacity.
func TestRunnerDirectTargetServerSkipsReservationChecks(t *testing.T) {
	r, plan, plans, exec := componentAdoptionRunner(t)
	a := &plan.Actions[0]
	a.Resource.CloudPlacement.ServerID = "server_1"
	checks := 0
	r.plans = reservationPlans{plans, &checks}
	id, _, err := r.Start(t.Context(), plan.OrgID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Run(t.Context(), plan.OrgID, id, a.ID, nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var recorded planning.ResourceConfig
	if err = json.Unmarshal(plans.bindings[a.ID].Config, &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.CloudPlacement == nil || recorded.CloudPlacement.ServerID != "server_1" || len(exec.jobs) != 0 {
		t.Fatalf("adoption didn't record the server: %+v, provider calls=%d", recorded.CloudPlacement, len(exec.jobs))
	}
	update := *a
	update.Op, update.AdoptCloudPlacement, update.CloudAdoption = planning.Update, false, nil
	if err = r.validateCloudPlacement(t.Context(), plan, update, nil); err != nil {
		t.Fatal(err)
	}
	if checks != 0 {
		t.Fatalf("a direct target's server checked %d reservations", checks)
	}
	// A pool placement still does.
	update.Resource = &planning.ResourceConfig{}
	*update.Resource = *a.Resource
	pin := *a.Resource.CloudPlacement
	pin.PoolID = "pool"
	update.Resource.CloudPlacement = &pin
	r.cloudProfiles.(*cloudResolver).assignment.Targets[0].PoolID = "pool"
	if err = r.validateCloudPlacement(t.Context(), plan, update, nil); err == nil || checks != 1 {
		t.Fatalf("pool placement skipped its reservation check: %v, checks=%d", err, checks)
	}
}
