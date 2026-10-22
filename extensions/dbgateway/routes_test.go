package dbgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/dbgateway/plane"
	"github.com/xraph/loomplane/internal/security"
)

// fakeGateways stands in for *dbgateway.Service. A request verifies when
// its Loomplane-Gateway header is "dbgw_1".
type fakeGateways struct {
	mu         sync.Mutex
	configured bool
	version    uint64 // the latest snapshot version; 0 is none
	polls      int
	verifyErr  error
	reportErr  error
	reports    []plane.Report
	holds      []plane.Label
	bodies     [][]byte
}

func (f *fakeGateways) Configured() bool { return f.configured }

func (f *fakeGateways) VerifyRequest(r *http.Request, body []byte, _ time.Time) (*dbgateway.Gateway, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies = append(f.bodies, body)
	if f.verifyErr != nil {
		return nil, f.verifyErr
	}
	if r.Header.Get("Loomplane-Gateway") != "dbgw_1" {
		return nil, dbgateway.ErrUnauthorized
	}
	return &dbgateway.Gateway{OrgID: "org_1", ID: "dbgw_1"}, nil
}

func (f *fakeGateways) Snapshot(_ context.Context, gw *dbgateway.Gateway, after uint64) (*plane.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.polls++
	if after >= math.MaxInt64 {
		return nil, dbgateway.ErrInvalid
	}
	if f.version <= after {
		return nil, nil
	}
	return &plane.Snapshot{GatewayID: gw.ID, Version: f.version, Routes: []plane.Route{}, Signature: []byte("sig")}, nil
}

func (f *fakeGateways) setVersion(v uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.version = v
}

func (f *fakeGateways) Report(_ context.Context, _ *dbgateway.Gateway, r plane.Report) (plane.ReportResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reportErr != nil {
		return plane.ReportResponse{}, f.reportErr
	}
	f.reports = append(f.reports, r)
	return plane.ReportResponse{LiveInstances: 2}, nil
}

func (f *fakeGateways) Hold(_ context.Context, _ *dbgateway.Gateway, l plane.Label) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.holds = append(f.holds, l)
	return dbgateway.ErrNoHoldHandler
}

// gatewayRouter mounts the gateway routes over f, polling every 10ms for at
// most wait.
func gatewayRouter(t *testing.T, f *fakeGateways, wait time.Duration) (forge.Router, *[]string) {
	t.Helper()
	var logged []string
	var mu sync.Mutex
	h := &handlers{
		svc: f, wait: wait, every: 10 * time.Millisecond, now: time.Now,
		warn: func(msg string, err error) {
			mu.Lock()
			defer mu.Unlock()
			logged = append(logged, msg+": "+err.Error())
		},
	}
	r := forge.NewRouter()
	if err := mount(r, h); err != nil {
		t.Fatal(err)
	}
	return r, &logged
}

func call(t *testing.T, r forge.Router, method, uri, gateway string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, uri, bytes.NewReader(body))
	if gateway != "" {
		req.Header.Set("Loomplane-Gateway", gateway)
	}
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	return out
}

func errorOf(t *testing.T, out *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q isn't {\"error\": ...}: %v", out.Body.String(), err)
	}
	return body.Error
}

func TestSnapshotLongPoll(t *testing.T) {
	f := &fakeGateways{configured: true, version: 3}
	r, _ := gatewayRouter(t, f, 50*time.Millisecond)

	// Nothing newer than 3: the poll waits out its time, then says so.
	start := time.Now()
	out := call(t, r, http.MethodGet, "/api/v1/db-gateways/dbgw_1/snapshot?after=3", "dbgw_1", nil)
	if out.Code != http.StatusNoContent || out.Body.Len() != 0 {
		t.Fatalf("got %d %q, want 204", out.Code, out.Body.String())
	}
	if waited := time.Since(start); waited < 50*time.Millisecond {
		t.Fatalf("answered after %s, before the 50ms wait", waited)
	}
	if f.polls < 2 {
		t.Fatalf("checked %d times during the wait", f.polls)
	}

	// A change during the wait answers with the new version.
	go func() {
		time.Sleep(15 * time.Millisecond)
		f.setVersion(4)
	}()
	long, _ := gatewayRouter(t, f, 5*time.Second)
	start = time.Now()
	out = call(t, long, http.MethodGet, "/api/v1/db-gateways/dbgw_1/snapshot?after=3", "dbgw_1", nil)
	if out.Code != http.StatusOK {
		t.Fatalf("got %d %q, want 200", out.Code, out.Body.String())
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the change didn't end the wait")
	}
	var s plane.Snapshot
	if err := json.Unmarshal(out.Body.Bytes(), &s); err != nil || s.Version != 4 || s.GatewayID != "dbgw_1" || string(s.Signature) != "sig" {
		t.Fatalf("snapshot %+v: %v", s, err)
	}
	if ct := out.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type %q", ct)
	}

	// No after is after 0: the first poll gets the current snapshot now.
	if out = call(t, r, http.MethodGet, "/api/v1/db-gateways/dbgw_1/snapshot", "dbgw_1", nil); out.Code != http.StatusOK {
		t.Fatalf("first poll: %d", out.Code)
	}
	if out = call(t, r, http.MethodGet, "/api/v1/db-gateways/dbgw_1/snapshot?after=soon", "dbgw_1", nil); out.Code != http.StatusBadRequest {
		t.Fatalf("a bad after: %d", out.Code)
	}
	if out = call(t, r, http.MethodGet, "/api/v1/db-gateways/dbgw_1/snapshot?after=18446744073709551615", "dbgw_1", nil); out.Code != http.StatusBadRequest {
		t.Fatalf("an after the service refuses: %d", out.Code)
	}
}

func TestReportRoute(t *testing.T) {
	f := &fakeGateways{configured: true}
	r, _ := gatewayRouter(t, f, time.Second)
	body := []byte(`{"instance_id":"inst-1","snapshot_version":4,"draining":true,"routes":[]}`)
	out := call(t, r, http.MethodPost, "/api/v1/db-gateways/dbgw_1/report", "dbgw_1", body)
	if out.Code != http.StatusOK {
		t.Fatalf("got %d %q", out.Code, out.Body.String())
	}
	var resp plane.ReportResponse
	if err := json.Unmarshal(out.Body.Bytes(), &resp); err != nil || resp.LiveInstances != 2 {
		t.Fatalf("response %q: %v", out.Body.String(), err)
	}
	if len(f.reports) != 1 || f.reports[0].InstanceID != "inst-1" || f.reports[0].SnapshotVersion != 4 || !f.reports[0].Draining {
		t.Fatalf("reported %+v", f.reports)
	}
	// The bytes that were verified are the bytes that were decoded.
	if len(f.bodies) != 1 || !bytes.Equal(f.bodies[0], body) {
		t.Fatalf("verified %q", f.bodies)
	}
	if out = call(t, r, http.MethodPost, "/api/v1/db-gateways/dbgw_1/report", "dbgw_1", []byte(`{"instance_id":`)); out.Code != http.StatusBadRequest {
		t.Fatalf("a malformed report: %d", out.Code)
	}
	f.reportErr = dbgateway.ErrInvalid
	if out = call(t, r, http.MethodPost, "/api/v1/db-gateways/dbgw_1/report", "dbgw_1", []byte(`{}`)); out.Code != http.StatusBadRequest {
		t.Fatalf("a report the store refuses: %d", out.Code)
	}
}

func TestHoldPhase1(t *testing.T) {
	f := &fakeGateways{configured: true}
	r, _ := gatewayRouter(t, f, time.Second)
	out := call(t, r, http.MethodPost, "/api/v1/db-gateways/dbgw_1/hold", "dbgw_1", []byte(`{"label":"orders--dev"}`))
	if out.Code != http.StatusNotFound || errorOf(t, out) != "no hold handler" {
		t.Fatalf("got %d %q, want 404 no hold handler", out.Code, out.Body.String())
	}
	if len(f.holds) != 1 || f.holds[0] != (plane.Label{DB: "orders", Branch: "dev"}) {
		t.Fatalf("held %+v", f.holds)
	}
	if out = call(t, r, http.MethodPost, "/api/v1/db-gateways/dbgw_1/hold", "dbgw_1", []byte(`{"label":"Not A Label"}`)); out.Code != http.StatusBadRequest {
		t.Fatalf("a bad label: %d", out.Code)
	}
}

func TestGatewayRoutesNeedASignature(t *testing.T) {
	f := &fakeGateways{configured: true, version: 1}
	r, _ := gatewayRouter(t, f, time.Second)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/db-gateways/dbgw_1/snapshot"},
		{http.MethodPost, "/api/v1/db-gateways/dbgw_1/report"},
		{http.MethodPost, "/api/v1/db-gateways/dbgw_1/hold"},
	} {
		out := call(t, r, tc.method, tc.path, "dbgw_unknown", []byte(`{}`))
		if out.Code != http.StatusUnauthorized || errorOf(t, out) != dbgateway.ErrUnauthorized.Error() {
			t.Fatalf("%s %s: %d %q, want 401", tc.method, tc.path, out.Code, out.Body.String())
		}
	}
	if len(f.reports) != 0 || len(f.holds) != 0 || f.polls != 0 {
		t.Fatal("an unsigned request reached the service")
	}
	// A failure to read the gateway isn't a 401, and its cause stays in
	// the log.
	f.verifyErr = errors.New("database is down")
	r, logged := gatewayRouter(t, f, time.Second)
	out := call(t, r, http.MethodGet, "/api/v1/db-gateways/dbgw_1/snapshot", "dbgw_1", nil)
	if out.Code != http.StatusInternalServerError || strings.Contains(out.Body.String(), "database is down") {
		t.Fatalf("got %d %q", out.Code, out.Body.String())
	}
	if len(*logged) != 1 {
		t.Fatalf("logged %q", *logged)
	}
}

func TestGatewayRoutesWithoutASigningKey(t *testing.T) {
	f := &fakeGateways{configured: false}
	r, _ := gatewayRouter(t, f, time.Second)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/db-gateways/dbgw_1/snapshot"},
		{http.MethodPost, "/api/v1/db-gateways/dbgw_1/report"},
		{http.MethodPost, "/api/v1/db-gateways/dbgw_1/hold"},
	} {
		out := call(t, r, tc.method, tc.path, "dbgw_1", []byte(`{}`))
		if out.Code != http.StatusServiceUnavailable || errorOf(t, out) != "the database gateway signing key is not configured" {
			t.Fatalf("%s %s: %d %q, want 503", tc.method, tc.path, out.Code, out.Body.String())
		}
	}
}

func TestGatewayRoutesCapTheBody(t *testing.T) {
	f := &fakeGateways{configured: true}
	r, _ := gatewayRouter(t, f, time.Second)
	big := bytes.Repeat([]byte("a"), 1<<20+1)
	for _, path := range []string{"/api/v1/db-gateways/dbgw_1/report", "/api/v1/db-gateways/dbgw_1/hold"} {
		out := call(t, r, http.MethodPost, path, "dbgw_1", big)
		if out.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("%s: %d, want 413", path, out.Code)
		}
	}
	if len(f.bodies) != 0 {
		t.Fatal("an oversized body was verified")
	}
	exact := bytes.Repeat([]byte(" "), 1<<20-2)
	exact = append(append([]byte("{"), exact...), '}')
	if out := call(t, r, http.MethodPost, "/api/v1/db-gateways/dbgw_1/report", "dbgw_1", exact); out.Code != http.StatusOK {
		t.Fatalf("a 1 MiB body: %d %q", out.Code, out.Body.String())
	}
}

// fakeManagement stands in for the service's management methods, and
// remembers each call.
type fakeManagement struct {
	mu    sync.Mutex
	err   error
	list  []dbgateway.GatewayStatus
	calls []string
}

func (f *fakeManagement) ProjectGateways(_ context.Context, p security.Principal, project string) ([]dbgateway.GatewayStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "list "+project+" "+p.UserID)
	if f.err != nil {
		return nil, f.err
	}
	return f.list, nil
}

func (f *fakeManagement) SetRouteState(_ context.Context, p security.Principal, project, gatewayID, routeID, state string) (*dbgateway.RouteSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join([]string{"state", project, gatewayID, routeID, state, p.UserID}, " "))
	if f.err != nil {
		return nil, f.err
	}
	return &dbgateway.RouteSummary{ID: routeID, GatewayID: gatewayID, Resource: "orders", State: state}, nil
}

func managementRouter(t *testing.T, f *fakeManagement) forge.Router {
	t.Helper()
	r := forge.NewRouter()
	if err := mountManagement(r, nil, f); err != nil {
		t.Fatal(err)
	}
	return r
}

// callAs makes a request as user, or with no principal when user is "".
func callAs(t *testing.T, r forge.Router, method, uri, user string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, uri, nil)
	if user != "" {
		req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org_1", UserID: user}))
	}
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	return out
}

func TestListGatewaysRoute(t *testing.T) {
	seen := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	f := &fakeManagement{list: []dbgateway.GatewayStatus{{
		ID: "dbgw_1", EnvironmentID: "env_1", Name: "main", Provider: "cloud-run", Region: "us-central1", SnapshotVersion: 3,
		Routes: []dbgateway.RouteStatus{{ID: "dbgr_1", Resource: "orders", State: "active", Reported: true, Reachable: true, BackendConns: 7, MaxConnections: 100, PinRate: 0.25}},
		Instances: []dbgateway.InstanceStatus{{
			ID: "i-1", SnapshotVersion: 2, LastSeenAt: seen, Reporting: true, Behind: true,
			Routes: []dbgateway.InstanceRouteStats{{RouteID: "dbgr_1", BackendConns: 7, MaxConnections: 100, PinRate: 0.25, Reachable: true}},
		}},
	}}}
	r := managementRouter(t, f)
	out := callAs(t, r, http.MethodGet, "/api/v1/projects/proj_1/db-gateways", "usr_1")
	if out.Code != http.StatusOK {
		t.Fatalf("got %d %q", out.Code, out.Body.String())
	}
	var body struct {
		Gateways []struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			Provider        string `json:"provider"`
			Region          string `json:"region"`
			SnapshotVersion uint64 `json:"snapshot_version"`
			Routes          []struct {
				Resource     string  `json:"resource"`
				State        string  `json:"state"`
				Reachable    bool    `json:"reachable"`
				BackendConns int     `json:"backend_conns"`
				MaxConns     int     `json:"max_connections"`
				PinRate      float64 `json:"pin_rate"`
			} `json:"routes"`
			Instances []struct {
				SnapshotVersion uint64    `json:"snapshot_version"`
				LastSeenAt      time.Time `json:"last_seen_at"`
				Draining        bool      `json:"draining"`
				Behind          bool      `json:"behind"`
				Routes          []struct {
					RouteID string `json:"route_id"`
				} `json:"routes"`
			} `json:"instances"`
		} `json:"gateways"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Gateways) != 1 {
		t.Fatalf("body %s", out.Body.String())
	}
	g := body.Gateways[0]
	if g.ID != "dbgw_1" || g.Name != "main" || g.Provider != "cloud-run" || g.Region != "us-central1" || g.SnapshotVersion != 3 ||
		len(g.Routes) != 1 || g.Routes[0].Resource != "orders" || g.Routes[0].BackendConns != 7 || g.Routes[0].MaxConns != 100 ||
		g.Routes[0].PinRate != 0.25 || !g.Routes[0].Reachable || g.Routes[0].State != "active" ||
		len(g.Instances) != 1 || !g.Instances[0].Behind || g.Instances[0].SnapshotVersion != 2 || !g.Instances[0].LastSeenAt.Equal(seen) ||
		len(g.Instances[0].Routes) != 1 || g.Instances[0].Routes[0].RouteID != "dbgr_1" {
		t.Fatalf("body %s", out.Body.String())
	}
	if f.calls[0] != "list proj_1 usr_1" {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestPauseAndResumeRoutes(t *testing.T) {
	f := &fakeManagement{}
	r := managementRouter(t, f)
	for _, c := range []struct{ verb, state string }{{"pause", "paused"}, {"resume", "active"}} {
		out := callAs(t, r, http.MethodPost, "/api/v1/projects/proj_1/db-gateways/dbgw_1/routes/dbgr_1/"+c.verb, "usr_admin")
		if out.Code != http.StatusOK {
			t.Fatalf("%s: %d %q", c.verb, out.Code, out.Body.String())
		}
		var got dbgateway.RouteSummary
		if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || got != (dbgateway.RouteSummary{ID: "dbgr_1", GatewayID: "dbgw_1", Resource: "orders", State: c.state}) {
			t.Fatalf("%s answered %q: %v", c.verb, out.Body.String(), err)
		}
	}
	want := []string{"state proj_1 dbgw_1 dbgr_1 paused usr_admin", "state proj_1 dbgw_1 dbgr_1 active usr_admin"}
	if !slices.Equal(f.calls, want) {
		t.Fatalf("calls %v, want %v", f.calls, want)
	}
}

// The management routes answer the service's refusals with their status,
// and a request with no principal never reaches the service.
func TestManagementRouteErrors(t *testing.T) {
	paths := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/projects/proj_1/db-gateways"},
		{http.MethodPost, "/api/v1/projects/proj_1/db-gateways/dbgw_1/routes/dbgr_1/pause"},
		{http.MethodPost, "/api/v1/projects/proj_1/db-gateways/dbgw_1/routes/dbgr_1/resume"},
	}
	f := &fakeManagement{}
	r := managementRouter(t, f)
	for _, p := range paths {
		if out := callAs(t, r, p.method, p.path, ""); out.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s with no principal: %d", p.method, p.path, out.Code)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("calls %v", f.calls)
	}
	for _, c := range []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w (infrastructure:modify)", security.ErrForbidden), http.StatusForbidden},
		{fmt.Errorf("%w: route dbgr_1", dbgateway.ErrNotFound), http.StatusNotFound},
		{fmt.Errorf("%w: route state", dbgateway.ErrInvalid), http.StatusUnprocessableEntity},
	} {
		f.err = c.err
		for _, p := range paths {
			if out := callAs(t, r, p.method, p.path, "usr_viewer"); out.Code != c.want {
				t.Fatalf("%s %s on %v: %d, want %d", p.method, p.path, c.err, out.Code, c.want)
			}
		}
	}
}
