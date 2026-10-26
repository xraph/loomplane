package azure

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/azure/azuretest"
)

// canaryApp is a container app on the fake that runs one revision,
// api--1, in single revision mode, with a secret and a registry login.
func canaryApp(t *testing.T) (*azuretest.Fake, *Session, string) {
	t.Helper()
	f := azuretest.New(t)
	s := fakeSession(t, f)
	app := pullApp(t, s)
	app.Properties.Configuration = Configuration{
		ActiveRevisionsMode: SingleRevision,
		Ingress:             &Ingress{External: true, TargetPort: 8080, Transport: "auto", Traffic: Latest()},
		Registries:          []AppRegistry{{Server: "registry.azurecr.io", Username: "registry", PasswordSecretRef: "registry-password"}},
		Secrets:             []Secret{{Name: "registry-password", Value: "registry-pw"}, {Name: "env-token-abc123", Value: "s3cret"}},
	}
	app.Properties.Template.Containers[0].Env = []EnvVar{{Name: "TOKEN", SecretRef: "env-token-abc123"}, {Name: "MODE", Value: "slow"}}
	id := putAndWait(t, s, app)
	return f, s, id
}

func putAndWait(t *testing.T, s *Session, app App) string {
	t.Helper()
	id, err := s.PutApp(t.Context(), "api", app)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WaitApp(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	return id
}

// candidateApp gives the app a second revision, api--c1, beside api--1,
// which keeps all of the traffic.
func candidateApp(t *testing.T, s *Session, id, image string) {
	t.Helper()
	app := pullApp(t, s)
	app.Properties.Configuration = Configuration{
		ActiveRevisionsMode: MultipleRevisions,
		Ingress:             &Ingress{External: true, TargetPort: 8080, Transport: "auto", Traffic: []TrafficWeight{{RevisionName: "api--1", Weight: 100}}},
		Registries:          []AppRegistry{{Server: "registry.azurecr.io", Username: "registry", PasswordSecretRef: "registry-password"}},
		Secrets:             []Secret{{Name: "registry-password", Value: "registry-pw"}, {Name: "env-token-abc123", Value: "s3cret"}},
	}
	app.Properties.Template.Containers[0].Image = image
	app.Properties.Template.RevisionSuffix = "c1"
	if err := s.SetTraffic(t.Context(), id, MultipleRevisions, []TrafficWeight{{RevisionName: "api--1", Weight: 100}}); err != nil {
		t.Fatal(err)
	}
	putAndWait(t, s, app)
}

func TestSetTrafficPatchesOnlyIngress(t *testing.T) {
	f, s, id := canaryApp(t)
	candidateApp(t, s, id, "registry.azurecr.io/api:2")
	if err := s.SetTraffic(t.Context(), id, MultipleRevisions, []TrafficWeight{{RevisionName: "api--1", Weight: 75}, {RevisionName: "api--c1", Weight: 25}}); err != nil {
		t.Fatal(err)
	}
	f.Lock()
	patch := f.AppPatches[len(f.AppPatches)-1]
	f.Unlock()
	props := patch["properties"].(map[string]any)
	if keys := slices.Sorted(maps.Keys(props)); !slices.Equal(keys, []string{"configuration"}) {
		t.Fatalf("the PATCH sets properties %v", keys)
	}
	conf := props["configuration"].(map[string]any)
	if keys := slices.Sorted(maps.Keys(conf)); !slices.Equal(keys, []string{"activeRevisionsMode", "ingress"}) {
		t.Fatalf("the PATCH sets configuration %v", keys)
	}
	ingress := conf["ingress"].(map[string]any)
	if conf["activeRevisionsMode"] != "Multiple" || ingress["external"] != true || ingress["targetPort"] != float64(8080) || ingress["transport"] != "auto" {
		t.Fatalf("configuration %v", conf)
	}
	if _, has := ingress["fqdn"]; has {
		t.Fatalf("the PATCH sends the read-only fqdn: %v", ingress)
	}
	if split := f.Split("api"); !maps.Equal(split, map[string]int{"api--1": 75, "api--c1": 25}) {
		t.Fatalf("split %v", split)
	}
	// The secrets, which the PATCH never named, keep their values.
	if secrets := f.Secrets("api"); secrets["env-token-abc123"] != "s3cret" || secrets["registry-password"] != "registry-pw" {
		t.Fatalf("secrets %v", secrets)
	}
}

func TestSetTrafficRefusesASplitThatDoesntAddUp(t *testing.T) {
	for _, traffic := range [][]TrafficWeight{
		nil,
		{{RevisionName: "a", Weight: 60}, {RevisionName: "b", Weight: 30}},
		{{RevisionName: "a", Weight: 120}, {RevisionName: "b", Weight: -20}},
		{{Weight: 100}},
		{{RevisionName: "a", Weight: 50}, {RevisionName: "a", Weight: 50}},
		{{RevisionName: "a", LatestRevision: true, Weight: 100}},
	} {
		if err := CheckTraffic(traffic); err == nil {
			t.Errorf("%+v passed", traffic)
		}
	}
	for _, traffic := range [][]TrafficWeight{
		Latest(),
		{{RevisionName: "a", Weight: 100}},
		{{RevisionName: "a", Weight: 95}, {RevisionName: "b", Weight: 5}},
	} {
		if err := CheckTraffic(traffic); err != nil {
			t.Errorf("%+v: %v", traffic, err)
		}
	}
}

// Container Apps can report an app whose newest revision won't run as
// failed after any change, a traffic-only one included. What counts is
// whether the split is in place.
func TestSetTrafficLooksAtTheSplitWhenTheAppFailed(t *testing.T) {
	f, s, id := canaryApp(t)
	f.FailImage = "registry.azurecr.io/api:bad"
	app := pullApp(t, s)
	app.Properties.Configuration = Configuration{
		ActiveRevisionsMode: MultipleRevisions,
		Ingress:             &Ingress{External: true, TargetPort: 8080, Traffic: []TrafficWeight{{RevisionName: "api--1", Weight: 100}}},
	}
	app.Properties.Template.Containers[0].Image = f.FailImage
	app.Properties.Template.RevisionSuffix = "bad"
	if err := s.SetTraffic(t.Context(), id, MultipleRevisions, []TrafficWeight{{RevisionName: "api--1", Weight: 100}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutApp(t.Context(), "api", app); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WaitApp(t.Context(), id); err == nil {
		t.Fatal("a crashing revision started")
	}
	if err := s.SetTraffic(t.Context(), id, MultipleRevisions, []TrafficWeight{{RevisionName: "api--1", Weight: 100}}); err != nil {
		t.Fatalf("the split is in place, but: %v", err)
	}
	// A split Azure refused is an error.
	if err := s.SetTraffic(t.Context(), id, MultipleRevisions, []TrafficWeight{{RevisionName: "api--1", Weight: 50}, {RevisionName: "api--bad", Weight: 50}}); err == nil {
		t.Fatal("traffic went to a revision that crashed")
	}
}

func TestActiveRevisionsAndDeactivate(t *testing.T) {
	f, s, id := canaryApp(t)
	candidateApp(t, s, id, "registry.azurecr.io/api:2")
	active, err := s.ActiveRevisions(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(active))
	for _, rev := range active {
		names = append(names, rev.Name)
	}
	if !slices.Equal(names, []string{"api--1", "api--c1"}) || active[0].Properties.TrafficWeight != 100 || !active[1].Up() {
		t.Fatalf("active %+v", active)
	}
	// A revision with traffic can't go; one without can, twice over, and
	// one that's gone already is gone.
	if err := s.DeactivateRevision(t.Context(), id, "api--1"); err == nil {
		t.Fatal("deactivated the revision with all of the traffic")
	}
	for range 2 {
		if err := s.DeactivateRevision(t.Context(), id, "api--c1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeactivateRevision(t.Context(), id, "api--gone"); err != nil {
		t.Fatal(err)
	}
	if got := f.ActiveRevisions("api"); !slices.Equal(got, []string{"api--1"}) {
		t.Fatalf("active %v", got)
	}
	f.Lock()
	defer f.Unlock()
	if !slices.Equal(f.Deactivations, []string{"api--c1"}) {
		t.Fatalf("deactivations %v", f.Deactivations)
	}
}

func TestRestoreTemplatePatchesOnlyTheTemplate(t *testing.T) {
	f, s, id := canaryApp(t)
	candidateApp(t, s, id, "registry.azurecr.io/api:2")
	if err := s.RestoreTemplate(t.Context(), id, "api--1", "r1"); err != nil {
		t.Fatal(err)
	}
	f.Lock()
	patch := f.AppPatches[len(f.AppPatches)-1]
	f.Unlock()
	props := patch["properties"].(map[string]any)
	if keys := slices.Sorted(maps.Keys(props)); !slices.Equal(keys, []string{"template"}) {
		t.Fatalf("the PATCH sets properties %v", keys)
	}
	revs := f.Revisions("api")
	if r := revs["api--r1"]; r.Image != "registry.azurecr.io/api:1" || !r.Active || r.Template["revisionSuffix"] != "r1" {
		t.Fatalf("restored revision %+v", r)
	}
	// It takes none of the traffic, which stays pinned.
	if split := f.Split("api"); !maps.Equal(split, map[string]int{"api--1": 100}) {
		t.Fatalf("split %v", split)
	}
	app, err := s.GetApp(t.Context(), id)
	if err != nil || app.Image() != "registry.azurecr.io/api:1" {
		t.Fatalf("app %+v, %v", app, err)
	}
	// Again, it changes nothing and makes no revision. Another template
	// can't take the suffix.
	if err := s.RestoreTemplate(t.Context(), id, "api--1", "r1"); err != nil {
		t.Fatal(err)
	}
	if n := len(f.Revisions("api")); n != 3 {
		t.Fatalf("%d revisions", n)
	}
	if err := s.RestoreTemplate(t.Context(), id, "api--c1", "r1"); err == nil {
		t.Fatal("two templates took one suffix")
	}
}

func TestListSecretsReadsTheValues(t *testing.T) {
	_, s, id := canaryApp(t)
	app, err := s.GetApp(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, sec := range app.Properties.Configuration.Secrets {
		if sec.Value != "" {
			t.Fatalf("GET showed secret %s's value", sec.Name)
		}
	}
	secrets, err := s.ListSecrets(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, sec := range secrets {
		got[sec.Name] = sec.Value
	}
	if !maps.Equal(got, map[string]string{"registry-password": "registry-pw", "env-token-abc123": "s3cret"}) {
		t.Fatalf("secrets %v", got)
	}
}

func TestRevisionUpAndWhy(t *testing.T) {
	rev := func(active bool, provisioning, running, health string) *Revision {
		r := &Revision{Name: "api--1"}
		r.Properties.Active, r.Properties.ProvisioningState, r.Properties.RunningState, r.Properties.HealthState = active, provisioning, running, health
		return r
	}
	if r := rev(true, "Provisioned", "Running", "Healthy"); !r.Up() || r.Why() != "" {
		t.Fatalf("a running revision: up %v, why %q", r.Up(), r.Why())
	}
	for what, r := range map[string]*Revision{
		"it's gone":         nil,
		"it isn't active":   rev(false, "Provisioned", "Running", "Healthy"),
		"provisioning":      rev(true, "Provisioning", "Processing", "None"),
		"failed":            rev(true, "Provisioned", "Failed", "Unhealthy"),
		"unhealthy":         rev(true, "Provisioned", "Running", "Unhealthy"),
		"stopped":           rev(true, "Provisioned", "Stopped", "None"),
		"degraded":          rev(true, "Provisioned", "Degraded", "Healthy"),
		"provisioning fail": rev(true, "Failed", "Failed", "None"),
	} {
		if r.Up() || r.Why() == "" {
			t.Errorf("%s: up %v, why %q", what, r.Up(), r.Why())
		}
	}
	if why := (*Revision)(nil).Why(); why != "it's gone" {
		t.Fatalf("why %q", why)
	}
}

func TestRevisionRunsComparesTheWholeTemplate(t *testing.T) {
	release := Template{
		Containers: []Container{{Name: "main", Image: "r.azurecr.io/api:2", Resources: &Resources{CPU: 0.5, Memory: "1Gi"},
			Env: []EnvVar{{Name: "MODE", Value: "fast"}, {Name: "TOKEN", SecretRef: "env-token-abc123-c1"}}}},
		Scale: &Scale{MinReplicas: 1, MaxReplicas: 3},
	}
	running := func(change func(*Template)) *Revision {
		r := &Revision{}
		r.Properties.Template = Template{
			Containers: []Container{{Name: "main", Image: "r.azurecr.io/api:2", Resources: &Resources{CPU: 0.5, Memory: "1Gi"},
				// Azure may list the variables in another order.
				Env: []EnvVar{{Name: "TOKEN", SecretRef: "env-token-abc123-c1"}, {Name: "MODE", Value: "fast"}}}},
			Scale:          &Scale{MinReplicas: 1, MaxReplicas: 3},
			RevisionSuffix: "c1",
		}
		if change != nil {
			change(&r.Properties.Template)
		}
		return r
	}
	if !running(nil).Runs(release) {
		t.Fatal("the same template doesn't run the release")
	}
	for what, change := range map[string]func(*Template){
		"image":       func(t *Template) { t.Containers[0].Image = "r.azurecr.io/api:1" },
		"plain env":   func(t *Template) { t.Containers[0].Env[1].Value = "slow" },
		"secret ref":  func(t *Template) { t.Containers[0].Env[0].SecretRef = "env-token-abc123" },
		"extra env":   func(t *Template) { t.Containers[0].Env = append(t.Containers[0].Env, EnvVar{Name: "X", Value: "1"}) },
		"cpu":         func(t *Template) { t.Containers[0].Resources.CPU = 1 },
		"memory":      func(t *Template) { t.Containers[0].Resources.Memory = "2Gi" },
		"scale":       func(t *Template) { t.Scale.MaxReplicas = 10 },
		"no scale":    func(t *Template) { t.Scale = nil },
		"container":   func(t *Template) { t.Containers[0].Name = "other" },
		"a second":    func(t *Template) { t.Containers = append(t.Containers, Container{Name: "sidecar", Image: "x"}) },
		"no resource": func(t *Template) { t.Containers[0].Resources = nil },
	} {
		if running(change).Runs(release) {
			t.Errorf("a revision with another %s runs the release", what)
		}
	}
	if (*Revision)(nil).Runs(release) {
		t.Fatal("a revision that's gone runs the release")
	}
}

func TestCanaryActionsAreAskedApart(t *testing.T) {
	f, s, _ := canaryApp(t)
	missing, err := s.MissingOf(t.Context(), CanaryActions)
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing %v, %v", missing, err)
	}
	f.Lock()
	f.NotActions = append(f.NotActions, "Microsoft.App/containerApps/revisions/deactivate/action")
	f.Unlock()
	missing, err = s.MissingOf(t.Context(), CanaryActions)
	if err != nil || !slices.Equal(missing, []string{"Microsoft.App/containerApps/revisions/deactivate/action"}) {
		t.Fatalf("missing %v, %v", missing, err)
	}
	// What every deploy needs doesn't include them.
	for _, a := range CanaryActions {
		if slices.ContainsFunc(Actions, func(b string) bool { return strings.EqualFold(a, b) }) {
			t.Fatalf("%s is in Actions", a)
		}
	}
}
