package registry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
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
	var scopes []string
	var rootChallenge string
	realm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "deploy" || pass != "password" {
			t.Error("missing token-service authentication")
		}
		scopes = append(scopes, r.URL.Query().Get("scope"))
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "scoped-token"})
	}))
	defer realm.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scoped-token" {
			challenge := `Bearer realm="` + realm.URL + `/token",service="registry"`
			if r.URL.Path == "/v2/" {
				challenge += rootChallenge
			} else {
				challenge += `,scope="repository:other:pull,push"`
			}
			w.Header().Set("WWW-Authenticate", challenge)
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
	want := []string{"", "repository:team/loomplane-check:pull", "repository:team/loomplane-check:pull,push"}
	if _, err = New(cfg).Check(t.Context()); err != nil || !slices.Equal(scopes, want) {
		t.Fatalf("token scopes: %q %v", scopes, err)
	}
	rootChallenge = `,scope="repository:user/image:pull"`
	scopes = nil
	want[0] = "repository:team/loomplane-check:pull"
	if _, err = New(cfg).Check(t.Context()); err != nil || !slices.Equal(scopes, want) {
		t.Fatalf("token scopes with scoped API challenge: %q %v", scopes, err)
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

func TestTokenPushDenialShowsReasonWithoutProviderSecrets(t *testing.T) {
	for _, tt := range []struct{ body, want string }{
		{`{"id":"forbidden","message":"registry is read-only some-secret"}`, "read-only access"},
		{`{"errors":[{"message":"repository limit exceeded some-secret"}]}`, "repository limit"},
		{`{"message":"garbage collection in progress some-secret"}`, "garbage collection"},
		{"some-secret", "push access to wakflo/loomplane-check was denied"},
	} {
		r := &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(tt.body))}
		err := tokenDenial(r, "wakflo/loomplane-check", "pull,push")
		if !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), "some-secret") {
			t.Fatalf("unexpected denial: %v", err)
		}
	}
}

func TestRegistryReportsWhichStepExceededTheDeadline(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tags/list") {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer s.Close()
	cfg, err := ParseConfig([]byte("{}"), map[string]string{"endpoint": s.URL, "repository": "team"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err = New(cfg).Check(ctx); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "registry:pull") {
		t.Fatalf("unexpected timeout: %v", err)
	}
}

func TestRegistryClientConfiguresBoundedHTTP2Transport(t *testing.T) {
	cfg, err := ParseConfig([]byte("{}"), map[string]string{"endpoint": "https://registry.example.com", "repository": "team"})
	if err != nil {
		t.Fatal(err)
	}
	client := New(cfg)
	tr, ok := client.HTTP.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.HTTP.Transport)
	}
	if !tr.ForceAttemptHTTP2 {
		t.Fatal("expected ForceAttemptHTTP2 to be true")
	}
	if tr.Proxy == nil {
		t.Fatal("expected Proxy to be configured")
	}
	if tr.TLSHandshakeTimeout <= 0 {
		t.Fatalf("expected TLSHandshakeTimeout > 0, got %v", tr.TLSHandshakeTimeout)
	}
	if tr.ResponseHeaderTimeout <= 0 {
		t.Fatalf("expected ResponseHeaderTimeout > 0, got %v", tr.ResponseHeaderTimeout)
	}
	if client.HTTP.Timeout <= 0 || client.HTTP.Timeout > 20*time.Second {
		t.Fatalf("unexpected HTTP client timeout: %v", client.HTTP.Timeout)
	}
}

func TestRegistryCheckReusesBearerChallengeAndHandlesFallbacks(t *testing.T) {
	t.Run("reuses challenge across pull and push checks", func(t *testing.T) {
		var realmURL string
		var registryHits []string
		var tokenScopes []string
		realm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenScopes = append(tokenScopes, r.URL.Query().Get("scope"))
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "tok-" + r.URL.Query().Get("scope")})
		}))
		defer realm.Close()
		realmURL = realm.URL
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			registryHits = append(registryHits, r.Method+" "+r.URL.Path+" bearer="+strings.TrimPrefix(auth, "Bearer "))
			if !strings.HasPrefix(auth, "Bearer tok-") {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+realmURL+`/token",service="registry"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			switch r.Method {
			case http.MethodPost:
				w.Header().Set("Location", "/v2/team/loomplane-check/blobs/uploads/probe")
				w.WriteHeader(http.StatusAccepted)
			case http.MethodDelete:
				w.WriteHeader(http.StatusAccepted)
			default:
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer s.Close()
		cfg, err := ParseConfig([]byte(`{"username":"deploy","password":"pw"}`), map[string]string{
			"endpoint":      s.URL,
			"repository":    "team",
			"auth_endpoint": realmURL + "/token",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = New(cfg).Check(t.Context()); err != nil {
			t.Fatalf("check failed: %v", err)
		}
		wantHits := []string{
			"GET /v2/ bearer=Basic ZGVwbG95OnB3",
			"GET /v2/ bearer=tok-",
			"GET /v2/team/loomplane-check/tags/list bearer=tok-repository:team/loomplane-check:pull",
			"POST /v2/team/loomplane-check/blobs/uploads/ bearer=tok-repository:team/loomplane-check:pull,push",
			"DELETE /v2/team/loomplane-check/blobs/uploads/probe bearer=tok-repository:team/loomplane-check:pull,push",
		}
		if !slices.Equal(registryHits, wantHits) {
			t.Fatalf("registry hits = %q, want %q", registryHits, wantHits)
		}
		if len(tokenScopes) != 3 {
			t.Fatalf("token fetches = %q, want 3", tokenScopes)
		}
	})

	t.Run("falls back when push endpoint returns a different challenge", func(t *testing.T) {
		var realmURL string
		var services []string
		realm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			svc := r.URL.Query().Get("service")
			services = append(services, svc)
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "tok-" + svc})
		}))
		defer realm.Close()
		realmURL = realm.URL
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if r.Method == http.MethodPost {
				if auth != "Bearer tok-upload-svc" {
					w.Header().Set("WWW-Authenticate", `Bearer realm="`+realmURL+`/token",service="upload-svc"`)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Header().Set("Location", "/v2/team/loomplane-check/blobs/uploads/probe")
				w.WriteHeader(http.StatusAccepted)
				return
			}
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if auth != "Bearer tok-root-svc" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+realmURL+`/token",service="root-svc"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer s.Close()
		cfg, err := ParseConfig([]byte(`{"username":"deploy","password":"pw"}`), map[string]string{
			"endpoint":      s.URL,
			"repository":    "team",
			"auth_endpoint": realmURL + "/token",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = New(cfg).Check(t.Context()); err != nil {
			t.Fatalf("check failed: %v", err)
		}
		wantServices := []string{"root-svc", "root-svc", "root-svc", "upload-svc"}
		if !slices.Equal(services, wantServices) {
			t.Fatalf("services = %q, want %q", services, wantServices)
		}
	})

	t.Run("does not refetch token when same challenge rejects pre-fetched bearer token", func(t *testing.T) {
		var realmURL string
		var tokenCalls int
		realm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			tokenCalls++
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "tok"})
		}))
		defer realm.Close()
		realmURL = realm.URL
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v2/" && r.Header.Get("Authorization") == "Bearer tok" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+realmURL+`/token",service="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer s.Close()
		cfg, err := ParseConfig([]byte(`{"username":"deploy","password":"pw"}`), map[string]string{
			"endpoint":      s.URL,
			"repository":    "team",
			"auth_endpoint": realmURL + "/token",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = New(cfg).Check(t.Context()); !errors.Is(err, ErrRejected) {
			t.Fatalf("expected ErrRejected, got %v", err)
		}
		// One call for /v2/, one pre-fetch for /tags/list, no duplicate retry call on identical 401 challenge.
		if tokenCalls != 2 {
			t.Fatalf("expected 2 token calls, got %d", tokenCalls)
		}
	})
}

func TestRegistryCancelCheckUploadAcceptsOCIStatuses(t *testing.T) {
	for _, tc := range []struct {
		status  int
		wantErr bool
	}{
		{http.StatusOK, false},
		{http.StatusAccepted, false},
		{http.StatusNoContent, false},
		{http.StatusNotFound, false},
		{http.StatusInternalServerError, true},
	} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				w.WriteHeader(tc.status)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		cfg, err := ParseConfig([]byte("{}"), map[string]string{"endpoint": s.URL, "repository": "team"})
		if err != nil {
			s.Close()
			t.Fatal(err)
		}
		err = New(cfg).cancelCheckUpload(t.Context(), "/v2/team/loomplane-check/blobs/uploads/probe", "team/loomplane-check", nil)
		s.Close()
		if (err != nil) != tc.wantErr {
			t.Fatalf("status %d: err=%v wantErr=%v", tc.status, err, tc.wantErr)
		}
	}
}
