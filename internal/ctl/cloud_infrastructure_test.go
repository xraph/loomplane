package ctl

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func infraTestApp(t *testing.T, body string, handler http.HandlerFunc) (*App, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	out := &bytes.Buffer{}
	return New(Options{Out: out, In: strings.NewReader(body), ConfigPath: t.TempDir() + "/none", Getenv: func(k string) string {
		if k == EnvURL {
			return srv.URL
		}
		return "scope-key"
	}}), out
}
func TestCloudOrganizationCommandsNeedNoProjects(t *testing.T) {
	for _, args := range [][]string{
		{"clouds", "--organization"}, {"clouds", "create", "--organization", "--name", "Shared"}, {"clouds", "show", "cloud", "--organization"}, {"clouds", "update", "cloud", "--organization", "--name", "New", "--revision", "2"}, {"clouds", "delete", "cloud", "--organization"},
		{"cloud-targets", "cloud", "--organization"}, {"cloud-targets", "show", "cloud", "target", "--organization"}, {"cloud-targets", "create", "cloud", "--organization", "--name", "host", "--provider", "server", "--runtime", "server", "--pool", "pool"},
		{"cloud-targets", "update", "cloud", "target", "--organization", "--revision", "2", "--tags", "eu,gpu"}, {"cloud-targets", "delete", "cloud", "target", "--organization"}, {"cloud-syncs", "--organization"}, {"cloud-syncs", "show", "sync", "--organization"},
		{"cloud-syncs", "routing", "sync", "--organization", "--environment", "env", "--evidence", "DNS checked"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			calls := 0
			app, _ := infraTestApp(t, "", func(w http.ResponseWriter, r *http.Request) {
				calls++
				if strings.Contains(r.URL.Path, "projects") || !strings.HasPrefix(r.URL.Path, "/api/v1/clouds") {
					t.Errorf("organization command requested %s", r.URL.Path)
				}
				if strings.Contains(strings.Join(args, " "), "--pool") {
					var in map[string]any
					if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
						t.Error(err)
					}
					if in["runtime"] != "server" || in["pool_id"] != "pool" {
						t.Errorf("placement %+v", in)
					}
				}
				if r.Method == http.MethodDelete {
					w.WriteHeader(204)
					return
				}
				_, _ = w.Write([]byte(`{"id":"cloud","clouds":[],"targets":[],"syncs":[],"status":"succeeded"}`))
			})
			if err := app.Run(args); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("requests=%d", calls)
			}
		})
	}
}
func TestInfraServerLifecycleTypedReviews(t *testing.T) {
	for _, kind := range []string{"create", "adopt", "resize", "disconnect", "delete"} {
		t.Run(kind, func(t *testing.T) {
			app, out := infraTestApp(t, `{"kind":"`+kind+`","cloud_revision":4,"server_revision":7,"confirm_adoption":true}`, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/v1/clouds/cloud/servers/preview" {
					t.Errorf("%s %s", r.Method, r.URL.Path)
				}
				var in map[string]any
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Error(err)
				}
				if in["kind"] != kind || in["cloud_revision"] != float64(4) || in["server_revision"] != float64(7) {
					t.Error(in)
				}
				_, _ = w.Write([]byte(`{"id":"op","review":"digest","input":{"kind":"` + kind + `"},"expires_at":"2026-10-23T12:00:00Z","estimated_hourly_usd":0.1,"actions":[],"status":"preview"}`))
			})
			if err := app.Run([]string{"cloud-servers", "preview", "cloud", "--file", "-"}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "estimated_hourly_usd") || !strings.Contains(out.String(), "expires_at") {
				t.Fatal(out.String())
			}
		})
	}
}
func TestInfraSavedReviewDenialStalenessAndProgress(t *testing.T) {
	for _, command := range []string{"cloud-servers", "domain-routes"} {
		for _, scenario := range []string{"preview_only", "digest_mismatch", "denied", "stale", "unsupported", "waiting", "succeeded"} {
			t.Run(command+"/"+scenario, func(t *testing.T) {
				applied := 0
				var out *bytes.Buffer
				var app *App
				app, out = infraTestApp(t, "", func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/v1/projects" {
						_, _ = w.Write([]byte(`{"projects":[{"id":"p","slug":"app"}]}`))
						return
					}
					if r.Method == "POST" {
						applied++
						if !strings.Contains(out.String(), "reviewed") || !strings.Contains(out.String(), "digest") {
							t.Error("mutation preceded review output")
						}
						switch scenario {
						case "denied":
							w.WriteHeader(403)
						case "stale":
							w.WriteHeader(409)
						case "unsupported":
							w.WriteHeader(501)
						default:
							w.WriteHeader(202)
						}
						_, _ = w.Write([]byte(`{"id":"op","status":"pending"}`))
						return
					}
					status := "preview"
					if applied > 0 {
						status = scenario
					}
					_, _ = w.Write([]byte(`{"id":"op","review":"digest","status":"` + status + `","message":"reviewed","state_revision":5,"route":{"revision":7,"verification":{"dns":true,"tls":false,"backend":true}},"desired_record":{"value":"192.0.2.1","ttl":7200},"actions":[{"id":"action","status":"in-progress"}]}`))
				})
				scope := "cloud"
				if command == "domain-routes" {
					scope = "app"
				}
				args := []string{command, "apply", scope, "op", "--review", "digest"}
				if scenario != "preview_only" {
					args = append(args, "--yes")
				}
				if scenario == "digest_mismatch" {
					args[5] = "old"
				}
				if scenario == "waiting" || scenario == "succeeded" {
					args = append(args, "--wait", "--timeout", "1s")
				}
				err := app.Run(args)
				shouldFail := scenario != "preview_only" && scenario != "succeeded"
				if (err != nil) != shouldFail {
					t.Fatalf("err=%v output=%s", err, out)
				}
				if (scenario == "preview_only" || scenario == "digest_mismatch") && applied != 0 {
					t.Fatal("unreviewed mutation")
				}
				if scenario == "succeeded" && !strings.Contains(out.String(), "succeeded") {
					t.Fatal(out.String())
				}
			})
		}
	}
}
func TestInfraMoveSavesIntentAndExactNextScope(t *testing.T) {
	accepted := 0
	var out *bytes.Buffer
	var app *App
	app, out = infraTestApp(t, `{"project_id":"p","environment_id":"env","service":"web","pool_id":"pool","destination_server_id":"next","review":"digest"}`, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/clouds/cloud/placements/preview-move":
			_, _ = w.Write([]byte(`{"review":"digest","placement":{"server_id":"next","pool_id":"pool","pool_revision":4}}`))
		case "/api/v1/projects/p/environments":
			_, _ = w.Write([]byte(`{"environments":[{"id":"env","name":"staging"}]}`))
		case "/api/v1/clouds/cloud/placements/accept-move":
			accepted++
			if !strings.Contains(out.String(), "pool_revision") {
				t.Error("move not shown")
			}
			_, _ = w.Write([]byte(`{"review":"digest"}`))
		default:
			t.Errorf("unexpected action %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	start := time.Now().UTC()
	if err := app.Run([]string{"cloud-pools", "move", "cloud", "--file", "-", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if accepted != 1 {
		t.Fatal(accepted)
	}
	for _, want := range []string{"intent_saved", "15 minutes", "local_review_and_approve_by", "loomctl plan 'p' --env 'staging' --json", "no deployment has started"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(out.Bytes()))
	var preview any
	if err := decoder.Decode(&preview); err != nil {
		t.Fatal(err)
	}
	var result struct {
		LocalDeadline time.Time `json:"local_review_and_approve_by"`
		NextApproval  string    `json:"next_approval_command"`
	}
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.LocalDeadline.Before(start.Add(15*time.Minute)) || result.LocalDeadline.After(time.Now().UTC().Add(15*time.Minute)) {
		t.Fatalf("invalid local deadline: %s", result.LocalDeadline)
	}
	if result.NextApproval != "loomctl approve <plan-id-returned-by-preview> --yes" {
		t.Fatal(result.NextApproval)
	}
}
func TestInfraDrainUsesExactInventoryAndTypedInputsRejectBypass(t *testing.T) {
	for _, review := range []string{"old", "current"} {
		t.Run(review, func(t *testing.T) {
			applied := 0
			app, _ := infraTestApp(t, "", func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "preview") {
					_, _ = w.Write([]byte(`{"review":"current","server_revision":4,"reservations":2,"usage":{"workloads":[{"phase":"serving"}]}}`))
					return
				}
				applied++
				_, _ = w.Write([]byte(`{"id":"server","draining":true}`))
			})
			err := app.Run([]string{"cloud-servers", "drain", "cloud", "server", "--yes", "--review", review})
			if (err != nil) != (review == "old") || applied != map[string]int{"old": 0, "current": 1}[review] {
				t.Fatalf("%d %v", applied, err)
			}
		})
	}
	app, _ := infraTestApp(t, `{"kind":"create","url":"/unreviewed-operation"}`, func(_ http.ResponseWriter, _ *http.Request) { t.Error("invalid input reached API") })
	if err := app.Run([]string{"cloud-servers", "preview", "cloud", "--file", "-"}); err == nil {
		t.Fatal("unknown input accepted")
	}
}
func TestInfraDomainCommandsPreserveProjectAndTTLAuthority(t *testing.T) {
	tests := []struct {
		args       []string
		path, body string
	}{
		{[]string{"domain-routes", "ttl", "app", "route", "--revision", "9", "--external-ttl-seconds", "7200"}, "/api/v1/projects/p/domain-routes/route/ttl", `"external_ttl_seconds":7200`},
		{[]string{"domain-routes", "remove", "app", "route", "--revision", "9", "--yes"}, "/api/v1/projects/p/domain-routes/route/remove", `"revision":9`},
		{[]string{"domain-routes", "history", "app", "route"}, "/api/v1/projects/p/domain-routes/route/operations", ""},
		{[]string{"domain-routes", "verify", "app", "route"}, "/api/v1/projects/p/domain-routes/route/verify", ""},
		{[]string{"cloud-grants", "grant", "cloud", "p", "--targets", "frontend,worker"}, "/api/v1/clouds/cloud/projects/p", `"target_ids":["frontend","worker"]`},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			app, _ := infraTestApp(t, "", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/projects" {
					_, _ = w.Write([]byte(`{"projects":[{"id":"p","slug":"app"}]}`))
					return
				}
				if r.URL.Path != tt.path {
					t.Error(r.URL.Path)
				}
				if tt.body != "" {
					var body bytes.Buffer
					if _, err := body.ReadFrom(r.Body); err != nil {
						t.Error(err)
					}
					if !strings.Contains(body.String(), tt.body) {
						t.Errorf("%s", body.String())
					}
				}
				_, _ = w.Write([]byte(`{"id":"op","status":"pending","operations":[]}`))
			})
			if err := app.Run(tt.args); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInfraZonesPoolsAndRoutesUseReviewedTypedInputs(t *testing.T) {
	tests := []struct {
		args              []string
		input, path, want string
	}{
		{[]string{"dns-zones", "create", "--file", "-"}, `{"name":"example.com","mode":"managed","connection_id":"dns-account","project_ids":["p"]}`, "/api/v1/dns-zones", `"connection_id":"dns-account"`},
		{[]string{"dns-zones", "create", "--file", "-"}, `{"name":"example.com","mode":"external","project_ids":["p"]}`, "/api/v1/dns-zones", `"mode":"external"`},
		{[]string{"dns-zones", "update", "zone", "--file", "-"}, `{"revision":4,"project_ids":["p"],"renew_proof":true}`, "/api/v1/dns-zones/zone/update", `"revision":4`},
		{[]string{"dns-zones", "verify", "zone"}, "", "/api/v1/dns-zones/zone/verify", ""},
		{[]string{"dns-zones", "remove", "zone", "--revision", "4", "--yes"}, "", "/api/v1/dns-zones/zone/remove", `"revision":4`},
		{[]string{"cloud-pools", "save", "cloud", "pool", "--file", "-"}, `{"name":"Shared","revision":4,"members":[{"server_id":"s","cpu_millis":250,"memory_bytes":1024,"draining":false}]}`, "/api/v1/clouds/cloud/pools/pool", `"cpu_millis":250`},
		{[]string{"domain-routes", "preview", "app", "--file", "-"}, `{"zone_id":"zone","cloud_id":"cloud","environment_id":"env","service":"web","hostname":"app.example.com","port":8080,"external_ttl_seconds":7200}`, "/api/v1/projects/p/domain-routes/preview", `"external_ttl_seconds":7200`},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " ")+tt.input, func(t *testing.T) {
			app, _ := infraTestApp(t, tt.input, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/projects" {
					_, _ = w.Write([]byte(`{"projects":[{"id":"p","slug":"app"}]}`))
					return
				}
				if r.Method != "POST" || r.URL.Path != tt.path {
					t.Errorf("%s %s", r.Method, r.URL.Path)
				}
				if tt.want != "" {
					var body bytes.Buffer
					if _, err := body.ReadFrom(r.Body); err != nil {
						t.Error(err)
					}
					if !strings.Contains(body.String(), tt.want) {
						t.Error(body.String())
					}
				}
				if strings.HasSuffix(tt.path, "/remove") {
					w.WriteHeader(204)
				} else {
					_, _ = w.Write([]byte(`{"id":"resource","review":"digest","status":"preview"}`))
				}
			})
			if err := app.Run(tt.args); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestInfraMoveRejectsStaleReviewOrDifferentEnvironmentBeforeAcceptance(t *testing.T) {
	for _, scenario := range []string{"stale", "different_environment", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			review := "digest"
			if scenario == "stale" {
				review = "old"
			}
			app, _ := infraTestApp(t, `{"project_id":"p","environment_id":"env","service":"web","pool_id":"pool","destination_server_id":"next","review":"`+review+`"}`, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/clouds/cloud/placements/preview-move":
					_, _ = w.Write([]byte(`{"review":"digest"}`))
				case "/api/v1/projects/p/environments":
					if scenario == "denied" {
						w.WriteHeader(403)
					}
					_, _ = w.Write([]byte(`{"environments":[{"id":"other","name":"production"}]}`))
				default:
					t.Error("accepted without matching scope", r.URL.Path)
				}
			})
			if err := app.Run([]string{"cloud-pools", "move", "cloud", "--file", "-", "--yes"}); err == nil {
				t.Fatal("invalid move accepted")
			}
		})
	}
}

func TestInfraReservationRecoveryKeepsReviewedAuthorityAndStorage(t *testing.T) {
	for _, scenario := range []string{"preview", "success", "stale", "changed_after_review", "denied", "cleanup_blocked"} {
		t.Run(scenario, func(t *testing.T) {
			recoveries := 0
			review := "digest"
			if scenario == "stale" {
				review = "old"
			}
			var app *App
			var out *bytes.Buffer
			app, out = infraTestApp(t, `{"plan_id":"accepted-plan","service":"web","review":"`+review+`"}`, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/clouds/cloud/placements/preview-recovery" && r.URL.Path != "/api/v1/clouds/cloud/placements/recover" {
					t.Error(r.URL.Path)
				}
				var in map[string]any
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Error(err)
				}
				if in["plan_id"] != "accepted-plan" || in["service"] != "web" {
					t.Error(in)
				}
				if strings.HasSuffix(r.URL.Path, "/recover") {
					recoveries++
					if !strings.Contains(out.String(), `"revision": 9`) || !strings.Contains(out.String(), `"active": true`) {
						t.Error("accepted before displaying exact current execution")
					}
					if in["review"] != "digest" {
						t.Error(in)
					}
					switch scenario {
					case "changed_after_review", "cleanup_blocked":
						w.WriteHeader(409)
						_, _ = w.Write([]byte(`{"message":"exact owned cleanup requires reconciliation"}`))
						return
					case "denied":
						w.WriteHeader(403)
						_, _ = w.Write([]byte(`{"message":"permission denied"}`))
						return
					}
					_, _ = w.Write([]byte(`{"plan_id":"accepted-plan","service":"web","phase":"released","revision":10,"storage_retained":true}`))
					return
				}
				_, _ = w.Write([]byte(`{"plan_id":"accepted-plan","service":"web","server_id":"server","phase":"pending_recovery","started":true,"active":true,"revision":9,"review":"digest","storage_retained":false}`))
			})
			args := []string{"cloud-pools", "recover", "cloud", "--file", "-"}
			if scenario != "preview" {
				args = append(args, "--yes")
			}
			err := app.Run(args)
			if (err != nil) != (scenario != "preview" && scenario != "success") {
				t.Fatalf("%v %s", err, out)
			}
			if (scenario == "stale" || scenario == "preview") && recoveries != 0 {
				t.Fatal("unreviewed recovery")
			}
			if scenario == "success" && (!strings.Contains(out.String(), `"phase": "released"`) || !strings.Contains(out.String(), `"storage_retained": true`)) {
				t.Fatal(out.String())
			}
		})
	}
}

func TestCloudOrganizationTimeoutPrintsUsableResumeScope(t *testing.T) {
	app, out := infraTestApp(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/clouds/syncs/sync" {
			t.Error(r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"sync","status":"running","attempt":2}`))
	})
	err := app.Run([]string{"cloud-syncs", "wait", "sync", "--organization", "--timeout", "100ms"})
	if err == nil || !strings.Contains(err.Error(), "cloud-syncs show --organization sync") || !strings.Contains(out.String(), `"attempt": 2`) {
		t.Fatalf("%v %s", err, out)
	}
}
