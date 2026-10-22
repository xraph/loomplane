package planning

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
	"github.com/xraph/loomplane/internal/security"
)

func TestCatalogKindsRouteListsKinds(t *testing.T) {
	r := forge.NewRouter()
	if err := (&Extension{}).routes(r, nil); err != nil {
		t.Fatal(err)
	}
	for _, authenticated := range []bool{false, true} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/catalog/kinds", nil)
		if authenticated {
			req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "member"}))
		}
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		if !authenticated {
			if out.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous: %d %s", out.Code, out.Body.String())
			}
			continue
		}
		if out.Code != http.StatusOK {
			t.Fatalf("authenticated: %d %s", out.Code, out.Body.String())
		}
		var list CatalogKindList
		if err := json.Unmarshal(out.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Kinds) < 5 {
			t.Fatalf("kinds: %+v", list.Kinds)
		}
		first := list.Kinds[0]
		if first.Name != "postgres" || !first.Engine || first.Placement != recipes.PlacementServiceProject {
			t.Fatalf("first kind: %+v", first)
		}
	}
}

func TestCatalogKindListIncludesRegisteredKinds(t *testing.T) {
	list := catalogKindList(recipestest.Registry(t))
	last := list.Kinds[len(list.Kinds)-1]
	if last.Name != "sample" || last.Engine || last.Placement != recipes.PlacementAppProject {
		t.Fatalf("last kind: %+v", last)
	}
	if len(list.Kinds) != 6 || list.Kinds[0].Name != "postgres" {
		t.Fatalf("kinds: %+v", list.Kinds)
	}
}

type allowAll struct{}

func (allowAll) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

// endpointStore keeps endpoints in memory; an ID in inUse is held by a
// binding.
type endpointStore struct {
	rows  map[string]catalogconnect.Endpoint
	inUse map[string]bool
}

func (s *endpointStore) Create(_ context.Context, e *catalogconnect.Endpoint) error {
	s.rows[e.ID] = *e
	return nil
}

func (s *endpointStore) Get(_ context.Context, orgID, id string) (*catalogconnect.Endpoint, error) {
	e, ok := s.rows[id]
	if !ok || e.OrgID != orgID {
		return nil, catalogconnect.ErrNotFound
	}
	return &e, nil
}

func (s *endpointStore) List(_ context.Context, orgID string) ([]*catalogconnect.Endpoint, error) {
	out := []*catalogconnect.Endpoint{}
	for _, e := range s.rows {
		if e.OrgID == orgID {
			out = append(out, &e)
		}
	}
	return out, nil
}

func (s *endpointStore) Delete(ctx context.Context, orgID, id string) error {
	if _, err := s.Get(ctx, orgID, id); err != nil {
		return err
	}
	if s.inUse[id] {
		return catalogconnect.ErrInUse
	}
	delete(s.rows, id)
	return nil
}

type endpointSecrets map[string][]byte

func (s endpointSecrets) Put(_ context.Context, key string, value []byte) error {
	s[key] = value
	return nil
}

func (s endpointSecrets) Get(_ context.Context, key string) ([]byte, error) { return s[key], nil }

func (s endpointSecrets) Delete(_ context.Context, key string) error {
	delete(s, key)
	return nil
}

func TestCatalogEndpointRoutes(t *testing.T) {
	store := &endpointStore{rows: map[string]catalogconnect.Endpoint{}, inUse: map[string]bool{}}
	secrets := endpointSecrets{}
	r := forge.NewRouter()
	if err := (&Extension{endpoints: catalogconnect.NewService(store, secrets, allowAll{})}).routes(r, nil); err != nil {
		t.Fatal(err)
	}
	send := func(method, path, body string, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), method, "/api/v1"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if authenticated {
			req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "admin"}))
		}
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out
	}
	if out := send(http.MethodGet, "/catalog/endpoints", "", false); out.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list: %d %s", out.Code, out.Body.String())
	}
	const dsn = "postgres://u:hunter2@db.example:5432/app?sslmode=require"
	out := send(http.MethodPost, "/catalog/endpoints", `{"name":"prod-pg","engine":"postgres","dsn":"`+dsn+`"}`, true)
	if out.Code != http.StatusCreated || strings.Contains(out.Body.String(), "hunter2") {
		t.Fatalf("create: %d %s", out.Code, out.Body.String())
	}
	var created catalogconnect.Endpoint
	if err := json.Unmarshal(out.Body.Bytes(), &created); err != nil || created.Host != "db.example" || created.TLS != "require" {
		t.Fatalf("created: %+v %v", created, err)
	}
	if string(secrets[catalogconnect.DSNKey("org", created.ID)]) != dsn {
		t.Fatalf("vault: %q", secrets)
	}
	if out = send(http.MethodPost, "/catalog/endpoints", `{"name":"bad","engine":"postgres","dsn":"mysql://u:hunter2@db.example/app"}`, true); out.Code != http.StatusUnprocessableEntity || strings.Contains(out.Body.String(), "hunter2") {
		t.Fatalf("bad DSN: %d %s", out.Code, out.Body.String())
	}
	if out = send(http.MethodPost, "/catalog/endpoints", `not json`, true); out.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d %s", out.Code, out.Body.String())
	}
	out = send(http.MethodGet, "/catalog/endpoints", "", true)
	var list EndpointList
	if err := json.Unmarshal(out.Body.Bytes(), &list); out.Code != http.StatusOK || err != nil || len(list.Endpoints) != 1 || list.Endpoints[0].ID != created.ID {
		t.Fatalf("list: %d %s", out.Code, out.Body.String())
	}
	store.inUse[created.ID] = true
	if out = send(http.MethodDelete, "/catalog/endpoints/"+created.ID, "", true); out.Code != http.StatusConflict {
		t.Fatalf("delete in use: %d %s", out.Code, out.Body.String())
	}
	store.inUse[created.ID] = false
	if out = send(http.MethodDelete, "/catalog/endpoints/"+created.ID, "", true); out.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", out.Code, out.Body.String())
	}
	if out = send(http.MethodDelete, "/catalog/endpoints/"+created.ID, "", true); out.Code != http.StatusNotFound {
		t.Fatalf("delete again: %d %s", out.Code, out.Body.String())
	}
}
