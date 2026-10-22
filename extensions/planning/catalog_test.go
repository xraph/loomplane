package planning

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
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

type dialCredentials map[string]*connections.Connection

func (c dialCredentials) Credential(_ context.Context, orgID, id string) (*connections.Connection, []byte, error) {
	conn, ok := c[id]
	if !ok || conn.OrgID != orgID {
		return nil, nil, connections.ErrNotFound
	}
	return conn, nil, nil
}

func TestEndpointChecksDialFromTheConnectionsTarget(t *testing.T) {
	creds := dialCredentials{
		"conn_cluster": {ID: "conn_cluster", OrgID: "org", Name: "prod", Cloud: connections.Kubernetes, Status: connections.StatusReady},
		"conn_gcp":     {ID: "conn_gcp", OrgID: "org", Name: "gcp", Cloud: "gcp", Status: connections.StatusReady},
	}
	d := ownedDialer{credentials: creds, targets: map[string]*deployments.OwnedTarget{
		connections.Server:     {Cloud: connections.Server, Credentials: creds},
		connections.Kubernetes: {Cloud: connections.Kubernetes, Credentials: creds},
	}}
	dial, closer, err := d.TargetDial(t.Context(), "org", "conn_cluster")
	if err != nil || dial == nil || closer == nil {
		t.Fatalf("cluster: %v", err)
	}
	if _, _, err = d.TargetDial(t.Context(), "org", "conn_gcp"); err == nil || !strings.Contains(err.Error(), "gcp is a gcp connection") {
		t.Fatalf("gcp: %v", err)
	}
	if _, _, err = d.TargetDial(t.Context(), "org", "conn_gone"); !errors.Is(err, connections.ErrNotFound) {
		t.Fatalf("missing connection: %v", err)
	}
}

// secretManagers lets only the "admin" user manage secrets.
type secretManagers struct{}

func (secretManagers) Require(_ context.Context, p security.Principal, perm security.Permission, _ string) error {
	if perm == security.SecretManage && p.UserID != "admin" {
		return security.ErrForbidden
	}
	return nil
}

// checkDialer hands out a dial that names the target it's for.
type checkDialer struct {
	clouds map[string]string
	asked  []string
	closed int
}

func (d *checkDialer) TargetDial(_ context.Context, orgID, connectionID string) (catalogconnect.DialFunc, io.Closer, error) {
	if _, ok := d.clouds[connectionID]; !ok || orgID != "org" {
		return nil, nil, connections.ErrNotFound
	}
	d.asked = append(d.asked, connectionID)
	dial := func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("through " + connectionID)
	}
	return dial, closer(func() error { d.closed++; return nil }), nil
}

func (d *checkDialer) TargetCloud(_ context.Context, _, connectionID string) (string, error) {
	return d.clouds[connectionID], nil
}

type closer func() error

func (c closer) Close() error { return c() }

// recordingProber records what it's asked and dials once.
type recordingProber struct {
	reqs   []recipes.Requirement
	dsns   []string
	dialed []string
	report catalogconnect.Report
}

func (p *recordingProber) Probe(ctx context.Context, dial catalogconnect.DialFunc, dsn string, req recipes.Requirement) (*catalogconnect.Report, error) {
	p.reqs = append(p.reqs, req)
	p.dsns = append(p.dsns, dsn)
	_, err := dial(ctx, "tcp", "db.example:5432")
	p.dialed = append(p.dialed, err.Error())
	report := p.report
	return &report, nil
}

func TestCheckRouteResolvesTheRequirement(t *testing.T) {
	prober := &recordingProber{report: catalogconnect.Report{Version: "16.4"}}
	svc := catalogconnect.NewService(&endpointStore{rows: map[string]catalogconnect.Endpoint{}}, endpointSecrets{}, secretManagers{})
	dialer := &checkDialer{clouds: map[string]string{"conn_server": connections.Server, "conn_cluster": connections.Kubernetes}}
	ext := &Extension{
		endpoints:      svc,
		endpointChecks: &catalogconnect.Checker{Endpoints: svc, Probers: map[string]catalogconnect.Prober{"postgres": prober}},
		endpointDialer: dialer,
		catalogKinds:   recipestest.Registry(t),
	}
	r := forge.NewRouter()
	if err := ext.routes(r, nil); err != nil {
		t.Fatal(err)
	}
	const dsn = "postgres://u:hunter2-Secret@db.example:5432/app?sslmode=require"
	body := func(kind, dependency, target string) string {
		return `{"engine":"postgres","dsn":"` + dsn + `","target":"` + target + `","kind":"` + kind + `","dependency":"` + dependency + `"}`
	}
	send := func(user, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/catalog/endpoints/checks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if user != "" {
			req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: user}))
		}
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		if strings.Contains(out.Body.String(), "hunter2") {
			t.Fatalf("the response shows the DSN: %s", out.Body.String())
		}
		return out
	}
	result := func(out *httptest.ResponseRecorder) catalogconnect.CheckResult {
		t.Helper()
		var got catalogconnect.CheckResult
		if out.Code != http.StatusOK {
			t.Fatalf("check: %d %s", out.Code, out.Body.String())
		}
		if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	got := result(send("admin", body("sample", "postgres", "conn_server")))
	if !got.OK || got.Version != "16.4" || len(got.Problems) != 0 || len(got.Notes) != 0 {
		t.Fatalf("result: %+v", got)
	}
	want := *recipestest.Sample().Dependencies[0].External
	if len(prober.reqs) != 1 || !reflect.DeepEqual(prober.reqs[0], want) || prober.dsns[0] != dsn {
		t.Fatalf("probed %+v", prober.reqs)
	}
	if !slices.Equal(dialer.asked, []string{"conn_server"}) || !slices.Equal(prober.dialed, []string{"through conn_server"}) || dialer.closed != 1 {
		t.Fatalf("dialed %v through %v, closed %d", dialer.asked, prober.dialed, dialer.closed)
	}

	// A cluster's check runs from Loomplane, and says so.
	got = result(send("admin", body("sample", "postgres", "conn_cluster")))
	if !got.OK || !slices.Equal(got.Notes, []string{"Checked from Loomplane, not from inside the cluster."}) {
		t.Fatalf("cluster result: %+v", got)
	}

	// What the endpoint lacks fails the check.
	prober.report = catalogconnect.Report{Version: "15.2", Problems: []string{"PostgreSQL 15.2 is older than 16"}}
	if got = result(send("admin", body("sample", "postgres", "conn_server"))); got.OK || !slices.Equal(got.Problems, prober.report.Problems) {
		t.Fatalf("old endpoint: %+v", got)
	}
	// A Redis DSN for a Postgres dependency fails without a probe.
	probes := len(prober.reqs)
	out := send("admin", strings.Replace(body("sample", "postgres", "conn_server"), `"engine":"postgres"`, `"engine":"redis"`, 1))
	if got = result(out); got.OK || !slices.Equal(got.Problems, []string{"this is a Redis endpoint, and Sample's postgres dependency needs PostgreSQL"}) || len(prober.reqs) != probes {
		t.Fatalf("engine mismatch: %+v", got)
	}

	for _, tc := range []struct {
		name, user, body string
		code             int
	}{
		{"anonymous", "", body("sample", "postgres", "conn_server"), http.StatusUnauthorized},
		{"no SecretManage", "viewer", body("sample", "postgres", "conn_server"), http.StatusForbidden},
		{"dependency takes no external endpoint", "admin", body("sample", "cache", "conn_server"), http.StatusUnprocessableEntity},
		{"unknown dependency", "admin", body("sample", "search", "conn_server"), http.StatusUnprocessableEntity},
		{"unknown kind", "admin", body("nope", "postgres", "conn_server"), http.StatusUnprocessableEntity},
		{"no target", "admin", body("sample", "postgres", ""), http.StatusUnprocessableEntity},
		{"unknown target", "admin", body("sample", "postgres", "conn_gone"), http.StatusNotFound},
		{"unreadable DSN", "admin", strings.Replace(body("sample", "postgres", "conn_server"), "?sslmode=require", "", 1), http.StatusUnprocessableEntity},
		{"not JSON", "admin", "not json", http.StatusBadRequest},
	} {
		probes = len(prober.reqs)
		if out = send(tc.user, tc.body); out.Code != tc.code || len(prober.reqs) != probes {
			t.Fatalf("%s: %d %s (probes %d)", tc.name, out.Code, out.Body.String(), len(prober.reqs)-probes)
		}
	}
}
