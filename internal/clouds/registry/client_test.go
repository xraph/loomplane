package registry

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegistry_checksPullAndCancelsPushProbe(t *testing.T) {
	canceled := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "deploy" || password != "test-password" {
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(401)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v2/":
			w.WriteHeader(200)
		case "GET /v2/team/loomplane-check/tags/list":
			w.WriteHeader(404)
		case "POST /v2/team/loomplane-check/blobs/uploads/":
			w.Header().Set("Location", "/v2/team/loomplane-check/blobs/uploads/probe")
			w.WriteHeader(202)
		case "DELETE /v2/team/loomplane-check/blobs/uploads/probe":
			canceled = true
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(500)
		}
	}))
	defer s.Close()
	cfg, err := ParseConfig([]byte(`{"username":"deploy","password":"test-password"}`), map[string]string{"endpoint": s.URL, "repository": "team"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(cfg).Check(t.Context())
	if err != nil || len(result.Missing) != 0 || !canceled {
		t.Fatalf("permissions/cancel: %+v %v canceled=%v", result, err, canceled)
	}
	for _, v := range result.Details {
		if strings.Contains(v, "test-password") {
			t.Fatal("credential disclosed")
		}
	}
}

func TestRegistry_refusedCredentialAndDeniedPush(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if status == 401 || r.Method == http.MethodPost {
					w.WriteHeader(status)
					return
				}
				w.WriteHeader(200)
			}))
			defer s.Close()
			cfg, err := ParseConfig([]byte(`{"token":"test-token"}`), map[string]string{"endpoint": s.URL, "repository": "team"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := New(cfg).Check(t.Context())
			if status == 401 && !errors.Is(err, ErrRejected) {
				t.Fatalf("refusal: %v", err)
			}
			if status == 403 && (err != nil || len(result.Missing) != 1 || result.Missing[0] != "registry:push") {
				t.Fatalf("missing permission: %+v %v", result, err)
			}
		})
	}
}

func TestRegistry_doesNotSendCredentialsToAnUnapprovedRealm(t *testing.T) {
	contacted := false
	realm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		contacted = true
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "stolen"})
	}))
	defer realm.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm.URL+`/token",service="registry",scope="repository:team:pull,push"`)
		w.WriteHeader(401)
	}))
	defer s.Close()
	cfg, err := ParseConfig([]byte(`{"username":"deploy","password":"secret"}`), map[string]string{"endpoint": s.URL, "repository": "team"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = New(cfg).Check(t.Context()); !errors.Is(err, ErrInvalid) || contacted {
		t.Fatalf("unsafe realm: %v contacted=%v", err, contacted)
	}
}

func TestRegistry_rejectsUnsafeConfiguration(t *testing.T) {
	for _, settings := range []map[string]string{
		{"endpoint": "http://registry.example.com", "repository": "team"},
		{"endpoint": "https://user:password@registry.example.com", "repository": "team"},
		{"endpoint": "https://registry.example.com/path", "repository": "team"},
		{"endpoint": "https://registry.example.com", "repository": "../team"},
	} {
		if _, err := ParseConfig([]byte(`{}`), settings); err == nil {
			t.Fatalf("accepted: %+v", settings)
		}
	}
}

func TestRegistry_approvedTokenServiceUsesReviewedRepositoryScope(t *testing.T) {
	var scope string
	realm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "deploy" || pass != "password" {
			t.Error("missing token-service authentication")
		}
		scope = r.URL.Query().Get("scope")
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "scoped-token"})
	}))
	defer realm.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scoped-token" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm.URL+`/token",service="registry",scope="repository:other:pull,push"`)
			w.WriteHeader(401)
			return
		}
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Location", "/v2/team/loomplane-check/blobs/uploads/probe")
			w.WriteHeader(202)
		case http.MethodDelete:
			w.WriteHeader(204)
		default:
			w.WriteHeader(200)
		}
	}))
	defer s.Close()
	cfg, err := ParseConfig([]byte(`{"username":"deploy","password":"password"}`), map[string]string{"endpoint": s.URL, "repository": "team", "auth_endpoint": realm.URL + "/token"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = New(cfg).Check(t.Context()); err != nil || scope != "repository:team/loomplane-check:pull,push" {
		t.Fatalf("token scope: %s %v", scope, err)
	}
}

func TestDockerAuth_brokersOnlyTheActualRepositoryAndNeverDelegatesThePassword(t *testing.T) {
	var scope string
	realm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "deploy" || pass != "original-password" {
			t.Error("token-service credential absent")
		}
		scope = r.URL.Query().Get("scope")
		_, _ = w.Write([]byte(`{"token":"repository-token"}`))
	}))
	defer realm.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("probe delegated a credential")
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm.URL+`/token",service="registry",scope="repository:foreign:pull,push"`)
		w.WriteHeader(401)
	}))
	defer api.Close()
	cfg, err := ParseConfig([]byte(`{"username":"deploy","password":"original-password"}`), map[string]string{"endpoint": api.URL, "repository": "team", "auth_endpoint": realm.URL + "/token"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := New(cfg).DockerAuth(t.Context(), "team/actual", "pull,push")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "original-password") || strings.Contains(string(raw), "identitytoken") || !strings.Contains(string(raw), `"registrytoken":"repository-token"`) || scope != "repository:team/actual:pull,push" {
		t.Fatalf("delegated auth %s, scope %s", raw, scope)
	}
	cfg.AuthEndpoint = ""
	if _, err = New(cfg).DockerAuth(t.Context(), "team/actual", "pull"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("changed unreviewed realm accepted: %v", err)
	}
}

func TestDockerAuth_refusesBasicOnlyAndForeignRepositories(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
		w.WriteHeader(401)
	}))
	defer api.Close()
	cfg, err := ParseConfig([]byte(`{"username":"deploy","password":"secret"}`), map[string]string{"endpoint": api.URL, "repository": "team"})
	if err != nil {
		t.Fatal(err)
	}
	for _, repository := range []string{"team/actual", "other/actual"} {
		if _, err = New(cfg).DockerAuth(t.Context(), repository, "pull"); err == nil {
			t.Fatalf("unsafe auth accepted: %s", repository)
		}
	}
}

func TestDockerAuth_publicReadsAndPrivatePushUseTheirOwnChallenges(t *testing.T) {
	var endpoint string
	pushes, canceled := 0, false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			user, password, ok := r.BasicAuth()
			if !ok || user != "deploy" || password != "secret" || r.URL.Query().Get("scope") != "repository:team/actual:pull,push" {
				t.Errorf("wrong scoped issuer request: %s", r.URL)
			}
			_, _ = w.Write([]byte(`{"token":"push-token"}`))
		case r.Method == http.MethodPost:
			pushes++
			if pushes == 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+endpoint+`/token",service="registry"`)
				w.WriteHeader(401)
			} else {
				w.Header().Set("Location", "/v2/team/actual/blobs/uploads/probe")
				w.WriteHeader(202)
			}
		case r.Method == http.MethodDelete:
			if r.URL.Path != "/v2/team/actual/blobs/uploads/probe" {
				t.Error("probe cleanup escaped its repository")
			}
			canceled = true
			w.WriteHeader(204)
		default:
			w.WriteHeader(200)
		}
	}))
	defer api.Close()
	endpoint = api.URL
	cfg, err := ParseConfig([]byte(`{"username":"deploy","password":"secret"}`), map[string]string{"endpoint": endpoint, "repository": "team"})
	if err != nil {
		t.Fatal(err)
	}
	client := New(cfg)
	pull, err := client.DockerAuth(t.Context(), "team/actual", "pull")
	if err != nil || strings.Contains(string(pull), "secret") || strings.Contains(string(pull), "push-token") {
		t.Fatalf("anonymous pull auth: %s %v", pull, err)
	}
	push, err := client.DockerAuth(t.Context(), "team/actual", "pull,push")
	if err != nil || !strings.Contains(string(push), "push-token") || strings.Contains(string(push), "secret") {
		t.Fatalf("private push auth: %s %v", push, err)
	}
	public, err := client.DockerAuth(t.Context(), "team/actual", "pull,push")
	if err != nil || strings.Contains(string(public), "secret") || !canceled {
		t.Fatalf("anonymous push probe cleanup: auth=%s err=%v canceled=%v", public, err, canceled)
	}
}
