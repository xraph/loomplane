package identity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/security"
)

const gateOwner = "usr_owner"

// gateIsOwner answers ok for the owner whoever they came as, an agent
// acting for them included, so every caller below reaches the gate's own
// checks.
func gateIsOwner(ctx context.Context) (string, bool) {
	p, ok := security.PrincipalFrom(ctx)
	if !ok {
		return "", false
	}
	if p.UserID == gateOwner || (p.OnBehalfOf != nil && p.OnBehalfOf.UserID == gateOwner) {
		return gateOwner, true
	}
	return "", false
}

// gateRig serves GET /owner behind OwnerOnly, answering with the owner it
// let through. The guard answers 401 to a request with no principal, as
// identity's does, or status when it isn't 0.
func gateRig(t *testing.T, status int, isOwner PlatformOwner) (forge.Router, *int) {
	t.Helper()
	reached := 0
	guard := Guard{func(next forge.Handler) forge.Handler {
		return func(c forge.Context) error {
			if status != 0 {
				return forge.NewHTTPError(status, "the guard")
			}
			if _, ok := security.PrincipalFrom(c.Request().Context()); !ok {
				return forge.NewHTTPError(http.StatusUnauthorized, "sign in")
			}
			return next(c)
		}
	}}
	r := forge.NewRouter()
	g := r.Group("/api/v1/platform/test", forge.WithGroupMiddleware(OwnerOnly(guard, isOwner)...))
	if err := g.GET("/owner", func(c forge.Context) error {
		reached++
		return c.String(http.StatusOK, OwnerID(c))
	}); err != nil {
		t.Fatal(err)
	}
	return r, &reached
}

func gateGet(t *testing.T, r forge.Router, p *security.Principal) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/platform/test/owner", http.NoBody)
	if p != nil {
		req = req.WithContext(security.WithPrincipal(req.Context(), *p))
	}
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	return out
}

func TestOwnerOnlyLetsTheOwnerThrough(t *testing.T) {
	r, reached := gateRig(t, 0, gateIsOwner)
	out := gateGet(t, r, &security.Principal{OrgID: "org_1", UserID: gateOwner})
	if out.Code != http.StatusOK || out.Body.String() != gateOwner || *reached != 1 {
		t.Fatalf("the owner: %d %q, reached %d", out.Code, out.Body.String(), *reached)
	}
}

// Everyone but the owner signed in gets the 404 an unknown route gives,
// and the handler never runs.
func TestOwnerOnlyHidesTheRouteFromEveryoneElse(t *testing.T) {
	owner := security.Principal{OrgID: "org_1", UserID: gateOwner}
	for _, c := range []struct {
		name string
		p    *security.Principal
	}{
		{"anonymous", nil},
		{"a workspace admin", &security.Principal{OrgID: "org_1", UserID: "usr_admin"}},
		{"the owner's API key", &security.Principal{OrgID: "org_1", UserID: gateOwner, KeyID: "key_1"}},
		{"the agent for the owner", &security.Principal{OrgID: "org_1", UserID: security.AgentSubject("org_1"), Kind: security.KindAgent, OnBehalfOf: &owner}},
		{"the owner read-only", &security.Principal{OrgID: "org_1", UserID: gateOwner, ReadOnly: true}},
		{"acting for the owner", &security.Principal{OrgID: "org_1", UserID: gateOwner, OnBehalfOf: &owner}},
		{"a fleet key", &security.Principal{OrgID: "org_1", UserID: gateOwner, Kind: security.KindFleet}},
	} {
		r, reached := gateRig(t, 0, gateIsOwner)
		if out := gateGet(t, r, c.p); out.Code != http.StatusNotFound || *reached != 0 {
			t.Errorf("%s: %d %s, reached %d", c.name, out.Code, out.Body.String(), *reached)
		}
	}
}

// A guard that refuses with 401 or 403 is a 404 too, so the refusal
// doesn't say the route is there. Other refusals pass as they are.
func TestOwnerOnlyHidesTheGuardsRefusals(t *testing.T) {
	owner := &security.Principal{OrgID: "org_1", UserID: gateOwner}
	for status, want := range map[int]int{
		http.StatusUnauthorized:    http.StatusNotFound,
		http.StatusForbidden:       http.StatusNotFound,
		http.StatusTooManyRequests: http.StatusTooManyRequests,
	} {
		r, reached := gateRig(t, status, gateIsOwner)
		if out := gateGet(t, r, owner); out.Code != want || *reached != 0 {
			t.Errorf("guard %d: %d, want %d", status, out.Code, want)
		}
	}
}

// The owner check saying ok without a user is no owner.
func TestOwnerOnlyNeedsTheOwnersID(t *testing.T) {
	r, reached := gateRig(t, 0, func(context.Context) (string, bool) { return "", true })
	if out := gateGet(t, r, &security.Principal{OrgID: "org_1", UserID: gateOwner}); out.Code != http.StatusNotFound || *reached != 0 {
		t.Fatalf("no owner ID: %d", out.Code)
	}
}
