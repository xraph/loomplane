package manifests

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
)

// addStore starts from one revision, or from none, and keeps what's saved.
type addStore struct {
	Store
	current *Revision
	writes  int
}

func (s *addStore) CurrentRevision(context.Context, string, string) (*Revision, error) {
	if s.current == nil {
		return nil, ErrNotFound
	}
	return s.current, nil
}

func (s *addStore) CreateRevision(_ context.Context, r *Revision) error {
	if r.expectedRevision != nil && (s.current == nil && *r.expectedRevision != 0 || s.current != nil && *r.expectedRevision != s.current.Revision) {
		return ErrConflict
	}
	s.writes++
	if s.current != nil {
		r.Revision = s.current.Revision + 1
	} else {
		r.Revision = 1
	}
	s.current = r
	return nil
}

const appYAML = `version: 1
# The API everyone calls.
services:
  api:
    source: go:cmd/api
    provider: cloud-run
resources:
  db:
    kind: postgres
    provider: cloud-sql
`

func addEdit(t *testing.T, start string, in EditInput) (*Revision, *addStore, error) {
	t.Helper()
	store := &addStore{}
	if start != "" {
		store.current = &Revision{Revision: 1, YAML: start}
	}
	svc := NewService(store, editProjects{}, editAuth{}, nil)
	rev, _, err := svc.Edit(context.Background(), security.Principal{}, "p", in)
	return rev, store, err
}

func TestAddServiceWritesItWithItsNeeds(t *testing.T) {
	public := false
	rev, _, err := addEdit(t, appYAML, EditInput{AddService: &NewServiceInput{
		Name: "worker", Source: "go:cmd/worker", Kind: "worker", Provider: "cloud-run",
		Public: &public, Needs: []string{"db", "api"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := rev.Manifest.Services["worker"]
	if got.Source != "go:cmd/worker" || got.Kind != "worker" || got.Provider != "cloud-run" ||
		got.Public == nil || *got.Public || !slices.Equal(got.Needs, []string{"db", "api"}) {
		t.Fatalf("worker = %+v\n%s", got, rev.YAML)
	}
	if !strings.Contains(rev.YAML, "# The API everyone calls.") {
		t.Errorf("lost the YAML's comment:\n%s", rev.YAML)
	}
}

func TestAddServiceRefusesWhatIsntThere(t *testing.T) {
	for name, in := range map[string]*NewServiceInput{
		"a taken name":            {Name: "db", Source: "cmd/db"},
		"a bad name":              {Name: "Worker", Source: "cmd/worker"},
		"no source":               {Name: "worker"},
		"an unknown need":         {Name: "worker", Source: "cmd/worker", Needs: []string{"cache"}},
		"an unknown env":          {Name: "worker", Source: "cmd/worker", EnabledIn: "qa"},
		"only without env":        {Name: "worker", Source: "cmd/worker", Only: true},
		"an unknown service":      {Name: "worker", Source: "cmd/worker", NeededBy: []string{"web"}},
		"a target and a provider": {Name: "worker", Source: "cmd/worker", CloudTarget: "eu-run", Provider: "cloud-run"},
	} {
		t.Run(name, func(t *testing.T) {
			_, store, err := addEdit(t, appYAML, EditInput{AddService: in})
			if err == nil || store.writes != 0 {
				t.Fatalf("err=%v writes=%d", err, store.writes)
			}
		})
	}
}

// Only keeps the new entry out of every other environment, and needed_by
// wires it to the services that use it, in one revision.
func TestAddResourceOnlyInOneEnvironment(t *testing.T) {
	const start = "version: 1\nservices:\n  api:\n    source: go:cmd/api\n    environments:\n      production:\n        enabled: false\n"
	rev, store, err := addEdit(t, start, EditInput{AddResource: &NewResource{
		Name: "cache", Kind: "redis", EnabledIn: "staging", Only: true, NeededBy: []string{"api"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if store.writes != 1 {
		t.Fatalf("writes = %d, want one revision", store.writes)
	}
	cache := rev.Manifest.Resources["cache"]
	if on := cache.Environments["staging"].Enabled; on == nil || !*on {
		t.Errorf("cache isn't on in staging:\n%s", rev.YAML)
	}
	if on := cache.Environments["production"].Enabled; on == nil || *on {
		t.Errorf("cache isn't off in production:\n%s", rev.YAML)
	}
	if !slices.Contains(rev.Manifest.Services["api"].Needs, "cache") {
		t.Errorf("api doesn't need cache:\n%s", rev.YAML)
	}
}

// A service that runs in production can't be made to need something
// that's only in staging: the plan for production would stop on it.
func TestAddRefusesANeedThatIsOffWhereTheServiceRuns(t *testing.T) {
	_, store, err := addEdit(t, appYAML, EditInput{AddResource: &NewResource{
		Name: "cache", Kind: "redis", EnabledIn: "staging", Only: true, NeededBy: []string{"api"},
	}})
	if !errors.Is(err, ErrInvalid) || store.writes != 0 {
		t.Fatalf("err=%v writes=%d", err, store.writes)
	}
	if !strings.Contains(err.Error(), "api runs in production and needs cache") {
		t.Errorf("the refusal doesn't say what's wrong: %v", err)
	}

	// A service only in staging may need what's only in staging.
	if _, _, err := addEdit(t, appYAML, EditInput{AddService: &NewServiceInput{
		Name: "worker", Source: "cmd/worker", Needs: []string{"db"}, EnabledIn: "staging", Only: true,
	}}); err != nil {
		t.Fatalf("a staging-only service needing an everywhere resource: %v", err)
	}
}

// The first thing added to a project without a manifest starts one.
func TestAddStartsAManifest(t *testing.T) {
	rev, store, err := addEdit(t, "", EditInput{AddService: &NewServiceInput{Name: "web", Source: "apps/web", EnabledIn: "production"}})
	if err != nil {
		t.Fatal(err)
	}
	if store.writes != 1 || rev.Revision != 1 || rev.Manifest.Version != manifest.Version || rev.Manifest.Services["web"].Source != "apps/web" {
		t.Fatalf("rev=%+v writes=%d\n%s", rev, store.writes, rev.YAML)
	}
	// Anything but an add still needs a manifest.
	if _, _, err := addEdit(t, "", EditInput{Ignore: "kafka"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ignore without a manifest: %v", err)
	}
}

// A dry run shows the YAML an add would save, and saves nothing.
func TestAddDryRunSavesNothing(t *testing.T) {
	rev, store, err := addEdit(t, appYAML, EditInput{DryRun: true, AddService: &NewServiceInput{
		Name: "worker", Source: "go:cmd/worker", Needs: []string{"db"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if store.writes != 0 || store.current.Revision != 1 {
		t.Fatalf("a dry run saved: writes=%d", store.writes)
	}
	if rev.ID != "" || rev.Revision != 2 || rev.Manifest.Services["worker"].Source != "go:cmd/worker" || !strings.Contains(rev.YAML, "worker:") {
		t.Fatalf("rev=%+v\n%s", rev, rev.YAML)
	}
}

func TestAddServiceOnACloudTarget(t *testing.T) {
	rev, _, err := addEdit(t, appYAML, EditInput{AddService: &NewServiceInput{
		Name: "trainer", Source: "python:trainer", Cloud: "GPU Lab", CloudTarget: "a100",
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := rev.Manifest.Services["trainer"]
	if got.Cloud != "GPU Lab" || got.CloudTarget != "a100" || got.Provider != "" {
		t.Fatalf("trainer = %+v\n%s", got, rev.YAML)
	}
	rev, _, err = addEdit(t, appYAML, EditInput{AddService: &NewServiceInput{
		Name: "web", Source: "node:web", CloudTarget: "frontend",
	}})
	if err != nil || rev.Manifest.Services["web"].CloudTarget != "frontend" || rev.Manifest.Services["web"].Cloud != "" {
		t.Fatalf("web on the default Cloud: %v\n%s", err, rev.YAML)
	}
}

func TestAddResourceWithConsumerOptions(t *testing.T) {
	rev, store, err := addEdit(t, appYAML, EditInput{AddResource: &NewResource{Name: "fabriq", Kind: "fabriq", Uses: map[string]manifest.Use{"postgres": {Resource: "shared-db"}}, Attachments: map[string]DependencyAttachment{"postgres": {Kind: "postgres", ResourceID: "providerresource_" + strings.Repeat("a", 64)}}, NeededBy: []string{"api"}, ConsumerOptions: map[string]string{"tenant": "wakflo"}}})
	if err != nil {
		t.Fatal(err)
	}
	if store.writes != 1 || rev.Manifest.Resources["shared-db"].ExistingResource == "" || rev.Manifest.Services["api"].NeedOptions["fabriq"]["tenant"] != "wakflo" {
		t.Fatal(rev.YAML)
	}
}

// A workspace service attached in production alone leaves a service that
// runs everywhere without it in staging, which the plan warns of, rather
// than refusing it: attachments listed in one environment are off in the
// rest without saying so.
func TestAddAttachesAWorkspaceServiceInOneEnvironment(t *testing.T) {
	const service = "endpoint_01jd8x5k2m3n4p5q6r7s8t9v0w"
	rev, store, err := addEdit(t, appYAML, EditInput{AddResource: &NewResource{
		Name: "orders-db", Kind: "postgres", ExistingResource: service, EnabledIn: "production", NeededBy: []string{"api"},
	}})
	if err != nil || store.writes != 1 {
		t.Fatalf("err=%v writes=%d", err, store.writes)
	}
	orders := rev.Manifest.Resources["orders-db"]
	if orders.ExistingResource != service || len(orders.Environments) != 1 {
		t.Fatalf("attachment:\n%s", rev.YAML)
	}
	if on := orders.Environments["production"].Enabled; on == nil || !*on {
		t.Fatalf("not on in production:\n%s", rev.YAML)
	}
	if !slices.Contains(rev.Manifest.Services["api"].Needs, "orders-db") {
		t.Fatalf("api doesn't need it:\n%s", rev.YAML)
	}
}

// Adding a resource can wire each service that needs it, in the same
// revision, and a later edit rewires one.
func TestAddResourceWiresItsConsumers(t *testing.T) {
	rev, store, err := addEdit(t, appYAML, EditInput{AddResource: &NewResource{
		Name: "orders", Kind: "postgres", NeededBy: []string{"api"},
		Wiring: map[string]map[string]string{"api": {"PGHOST": "host", "PGPASSWORD": "password"}},
	}})
	if err != nil || store.writes != 1 {
		t.Fatalf("err=%v writes=%d", err, store.writes)
	}
	if got := rev.Manifest.Services["api"].Wiring["orders"].Env; got["PGHOST"] != "host" || got["PGPASSWORD"] != "password" || len(got) != 2 {
		t.Fatalf("wiring:\n%s", rev.YAML)
	}
	if _, _, err = addEdit(t, appYAML, EditInput{AddResource: &NewResource{
		Name: "orders", Kind: "postgres", NeededBy: []string{"api"},
		Wiring: map[string]map[string]string{"worker": {"DB": "url"}},
	}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wired a service that doesn't need it: %v", err)
	}

	store.current = rev
	svc := NewService(store, editProjects{}, editAuth{}, nil)
	next, _, err := svc.Edit(context.Background(), security.Principal{}, "p", EditInput{Wire: &ServiceWiring{Service: "api", Need: "orders", Env: map[string]string{"DATABASE_DSN": "url"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := next.Manifest.Services["api"].Wiring["orders"].Env; len(got) != 1 || got["DATABASE_DSN"] != "url" {
		t.Fatalf("rewired:\n%s", next.YAML)
	}
	cleared, _, err := svc.Edit(context.Background(), security.Principal{}, "p", EditInput{Wire: &ServiceWiring{Service: "api", Need: "orders"}})
	if err != nil || cleared.Manifest.Services["api"].Wiring != nil {
		t.Fatalf("cleared: %v\n%s", err, cleared.YAML)
	}
}
