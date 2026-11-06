package vercel

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// envCall is one request a fake Vercel saw while variables were written.
type envCall struct {
	Method, Path string
	Query        url.Values
	Body         json.RawMessage
}

func (c envCall) String() string { return c.Method + " " + c.Path }

// envAPI is a fake Vercel that lists list as the project's variables and
// records every request. patch, when set, answers a PATCH instead of the
// default success.
type envAPI struct {
	list  string
	patch func(w http.ResponseWriter, r *http.Request) bool
	calls []envCall
}

func (a *envAPI) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		a.calls = append(a.calls, envCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Body: body})
		switch r.Method {
		case http.MethodGet:
			list := a.list
			if list == "" {
				list = `{"envs":[]}`
			}
			fmt.Fprint(w, list)
		case http.MethodPatch:
			if a.patch != nil && a.patch(w, r) {
				return
			}
			fmt.Fprint(w, `{}`)
		case http.MethodPost:
			fmt.Fprint(w, `{"created":[],"failed":[]}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// only returns the calls with this method.
func (a *envAPI) only(method string) []envCall {
	var out []envCall
	for _, c := range a.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// noUpsert fails the test if any request asked Vercel to upsert: upsert
// matches by key alone, so it overwrites another environment's row.
func (a *envAPI) noUpsert(t *testing.T) {
	t.Helper()
	for _, c := range a.calls {
		if c.Query.Has("upsert") {
			t.Errorf("%s asked to upsert: %s", c, c.Query.Encode())
		}
	}
}

// items decodes a POST body, which is one item or a list of them.
func items(t *testing.T, body json.RawMessage) []map[string]any {
	t.Helper()
	var many []map[string]any
	if json.Unmarshal(body, &many) == nil {
		return many
	}
	var one map[string]any
	if err := json.Unmarshal(body, &one); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	return []map[string]any{one}
}

func object(t *testing.T, body json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	return m
}

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
			a := &envAPI{}
			srv := a.serve(t)
			err := New(srv.URL).SetEnv(t.Context(), Token{Value: "t"}, "prj_1", tc.target,
				map[string]string{"A": "1", "S": "2"}, map[string]bool{"S": true})
			if err != nil {
				t.Fatalf("SetEnv: %v", err)
			}
			a.noUpsert(t)
			for _, c := range a.calls {
				if c.Method == http.MethodGet {
					if c.Path != "/v10/projects/prj_1/env" || c.Query.Get("decrypt") == "true" {
						t.Errorf("list: %s %s", c, c.Query.Encode())
					}
					continue
				}
				if c.Method != http.MethodPost || c.Path != "/v10/projects/prj_1/env" {
					t.Errorf("request: %s", c)
				}
			}
			posts := a.only(http.MethodPost)
			if len(posts) != 1 {
				t.Fatalf("%d creates, want one batch: %v", len(posts), a.calls)
			}
			got := items(t, posts[0].Body)
			if len(got) != 2 {
				t.Fatalf("%d items, want 2", len(got))
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

// TestSetEnvNeverTouchesAnotherTargetsRow: production already has
// DATABASE_URL. Writing staging's must leave that row alone and add a
// row of its own, never upserting over production's by key.
func TestSetEnvNeverTouchesAnotherTargetsRow(t *testing.T) {
	a := &envAPI{list: `{"envs":[{"id":"p1","key":"DATABASE_URL","type":"encrypted","target":["production"]}]}`}
	srv := a.serve(t)
	err := New(srv.URL).SetEnv(t.Context(), Token{Value: "t"}, "prj", Target{CustomID: "env_9"},
		map[string]string{"DATABASE_URL": "postgres://staging"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.noUpsert(t)
	for _, c := range a.calls {
		if (c.Method == http.MethodPatch || c.Method == http.MethodDelete) && strings.HasSuffix(c.Path, "/p1") {
			t.Errorf("touched production's row: %s %s", c, c.Body)
		}
	}
	if n := len(a.only(http.MethodPatch)) + len(a.only(http.MethodDelete)); n != 0 {
		t.Errorf("%d updates or deletes: %v", n, a.calls)
	}
	posts := a.only(http.MethodPost)
	if len(posts) != 1 || posts[0].Path != "/v10/projects/prj/env" {
		t.Fatalf("creates: %v", posts)
	}
	got := items(t, posts[0].Body)
	if len(got) != 1 {
		t.Fatalf("items: %v", got)
	}
	item := got[0]
	if item["key"] != "DATABASE_URL" || item["value"] != "postgres://staging" || !reflect.DeepEqual(item["customEnvironmentIds"], []any{"env_9"}) {
		t.Errorf("item: %v", item)
	}
	if _, has := item["target"]; has {
		t.Errorf("item has a target key: %v", item)
	}
}

// TestSetEnvUpdatesItsOwnRowInPlace: a row for this target alone is
// updated where it is.
func TestSetEnvUpdatesItsOwnRowInPlace(t *testing.T) {
	a := &envAPI{list: `{"envs":[
		{"id":"p1","key":"DATABASE_URL","type":"encrypted","target":["production"]},
		{"id":"e1","key":"DATABASE_URL","type":"encrypted","customEnvironmentIds":["env_9"]}]}`}
	srv := a.serve(t)
	err := New(srv.URL).SetEnv(t.Context(), Token{Value: "t"}, "prj", Target{CustomID: "env_9"},
		map[string]string{"DATABASE_URL": "postgres://new"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.noUpsert(t)
	if posts := a.only(http.MethodPost); len(posts) != 0 {
		t.Errorf("created: %v", posts)
	}
	if dels := a.only(http.MethodDelete); len(dels) != 0 {
		t.Errorf("deleted: %v", dels)
	}
	patches := a.only(http.MethodPatch)
	if len(patches) != 1 || patches[0].Path != "/v9/projects/prj/env/e1" {
		t.Fatalf("updates: %v", patches)
	}
	body := object(t, patches[0].Body)
	if body["value"] != "postgres://new" || body["type"] != "encrypted" || !reflect.DeepEqual(body["customEnvironmentIds"], []any{"env_9"}) {
		t.Errorf("update body: %v", body)
	}
	if _, has := body["target"]; has {
		t.Errorf("update body has a target key: %v", body)
	}
}

// TestSetEnvSplitsASharedRow: a row shared by production and preview
// keeps production's value; preview gets a row of its own.
func TestSetEnvSplitsASharedRow(t *testing.T) {
	a := &envAPI{list: `{"envs":[{"id":"s1","key":"API_KEY","type":"encrypted","target":["production","preview"]}]}`}
	srv := a.serve(t)
	err := New(srv.URL).SetEnv(t.Context(), Token{Value: "t"}, "prj", Preview,
		map[string]string{"API_KEY": "preview-key"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.noUpsert(t)
	patches := a.only(http.MethodPatch)
	if len(patches) != 1 || patches[0].Path != "/v9/projects/prj/env/s1" {
		t.Fatalf("updates: %v", patches)
	}
	body := object(t, patches[0].Body)
	if !reflect.DeepEqual(body, map[string]any{"target": []any{"production"}}) {
		t.Errorf("shared row update = %v, want only target [production]", body)
	}
	posts := a.only(http.MethodPost)
	if len(posts) != 1 {
		t.Fatalf("creates: %v", posts)
	}
	got := items(t, posts[0].Body)
	if len(got) != 1 || got[0]["key"] != "API_KEY" || got[0]["value"] != "preview-key" || !reflect.DeepEqual(got[0]["target"], []any{"preview"}) {
		t.Errorf("created %v", got)
	}
	if _, has := got[0]["customEnvironmentIds"]; has {
		t.Errorf("created row has customEnvironmentIds: %v", got[0])
	}
	// The shared row is narrowed before the new row is made, or Vercel
	// would see two preview rows for one key.
	var order []string
	for _, c := range a.calls {
		if c.Method != http.MethodGet {
			order = append(order, c.Method)
		}
	}
	if !reflect.DeepEqual(order, []string{http.MethodPatch, http.MethodPost}) {
		t.Errorf("order %v", order)
	}
}

// TestSetEnvSplitsACustomEnvironmentOutOfASharedRow: a row shared with
// another custom environment loses only this one.
func TestSetEnvSplitsACustomEnvironmentOutOfASharedRow(t *testing.T) {
	a := &envAPI{list: `{"envs":[{"id":"s2","key":"API_KEY","type":"encrypted","target":["production"],"customEnvironmentIds":["env_8","env_9"]}]}`}
	srv := a.serve(t)
	err := New(srv.URL).SetEnv(t.Context(), Token{Value: "t"}, "prj", Target{CustomID: "env_9"},
		map[string]string{"API_KEY": "staging-key"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	patches := a.only(http.MethodPatch)
	if len(patches) != 1 || patches[0].Path != "/v9/projects/prj/env/s2" {
		t.Fatalf("updates: %v", patches)
	}
	if body := object(t, patches[0].Body); !reflect.DeepEqual(body, map[string]any{"customEnvironmentIds": []any{"env_8"}}) {
		t.Errorf("shared row update = %v", body)
	}
	if posts := a.only(http.MethodPost); len(posts) != 1 {
		t.Fatalf("creates: %v", posts)
	}
}

// TestSetEnvLeavesBranchRowsAlone: a preview row for one git branch is
// its own scope, not this target's row.
func TestSetEnvLeavesBranchRowsAlone(t *testing.T) {
	a := &envAPI{list: `{"envs":[{"id":"b1","key":"API_KEY","type":"encrypted","target":["preview"],"gitBranch":"feature-1"}]}`}
	srv := a.serve(t)
	if err := New(srv.URL).SetEnv(t.Context(), Token{Value: "t"}, "prj", Preview, map[string]string{"API_KEY": "v"}, nil); err != nil {
		t.Fatal(err)
	}
	if n := len(a.only(http.MethodPatch)) + len(a.only(http.MethodDelete)); n != 0 {
		t.Errorf("touched the branch row: %v", a.calls)
	}
	if posts := a.only(http.MethodPost); len(posts) != 1 {
		t.Fatalf("creates: %v", posts)
	}
}

// TestSetEnvNeverWritesASecretIntoAPlainRow: a secret whose own row is
// not sensitive gets a new sensitive row; its value is never sent to the
// old one.
func TestSetEnvNeverWritesASecretIntoAPlainRow(t *testing.T) {
	a := &envAPI{list: `{"envs":[{"id":"e1","key":"TOKEN","type":"encrypted","target":["production"]}]}`}
	srv := a.serve(t)
	err := New(srv.URL).SetEnv(t.Context(), Token{Value: "t"}, "prj", Production,
		map[string]string{"TOKEN": "s3cret"}, map[string]bool{"TOKEN": true})
	if err != nil {
		t.Fatal(err)
	}
	if patches := a.only(http.MethodPatch); len(patches) != 0 {
		t.Errorf("sent the secret to a non-sensitive row: %v", patches)
	}
	dels := a.only(http.MethodDelete)
	if len(dels) != 1 || dels[0].Path != "/v9/projects/prj/env/e1" {
		t.Fatalf("deletes: %v", dels)
	}
	posts := a.only(http.MethodPost)
	if len(posts) != 1 {
		t.Fatalf("creates: %v", posts)
	}
	got := items(t, posts[0].Body)
	if len(got) != 1 || got[0]["type"] != "sensitive" || got[0]["value"] != "s3cret" || !reflect.DeepEqual(got[0]["target"], []any{"production"}) {
		t.Errorf("created %v", got)
	}
}

// TestSetEnvRecreatesARowVercelWontRetype: when Vercel refuses to change
// a row's type, the row is deleted and made again.
func TestSetEnvRecreatesARowVercelWontRetype(t *testing.T) {
	a := &envAPI{
		list: `{"envs":[{"id":"e1","key":"TOKEN","type":"sensitive","target":["preview"]}]}`,
		patch: func(w http.ResponseWriter, _ *http.Request) bool {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"code":"bad_request","message":"cannot change the type of a sensitive variable"}}`)
			return true
		},
	}
	srv := a.serve(t)
	err := New(srv.URL).SetEnv(t.Context(), Token{Value: "t"}, "prj", Preview, map[string]string{"TOKEN": "now-plain"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, c := range a.calls {
		if c.Method != http.MethodGet {
			order = append(order, c.String())
		}
	}
	want := []string{"PATCH /v9/projects/prj/env/e1", "DELETE /v9/projects/prj/env/e1", "POST /v10/projects/prj/env"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("calls %v, want %v", order, want)
	}
	if got := items(t, a.only(http.MethodPost)[0].Body); got[0]["type"] != "encrypted" || !reflect.DeepEqual(got[0]["target"], []any{"preview"}) {
		t.Errorf("created %v", got)
	}
}

// TestSetEnvReportsFailedCreates: Vercel answers 201 even when some
// variables weren't created, listing them as failed.
func TestSetEnvReportsFailedCreates(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"envs":[]}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"created":[],"failed":[{"error":{"code":"ENV_ALREADY_EXISTS","message":"A variable with the name A already exists","key":"A"}}]}`)
	}))
	defer api.Close()
	err := New(api.URL).SetEnv(t.Context(), Token{Value: "t"}, "prj", Preview, map[string]string{"A": "1"}, nil)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("SetEnv = %v, want the failed variable reported", err)
	}
}

// TestSetEnvListsEveryPage: rows on a later page of the listing count.
func TestSetEnvListsEveryPage(t *testing.T) {
	var patched []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("until") == "" {
				fmt.Fprint(w, `{"envs":[{"id":"x1","key":"OTHER","type":"encrypted","target":["preview"]}],"pagination":{"count":1,"next":42,"prev":null}}`)
				return
			}
			fmt.Fprint(w, `{"envs":[{"id":"e1","key":"A","type":"encrypted","target":["preview"]}],"pagination":{"count":1,"next":null,"prev":42}}`)
		case http.MethodPatch:
			patched = append(patched, r.URL.Path)
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer api.Close()
	if err := New(api.URL).SetEnv(t.Context(), Token{Value: "t"}, "prj", Preview, map[string]string{"A": "1"}, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(patched, []string{"/v9/projects/prj/env/e1"}) {
		t.Fatalf("patched %v", patched)
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
			_ = json.NewDecoder(r.Body).Decode(&b)
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
		{"paid plan", &APIError{Status: 402, Message: "Custom environments need a Pro plan"}, "Vercel says this needs a paid plan: Custom environments need a Pro plan", false, true},
		{"paid plan, no message", &APIError{Status: 402}, "Vercel says this needs a paid plan", false, true},
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

// TestEmptyTargetIsRefused: a zero Target names no environment. Written as
// a variable it would have no scope, and as a deploy it would silently be
// Preview, so nothing is sent.
func TestEmptyTargetIsRefused(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request: %s %s", r.Method, r.URL)
		fmt.Fprint(w, `{}`)
	}))
	defer api.Close()
	c, tok := New(api.URL), Token{Value: "t"}
	if err := c.SetEnv(t.Context(), tok, "prj_1", Target{}, map[string]string{"A": "1"}, nil); err == nil {
		t.Error("SetEnv wrote variables for no target")
	}
	if _, err := c.CreateDeploymentWithRuntimeEnv(t.Context(), tok, &Project{ID: "prj_1", Name: "web"}, Target{}, nil, nil, nil, nil); err == nil {
		t.Error("CreateDeploymentWithRuntimeEnv deployed to no target")
	}
	if _, err := c.CreateDeployment(t.Context(), tok, &Project{ID: "prj_1", Name: "web"}, Target{}, nil, nil); err == nil {
		t.Error("CreateDeployment deployed to no target")
	}
	if _, err := c.TargetDeployments(t.Context(), tok, "prj_1", Target{}); err == nil {
		t.Error("TargetDeployments listed every deployment of the project")
	}
	if err := c.DeleteEnv(t.Context(), tok, "prj_1", Target{}); err == nil {
		t.Error("DeleteEnv ran for no target")
	}
}
