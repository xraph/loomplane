package vercel

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestSetEnvScopesToOneTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target Target
		want   map[string]any
	}{
		{"production", Production, map[string]any{"target": []any{"production"}}},
		{"preview", Preview, map[string]any{"target": []any{"preview"}}},
		{"custom", Target{CustomID: "env_9"}, map[string]any{"customEnvironmentIds": []any{"env_9"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []map[string]any
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v10/projects/prj_1/env" {
					t.Errorf("request: %s %s", r.Method, r.URL)
				}
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				fmt.Fprint(w, `{}`)
			}))
			defer api.Close()
			err := New(api.URL).SetEnv(t.Context(), Token{Value: "t"}, "prj_1", tc.target,
				map[string]string{"A": "1", "S": "2"}, map[string]bool{"S": true})
			if err != nil || len(got) != 2 {
				t.Fatalf("SetEnv: %v, %d items", err, len(got))
			}
			for _, item := range got {
				for _, k := range []string{"target", "customEnvironmentIds"} {
					if w, ok := tc.want[k]; ok {
						if !reflect.DeepEqual(item[k], w) {
							t.Errorf("%v: %s = %v, want %v", item["key"], k, item[k], w)
						}
					} else if _, has := item[k]; has {
						t.Errorf("%v has a %s key: %v", item["key"], k, item)
					}
				}
				wantType := "encrypted"
				if item["key"] == "S" {
					wantType = "sensitive"
				}
				if item["type"] != wantType {
					t.Errorf("%v type = %v", item["key"], item["type"])
				}
			}
		})
	}
}

func TestDeploymentBodyTargets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target Target
		want   map[string]any // absent keys must be absent from the body
	}{
		{"production", Production, map[string]any{"target": "production"}},
		{"preview", Preview, map[string]any{}},
		{"custom", Target{CustomID: "env_9"}, map[string]any{"customEnvironmentSlugOrId": "env_9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got map[string]any
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				fmt.Fprint(w, `{"id":"dpl_1"}`)
			}))
			defer api.Close()
			p := &Project{ID: "prj_1", Name: "app"}
			if _, err := New(api.URL).CreateDeployment(t.Context(), Token{Value: "t"}, p, tc.target, nil, nil); err != nil {
				t.Fatal(err)
			}
			for _, k := range []string{"target", "customEnvironmentSlugOrId"} {
				if w, ok := tc.want[k]; ok {
					if got[k] != w {
						t.Errorf("%s = %v, want %v", k, got[k], w)
					}
				} else if _, has := got[k]; has {
					t.Errorf("body has %s: %v", k, got)
				}
			}
		})
	}
}

func TestEnsureCustomEnvironmentReadsBackOnConflict(t *testing.T) {
	gets := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v9/projects/prj_1/custom-environments/staging":
			gets++
			if gets == 1 {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"error":{"code":"not_found","message":"nope"}}`)
				return
			}
			fmt.Fprint(w, `{"id":"env_9","slug":"staging"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v9/projects/prj_1/custom-environments":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b) //nolint:errcheck // asserted below
			if b["slug"] != "staging" {
				t.Errorf("body: %v", b)
			}
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":{"code":"conflict","message":"exists"}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer api.Close()
	e, created, err := New(api.URL).EnsureCustomEnvironmentCreated(t.Context(), Token{Value: "t"}, "prj_1", "staging")
	if err != nil || e.ID != "env_9" || created {
		t.Fatalf("got %+v, %v", e, err)
	}
}

func TestDeleteEnvOnlyTouchesTarget(t *testing.T) {
	var deleted []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("customEnvironmentId") != "env_9" {
				t.Errorf("list query: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"envs":[
				{"id":"e1","key":"A","customEnvironmentIds":["env_9"]},
				{"id":"e2","key":"A","target":["production"]},
				{"id":"e3","key":"B","customEnvironmentIds":["env_9","env_8"]}]}`)
		case http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
			fmt.Fprint(w, `{}`)
		}
	}))
	defer api.Close()
	if err := New(api.URL).DeleteEnv(t.Context(), Token{Value: "t"}, "prj_1", Target{CustomID: "env_9"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"/v9/projects/prj_1/env/e1"}; !reflect.DeepEqual(deleted, want) {
		t.Fatalf("deleted %v, want %v", deleted, want)
	}
}

func TestTargetDeployments(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v7/deployments" || q.Get("projectId") != "prj_1" || q.Get("target") != "" {
			t.Errorf("request: %s", r.URL)
		}
		if q.Get("until") == "" {
			fmt.Fprint(w, `{"deployments":[{"uid":"d3","customEnvironment":{"id":"env_9"}},{"uid":"d2"}],"pagination":{"next":50}}`)
			return
		}
		fmt.Fprint(w, `{"deployments":[{"uid":"d1","customEnvironment":{"id":"env_9"}}],"pagination":{"next":null}}`)
	}))
	defer api.Close()
	got, err := New(api.URL).TargetDeployments(t.Context(), Token{Value: "t"}, "prj_1", Target{CustomID: "env_9"})
	if err != nil || len(got) != 2 || got[0].ID != "d3" || got[1].ID != "d1" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestEnsureProjectReadsBackOnConflict(t *testing.T) {
	gets := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v9/projects/app":
			gets++
			if gets == 1 {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"error":{"code":"not_found","message":"nope"}}`)
				return
			}
			fmt.Fprint(w, `{"id":"prj_1","name":"app"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v10/projects":
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":{"code":"conflict","message":"exists"}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer api.Close()
	p, created, err := New(api.URL).EnsureProjectCreated(t.Context(), Token{Value: "t"}, "app", ProjectSettings{})
	if err != nil || p.ID != "prj_1" || created {
		t.Fatalf("got %+v, %v", p, err)
	}
}

func TestFallbackReason(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		reason    string
		permanent bool
		ok        bool
	}{
		{"paid plan", &APIError{Status: 402, Message: "pay"}, "Vercel says this needs a paid plan", false, true},
		{"forbidden", fmt.Errorf("%w: %w", ErrRejected, &APIError{Status: 403, Message: "no custom environments"}), "Vercel refused: no custom environments", false, true},
		{"reserved slug", &APIError{Status: 400, Message: "The slug \"production\" is reserved"}, "Vercel reserves that environment name: The slug \"production\" is reserved", true, true},
		{"other 400", &APIError{Status: 400, Message: "bad body"}, "", false, false},
		{"rate limited", &APIError{Status: 429}, "", false, false},
		{"unavailable", &APIError{Status: 503}, "", false, false},
		{"transport", errors.New("vercel: connection reset"), "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason, permanent, ok := FallbackReason(tc.err)
			if reason != tc.reason || permanent != tc.permanent || ok != tc.ok {
				t.Fatalf("got %q %v %v", reason, permanent, ok)
			}
		})
	}
}

func TestEnsureReportsWhatItCreated(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":"not_found","message":"nope"}}`)
		case r.URL.Path == "/v10/projects":
			fmt.Fprint(w, `{"id":"prj_1","name":"app"}`)
		case r.URL.Path == "/v9/projects/prj_1/custom-environments":
			fmt.Fprint(w, `{"id":"env_9","slug":"staging"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer api.Close()
	c := New(api.URL)
	p, created, err := c.EnsureProjectCreated(t.Context(), Token{Value: "t"}, "app", ProjectSettings{})
	if err != nil || p.ID != "prj_1" || !created {
		t.Fatalf("project: %+v, %v, %v", p, created, err)
	}
	e, created, err := c.EnsureCustomEnvironmentCreated(t.Context(), Token{Value: "t"}, "prj_1", "staging")
	if err != nil || e.ID != "env_9" || !created {
		t.Fatalf("custom environment: %+v, %v, %v", e, created, err)
	}
}

func TestEnsureFoundOrRacedIsNotCreated(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v9/projects/app":
			fmt.Fprint(w, `{"id":"prj_1","name":"app"}`)
		case "/v9/projects/prj_1/custom-environments/staging":
			fmt.Fprint(w, `{"id":"env_9","slug":"staging"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer api.Close()
	c := New(api.URL)
	if _, created, err := c.EnsureProjectCreated(t.Context(), Token{Value: "t"}, "app", ProjectSettings{}); err != nil || created {
		t.Fatalf("existing project: created %v, %v", created, err)
	}
	if _, created, err := c.EnsureCustomEnvironmentCreated(t.Context(), Token{Value: "t"}, "prj_1", "staging"); err != nil || created {
		t.Fatalf("existing custom environment: created %v, %v", created, err)
	}
}

func TestWaitDeploymentNeedsAnAliasOnlyInProduction(t *testing.T) {
	gets := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets++
		if r.URL.Path != "/v13/deployments/dpl_1" {
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
		if gets > 1 {
			fmt.Fprint(w, `{"id":"dpl_1","readyState":"READY","alias":["app.vercel.app"]}`)
			return
		}
		fmt.Fprint(w, `{"id":"dpl_1","readyState":"READY","url":"app-abc.vercel.app"}`)
	}))
	defer api.Close()
	c := New(api.URL)
	for _, target := range []Target{Preview, {CustomID: "env_9"}} {
		gets = 0
		d, err := c.WaitDeployment(t.Context(), Token{Value: "t"}, "dpl_1", target)
		if err != nil || gets != 1 || d.Address() != "https://app-abc.vercel.app" {
			t.Fatalf("%+v: got %+v after %d reads, %v", target, d, gets, err)
		}
	}
	gets = 0
	d, err := c.WaitDeployment(t.Context(), Token{Value: "t"}, "dpl_1", Production)
	if err != nil || gets != 2 || d.Address() != "https://app.vercel.app" {
		t.Fatalf("production: got %+v after %d reads, %v", d, gets, err)
	}
}
