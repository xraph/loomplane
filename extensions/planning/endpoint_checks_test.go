package planning

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
	"github.com/xraph/loomplane/internal/security"
)

// endpointAudit keeps what's audited.
type endpointAudit struct{ events []catalogconnect.AuditEvent }

func (a *endpointAudit) Record(_ context.Context, e catalogconnect.AuditEvent) {
	a.events = append(a.events, e)
}

func TestCheckLimiterKeepsTenAMinutePerUserAndOrg(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newCheckLimiter(10, 30)
	l.now = func() time.Time { return now }
	alice := security.Principal{OrgID: "org", UserID: "alice"}
	for i := range 10 {
		if ok, _ := l.allow(alice); !ok {
			t.Fatalf("check %d refused", i+1)
		}
	}
	if ok, first := l.allow(alice); ok || !first {
		t.Fatalf("the 11th check: allowed %t, first refusal %t", ok, first)
	}
	if ok, first := l.allow(alice); ok || first {
		t.Fatalf("the 12th check: allowed %t, first refusal %t", ok, first)
	}
	// Someone else, or the same person in another organization, has a
	// bucket of their own.
	for _, p := range []security.Principal{{OrgID: "org", UserID: "bob"}, {OrgID: "other", UserID: "alice"}} {
		if ok, _ := l.allow(p); !ok {
			t.Fatalf("%+v was refused", p)
		}
	}
	// A check comes back every six seconds.
	now = now.Add(6 * time.Second)
	if ok, _ := l.allow(alice); !ok {
		t.Fatal("a refilled check was refused")
	}
	if ok, _ := l.allow(alice); ok {
		t.Fatal("refilled more than one check")
	}
	now = now.Add(time.Hour)
	for i := range 10 {
		if ok, _ := l.allow(alice); !ok {
			t.Fatalf("after an hour, check %d refused", i+1)
		}
	}
}

func TestCheckLimiterKeepsThirtyAMinutePerUserAcrossOrgs(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newCheckLimiter(checksPerMinute, checksPerUserPerMinute)
	l.now = func() time.Time { return now }
	if checksPerUserPerMinute != 30 {
		t.Fatalf("a person checks %d a minute across organizations", checksPerUserPerMinute)
	}
	for _, org := range []string{"a", "b", "c"} {
		for i := range 10 {
			if ok, _ := l.allow(security.Principal{OrgID: org, UserID: "alice"}); !ok {
				t.Fatalf("%s: check %d refused", org, i+1)
			}
		}
	}
	// A fourth organization has a full bucket of its own, but alice has
	// used her thirty.
	d := security.Principal{OrgID: "d", UserID: "alice"}
	if ok, first := l.allow(d); ok || !first {
		t.Fatalf("the 31st check: allowed %t, first refusal %t", ok, first)
	}
	if ok, _ := l.allow(security.Principal{OrgID: "d", UserID: "bob"}); !ok {
		t.Fatal("bob was refused for alice's checks")
	}
	// One comes back every two seconds, and a refusal took nothing.
	now = now.Add(2 * time.Second)
	if ok, _ := l.allow(d); !ok {
		t.Fatal("a refilled check was refused")
	}
	if ok, _ := l.allow(d); ok {
		t.Fatal("refilled more than one check")
	}
}

func TestCheckRouteIsRateLimitedAndAudited(t *testing.T) {
	prober := &recordingProber{report: catalogconnect.Report{Version: "16.4"}}
	audit := &endpointAudit{}
	svc := catalogconnect.NewService(&endpointStore{rows: map[string]catalogconnect.Endpoint{}}, endpointSecrets{}, secretManagers{})
	svc.Audit = audit
	ext := &Extension{
		endpoints:      svc,
		endpointChecks: &catalogconnect.Checker{Endpoints: svc, Probers: map[string]catalogconnect.Prober{"postgres": prober}},
		endpointDialer: &checkDialer{clouds: map[string]string{"conn_server": connections.Server}},
		catalogKinds:   recipestest.Registry(t),
	}
	r := forge.NewRouter()
	if err := ext.routes(r, nil); err != nil {
		t.Fatal(err)
	}
	const dsn = "postgres://u:hunter2-Secret@db.example:6543/app?sslmode=require&application_name=x"
	send := func(user, target string) *httptest.ResponseRecorder {
		t.Helper()
		body := `{"engine":"postgres","dsn":"` + dsn + `","target":"` + target + `","kind":"sample","dependency":"postgres"}`
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/catalog/endpoints/checks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: user}))
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out
	}

	if out := send("admin", "conn_server"); out.Code != http.StatusOK {
		t.Fatalf("check: %d %s", out.Code, out.Body.String())
	}
	if out := send("admin", "conn_gone"); out.Code != http.StatusNotFound {
		t.Fatalf("unknown target: %d %s", out.Code, out.Body.String())
	}
	if out := send("viewer", "conn_server"); out.Code != http.StatusForbidden {
		t.Fatalf("viewer: %d %s", out.Code, out.Body.String())
	}
	prober.report = catalogconnect.Report{Version: "15.2", Problems: []string{"PostgreSQL 15.2 is older than 16"}}
	if out := send("admin", "conn_server"); out.Code != http.StatusOK {
		t.Fatalf("failing check: %d %s", out.Code, out.Body.String())
	}
	for i, want := range []catalogconnect.AuditEvent{
		{Outcome: catalogconnect.OutcomeSuccess, Principal: security.Principal{OrgID: "org", UserID: "admin"}, Target: "conn_server"},
		{Outcome: catalogconnect.OutcomeFailure, Principal: security.Principal{OrgID: "org", UserID: "admin"}, Target: "conn_gone"},
		{Outcome: catalogconnect.OutcomeDenied, Principal: security.Principal{OrgID: "org", UserID: "viewer"}, Target: "conn_server"},
		{Outcome: catalogconnect.OutcomeFailure, Principal: security.Principal{OrgID: "org", UserID: "admin"}, Target: "conn_server"},
	} {
		want.Action, want.Engine, want.Address = catalogconnect.AuditCheck, catalogconnect.EnginePostgres, "db.example:6543"
		if i >= len(audit.events) || audit.events[i] != want {
			t.Fatalf("audit %d: got %+v, want %+v", i, audit.events, want)
		}
	}

	// Ten checks a minute each: admin has used three.
	for i := range 7 {
		if out := send("admin", "conn_server"); out.Code != http.StatusOK {
			t.Fatalf("check %d: %d %s", i+4, out.Code, out.Body.String())
		}
	}
	probes := len(prober.reqs)
	for range 3 {
		out := send("admin", "conn_server")
		if out.Code != http.StatusTooManyRequests || !strings.Contains(out.Body.String(), "Too many endpoint checks") {
			t.Fatalf("over the limit: %d %s", out.Code, out.Body.String())
		}
	}
	if len(prober.reqs) != probes {
		t.Fatal("a refused check probed the endpoint")
	}
	// One refusal is audited for each run of them, not each request.
	limited := 0
	for _, e := range audit.events {
		if e.Outcome == catalogconnect.OutcomeRateLimited {
			limited++
		}
		if text := fmt.Sprintf("%+v", e); strings.Contains(text, "hunter2") || strings.Contains(text, "sslmode") || strings.Contains(text, "application_name") {
			t.Fatalf("the audit shows the DSN: %s", text)
		}
	}
	if limited != 1 || len(audit.events) != 4+7+1 {
		t.Fatalf("audited %d refusals in %d events", limited, len(audit.events))
	}
	// Someone else still checks.
	if out := send("viewer", "conn_server"); out.Code != http.StatusForbidden {
		t.Fatalf("viewer after admin's limit: %d", out.Code)
	}
}
