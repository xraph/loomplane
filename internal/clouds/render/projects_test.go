package render

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type call struct{ method, path, body string }

// fakeRender answers each request with handle, and records every call.
func fakeRender(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, body string)) (*Client, func() []call) {
	t.Helper()
	var mu sync.Mutex
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, call{r.Method, r.URL.Path, string(b)})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		handle(w, r, string(b))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL), func() []call {
		mu.Lock()
		defer mu.Unlock()
		return append([]call(nil), calls...)
	}
}

func count(calls []call, method, path string) int {
	n := 0
	for _, c := range calls {
		if c.method == method && c.path == path {
			n++
		}
	}
	return n
}

func TestEnsureEnvironmentToleratesConflict(t *testing.T) {
	gets := 0
	c, calls := fakeRender(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/environments":
			gets++
			if gets == 1 {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[{"environment":{"id":"evm_1","name":"staging","projectId":"prj_1"}}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/environments":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"already exists"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	e, err := c.EnsureEnvironment(context.Background(), Key{Value: "k", OwnerID: "own_1"}, "prj_1", "staging")
	if err != nil || e.ID != "evm_1" {
		t.Fatalf("got %+v, %v", e, err)
	}
	if n := count(calls(), http.MethodPost, "/environments"); n != 1 {
		t.Fatalf("posts = %d", n)
	}
}

func TestEnsureProjectCreatesWithFirstEnvironment(t *testing.T) {
	c, calls := fakeRender(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"prj_1","name":"shop"}`))
	})
	p, err := c.EnsureProject(context.Background(), Key{Value: "k", OwnerID: "own_1"}, "shop", "production")
	if err != nil || p.ID != "prj_1" {
		t.Fatalf("got %+v, %v", p, err)
	}
	var got struct {
		Name         string              `json:"name"`
		OwnerID      string              `json:"ownerId"`
		Environments []map[string]string `json:"environments"`
	}
	for _, cl := range calls() {
		if cl.method == http.MethodPost {
			_ = json.Unmarshal([]byte(cl.body), &got)
		}
	}
	if got.Name != "shop" || got.OwnerID != "own_1" || len(got.Environments) != 1 || got.Environments[0]["name"] != "production" {
		t.Fatalf("body = %+v", got)
	}
}

func TestEnsureServiceCreatesInEnvironment(t *testing.T) {
	c, calls := fakeRender(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/services":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"service":{"id":"srv_1","environmentId":"evm_1"}}`))
		}
	})
	_, err := c.EnsureService(context.Background(), Key{Value: "k", OwnerID: "own_1"}, ServiceSpec{Type: WebService, Name: "api", EnvironmentID: "evm_1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, cl := range calls() {
		if cl.method == http.MethodPost && cl.path == "/services" {
			var b map[string]any
			_ = json.Unmarshal([]byte(cl.body), &b)
			if b["environmentId"] != "evm_1" {
				t.Fatalf("body = %s", cl.body)
			}
			return
		}
	}
	t.Fatal("no create")
}

func TestEnsureServiceMovesExistingService(t *testing.T) {
	c, calls := fakeRender(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/services":
			_, _ = w.Write([]byte(`[{"service":{"id":"srv_1","name":"api","environmentId":""}}]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})
	_, err := c.EnsureService(context.Background(), Key{Value: "k", OwnerID: "own_1"}, ServiceSpec{Type: WebService, Name: "api", EnvironmentID: "evm_1"})
	if err != nil {
		t.Fatal(err)
	}
	all := calls()
	if n := count(all, http.MethodPost, "/environments/evm_1/resources"); n != 1 {
		t.Fatalf("moves = %d", n)
	}
	for _, cl := range all {
		if cl.method == http.MethodDelete {
			t.Fatalf("deleted %s", cl.path)
		}
		if cl.path == "/environments/evm_1/resources" && !strings.Contains(cl.body, `"resourceIds":["srv_1"]`) {
			t.Fatalf("body = %s", cl.body)
		}
	}
}

func TestEnsureDatastoreMovesNeverRecreates(t *testing.T) {
	c, calls := fakeRender(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		if r.Method == http.MethodGet && r.URL.Path == "/postgres" {
			_, _ = w.Write([]byte(`[{"postgres":{"id":"dpg_1","name":"db","status":"available","environmentId":""}}]`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	d, err := c.EnsureDatastore(context.Background(), Key{Value: "k", OwnerID: "own_1"}, DatastoreSpec{Kind: Postgres, Name: "db", EnvironmentID: "evm_1"})
	if err != nil || d.ID != "dpg_1" {
		t.Fatalf("got %+v, %v", d, err)
	}
	all := calls()
	if n := count(all, http.MethodPost, "/environments/evm_1/resources"); n != 1 {
		t.Fatalf("moves = %d", n)
	}
	for _, cl := range all {
		if cl.method == http.MethodDelete || (cl.method == http.MethodPost && cl.path == "/postgres") {
			t.Fatalf("recreated: %s %s", cl.method, cl.path)
		}
	}
}

func TestEnvironmentEmpty(t *testing.T) {
	body := `{"id":"evm_1","databasesIds":[],"redisIds":[],"serviceIds":["srv_1"],"envGroupIds":[]}`
	c, _ := fakeRender(t, func(w http.ResponseWriter, _ *http.Request, _ string) { _, _ = w.Write([]byte(body)) })
	empty, err := c.EnvironmentEmpty(context.Background(), Key{Value: "k"}, "evm_1")
	if err != nil || empty {
		t.Fatalf("empty=%v err=%v", empty, err)
	}
	body = `{"id":"evm_1","databasesIds":[],"redisIds":[],"serviceIds":[],"envGroupIds":[]}`
	if empty, err = c.EnvironmentEmpty(context.Background(), Key{Value: "k"}, "evm_1"); err != nil || !empty {
		t.Fatalf("empty=%v err=%v", empty, err)
	}
}

func TestDeleteEnvironmentAndProjectTolerateNotFound(t *testing.T) {
	c, _ := fakeRender(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"gone"}`))
	})
	if err := c.DeleteEnvironment(context.Background(), Key{Value: "k"}, "evm_1"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteProject(context.Background(), Key{Value: "k"}, "prj_1"); err != nil {
		t.Fatal(err)
	}
}

func TestRenderFallbackReason(t *testing.T) {
	tests := []struct {
		name string
		err  error
		ok   bool
	}{
		{"payment required", &APIError{Status: 402, Message: "upgrade"}, true},
		{"rejected", fmt.Errorf("%w: nope", ErrRejected), true},
		{"plan limit", &APIError{Status: 400, Message: "Plan does not allow this"}, true},
		{"other 400", &APIError{Status: 400, Message: "name is invalid"}, false},
		{"429", &APIError{Status: 429, Message: "slow down"}, false},
		{"503", &APIError{Status: 503, Message: "down"}, false},
		{"transport", errors.New("render: connection refused"), false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, permanent, ok := FallbackReason(tt.err)
			if ok != tt.ok || permanent || (ok && reason == "") {
				t.Fatalf("got %q %v %v", reason, permanent, ok)
			}
		})
	}
}
