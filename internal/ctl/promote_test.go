package ctl

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	promotionPlan = `{"id":"plan_1","project_id":"proj_1","environment":"production","status":"ready","executable":true,"revision":"r1","connections":{},
		"kind":"promotion","from_environment":"staging",
		"sources":[{"alias":"web","repository":"acme/web","commit_sha":"aaaaaaa1111111","in_scope":true},{"alias":"api","repository":"acme/api","commit_sha":"bbbbbbb2222222","in_scope":true}],
		"actions":[{"id":"service:web","op":"update","subject":"service","name":"web","provider":"render","step":1},{"id":"service:api","op":"update","subject":"service","name":"api","provider":"cloudrun","step":1}],
		"problems":[],"warnings":[],
		"artifacts":[{"service":"web","reused":false,"reason":"Render builds each deployment from the commit itself"},{"service":"api","reused":true,"reference":"gcr.io/acme/api@sha256:abc","from":"staging"}]}`
	rollbackTarget = `{"environment":"production","target":{"deployment_id":"dep_3","plan_id":"plan_3","kind":"release","status":"succeeded","finished_at":"2026-10-28T10:00:00Z"},"restore":{"deployment_id":"dep_1","plan_id":"plan_0","kind":"release","status":"succeeded","finished_at":"2026-10-27T10:00:00Z"},"commits":[{"alias":"web","from":"bbbbbbb2222222","to":"aaaaaaa1111111"}]}`
	rollbackPlan   = `{"id":"plan_r","project_id":"proj_1","environment":"production","status":"ready","executable":true,"revision":"r1","connections":{},
		"kind":"rollback","rollback":{"reason":"5xx","trigger":"manual","target_deployment_id":"dep_3","target_plan_id":"plan_3","restore_deployment_id":"dep_1","restore_plan_id":"plan_0"},
		"sources":[{"alias":"web","repository":"acme/web","commit_sha":"aaaaaaa1111111","in_scope":true}],
		"actions":[{"id":"service:web","op":"update","subject":"service","name":"web","provider":"cloudrun","step":1},{"id":"resource:cache","op":"keep","subject":"resource","name":"cache","provider":"memorystore","step":1}],
		"problems":[],"warnings":[],
		"leftovers":[{"subject":"resource","name":"cache","op":"delete","data_bearing":true,"reason":"Created by the release you're rolling back. Rollback never removes a data-bearing resource; delete it yourself if you don't need it."}],
		"artifacts":[{"service":"web","reused":true,"reference":"gcr.io/acme/web@sha256:def","from":"production"}]}`
)

const (
	deployment9   = `{"id":"dep_9","project_id":"proj_1","plan_id":"plan_9","environment":"production","status":"succeeded","steps":[{"action_id":"service:api","name":"api","op":"update","provider":"render","status":"succeeded"}]}`
	soakHealthy   = `{"environment":"staging","running":true,"since":"2026-10-29T08:00:00Z","duration":9000000000000,"healthy":true,"unverified":false,"reasons":[],"services":[{"name":"api","provider":"render","verdict":"pass","reason":""}]}`
	soakUnhealthy = `{"environment":"staging","running":true,"since":"2026-10-29T08:00:00Z","duration":3600000000000,"healthy":false,"unverified":false,"reasons":["api: 4.1% of the candidate's requests failed; the ceiling is 1%."],"services":[{"name":"api","provider":"render","verdict":"fail","reason":"4.1% of the candidate's requests failed; the ceiling is 1%."}]}`
)

// watchJSON is a release watch as the server sends it.
func watchJSON(status, verdict, reason, rollbackPlan string, unverified bool, services string) string {
	return `{"id":"rw_1","org_id":"org_1","project_id":"proj_1","environment_id":"env_1","environment":"production","deployment_id":"dep_9","plan_id":"plan_9",
		"status":"` + status + `","verdict":"` + verdict + `","reason":"` + reason + `","thresholds":{},"services":` + services + `,"unwatched":[],"held":[],"held_released":false,
		"unverified":` + fmt.Sprint(unverified) + `,"rollback_plan_id":"` + rollbackPlan + `","rollback_approved_at":null,
		"starts_at":"2026-10-29T14:00:00Z","ends_at":"2026-10-29T14:15:00Z","bound_at":"2026-10-29T14:30:00Z","checked_at":null,"settled_at":null,
		"created_at":"2026-10-29T14:00:00Z","updated_at":"2026-10-29T14:00:00Z"}`
}

// releaseAPI is a fake Loomplane for promotions and rollbacks. It records
// each request and the body of each POST, and never approves.
type releaseAPI struct {
	mu       sync.Mutex
	calls    []string
	bodies   map[string]map[string]any
	approved bool
	target   string // the rollback target's answer; empty means rollbackTarget
	status   int    // the rollback target's status; 0 means 200
	soak     string // the promotion's soak; empty means a healthy one
	watch    string // dep_9's watch; empty means it has none (404)
}

func (f *releaseAPI) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.RequestURI())
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if f.bodies == nil {
				f.bodies = map[string]map[string]any{}
			}
			f.bodies[r.URL.Path] = body
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/approve"):
			f.approved = true
		case r.URL.Path == "/api/v1/projects":
			_, _ = w.Write([]byte(`{"projects":[{"id":"proj_1","name":"Shop","slug":"shop"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/projects/proj_1/plans":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"plan_1","status":"planning"}`))
		case r.URL.Path == "/api/v1/plans/plan_1/soak":
			if f.soak == "" {
				f.soak = soakHealthy
			}
			_, _ = w.Write([]byte(f.soak))
		case r.URL.Path == "/api/v1/deployments/dep_9":
			_, _ = w.Write([]byte(deployment9))
		case r.URL.Path == "/api/v1/deployments/dep_9/watch":
			if f.watch == "" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"release watch not found"}`))
				return
			}
			_, _ = w.Write([]byte(f.watch))
		case r.URL.Path == "/api/v1/plans/plan_1":
			_, _ = w.Write([]byte(promotionPlan))
		case r.URL.Path == "/api/v1/projects/proj_1/environments/production/rollback-target":
			if f.status != 0 {
				w.WriteHeader(f.status)
				_, _ = w.Write([]byte(f.target))
				return
			}
			_, _ = w.Write([]byte(rollbackTarget))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/projects/proj_1/rollbacks":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"plan_r","status":"planning","kind":"rollback"}`))
		case r.URL.Path == "/api/v1/plans/plan_r":
			_, _ = w.Write([]byte(rollbackPlan))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}
}

func TestPromote_createsAPlanFromStaging(t *testing.T) {
	api := &releaseAPI{}
	app, out := canaryApp(t, api.handler(t))
	if err := app.Run([]string{"promote", "shop"}); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if want := map[string]any{"environment": "production", "from": "staging"}; !reflect.DeepEqual(api.bodies["/api/v1/projects/proj_1/plans"], want) {
		t.Fatalf("plans.create body %v", api.bodies)
	}
	got := out.String()
	for _, want := range []string{
		"Promotes staging to production: web@aaaaaaa api@bbbbbbb",
		"api: reuses staging's image",
		"web: builds again (Render builds each deployment from the commit itself)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "Approve it with: loomctl approve plan_1\n") {
		t.Fatalf("doesn't end with the approve line:\n%s", got)
	}
	if api.approved {
		t.Fatal("promote approved the plan")
	}

	api = &releaseAPI{}
	app, out = canaryApp(t, api.handler(t))
	if err := app.Run([]string{"promote", "shop", "--from", "qa", "--to", "prod", "--scope", "web", "--no-wait", "--json"}); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if want := map[string]any{"environment": "prod", "from": "qa", "scope": []any{"web"}}; !reflect.DeepEqual(api.bodies["/api/v1/projects/proj_1/plans"], want) {
		t.Fatalf("plans.create body %v", api.bodies)
	}
	var plan map[string]any
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil || plan["id"] != "plan_1" {
		t.Fatalf("--json printed %s: %v", out, err)
	}
	if want := []string{"GET /api/v1/projects", "POST /api/v1/projects/proj_1/plans"}; !reflect.DeepEqual(api.calls, want) || api.approved {
		t.Fatalf("--no-wait made %v", api.calls)
	}
}

func TestPlan_fromFlag(t *testing.T) {
	api := &releaseAPI{}
	app, out := canaryApp(t, api.handler(t))
	if err := app.Run([]string{"plan", "shop", "--env", "production", "--from", "staging"}); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if want := map[string]any{"environment": "production", "from": "staging"}; !reflect.DeepEqual(api.bodies["/api/v1/projects/proj_1/plans"], want) {
		t.Fatalf("plans.create body %v", api.bodies)
	}
}

func TestRollback_createsARollbackPlan(t *testing.T) {
	api := &releaseAPI{}
	app, out := canaryApp(t, api.handler(t))
	if err := app.Run([]string{"rollback", "shop", "--env", "production", "--reason", "5xx"}); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	want := []string{
		"GET /api/v1/projects",
		"GET /api/v1/projects/proj_1/environments/production/rollback-target",
		"POST /api/v1/projects/proj_1/rollbacks",
		"GET /api/v1/plans/plan_r",
	}
	if !reflect.DeepEqual(api.calls, want) || api.approved {
		t.Fatalf("calls %v, approved %v", api.calls, api.approved)
	}
	if body := map[string]any{"environment": "production", "reason": "5xx"}; !reflect.DeepEqual(api.bodies["/api/v1/projects/proj_1/rollbacks"], body) {
		t.Fatalf("rollbacks.create body %v", api.bodies)
	}
	got := out.String()
	for _, want := range []string{
		"Rolls production back from dep_3 to dep_1",
		"web: bbbbbbb to aaaaaaa",
		"Left in place: cache (resource, created by the release)",
		"Rollback never removes a data-bearing resource",
		"web: reuses production's image",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "Approve it with: loomctl approve plan_r\n") {
		t.Fatalf("doesn't end with the approve line:\n%s", got)
	}

	// --deployment names the deployment to undo, in both requests.
	api = &releaseAPI{}
	app, out = canaryApp(t, api.handler(t))
	if err := app.Run([]string{"rollback", "shop", "--reason", "5xx", "--deployment", "dep_3", "--no-wait"}); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if api.calls[1] != "GET /api/v1/projects/proj_1/environments/production/rollback-target?deployment_id=dep_3" || len(api.calls) != 3 {
		t.Fatalf("calls %v", api.calls)
	}
	if body := map[string]any{"environment": "production", "reason": "5xx", "deployment_id": "dep_3"}; !reflect.DeepEqual(api.bodies["/api/v1/projects/proj_1/rollbacks"], body) {
		t.Fatalf("rollbacks.create body %v", api.bodies)
	}
}

func TestRollback_refusesBeforePlanning(t *testing.T) {
	// No reason: nothing is asked of the server.
	api := &releaseAPI{}
	app, out := canaryApp(t, api.handler(t))
	err := app.Run([]string{"rollback", "shop", "--env", "production"})
	if err == nil || !strings.Contains(err.Error(), "--reason") || len(api.calls) != 0 {
		t.Fatalf("err %v, calls %v\n%s", err, api.calls, out)
	}

	// A target the server refuses plans nothing.
	api = &releaseAPI{status: http.StatusConflict, target: `{"error":"a deployment is running in this environment; wait for it to finish, or cancel it, before rolling back"}`}
	app, out = canaryApp(t, api.handler(t))
	err = app.Run([]string{"rollback", "shop", "--reason", "5xx"})
	if err == nil || !strings.Contains(err.Error(), "a deployment is running") {
		t.Fatalf("err %v\n%s", err, out)
	}
	for _, c := range api.calls {
		if strings.HasPrefix(c, "POST") {
			t.Fatalf("planned a rollback after a refusal: %v", api.calls)
		}
	}
}

func TestDeployment_showsItsWatch(t *testing.T) {
	until := time.Date(2026, 10, 29, 14, 15, 0, 0, time.UTC).Local().Format("15:04")
	for _, tc := range []struct {
		name  string
		watch string
		want  []string
	}{
		{"watching", watchJSON("watching", "", "", "", false, `[{"name":"api","provider":"render","verdict":"pass","reason":"","op":"update"}]`),
			[]string{"Watch: watching until " + until + " (api on render: pass)"}},
		{"rolled back", watchJSON("rolled_back", "fail", "api: 4.1% of the candidate's requests failed; the ceiling is 1%.", "plan_r", false, `[]`),
			[]string{"Watch: breached (api: 4.1% of the candidate's requests failed; the ceiling is 1%.), rolled back by plan_r (Loomplane automation)"}},
		{"inconclusive", watchJSON("inconclusive", "inconclusive", "api: The candidate served 120 requests; at least 200 are needed to judge it.", "", true, `[]`),
			[]string{"Watch: inconclusive, release unverified (api: The candidate served 120 requests; at least 200 are needed to judge it.)"}},
		{"blocked", watchJSON("rollback_blocked", "fail", "Loomplane couldn't roll the release back: nothing to restore", "", false, `[]`),
			[]string{"Watch: breached, rollback blocked (Loomplane couldn't roll the release back: nothing to restore)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &releaseAPI{watch: tc.watch}
			app, out := canaryApp(t, api.handler(t))
			if err := app.Run([]string{"deployment", "dep_9"}); err != nil {
				t.Fatalf("run: %v\n%s", err, out)
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q in:\n%s", want, out)
				}
			}
		})
	}

	// A deployment with no watch prints nothing about one.
	api := &releaseAPI{}
	app, out := canaryApp(t, api.handler(t))
	if err := app.Run([]string{"deployment", "dep_9"}); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if strings.Contains(out.String(), "Watch") {
		t.Fatalf("printed a watch for none:\n%s", out)
	}
}

func TestPromote_showsTheSoak(t *testing.T) {
	api := &releaseAPI{}
	app, out := canaryApp(t, api.handler(t))
	if err := app.Run([]string{"promote", "shop"}); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "staging has run these commits for 2h30m, healthy") || !strings.HasSuffix(out.String(), "Approve it with: loomctl approve plan_1\n") {
		t.Fatalf("healthy soak:\n%s", out)
	}

	api = &releaseAPI{soak: soakUnhealthy}
	app, out = canaryApp(t, api.handler(t))
	if err := app.Run([]string{"promote", "shop"}); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if want := "staging has run these commits for 1h, not healthy: api: 4.1% of the candidate's requests failed; the ceiling is 1%."; !strings.Contains(out.String(), want) {
		t.Fatalf("missing %q in:\n%s", want, out)
	}

	// --json prints the plan alone, and --no-wait has no plan to judge yet.
	for _, args := range [][]string{{"promote", "shop", "--json"}, {"promote", "shop", "--no-wait"}} {
		api = &releaseAPI{}
		app, out = canaryApp(t, api.handler(t))
		if err := app.Run(args); err != nil {
			t.Fatalf("run: %v\n%s", err, out)
		}
		for _, c := range api.calls {
			if strings.HasSuffix(c, "/soak") {
				t.Fatalf("%v read the soak: %v", args, api.calls)
			}
		}
	}
}

func TestRollback_jsonPrintsThePlanAlone(t *testing.T) {
	api := &releaseAPI{}
	app, out := canaryApp(t, api.handler(t))
	if err := app.Run([]string{"rollback", "shop", "--reason", "5xx", "--json"}); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	var plan map[string]any
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil || plan["id"] != "plan_r" {
		t.Fatalf("--json printed %s: %v", out, err)
	}
	// The preview is for people; --json doesn't fetch what it won't print.
	want := []string{"GET /api/v1/projects", "POST /api/v1/projects/proj_1/rollbacks", "GET /api/v1/plans/plan_r"}
	if !reflect.DeepEqual(api.calls, want) {
		t.Fatalf("calls %v", api.calls)
	}
}
