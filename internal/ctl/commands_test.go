package ctl

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/xraph/loomplane/pkg/client"
)

func TestNewRepositories(t *testing.T) {
	got, err := newRepositories([]string{
		"acme/web", " ", "billing=other/web", "https://github.com/acme/api", "mono = git@github.com:acme/mono.git",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []client.NewRepository{
		{Repository: "acme/web"},
		{Alias: "billing", Repository: "other/web"},
		{Repository: "https://github.com/acme/api"},
		{Alias: "mono", Repository: "git@github.com:acme/mono.git"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for _, bad := range []string{"=acme/web", "billing="} {
		if _, err := newRepositories([]string{bad}); err == nil {
			t.Fatalf("%q was taken", bad)
		}
	}
	if got, err := newRepositories(nil); err != nil || got != nil {
		t.Fatalf("no --repo gave %+v, %v", got, err)
	}
}

func TestCreateProject_sendsEachRepo(t *testing.T) {
	var got struct {
		Name         string                 `json:"name"`
		Repositories []client.NewRepository `json:"repositories"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/projects" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"proj_1","name":"Shop","slug":"shop"}`))
	}))
	t.Cleanup(srv.Close)
	env := map[string]string{EnvURL: srv.URL, EnvKey: "lp_test_key"}
	var out bytes.Buffer
	app := New(Options{Out: &out, Getenv: func(k string) string { return env[k] }, ConfigPath: t.TempDir() + "/none.json"})

	if err := app.Run([]string{"projects", "create", "Shop", "--repo", "acme/web", "--repo", "billing=other/web,acme/api"}); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	want := []client.NewRepository{{Repository: "acme/web"}, {Alias: "billing", Repository: "other/web"}, {Repository: "acme/api"}}
	if got.Name != "Shop" || !slices.Equal(got.Repositories, want) {
		t.Fatalf("sent %+v", got)
	}
}
