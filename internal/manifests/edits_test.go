package manifests

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
)

type editStore struct {
	Store
	current *Revision
	reads   int
	writes  int
}

func (s *editStore) CurrentRevision(context.Context, string, string) (*Revision, error) {
	s.reads++
	if s.reads == 1 {
		return &Revision{Revision: 1, YAML: "version: 1\nservices:\n  api:\n    source: go:cmd/api\n    provider: cloud-run\n"}, nil
	}
	return s.current, nil
}
func (s *editStore) CreateRevision(_ context.Context, r *Revision) error {
	s.writes++
	s.current = r
	return nil
}

type editAuth struct{}

func (editAuth) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

type editProjects struct{}

func (editProjects) GetProject(context.Context, string, string) (*projects.Project, error) {
	return &projects.Project{}, nil
}

func TestEdit_refusesToOverwriteANewerRevision(t *testing.T) {
	store := &editStore{current: &Revision{Revision: 2, YAML: "# Saved by another editor\nversion: 1\n"}}
	svc := NewService(store, editProjects{}, editAuth{}, nil)
	provider := "ecs-fargate"
	_, _, err := svc.Edit(context.Background(), security.Principal{}, "p", EditInput{Placement: &manifest.Placement{
		Section: "services", Name: "api", Environment: "preview", Provider: &provider,
	}})
	if !errors.Is(err, ErrConflict) || store.writes != 0 || store.current.Revision != 2 {
		t.Fatalf("overwrote another editor's revision: err=%v writes=%d revision=%d", err, store.writes, store.current.Revision)
	}
}

func TestEditAddResourcePassesCatalogFields(t *testing.T) {
	store := &editStore{}
	store.current = &Revision{Revision: 1}
	svc := NewService(store, editProjects{}, editAuth{}, nil)
	endpoint := "endpoint_01jbz7q5mx8vh3k2r9t4w6y0ae"
	on := true
	in := EditInput{AddResource: &NewResource{
		Name: "gateway", Kind: "sample", Provider: "server", Target: "conn_1", Version: "1",
		Uses: map[string]manifest.Use{
			"postgres": {External: endpoint},
			"falkordb": {Enabled: &on},
		},
	}}
	rev, _, err := svc.Edit(context.Background(), security.Principal{}, "p", in)
	if err != nil {
		t.Fatal(err)
	}
	got := rev.Manifest.Resources["gateway"]
	want := manifest.Resource{
		Kind: "sample", Provider: "server", Target: "conn_1", Version: "1",
		Uses: in.AddResource.Uses,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stored %+v, want %+v\n%s", got, want, rev.YAML)
	}
	if rev.Manifest.Services["api"].Provider != "cloud-run" {
		t.Errorf("lost the existing service:\n%s", rev.YAML)
	}
}

func TestEditLinkUnlinkSetUse(t *testing.T) {
	const base = "version: 1\nservices:\n  api:\n    source: go:cmd/api\n    needs: [db]\nresources:\n  db:\n    kind: postgres\n  fabriq:\n    kind: sample\n  cache:\n    kind: redis\n"
	edit := func(t *testing.T, yaml string, in EditInput) (*Revision, error) {
		t.Helper()
		store := &editStore{current: &Revision{Revision: 1, YAML: yaml}}
		store.reads = 1
		svc := NewService(store, editProjects{}, editAuth{}, nil)
		rev, _, err := svc.Edit(context.Background(), security.Principal{}, "p", in)
		return rev, err
	}

	t.Run("link", func(t *testing.T) {
		rev, err := edit(t, base, EditInput{Link: &ServiceLink{Service: " api ", Need: "fabriq", Options: map[string]string{"tenant": "acme"}}})
		if err != nil {
			t.Fatal(err)
		}
		api := rev.Manifest.Services["api"]
		if !reflect.DeepEqual(api.Needs, []string{"db", "fabriq"}) || api.NeedOptions["fabriq"]["tenant"] != "acme" {
			t.Fatalf("stored %+v\n%s", api, rev.YAML)
		}
	})
	t.Run("unlink", func(t *testing.T) {
		linked := strings.Replace(base, "needs: [db]", "needs: [db, fabriq]\n    need_options:\n      fabriq: {tenant: acme}", 1)
		rev, err := edit(t, linked, EditInput{Unlink: &ServiceLink{Service: "api", Need: "fabriq"}})
		if err != nil {
			t.Fatal(err)
		}
		api := rev.Manifest.Services["api"]
		if !reflect.DeepEqual(api.Needs, []string{"db"}) || api.NeedOptions != nil {
			t.Fatalf("stored %+v\n%s", api, rev.YAML)
		}
	})
	t.Run("set use", func(t *testing.T) {
		rev, err := edit(t, base, EditInput{SetUse: &ResourceUse{Resource: "fabriq", Dependency: "redis", Use: manifest.Use{Resource: "cache"}}})
		if err != nil {
			t.Fatal(err)
		}
		if got := rev.Manifest.Resources["fabriq"].Uses["redis"].Resource; got != "cache" {
			t.Fatalf("uses %+v\n%s", rev.Manifest.Resources["fabriq"].Uses, rev.YAML)
		}
	})
	t.Run("two at once", func(t *testing.T) {
		link := &ServiceLink{Service: "api", Need: "fabriq"}
		unlink := &ServiceLink{Service: "api", Need: "db"}
		use := &ResourceUse{Resource: "fabriq", Dependency: "redis", Use: manifest.Use{Resource: "cache"}}
		for name, in := range map[string]EditInput{
			"link and unlink":     {Link: link, Unlink: unlink},
			"link and set use":    {Link: link, SetUse: use},
			"unlink and set use":  {Unlink: unlink, SetUse: use},
			"link and ignore":     {Link: link, Ignore: "kafka"},
			"set use and add one": {SetUse: use, AddResource: &NewResource{Name: "x", Kind: "redis"}},
		} {
			_, err := edit(t, base, in)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "make one edit at a time") {
				t.Errorf("%s: got %v", name, err)
			}
		}
	})
	t.Run("a bad option is an invalid request", func(t *testing.T) {
		_, err := edit(t, base, EditInput{Link: &ServiceLink{Service: "api", Need: "fabriq", Options: map[string]string{"Tenant": "x"}}})
		if !errors.Is(err, manifest.ErrInvalid) {
			t.Fatalf("got %v", err)
		}
	})
}
