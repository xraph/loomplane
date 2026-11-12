package fleets

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/agentruns"
	domain "github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type monitoringFixture struct {
	routeFixture
	called bool
	err    error
	after  string
	limit  int
	window fleet.MetricWindow
}

func (s *monitoringFixture) Events(_ context.Context, p security.Principal, id, after string, limit int) (*fleet.EventPage, error) {
	s.called = true
	if p.OrgID != "org" || id != "instance" {
		return nil, domain.ErrNotFound
	}
	s.after, s.limit = after, limit
	return &fleet.EventPage{Items: []fleet.Event{{ID: "event", InstanceID: id, FleetID: "fleet", Kind: "lifecycle", Lifecycle: &fleet.LifecycleEvent{State: "waiting_approval"}}}, NextCursor: "next", ReplayTruncated: true}, s.err
}
func (s *monitoringFixture) Metrics(_ context.Context, p security.Principal, id string, window fleet.MetricWindow) (*fleet.MetricSummary, error) {
	s.called = true
	if p.OrgID != "org" || id != "instance" {
		return nil, domain.ErrNotFound
	}
	s.window = window
	return &fleet.MetricSummary{Window: window, Freshness: "unknown", Partial: true, CostStatus: "unknown", Reason: "source_unavailable"}, s.err
}
func TestFleetMonitoringRoutes(t *testing.T) {
	for _, tt := range []struct {
		name, suffix           string
		fixture                monitoringFixture
		anonymous, uninstalled bool
		status                 int
		called                 bool
	}{
		{name: "events", suffix: "events?after=cursor%2Bvalue&limit=7", status: 200, called: true},
		{name: "metrics", suffix: "metrics?start=2026-01-01T00:00:00Z&end=2026-01-01T01:00:00Z", status: 200, called: true},
		{name: "anonymous", suffix: "events", anonymous: true, status: 401},
		{name: "sibling fleet", suffix: "events", fixture: monitoringFixture{routeFixture: routeFixture{instanceFleet: "other"}}, status: 404},
		{name: "sibling metrics", suffix: "metrics?start=2026-01-01T00:00:00Z&end=2026-01-01T01:00:00Z", fixture: monitoringFixture{routeFixture: routeFixture{instanceFleet: "other"}}, status: 404},
		{name: "denied", suffix: "events", fixture: monitoringFixture{routeFixture: routeFixture{err: security.ErrForbidden}}, status: 403},
		{name: "revoked during read", suffix: "events", fixture: monitoringFixture{err: security.ErrForbidden}, status: 403, called: true},
		{name: "redacted", suffix: "events", fixture: monitoringFixture{err: errors.New("private-provider-token")}, status: 500, called: true},
		{name: "bad limit", suffix: "events?limit=broken", status: 422},
		{name: "negative limit", suffix: "events?limit=-1", status: 422},
		{name: "oversized limit", suffix: "events?limit=201", status: 422},
		{name: "duplicate limit", suffix: "events?limit=1&limit=2", status: 422},
		{name: "long cursor", suffix: "events?after=" + strings.Repeat("a", 161), status: 422},
		{name: "default server window", suffix: "metrics", status: 200, called: true},
		{name: "missing end", suffix: "metrics?start=2026-01-01T00:00:00Z", status: 422},
		{name: "missing start", suffix: "metrics?end=2026-01-01T01:00:00Z", status: 422},
		{name: "bad window", suffix: "metrics?start=no&end=no", status: 422},
		{name: "reversed window", suffix: "metrics?start=2026-01-02T00:00:00Z&end=2026-01-01T00:00:00Z", status: 422},
		{name: "oversized window", suffix: "metrics?start=2026-01-01T00:00:00Z&end=2026-01-03T00:00:00Z", status: 422},
		{name: "uninstalled", suffix: "events", uninstalled: true, status: 503},
	} {
		t.Run(tt.name, func(t *testing.T) {
			router := forge.NewRouter()
			var svc readService = &tt.fixture
			if tt.uninstalled {
				svc = routeFixture{}
			}
			if err := (&Extension{svc: svc}).routes(router, nil); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/projects/project/fleet/instances/instance/"+tt.suffix, nil)
			if !tt.anonymous {
				req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "operator"}))
			}
			out := httptest.NewRecorder()
			router.ServeHTTP(out, req)
			if out.Code != tt.status {
				t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
			}
			if tt.fixture.called != tt.called {
				t.Fatalf("monitoring read called=%v", tt.fixture.called)
			}
			if strings.Contains(out.Body.String(), "private-provider-token") {
				t.Fatal("provider error exposed")
			}
			if tt.name == "events" {
				var page fleet.EventPage
				if err := json.Unmarshal(out.Body.Bytes(), &page); err != nil {
					t.Fatal(err)
				}
				if len(page.Items) != 1 || !page.ReplayTruncated || page.NextCursor != "next" || tt.fixture.after != "cursor+value" || tt.fixture.limit != 7 {
					t.Fatalf("page=%+v input=%s/%d", page, tt.fixture.after, tt.fixture.limit)
				}
			}
			if tt.name == "default server window" {
				now := time.Now()
				w := tt.fixture.window
				if _, err := domain.AggregateMetrics(w, now, domain.MetricBatch{Partial: true}); err != nil {
					t.Fatalf("server window rejected by metric validation: %v", err)
				}
				if w.End.Sub(w.Start) != 15*time.Minute || w.End.After(now) || now.Sub(w.End) > time.Minute {
					t.Fatalf("invalid server window: %+v", w)
				}
			}
			if tt.name == "metrics" {
				var summary fleet.MetricSummary
				if err := json.Unmarshal(out.Body.Bytes(), &summary); err != nil {
					t.Fatal(err)
				}
				if summary.CurrentReplicas != nil || summary.CPUUtilization != nil || summary.Freshness != "unknown" || summary.CostStatus != "unknown" {
					t.Fatalf("unknown observations lost: %+v", summary)
				}
				if tt.fixture.window.End.Sub(tt.fixture.window.Start) != time.Hour {
					t.Fatal("window changed")
				}
			}
		})
	}
}

func TestFleetMonitoringOpenAPI(t *testing.T) {
	r := forge.NewRouter(forge.WithOpenAPI(forge.OpenAPIConfig{Title: "Fleet monitoring", Version: "1", SpecEnabled: true}))
	if err := r.GET("/existing-agent-event", func(c forge.Context) error { return c.JSON(http.StatusOK, agentruns.Event{}) }, forge.WithResponseSchema(http.StatusOK, "Agent event", agentruns.Event{})); err != nil {
		t.Fatal(err)
	}
	if err := (&Extension{svc: routeFixture{}}).routes(r, nil); err != nil {
		t.Fatal(err)
	}
	document := r.OpenAPISpec()
	if schema := document.Components.Schemas["Event"]; schema == nil || schema.Properties["seq"] == nil || schema.Properties["task_id"] == nil {
		t.Error("existing agent Event schema changed")
	}
	if schema := document.Components.Schemas["FleetEvent"]; schema == nil || schema.Properties["sequence"] == nil || schema.Properties["instance_id"] == nil {
		t.Error("Fleet event needs a distinct schema")
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			Responses  map[string]any `json:"responses"`
			Parameters []struct {
				Name string `json:"name"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	if err = json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	for suffix, params := range map[string][]string{"events": {"after", "limit"}, "metrics": {"start", "end"}} {
		op, ok := spec.Paths["/api/v1/projects/{project_id}/fleet/instances/{id}/"+suffix]["get"]
		if !ok {
			t.Errorf("missing %s route", suffix)
			continue
		}
		for _, status := range []string{"200", "422", "503"} {
			if op.Responses[status] == nil {
				t.Errorf("%s missing %s", suffix, status)
			}
		}
		for _, name := range params {
			found := false
			for _, p := range op.Parameters {
				found = found || p.Name == name
			}
			if !found {
				t.Errorf("%s missing parameter %s", suffix, name)
			}
		}
	}
	for _, field := range []string{"replay_truncated", "lifecycle", "scaling", "release", "cpu_utilization", "coverage", "cost_status", "runtime_observed_at"} {
		if !strings.Contains(string(raw), `"`+field+`"`) {
			t.Errorf("missing typed field %s", field)
		}
	}
}
