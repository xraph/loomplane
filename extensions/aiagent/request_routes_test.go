package aiagent

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/agentrequests"
	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/security"
)

// tenants is the agent's switch, as the binder sees it.
type tenants struct{ on bool }

func (t tenants) NexusTenant(context.Context, string) (string, error) {
	if !t.on {
		return "", agentsettings.ErrDisabled
	}
	return "tenant_1", nil
}

// fakeRequests answers every call with err, and records what it was given.
type fakeRequests struct {
	mu     sync.Mutex
	err    error
	calls  []string
	secret string
	filter agentrequests.ListFilter
}

func (f *fakeRequests) record(call string) (*agentrequests.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	if f.err != nil {
		return nil, f.err
	}
	return &agentrequests.Request{ID: "areq_1", Status: agentrequests.StatusAnswered}, nil
}

func (f *fakeRequests) List(_ context.Context, _ security.Principal, lf agentrequests.ListFilter) (*agentrequests.RequestList, error) {
	f.mu.Lock()
	f.filter = lf
	f.mu.Unlock()
	if _, err := f.record("list"); err != nil {
		return nil, err
	}
	return &agentrequests.RequestList{Requests: []*agentrequests.Request{}}, nil
}
func (f *fakeRequests) Get(_ context.Context, _ security.Principal, id string) (*agentrequests.Request, error) {
	return f.record("get " + id)
}
func (f *fakeRequests) Answer(_ context.Context, _ security.Principal, id string, in agentrequests.AnswerInput) (*agentrequests.Request, error) {
	return f.record("answer " + id + " " + in.Text)
}
func (f *fakeRequests) ProvideSecret(_ context.Context, _ security.Principal, id string, in agentrequests.SecretInput) (*agentrequests.Request, error) {
	f.mu.Lock()
	f.secret = in.Value
	f.mu.Unlock()
	return f.record("secret " + id)
}
func (f *fakeRequests) Complete(_ context.Context, _ security.Principal, id string) (*agentrequests.Request, error) {
	return f.record("complete " + id)
}
func (f *fakeRequests) Decline(_ context.Context, _ security.Principal, id string, in agentrequests.DeclineInput) (*agentrequests.Request, error) {
	return f.record("decline " + id + " " + in.Reason)
}

func requestRig(t *testing.T, on bool) (forge.Router, *fakeRequests) {
	t.Helper()
	r := forge.NewRouter()
	api := &fakeRequests{}
	if err := mountRequestRoutes(r, nil, &agentscope.Binder{Tenants: tenants{on: on}}, api); err != nil {
		t.Fatal(err)
	}
	return r, api
}

func TestRequestRoutesCallTheService(t *testing.T) {
	r, api := requestRig(t, true)
	ada := &security.Principal{OrgID: "org_1", UserID: "usr_ada"}
	for _, c := range []struct{ method, path, body, call string }{
		{http.MethodGet, "/api/v1/agent/requests/areq_1", "", "get areq_1"},
		{http.MethodPost, "/api/v1/agent/requests/areq_1/answer", `{"text":"blue"}`, "answer areq_1 blue"},
		{http.MethodPost, "/api/v1/agent/requests/areq_1/secret", `{"value":"s3cret"}`, "secret areq_1"},
		{http.MethodPost, "/api/v1/agent/requests/areq_1/complete", "", "complete areq_1"},
		{http.MethodPost, "/api/v1/agent/requests/areq_1/decline", "", "decline areq_1 "},
		{http.MethodPost, "/api/v1/agent/requests/areq_1/decline", `{"reason":"no"}`, "decline areq_1 no"},
	} {
		out := serve(t, r, c.method, c.path, c.body, ada)
		if out.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", c.method, c.path, out.Code, out.Body.String())
		}
		if strings.Contains(out.Body.String(), "s3cret") {
			t.Fatal("a response carried the secret")
		}
		if last := api.calls[len(api.calls)-1]; last != c.call {
			t.Fatalf("%s %s called %q", c.method, c.path, last)
		}
	}
	if api.secret != "s3cret" {
		t.Fatalf("the secret reached the service as %q", api.secret)
	}
	out := serve(t, r, http.MethodGet, "/api/v1/agent/requests?status=all&project_id=proj_1&for=me&limit=10", "", ada)
	if out.Code != http.StatusOK || api.filter != (agentrequests.ListFilter{Status: "all", ProjectID: "proj_1", For: "me", Limit: 10}) {
		t.Fatalf("list: %d %+v", out.Code, api.filter)
	}
	if out = serve(t, r, http.MethodPost, "/api/v1/agent/requests/areq_1/secret", `{"value":"s3cret"`, ada); out.Code != http.StatusBadRequest || strings.Contains(out.Body.String(), "s3cret") {
		t.Fatalf("bad secret body: %d %s", out.Code, out.Body.String())
	}
}

// The request routes sit behind the binder: with the agent off they
// answer 409 and never reach the service.
func TestRequestRoutesNeedTheAgentOn(t *testing.T) {
	r, api := requestRig(t, false)
	out := serve(t, r, http.MethodGet, "/api/v1/agent/requests", "", &security.Principal{OrgID: "org_1", UserID: "usr_ada"})
	if out.Code != http.StatusConflict || len(api.calls) != 0 {
		t.Fatalf("agent off: %d, calls %q", out.Code, api.calls)
	}
	if out = serve(t, r, http.MethodGet, "/api/v1/agent/requests", "", nil); out.Code != http.StatusUnauthorized {
		t.Fatalf("no principal: %d", out.Code)
	}
}

func TestRequestErrorsMapToStatuses(t *testing.T) {
	r, api := requestRig(t, true)
	ada := &security.Principal{OrgID: "org_1", UserID: "usr_ada"}
	for err, status := range map[error]int{
		agentrequests.ErrNotFound:                                 http.StatusNotFound,
		agentrequests.ErrNotPending:                               http.StatusConflict,
		agentrequests.ErrNotDone:                                  http.StatusConflict,
		agentrequests.ErrInvalid:                                  http.StatusUnprocessableEntity,
		security.ErrForbidden:                                     http.StatusForbidden,
		errors.Join(errors.New("x"), agentrequests.ErrNotPending): http.StatusConflict,
	} {
		api.err = err
		if out := serve(t, r, http.MethodPost, "/api/v1/agent/requests/areq_1/answer", `{"text":"x"}`, ada); out.Code != status {
			t.Errorf("%v: %d, want %d", err, out.Code, status)
		}
	}
}
