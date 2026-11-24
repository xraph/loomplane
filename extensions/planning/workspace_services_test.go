package planning

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
)

// workspaceStore adds a workspace service's extras to endpointStore.
type workspaceStore struct{ *endpointStore }

var _ catalogconnect.WorkspaceStore = workspaceStore{}

func (s workspaceStore) change(orgID, id string, f func(*catalogconnect.Endpoint)) error {
	e, ok := s.rows[id]
	if !ok || e.OrgID != orgID {
		return catalogconnect.ErrNotFound
	}
	f(&e)
	s.rows[id] = e
	return nil
}

func (s workspaceStore) Update(_ context.Context, orgID, id string, in catalogconnect.Details, _ time.Time) (*catalogconnect.Endpoint, error) {
	err := s.change(orgID, id, func(e *catalogconnect.Endpoint) {
		e.Name, e.Description, e.EnvironmentLabel = in.Name, in.Description, in.EnvironmentLabel
	})
	if err != nil {
		return nil, err
	}
	e := s.rows[id]
	return &e, nil
}

func (s workspaceStore) Replace(_ context.Context, e *catalogconnect.Endpoint, from int, _ time.Time) error {
	if s.rows[e.ID].Revision != from {
		return catalogconnect.ErrConflict
	}
	return s.change(e.OrgID, e.ID, func(row *catalogconnect.Endpoint) {
		row.Host, row.Port, row.Database, row.Username, row.TLS = e.Host, e.Port, e.Database, e.Username, e.TLS
		row.Revision++
	})
}

func (s workspaceStore) SetSharing(_ context.Context, orgID, id string, restricted bool, projects []string, _ string, _ time.Time) error {
	return s.change(orgID, id, func(e *catalogconnect.Endpoint) { e.Restricted, e.Projects = restricted, projects })
}

func (s workspaceStore) SetAdmin(_ context.Context, orgID, id string, set bool, _ time.Time) error {
	return s.change(orgID, id, func(e *catalogconnect.Endpoint) { e.AdminSet = set })
}

func (s workspaceStore) SaveCheck(_ context.Context, orgID, id string, c *catalogconnect.LastCheck) error {
	return s.change(orgID, id, func(e *catalogconnect.Endpoint) { saved := *c; e.LastCheck = &saved })
}

func (s workspaceStore) Uses(context.Context, string, string) ([]catalogconnect.Use, error) {
	return []catalogconnect.Use{{ProjectID: "project_a", Project: "shop", Environment: "production", Resource: "orders"}}, nil
}

const (
	workspaceDSN = "postgres://app:hunter2-Secret@db.example:5432/orders?sslmode=require"
	rotatedDSN   = "postgres://app:new-Secret@db.example:5432/orders?sslmode=require"
)

func workspaceRoutes(t *testing.T) (forge.Router, *recordingProber, *workspaceStore, endpointSecrets, *endpointAudit) {
	t.Helper()
	prober := &recordingProber{report: catalogconnect.Report{Version: "16.4", Capabilities: &catalogconnect.Capabilities{CreateDatabase: true}}}
	store := &workspaceStore{&endpointStore{rows: map[string]catalogconnect.Endpoint{}}}
	secrets := endpointSecrets{}
	audit := &endpointAudit{}
	svc := catalogconnect.NewService(store, secrets, secretManagers{})
	svc.Audit = audit
	svc.ProjectsIn = func(_ context.Context, _ string, ids []string) ([]string, error) {
		var missing []string
		for _, id := range ids {
			if id != "project_a" && id != "project_b" {
				missing = append(missing, id)
			}
		}
		return missing, nil
	}
	ext := &Extension{
		endpoints:      svc,
		endpointChecks: &catalogconnect.Checker{Endpoints: svc, Probers: map[string]catalogconnect.Prober{"postgres": prober}},
		endpointDialer: &checkDialer{clouds: map[string]string{"conn_server": connections.Server, "conn_cluster": "kubernetes"}},
	}
	r := forge.NewRouter()
	if err := ext.routes(r, nil); err != nil {
		t.Fatal(err)
	}
	return r, prober, store, secrets, audit
}

func call(t *testing.T, r forge.Router, user, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: user}))
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	return out
}

func TestWorkspaceServiceRoutesRegisterOnlyWhatChecksOut(t *testing.T) {
	r, prober, store, secrets, audit := workspaceRoutes(t)

	// A probe says what the credentials may do, and keeps nothing.
	out := call(t, r, "admin", http.MethodPost, "/api/v1/catalog/endpoints/probes", `{"engine":"postgres","dsn":"`+workspaceDSN+`","target":"conn_server"}`)
	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"create_database":true`) {
		t.Fatalf("probe: %d %s", out.Code, out.Body.String())
	}
	if len(store.rows) != 0 || len(secrets) != 0 {
		t.Fatal("a probe stored something")
	}
	if prober.dialed[0] != "through conn_server" {
		t.Fatalf("dialed: %v", prober.dialed)
	}
	if out = call(t, r, "admin", http.MethodPost, "/api/v1/catalog/endpoints/probes", `{"engine":"postgres","dsn":"`+workspaceDSN+`","target":""}`); out.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a probe from nowhere: %d %s", out.Code, out.Body.String())
	}
	if out = call(t, r, "viewer", http.MethodPost, "/api/v1/catalog/endpoints/probes", `{"engine":"postgres","dsn":"`+workspaceDSN+`","target":"conn_server"}`); out.Code != http.StatusForbidden {
		t.Fatalf("a viewer's probe: %d", out.Code)
	}

	// Registering checks again, and refuses what fails.
	register := `{"name":"orders","description":"Orders database","environment_label":"production","engine":"postgres","dsn":"` + workspaceDSN + `","target":"conn_server","restricted":true,"projects":["project_a"]}`
	prober.report = catalogconnect.Report{Version: "16.4", Problems: []string{"db.example:5432 refused the password"}}
	if out = call(t, r, "admin", http.MethodPost, "/api/v1/catalog/endpoints/registrations", register); out.Code != http.StatusUnprocessableEntity || !strings.Contains(out.Body.String(), "refused the password") {
		t.Fatalf("a failing registration: %d %s", out.Code, out.Body.String())
	}
	if len(store.rows) != 0 {
		t.Fatal("a failing registration was stored")
	}
	prober.report = catalogconnect.Report{Version: "16.4", Capabilities: &catalogconnect.Capabilities{CreateDatabase: true}}
	if out = call(t, r, "admin", http.MethodPost, "/api/v1/catalog/endpoints/registrations", strings.Replace(register, `"project_a"`, `"project_z"`, 1)); out.Code != http.StatusUnprocessableEntity {
		t.Fatalf("shared with a stranger project: %d %s", out.Code, out.Body.String())
	}
	out = call(t, r, "admin", http.MethodPost, "/api/v1/catalog/endpoints/registrations", register)
	if out.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", out.Code, out.Body.String())
	}
	var e catalogconnect.Endpoint
	if err := json.Unmarshal(out.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if e.Name != "orders" || !e.Restricted || !slices.Equal(e.Projects, []string{"project_a"}) || e.LastCheck == nil ||
		e.LastCheck.Target != "conn_server" || !e.LastCheck.Capabilities.CreateDatabase {
		t.Fatalf("registered: %+v", e)
	}
	id := e.ID

	// Rotating needs the revision that was read, and a passing check.
	if out = call(t, r, "admin", http.MethodPut, "/api/v1/catalog/endpoints/"+id+"/credential", `{"dsn":"`+rotatedDSN+`","target":"conn_server","revision":7}`); out.Code != http.StatusConflict {
		t.Fatalf("a stale rotation: %d %s", out.Code, out.Body.String())
	}
	if out = call(t, r, "admin", http.MethodPut, "/api/v1/catalog/endpoints/"+id+"/credential", `{"dsn":"`+rotatedDSN+`","target":"conn_server","revision":1}`); out.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", out.Code, out.Body.String())
	}
	if string(secrets[catalogconnect.DSNKey("org", id)]) != rotatedDSN || store.rows[id].Revision != 2 {
		t.Fatalf("rotated: vault %q, revision %d", secrets[catalogconnect.DSNKey("org", id)], store.rows[id].Revision)
	}

	// Sharing, details, uses and a recheck.
	if out = call(t, r, "admin", http.MethodPut, "/api/v1/catalog/endpoints/"+id+"/sharing", `{"restricted":false}`); out.Code != http.StatusOK || store.rows[id].Restricted {
		t.Fatalf("share with every project: %d %s", out.Code, out.Body.String())
	}
	if out = call(t, r, "developer", http.MethodPut, "/api/v1/catalog/endpoints/"+id+"/sharing", `{"restricted":true}`); out.Code != http.StatusForbidden {
		t.Fatalf("a developer's sharing: %d", out.Code)
	}
	if out = call(t, r, "admin", http.MethodPatch, "/api/v1/catalog/endpoints/"+id, `{"name":"orders-db","description":"Orders","environment_label":"staging"}`); out.Code != http.StatusOK || store.rows[id].Name != "orders-db" {
		t.Fatalf("update: %d %s", out.Code, out.Body.String())
	}
	if out = call(t, r, "viewer", http.MethodGet, "/api/v1/catalog/endpoints/"+id+"/uses", ""); out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"project":"shop"`) {
		t.Fatalf("uses: %d %s", out.Code, out.Body.String())
	}
	if out = call(t, r, "viewer", http.MethodGet, "/api/v1/catalog/endpoints/"+id, ""); out.Code != http.StatusOK {
		t.Fatalf("get: %d %s", out.Code, out.Body.String())
	}
	prober.report = catalogconnect.Report{Problems: []string{"no answer within 10s"}}
	if out = call(t, r, "admin", http.MethodPost, "/api/v1/catalog/endpoints/"+id+"/checks", `{"target":"conn_server"}`); out.Code != http.StatusOK {
		t.Fatalf("recheck: %d %s", out.Code, out.Body.String())
	}
	if check := store.rows[id].LastCheck; check == nil || check.OK || !slices.Equal(check.Problems, []string{"no answer within 10s"}) {
		t.Fatalf("a failing recheck is kept: %+v", check)
	}

	// The admin credential must reach the same server.
	prober.report = catalogconnect.Report{Version: "16.4", Capabilities: &catalogconnect.Capabilities{CreateDatabase: true, CreateUser: true}}
	if out = call(t, r, "admin", http.MethodPut, "/api/v1/catalog/endpoints/"+id+"/admin", `{"dsn":"postgres://root:x@elsewhere.example:5432/postgres?sslmode=require","target":"conn_server"}`); out.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an admin credential for another server: %d %s", out.Code, out.Body.String())
	}
	if out = call(t, r, "admin", http.MethodPut, "/api/v1/catalog/endpoints/"+id+"/admin", `{"dsn":"postgres://root:x@db.example:5432/postgres?sslmode=require","target":"conn_server"}`); out.Code != http.StatusOK || !store.rows[id].AdminSet {
		t.Fatalf("admin: %d %s", out.Code, out.Body.String())
	}
	if out = call(t, r, "admin", http.MethodDelete, "/api/v1/catalog/endpoints/"+id+"/admin", ""); out.Code != http.StatusOK || store.rows[id].AdminSet {
		t.Fatalf("clear admin: %d %s", out.Code, out.Body.String())
	}

	// No answer and no audit record ever holds a password.
	for _, a := range audit.events {
		if strings.Contains(a.Address, "Secret") || strings.Contains(a.Address, "@") {
			t.Fatalf("audited a credential: %+v", a)
		}
	}
	if !slices.ContainsFunc(audit.events, func(a catalogconnect.AuditEvent) bool { return a.Action == catalogconnect.AuditShare }) ||
		!slices.ContainsFunc(audit.events, func(a catalogconnect.AuditEvent) bool { return a.Action == catalogconnect.AuditReplace }) {
		t.Fatalf("audit: %+v", audit.events)
	}
}

func TestWorkspaceServiceRoutesLeakNoCredentials(t *testing.T) {
	r, _, _, _, _ := workspaceRoutes(t)
	out := call(t, r, "admin", http.MethodPost, "/api/v1/catalog/endpoints/registrations",
		`{"name":"orders","engine":"postgres","dsn":"`+workspaceDSN+`","target":"conn_server"}`)
	if out.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", out.Code, out.Body.String())
	}
	var e catalogconnect.Endpoint
	if err := json.Unmarshal(out.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/catalog/endpoints", "/api/v1/catalog/endpoints/" + e.ID} {
		body := call(t, r, "admin", http.MethodGet, path, "").Body.String()
		if strings.Contains(body, "hunter2") || strings.Contains(out.Body.String(), "hunter2") {
			t.Fatalf("%s leaked the password: %s", path, body)
		}
	}
}

func TestAProviderResourcesEndpointsStayItsOwn(t *testing.T) {
	store := &workspaceStore{&endpointStore{rows: map[string]catalogconnect.Endpoint{}}}
	svc := catalogconnect.NewService(store, endpointSecrets{}, secretManagers{})
	e, err := resourceEndpoints{svc}.Create(t.Context(), security.Principal{OrgID: "org", UserID: "admin"},
		catalogconnect.CreateInput{Name: "resource-x-1", Engine: "postgres", DSN: workspaceDSN})
	if err != nil {
		t.Fatal(err)
	}
	if e.Origin != catalogconnect.OriginProvider || e.Registered() {
		t.Fatalf("origin: %q", e.Origin)
	}
}
