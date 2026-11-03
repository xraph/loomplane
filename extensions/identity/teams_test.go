package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/chronicle"
	"github.com/xraph/chronicle/audit"
	"github.com/xraph/chronicle/store"
	"github.com/xraph/chronicle/store/memory"
	"github.com/xraph/forge"
	forgeerrors "github.com/xraph/forge/errors"
	forgehttp "github.com/xraph/go-utils/http"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/teams"
)

func statusOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		t.Fatal("no error")
	}
	return forgeerrors.GetHTTPStatusCode(err)
}

// authsome's own team routes are refused on any method, however the path
// is spelled, and nothing else under /v1/orgs is.
func TestRefuseAuthsomeTeams(t *testing.T) {
	cases := []struct {
		method, path string
		refused      bool
	}{
		{http.MethodGet, "/v1/orgs/aorg_1/teams", true},
		{http.MethodGet, "/v1/orgs/aorg_1/teams/", true},
		{http.MethodGet, "/v1/orgs/aorg_1/teams/x", true},
		{http.MethodGet, "/v1/orgs/aorg_1//teams", true},
		{http.MethodGet, "/v1/orgs/aorg_1/./teams/x/members", true},
		{http.MethodPost, "/v1/orgs/aorg_1/teams", true},
		{http.MethodPatch, "/v1/orgs/aorg_1/teams/x", true},
		{http.MethodDelete, "/v1/orgs/aorg_1/teams/x", true},
		{http.MethodGet, "/v1/orgs/aorg_1/members", false},
		{http.MethodDelete, "/v1/orgs/aorg_1/members/amem_1", false},
		{http.MethodGet, "/v1/orgs", false},
		{http.MethodGet, "/v1/orgs/aorg_1/teamsx", false},
		// bunrouter routes on the raw path. Path.Clean would turn this
		// into /v1/orgs/aorg_1, while the router hands "%2e%2e" to
		// authsome's team handler as the team ID.
		{http.MethodDelete, "/v1/orgs/aorg_1/teams/%2e%2e", true},
		{http.MethodGet, "/v1/orgs/aorg_1/teams/%2E%2E/x", true},
		// An encoded slash in the org ID: the decoded path has one segment
		// too many, the raw one routes to the teams handler.
		{http.MethodPost, "/v1/orgs/aorg%2F1/teams", true},
		{http.MethodGet, "/v1/orgs/aorg_1/%74eams", true},
		{http.MethodGet, "/v1/orgs/aorg%2F1/members", false},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			called := false
			next := func(forge.Context) error { called = true; return nil }
			// Parsed as the server parses it, so Path and RawPath differ
			// the way they would on the wire.
			req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, http.NoBody)
			err := refuseAuthsomeTeams(next)(forgehttp.NewContext(httptest.NewRecorder(), req, nil))
			if !tc.refused {
				if err != nil || !called {
					t.Fatalf("err %v, next called %v; want it passed through", err, called)
				}
				return
			}
			if called {
				t.Fatal("next was called")
			}
			if got := statusOf(t, err); got != http.StatusNotFound {
				t.Fatalf("status %d, want 404", got)
			}
			if want := "Loomplane's teams are at /api/v1/teams"; !strings.Contains(err.Error(), want) {
				t.Fatalf("message %q, want it to say %q", err.Error(), want)
			}
		})
	}
}

func TestTeamErrorsMapToStatuses(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{teams.ErrNotFound, http.StatusNotFound},
		{teams.ErrGrantNotFound, http.StatusNotFound},
		{teams.ErrMemberNotFound, http.StatusNotFound},
		{teams.ErrSlugTaken, http.StatusConflict},
		{fmt.Errorf("%w: a team's name is 1 to 80 characters", teams.ErrInvalid), http.StatusUnprocessableEntity},
		{teams.ErrNotMember, http.StatusUnprocessableEntity},
		{fmt.Errorf("%w: you can't grant a role above your own", security.ErrForbidden), http.StatusForbidden},
		{security.ErrUnauthenticated, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		if got := statusOf(t, teamError(tc.err)); got != tc.want {
			t.Errorf("%v: status %d, want %d", tc.err, got, tc.want)
		}
	}
	// Anything else stays an internal error, its text kept from the caller.
	boom := errors.New("connection refused")
	if got := teamError(boom); !errors.Is(got, boom) {
		t.Fatalf("an unknown error became %v", got)
	}
}

// Every team change lands in the organization's audit stream: the team as
// the resource, the actor as the user, the member or grant as meta. A
// deletion is a warning.
func TestTeamAuditorRecords(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	c, err := chronicle.New(chronicle.WithStore(store.NewAdapter(mem)))
	if err != nil {
		t.Fatal(err)
	}
	a := teamAuditor{chronicle: c, appID: "aapp_test", log: forge.NewNoopLogger()}
	ada := security.Principal{UserID: "ausr_ada", OrgID: "aorg_1"}
	a.Record(ctx, teams.AuditEvent{Action: "team.member.add", TeamID: "team_1", Principal: ada, Attributes: map[string]any{"user_id": "ausr_grace"}})
	a.Record(ctx, teams.AuditEvent{Action: "team.grant.set", TeamID: "team_1", Principal: ada, Attributes: map[string]any{"grant_id": "tgrant_1", "role": "developer", "project_id": "proj_1"}})
	a.Record(ctx, teams.AuditEvent{Action: "team.delete", TeamID: "team_1", Principal: ada})
	res, err := mem.Query(ctx, &audit.Query{AppID: "aapp_test"})
	if err != nil || len(res.Events) != 3 {
		t.Fatalf("query: %v, %+v", err, res)
	}
	byAction := map[string]*audit.Event{}
	for _, ev := range res.Events {
		byAction[ev.Action] = ev
	}
	for action, want := range map[string]struct {
		severity string
		meta     map[string]string
	}{
		"team.member.add": {audit.SeverityInfo, map[string]string{"user_id": "ausr_grace"}},
		"team.grant.set":  {audit.SeverityInfo, map[string]string{"grant_id": "tgrant_1", "role": "developer", "project_id": "proj_1"}},
		"team.delete":     {audit.SeverityWarning, nil},
	} {
		ev := byAction[action]
		if ev == nil {
			t.Fatalf("no %s event", action)
		}
		if ev.Resource != "team" || ev.ResourceID != "team_1" || ev.Category != "members" || ev.TenantID != "aorg_1" || ev.UserID != "ausr_ada" || ev.Severity != want.severity {
			t.Errorf("%s: %+v", action, ev)
		}
		for k, v := range want.meta {
			if got := fmt.Sprint(ev.Metadata[k]); got != v {
				t.Errorf("%s meta %s = %q, want %q", action, k, got, v)
			}
		}
	}
}
