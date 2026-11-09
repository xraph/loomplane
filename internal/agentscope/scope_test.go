package agentscope

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/cortex"
	"github.com/xraph/forge"
	"github.com/xraph/nexus/pipeline"
	"github.com/xraph/shield"
	"github.com/xraph/weave"

	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/security"
)

// tenants is a fake Tenants that answers tenant_<org>, or err.
type tenants struct {
	err   error
	asked []string
}

func (f *tenants) NexusTenant(_ context.Context, orgID string) (string, error) {
	f.asked = append(f.asked, orgID)
	if f.err != nil {
		return "", f.err
	}
	return "tenant_" + orgID, nil
}

func levels(s cortex.Scope) []string {
	out := make([]string, len(s.Levels))
	for i, l := range s.Levels {
		out[i] = l.Key + "=" + l.Value
	}
	return out
}

func TestScopeCortexLevels(t *testing.T) {
	for _, tc := range []struct {
		s    Scope
		want []string
	}{
		{Scope{OrgID: "o"}, []string{"org=o"}},
		{Scope{OrgID: "o", ProjectID: "p"}, []string{"org=o", "project=p"}},
		{Scope{OrgID: "o", ProjectID: "p", EnvironmentID: "e"}, []string{"org=o", "project=p", "environment=e"}},
	} {
		if err := tc.s.Validate(); err != nil {
			t.Fatalf("%+v: %v", tc.s, err)
		}
		if got := levels(tc.s.Cortex()); !slices.Equal(got, tc.want) {
			t.Fatalf("%+v: %v, want %v", tc.s, got, tc.want)
		}
		if err := cortex.ValidateScope(tc.s.Cortex()); err != nil {
			t.Fatalf("%+v: cortex refuses it: %v", tc.s, err)
		}
	}
	for _, bad := range []Scope{
		{},
		{ProjectID: "p"},
		{OrgID: "o", EnvironmentID: "e"},
		{OrgID: "o/p"},
		{OrgID: "o", ProjectID: "a=b"},
		{OrgID: " "},
	} {
		if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
}

func TestFromCortexRoundTrips(t *testing.T) {
	for _, s := range []Scope{
		{OrgID: "o"},
		{OrgID: "o", ProjectID: "p"},
		{OrgID: "o", ProjectID: "p", EnvironmentID: "e"},
	} {
		got, err := FromCortex(s.Cortex())
		if err != nil || got != s {
			t.Fatalf("%+v: %+v, %v", s, got, err)
		}
	}
	for name, cs := range map[string]cortex.Scope{
		"zero":           {},
		"no org first":   {Levels: []cortex.Level{{Key: LevelProject, Value: "p"}}},
		"wrong order":    {Levels: []cortex.Level{{Key: LevelOrg, Value: "o"}, {Key: LevelEnvironment, Value: "e"}}},
		"unknown level":  {Levels: []cortex.Level{{Key: LevelOrg, Value: "o"}, {Key: "team", Value: "t"}}},
		"empty value":    {Levels: []cortex.Level{{Key: LevelOrg, Value: ""}}},
		"too many":       {Levels: []cortex.Level{{Key: LevelOrg, Value: "o"}, {Key: LevelProject, Value: "p"}, {Key: LevelEnvironment, Value: "e"}, {Key: "x", Value: "y"}}},
		"repeated level": {Levels: []cortex.Level{{Key: LevelOrg, Value: "o"}, {Key: LevelOrg, Value: "o"}}},
	} {
		if _, err := FromCortex(cs); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestBindSetsEveryTenant(t *testing.T) {
	ada := security.Principal{UserID: "usr_ada", OrgID: "o"}
	agent := security.AgentPrincipal("o", &ada)
	f := &tenants{}
	b := &Binder{Tenants: f}
	s := Scope{OrgID: "o", ProjectID: "p"}
	ctx, err := b.Bind(context.Background(), agent, s)
	if err != nil {
		t.Fatal(err)
	}
	if got := levels(cortex.ScopeFromContext(ctx)); !slices.Equal(got, []string{"org=o", "project=p"}) {
		t.Fatalf("cortex scope %v", got)
	}
	if got, ok := cortex.PrincipalFromContext(ctx).(security.Principal); !ok || got.UserID != agent.UserID || got.OnBehalfOf == nil || got.OnBehalfOf.UserID != "usr_ada" {
		t.Fatalf("cortex principal %#v", cortex.PrincipalFromContext(ctx))
	}
	if got := pipeline.TenantID(ctx); got != "tenant_o" {
		t.Fatalf("nexus tenant %q", got)
	}
	if got := shield.TenantFromContext(ctx); got != "o" {
		t.Fatalf("shield tenant %q", got)
	}
	if got := shield.AppFromContext(ctx); got != App || App != "loomplane" {
		t.Fatalf("shield app %q", got)
	}
	if got := weave.TenantFromContext(ctx); got != "o" {
		t.Fatalf("weave tenant %q", got)
	}
	// The cortex adapters take the shield and weave tenant from the scope's
	// first level, so they land on the same organization as Bind's.
	if root, rerr := cortex.RootTenant(ctx, cortex.ScopeFromContext(ctx)); rerr != nil || root != "o" {
		t.Fatalf("cortex root tenant %q, %v", root, rerr)
	}
	if got, ok := From(ctx); !ok || got != s {
		t.Fatalf("From: %+v, %v", got, ok)
	}
	if !slices.Equal(f.asked, []string{"o"}) {
		t.Fatalf("tenant lookups %v", f.asked)
	}
	if _, ok := From(context.Background()); ok {
		t.Fatal("From found a scope on an unbound context")
	}
}

func TestBindRefusesAnotherOrg(t *testing.T) {
	ada := security.Principal{UserID: "usr_ada", OrgID: "a"}
	strayPerson := security.AgentPrincipal("b", &security.Principal{UserID: "usr_ada", OrgID: "b"})
	strayPerson.OnBehalfOf.OrgID = "a"
	for name, p := range map[string]security.Principal{
		"person":                         ada,
		"agent of a":                     security.AgentPrincipal("a", nil),
		"agent of b with a's subject":    {Kind: security.KindAgent, UserID: security.AgentSubject("a"), OrgID: "b"},
		"agent of b for a person of a":   strayPerson,
		"automation of a":                security.SystemPrincipal("a"),
		"principal without organization": {UserID: "usr_ada"},
	} {
		f := &tenants{}
		b := &Binder{Tenants: f}
		parent := context.WithValue(context.Background(), parentKey{}, "parent")
		ctx, err := b.Bind(parent, p, Scope{OrgID: "b"})
		if !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("%s: %v", name, err)
		}
		if ctx != parent {
			t.Fatalf("%s: Bind returned a different context", name)
		}
		if len(f.asked) != 0 {
			t.Fatalf("%s: looked up tenants %v", name, f.asked)
		}
	}
	// An invalid scope is refused before anything else.
	b := &Binder{Tenants: &tenants{}}
	if _, err := b.Bind(context.Background(), security.Principal{UserID: "usr_ada", OrgID: "a"}, Scope{OrgID: "a", EnvironmentID: "e"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid scope: %v", err)
	}
}

func TestBindFailsWhenTheTenantLookupFails(t *testing.T) {
	ada := security.Principal{UserID: "usr_ada", OrgID: "o"}
	lookup := errors.New("the database is down")
	for name, f := range map[string]Tenants{
		"lookup error": &tenants{err: lookup},
		"agent off":    &tenants{err: agentsettings.ErrDisabled},
		"no tenant":    emptyTenant{},
	} {
		b := &Binder{Tenants: f}
		parent := context.Background()
		ctx, err := b.Bind(parent, ada, Scope{OrgID: "o"})
		if err == nil {
			t.Fatalf("%s: no error", name)
		}
		if ctx != parent {
			t.Fatalf("%s: Bind returned a partly bound context", name)
		}
		if name == "lookup error" && !errors.Is(err, lookup) {
			t.Fatalf("%s: %v", name, err)
		}
		if name == "agent off" && !errors.Is(err, agentsettings.ErrDisabled) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := (&Binder{}).Bind(context.Background(), ada, Scope{OrgID: "o"}); err == nil {
		t.Fatal("a Binder without Tenants bound a context")
	}
}

type parentKey struct{}

type emptyTenant struct{}

func (emptyTenant) NexusTenant(context.Context, string) (string, error) { return "", nil }

func TestMiddlewareBindsTheCallersOrganization(t *testing.T) {
	serve := func(b *Binder, p *security.Principal) (int, string, Scope, string) {
		var seen Scope
		var tenant string
		r := forge.NewRouter()
		if p != nil {
			who := *p
			r.Use(func(next forge.Handler) forge.Handler {
				return func(c forge.Context) error {
					c.WithContext(security.WithPrincipal(c.Request().Context(), who))
					return next(c)
				}
			})
		}
		r.Use(b.Middleware())
		if err := r.GET("/agent", func(c forge.Context) error {
			seen, _ = From(c.Request().Context())
			tenant = pipeline.TenantID(c.Request().Context())
			return c.JSON(http.StatusOK, map[string]string{"ok": "yes"})
		}); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		r.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/agent", nil))
		return w.Code, w.Body.String(), seen, tenant
	}

	ada := security.Principal{UserID: "usr_ada", OrgID: "o"}
	code, _, seen, tenant := serve(&Binder{Tenants: &tenants{}}, &ada)
	if code != http.StatusOK || seen != (Scope{OrgID: "o"}) || tenant != "tenant_o" {
		t.Fatalf("bound: %d %+v %q", code, seen, tenant)
	}
	if code, _, _, _ = serve(&Binder{Tenants: &tenants{}}, nil); code != http.StatusUnauthorized {
		t.Fatalf("no principal: %d", code)
	}
	code, body, _, _ := serve(&Binder{Tenants: &tenants{err: agentsettings.ErrDisabled}}, &ada)
	if code != http.StatusConflict || !strings.Contains(body, "turn it on") {
		t.Fatalf("agent off: %d %s", code, body)
	}
	code, body, _, _ = serve(&Binder{Tenants: &tenants{err: errors.New("dial tcp 10.0.0.5: secret detail")}}, &ada)
	if code != http.StatusInternalServerError || strings.Contains(body, "10.0.0.5") {
		t.Fatalf("lookup error: %d %s", code, body)
	}
}
