//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/config"
)

type planPage struct {
	Plans []struct {
		ID string `json:"id"`
	} `json:"plans"`
	Next string `json:"next"`
}

type deploymentPage struct {
	Deployments []struct {
		ID string `json:"id"`
	} `json:"deployments"`
	Next string `json:"next"`
}

// seedRuns writes n settled plans, each with a failed deployment, straight
// into the database, newest last. The middle three share a timestamp, so
// a page boundary has to fall between rows the clock can't tell apart.
func seedRuns(t *testing.T, cfg config.Config, projectID string, n int) (plans, deps []string) {
	t.Helper()
	db, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(t.Context())
	var org, env, manifest string
	if err = db.QueryRow(t.Context(), `SELECT p.org_id, e.id FROM loomplane_projects p
		JOIN loomplane_environments e ON e.project_id = p.id AND e.name = 'production'
		WHERE p.id = $1`, projectID).Scan(&org, &env); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(t.Context(), `SELECT id FROM loomplane_manifests WHERE project_id = $1
		ORDER BY revision DESC LIMIT 1`, projectID).Scan(&manifest); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	for i := range n {
		at := base.Add(time.Duration(i) * time.Minute)
		if i >= 1 && i <= 3 {
			at = base.Add(time.Minute)
		}
		plan, dep := fmt.Sprintf("plan_page_%02d", i), fmt.Sprintf("dep_page_%02d", i)
		if _, err = db.Exec(t.Context(), `INSERT INTO loomplane_plans
			(id, org_id, project_id, environment_id, environment, status, manifest_id, manifest_revision, created_by, created_at)
			VALUES ($1, $2, $3, $4, 'production', 'rejected', $5, 1, 'test', $6)`,
			plan, org, projectID, env, manifest, at); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(t.Context(), `INSERT INTO loomplane_deployments
			(id, org_id, project_id, environment_id, environment, plan_id, commit_sha, status, started_by, started_at)
			VALUES ($1, $2, $3, $4, 'production', $5, 'abc', 'failed', 'test', $6)`,
			dep, org, projectID, env, plan, at); err != nil {
			t.Fatal(err)
		}
		plans, deps = append(plans, plan), append(deps, dep)
	}
	slices.Reverse(plans)
	slices.Reverse(deps)
	return plans, deps
}

func TestPagination_plansAndDeploymentsPageOlderWithoutGapsOrRepeats(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Paged"}, &proj)
	waitReady(t, c, proj.ID)
	base := "/api/v1/projects/" + proj.ID
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest",
		map[string]string{"yaml": "version: 1\nservices:\n  api:\n    source: go:services/api\n    provider: cloud-run\n"}, nil)
	wantPlans, wantDeps := seedRuns(t, cfg, proj.ID, 5)

	// Two at a time: 2, 2, then the last one with nothing after it.
	var gotPlans []string
	cursor := ""
	for page := 0; ; page++ {
		if page > 3 {
			t.Fatalf("plans kept paging: %v", gotPlans)
		}
		var p planPage
		c.must(http.StatusOK, http.MethodGet, base+"/plans?limit=2&before="+cursor, nil, &p)
		if page == 0 && (len(p.Plans) != 2 || p.Next == "") {
			t.Fatalf("the first page of two has %d plans and next %q", len(p.Plans), p.Next)
		}
		for _, pl := range p.Plans {
			gotPlans = append(gotPlans, pl.ID)
		}
		if p.Next == "" {
			break
		}
		cursor = p.Next
	}
	if !slices.Equal(gotPlans, wantPlans) {
		t.Fatalf("paged plans %v, want %v", gotPlans, wantPlans)
	}

	var gotDeps []string
	cursor = ""
	for page := 0; ; page++ {
		if page > 3 {
			t.Fatalf("deployments kept paging: %v", gotDeps)
		}
		var d deploymentPage
		c.must(http.StatusOK, http.MethodGet, base+"/deployments?limit=2&before="+cursor, nil, &d)
		if page == 0 && (len(d.Deployments) != 2 || d.Next == "") {
			t.Fatalf("the first page of two has %d deployments and next %q", len(d.Deployments), d.Next)
		}
		for _, dep := range d.Deployments {
			gotDeps = append(gotDeps, dep.ID)
		}
		if d.Next == "" {
			break
		}
		cursor = d.Next
	}
	if !slices.Equal(gotDeps, wantDeps) {
		t.Fatalf("paged deployments %v, want %v", gotDeps, wantDeps)
	}

	// A page that holds everything says there's nothing more.
	var all planPage
	c.must(http.StatusOK, http.MethodGet, base+"/plans", nil, &all)
	if len(all.Plans) != 5 || all.Next != "" {
		t.Fatalf("the default page has %d plans and next %q", len(all.Plans), all.Next)
	}

	// A cursor or a limit that can't be read is the caller's mistake.
	c.must(http.StatusBadRequest, http.MethodGet, base+"/plans?before=not-a-cursor", nil, nil)
	c.must(http.StatusBadRequest, http.MethodGet, base+"/deployments?before=not-a-cursor", nil, nil)
	c.must(http.StatusBadRequest, http.MethodGet, base+"/plans?limit=0", nil, nil)
	c.must(http.StatusBadRequest, http.MethodGet, base+"/deployments?limit=lots", nil, nil)
}
