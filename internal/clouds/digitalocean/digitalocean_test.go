package digitalocean

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const approved = "0123456789abcdef0123456789abcdef01234567"

// fake answers DigitalOcean's API from handlers keyed by "METHOD /path".
type fake struct {
	mu       sync.Mutex
	srv      *httptest.Server
	handlers map[string]func(w http.ResponseWriter, r *http.Request, body map[string]any)
	calls    []string
}

func newFake(t *testing.T) (*fake, *Client) {
	t.Helper()
	f := &fake{handlers: map[string]func(http.ResponseWriter, *http.Request, map[string]any){}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dop_v1_good" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"id":"Unauthorized","message":"Unable to authenticate you"}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		key := r.Method + " " + r.URL.Path
		f.calls = append(f.calls, key)
		h := f.handlers[key]
		f.mu.Unlock()
		if h == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"id":"not_found","message":"The resource you were accessing could not be found."}`))
			return
		}
		h(w, r, body)
	}))
	t.Cleanup(f.srv.Close)
	return f, New(f.srv.URL)
}

func (f *fake) on(key string, h func(w http.ResponseWriter, r *http.Request, body map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[key] = h
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestCheckToken(t *testing.T) {
	for _, bad := range []string{"", "  ", "dop v1", strings.Repeat("a", maxToken+1)} {
		if !errors.Is(CheckToken(bad), ErrInvalidToken) {
			t.Errorf("CheckToken(%q) accepted it", bad)
		}
	}
	if err := CheckToken(" dop_v1_good\n"); err != nil {
		t.Errorf("CheckToken: %v", err)
	}
}

func TestAccount_aRefusedTokenIsErrRejected(t *testing.T) {
	f, c := newFake(t)
	f.on("GET /v2/account", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"account": map[string]any{"uuid": "acct-1", "email": "ada@example.com", "status": "active",
			"team": map[string]any{"uuid": "team-1", "name": "Acme"}}})
	})
	if _, err := c.Account(context.Background(), "dop_v1_bad"); !errors.Is(err, ErrRejected) {
		t.Fatalf("bad token: %v", err)
	}
	a, err := c.Account(context.Background(), "dop_v1_good")
	if err != nil || a.UUID != "acct-1" || a.Team == nil || a.Team.UUID != "team-1" {
		t.Fatalf("account %+v, %v", a, err)
	}
}

func TestFindApp_readsEveryPage(t *testing.T) {
	f, c := newFake(t)
	f.on("GET /v2/apps", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.URL.Query().Get("page") == "1" {
			reply(w, 200, map[string]any{"apps": []any{map[string]any{"id": "a1", "spec": map[string]any{"name": "other"}}},
				"links": map[string]any{"pages": map[string]any{"next": "page=2"}}})
			return
		}
		reply(w, 200, map[string]any{"apps": []any{map[string]any{"id": "a2", "spec": map[string]any{"name": "api-prod-abc"}}}})
	})
	a, err := c.FindApp(context.Background(), "dop_v1_good", "api-prod-abc")
	if err != nil || a == nil || a.ID != "a2" {
		t.Fatalf("FindApp = %+v, %v", a, err)
	}
	if a, err := c.FindApp(context.Background(), "dop_v1_good", "missing"); err != nil || a != nil {
		t.Fatalf("missing app = %+v, %v", a, err)
	}
}

func TestWaitDeployment(t *testing.T) {
	deployment := func(phase, hash string) map[string]any {
		return map[string]any{"deployment": map[string]any{"id": "d1", "phase": phase,
			"services": []any{map[string]any{"name": "api", "source_commit_hash": hash}}}}
	}
	tests := []struct {
		name    string
		phases  [][2]string // phase, hash per poll
		want    string      // "" = live, "mismatch", or an error substring
		polls   int
		phaseAt string
	}{
		{name: "live on the approved commit", phases: [][2]string{{"PENDING_BUILD", ""}, {"BUILDING", approved}, {"ACTIVE", approved}}, polls: 3},
		{name: "a short hash still matches", phases: [][2]string{{"ACTIVE", approved[:7]}}, polls: 1},
		{name: "another commit stops it while building", phases: [][2]string{{"BUILDING", "ffffffffffffffffffffffffffffffffffffffff"}, {"ACTIVE", approved}}, want: "mismatch", polls: 1, phaseAt: "BUILDING"},
		{name: "live without a reported commit is refused", phases: [][2]string{{"ACTIVE", ""}}, want: "mismatch", polls: 1, phaseAt: "ACTIVE"},
		{name: "a failed build is an error", phases: [][2]string{{"BUILDING", ""}, {"ERROR", ""}}, want: "deployment d1: error", polls: 2},
		{name: "a superseded one is an error", phases: [][2]string{{"SUPERSEDED", approved}}, want: "superseded", polls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, c := newFake(t)
			n := 0
			f.on("GET /v2/apps/app-1/deployments/d1", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
				p := tt.phases[min(n, len(tt.phases)-1)]
				n++
				reply(w, 200, deployment(p[0], p[1]))
			})
			d, err := c.WaitDeployment(context.Background(), "dop_v1_good", "app-1", "d1", approved)
			var mm *CommitMismatchError
			switch tt.want {
			case "":
				if err != nil || d.Phase != PhaseActive {
					t.Fatalf("got %+v, %v", d, err)
				}
			case "mismatch":
				if !errors.As(err, &mm) || mm.Deployment.Phase != tt.phaseAt || mm.Want != approved {
					t.Fatalf("want a mismatch at %s, got %v", tt.phaseAt, err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), tt.want) || errors.As(err, &mm) {
					t.Fatalf("want error containing %q, got %v", tt.want, err)
				}
			}
			if n != tt.polls {
				t.Fatalf("polled %d times, want %d", n, tt.polls)
			}
		})
	}
}

func TestEnsureDatabase_createsOnceAndWaitsUntilOnline(t *testing.T) {
	f, c := newFake(t)
	var created map[string]any
	polls := 0
	f.on("GET /v2/databases", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"databases": []any{}})
	})
	f.on("POST /v2/databases", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		created = body
		reply(w, 201, map[string]any{"database": map[string]any{"id": "db-1", "name": body["name"], "status": "creating"}})
	})
	f.on("GET /v2/databases/db-1", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		polls++
		status := "creating"
		if polls > 1 {
			status = "online"
		}
		reply(w, 200, map[string]any{"database": map[string]any{"id": "db-1", "status": status,
			"connection": map[string]any{"uri": "postgresql://doadmin:pw@db-1.db.ondigitalocean.com:25060/defaultdb?sslmode=require"}}})
	})
	d, err := c.EnsureDatabase(context.Background(), "dop_v1_good", DatabaseSpec{Name: "db-prod", Engine: Postgres, Version: "16", Region: "nyc3", Size: "db-s-1vcpu-1gb", NumNodes: 1})
	if err != nil || d.Status != "online" || d.Connection == nil || !strings.HasPrefix(d.Connection.URI, "postgresql://") {
		t.Fatalf("EnsureDatabase = %+v, %v", d, err)
	}
	if created["engine"] != "pg" || created["version"] != "16" || created["region"] != "nyc3" || created["num_nodes"] != float64(1) {
		t.Fatalf("created %v", created)
	}
	if polls != 2 {
		t.Fatalf("polled %d times", polls)
	}
}

func TestDeletes_ignoreWhatsAlreadyGone(t *testing.T) {
	_, c := newFake(t)
	if err := c.DeleteApp(context.Background(), "dop_v1_good", "gone"); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	if err := c.DeleteDatabase(context.Background(), "dop_v1_good", "gone"); err != nil {
		t.Fatalf("DeleteDatabase: %v", err)
	}
	if err := c.CancelDeployment(context.Background(), "dop_v1_good", "app", "gone"); err != nil {
		t.Fatalf("CancelDeployment: %v", err)
	}
}

func TestRollback_skipsThePin(t *testing.T) {
	f, c := newFake(t)
	var got map[string]any
	f.on("POST /v2/apps/app-1/rollback", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		got = body
		reply(w, 200, map[string]any{"deployment": map[string]any{"id": "d0"}})
	})
	if err := c.Rollback(context.Background(), "dop_v1_good", "app-1", "d0"); err != nil {
		t.Fatal(err)
	}
	if got["deployment_id"] != "d0" || got["skip_pin"] != true {
		t.Fatalf("rollback body %v", got)
	}
}

func TestUpdateAppSendsDomains(t *testing.T) {
	f, c := newFake(t)
	var got map[string]any
	f.on("PUT /v2/apps/app-1", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		got = body
		reply(w, 200, map[string]any{"app": map[string]any{"id": "app-1"}})
	})
	spec := AppSpec{Name: "shop", Domains: []AppDomainSpec{{Domain: "app.example.com", Type: AppDomainAlias}}}
	if _, err := c.UpdateApp(context.Background(), "dop_v1_good", "app-1", spec); err != nil {
		t.Fatalf("UpdateApp: %v", err)
	}
	s, _ := got["spec"].(map[string]any)
	want := `[{"domain":"app.example.com","type":"ALIAS"}]`
	b, _ := json.Marshal(s["domains"])
	if string(b) != want {
		t.Errorf("spec.domains = %s, want %s", b, want)
	}
}

func TestGetAppDecodesDomains(t *testing.T) {
	f, c := newFake(t)
	f.on("GET /v2/apps/app-1", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"app": map[string]any{"id": "app-1", "domains": []any{map[string]any{
			"id":                     "dom-1",
			"spec":                   map[string]any{"domain": "app.example.com", "type": "ALIAS"},
			"phase":                  "CONFIGURING",
			"certificate_expires_at": "2027-01-29T23:59:59Z",
			"progress": map[string]any{"steps": []any{map[string]any{
				"name": "verify-cname", "status": "PENDING"}}},
		}}}})
	})
	app, err := c.GetApp(context.Background(), "dop_v1_good", "app-1")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if len(app.Domains) != 1 {
		t.Fatalf("domains = %d, want 1", len(app.Domains))
	}
	d := app.Domains[0]
	if d.Phase != AppDomainConfiguring || d.Spec.Domain != "app.example.com" {
		t.Errorf("domain = %+v", d)
	}
	if d.CertificateExpiresAt.Year() != 2027 {
		t.Errorf("certificate_expires_at = %v", d.CertificateExpiresAt)
	}
	if d.Progress == nil || len(d.Progress.Steps) != 1 || d.Progress.Steps[0].Name != "verify-cname" {
		t.Errorf("progress = %+v", d.Progress)
	}
}

func TestGetAppReadsAnEmptyTimestampAsZero(t *testing.T) {
	f, c := newFake(t)
	f.on("GET /v2/apps/app-1", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"app": map[string]any{"id": "app-1", "domains": []any{map[string]any{
			"spec":                   map[string]any{"domain": "app.example.com", "type": "ALIAS"},
			"phase":                  "PENDING",
			"certificate_expires_at": "",
			"progress": map[string]any{"steps": []any{map[string]any{
				"name": "verify-cname", "status": "PENDING", "started_at": "", "ended_at": nil}}},
		}}}})
	})
	app, err := c.GetApp(context.Background(), "dop_v1_good", "app-1")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	d := app.Domains[0]
	if !d.CertificateExpiresAt.IsZero() || !d.Progress.Steps[0].StartedAt.IsZero() || !d.Progress.Steps[0].EndedAt.IsZero() {
		t.Errorf("domain = %+v", d)
	}
}
