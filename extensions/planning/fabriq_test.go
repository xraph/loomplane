package planning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/fabriqloom"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// downFabriq is a hosted Fabriq whose console can't be reached.
type downFabriq struct{}

func (downFabriq) Components(_ context.Context, _ security.Principal, _ security.Permission, ref deployments.ResourceRef) ([]*planning.Binding, error) {
	if ref.Resource != "fabriq" {
		return nil, deployments.ErrComponentNotFound
	}
	return []*planning.Binding{{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ResourceSubject, Name: "fabriq",
		Config: []byte(`{"provider":"server","kind":"fabriq","composition":{"kind":"fabriq","version":"1.9","revision":"1"}}`)}}, nil
}

func (downFabriq) Channel(context.Context, security.Principal, security.Permission, deployments.ComponentRef) (*deployments.ComponentChannel, error) {
	return nil, errors.New("dial lp-x:8081: connection refused")
}

func (downFabriq) RunJob(context.Context, string, deployments.ResourceRef, string, string) (owned.JobResult, error) {
	return owned.JobResult{}, errors.New("not here")
}

func TestTenantsUnavailableIs503WithReason(t *testing.T) {
	r := forge.NewRouter()
	e := &Extension{fabriq: &fabriqloom.Service{Access: downFabriq{}}}
	if err := e.routes(r, nil); err != nil {
		t.Fatal(err)
	}
	send := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), method, "/api/v1/projects/proj/environments/env/fabriq/"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "admin"}))
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out
	}
	for _, rt := range []struct{ method, path, body string }{
		{http.MethodGet, "fabriq/tenants", ""},
		{http.MethodPost, "fabriq/tenants", `{"tenant_id":"umbrella"}`},
		{http.MethodGet, "fabriq/keys", ""},
	} {
		out := send(rt.method, rt.path, rt.body)
		var body struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil || out.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: %d %s", rt.method, rt.path, out.Code, out.Body.String())
		}
		if body.Error != "Fabriq isn't reachable right now: dial lp-x:8081: connection refused" || strings.Contains(out.Body.String(), "fq_") {
			t.Fatalf("%s %s: %s", rt.method, rt.path, out.Body.String())
		}
	}
	// Status still answers, so the panel can draw the stack from bindings.
	out := send(http.MethodGet, "fabriq", "")
	var st fabriqloom.Status
	if err := json.Unmarshal(out.Body.Bytes(), &st); err != nil || out.Code != http.StatusOK || st.Reachable || st.Reason != "dial lp-x:8081: connection refused" || st.Kind != "fabriq" {
		t.Fatalf("status: %d %s", out.Code, out.Body.String())
	}
	if out = send(http.MethodGet, "nope", ""); out.Code != http.StatusNotFound {
		t.Fatalf("another resource: %d %s", out.Code, out.Body.String())
	}
	if out = send(http.MethodPost, "fabriq/tenants", `{"tenant_id":"Umbrella"}`); out.Code != http.StatusUnprocessableEntity || !strings.Contains(out.Body.String(), "Use lowercase letters, digits and dashes.") {
		t.Fatalf("invalid tenant: %d %s", out.Code, out.Body.String())
	}
	if out = send(http.MethodPost, "fabriq/rebuilds", `{"projection":"graph","request_id":"request-0001"}`); out.Code != http.StatusUnprocessableEntity || !strings.Contains(out.Body.String(), "FalkorDB is off for this Fabriq") {
		t.Fatalf("rebuild with falkordb off: %d %s", out.Code, out.Body.String())
	}
}

// A key change that lost its lock to another is a conflict you can retry,
// and says so.
func TestBusyFabriqKeyIs409(t *testing.T) {
	r := forge.NewRouter()
	busy := fmt.Errorf("another change to api's key: %w", fabriqloom.ErrBusy)
	if err := r.POST("/busy", func(c forge.Context) error { return failFabriq(c, busy) }); err != nil {
		t.Fatal(err)
	}
	out := httptest.NewRecorder()
	r.ServeHTTP(out, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/busy", nil))
	if out.Code != http.StatusConflict || !strings.Contains(out.Body.String(), busy.Error()) {
		t.Fatalf("busy: %d %s", out.Code, out.Body.String())
	}
}
