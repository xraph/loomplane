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
