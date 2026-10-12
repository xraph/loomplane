//go:build integration

package integration

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/config"
)

// fakeGoogle is Google's token endpoint and Resource Manager's
// testIamPermissions, with the permissions it grants adjustable.
type fakeGoogle struct {
	srv     *httptest.Server
	mu      sync.Mutex
	granted []string
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	f := &fakeGoogle{granted: slices.Clone(gcp.Permissions)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || r.Form.Get("assertion") == "" {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"ya29.fake","token_type":"Bearer","expires_in":3600}`))
		case "/v3/projects/acme-prod:testIamPermissions":
			if r.Header.Get("Authorization") != "Bearer ya29.fake" {
				http.Error(w, `{"error":{"message":"unauthenticated"}}`, http.StatusUnauthorized)
				return
			}
			var in struct {
				Permissions []string `json:"permissions"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &in)
			f.mu.Lock()
			held := []string{}
			for _, p := range in.Permissions {
				if slices.Contains(f.granted, p) {
					held = append(held, p)
				}
			}
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"permissions": held})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGoogle) grant(perms []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.granted = perms
}

// serviceAccountKey is a key for acme-prod whose token endpoint is the fake.
func serviceAccountKey(t *testing.T, tokenURI string) string {
	t.Helper()
	pk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(pk)
	if err != nil {
		t.Fatal(err)
	}
	key, err := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "acme-prod", "private_key_id": "k1",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "loomplane@acme-prod.iam.gserviceaccount.com", "client_id": "1",
		"token_uri": tokenURI,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(key)
}

// fakeVercel accepts one token, and one team.
func fakeVercel(t *testing.T, token string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"Not authorized"}}`))
			return
		}
		switch r.URL.Path {
		case "/v2/user":
			_, _ = w.Write([]byte(`{"user":{"username":"ada"}}`))
		case "/v2/teams/team_acme":
			_, _ = w.Write([]byte(`{"id":"team_acme","slug":"acme"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"Not found"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type connectionBody struct {
	ID      string            `json:"id"`
	Cloud   string            `json:"cloud"`
	Account string            `json:"account"`
	Details map[string]string `json:"details"`
	Status  string            `json:"status"`
	Missing []string          `json:"missing"`
	Message string            `json:"message"`
}

func TestConnections_aCredentialIsCheckedStoredAndNeverReturned(t *testing.T) {
	google := newFakeGoogle(t)
	const vercelToken = "vcl_0123456789abcdef"
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.Clouds.GoogleEndpoint = google.srv.URL
	cfg.Clouds.VercelEndpoint = fakeVercel(t, vercelToken).URL
	r := start(t, cfg)
	defer r.stop()

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)

	key := serviceAccountKey(t, google.srv.URL+"/token")

	// Malformed credentials and ones the cloud refuses are never stored.
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/connections", map[string]string{"cloud": "gcp", "name": "Prod", "credential": "{}"}, nil)
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/connections", map[string]string{"cloud": "vercel", "name": "Web", "credential": "vcl_wrong"}, nil)
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/connections", map[string]string{"cloud": "aws", "name": "Prod", "credential": "x"}, nil)

	// A key that works but can't deploy everything is kept, and says what
	// it's missing.
	google.grant(slices.DeleteFunc(slices.Clone(gcp.Permissions), func(p string) bool {
		return p == "iam.serviceAccounts.actAs" || p == "storage.buckets.create"
	}))
	var gc connectionBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]string{"cloud": "gcp", "name": "Prod", "credential": key}, &gc)
	if gc.Status != "incomplete" || gc.Account != "acme-prod" || gc.Details["service_account"] != "loomplane@acme-prod.iam.gserviceaccount.com" ||
		!slices.Equal(gc.Missing, []string{"iam.serviceAccounts.actAs", "storage.buckets.create"}) {
		t.Fatalf("gcp connection %+v", gc)
	}
	c.must(http.StatusConflict, http.MethodPost, "/api/v1/connections", map[string]string{"cloud": "gcp", "name": "Prod", "credential": key}, nil)

	// Granting them and checking again makes it ready.
	google.grant(gcp.Permissions)
	c.must(http.StatusOK, http.MethodPost, "/api/v1/connections/"+gc.ID+"/check", nil, &gc)
	if gc.Status != "ready" || len(gc.Missing) != 0 {
		t.Fatalf("rechecked %+v", gc)
	}

	var vc connectionBody
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "vercel", "name": "Web", "credential": vercelToken, "settings": map[string]string{"team_id": "team_other"}}, nil)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "vercel", "name": "Web", "credential": vercelToken, "settings": map[string]string{"team_id": "team_acme"}}, &vc)
	if vc.Status != "ready" || vc.Account != "acme" || vc.Details["team_id"] != "team_acme" {
		t.Fatalf("vercel connection %+v", vc)
	}

	// Nothing the API sends carries a credential back.
	for _, path := range []string{"/api/v1/connections", "/api/v1/connections/" + gc.ID, "/api/v1/connections/" + vc.ID} {
		body := c.raw(http.MethodGet, path)
		if strings.Contains(body, "PRIVATE KEY") || strings.Contains(body, vercelToken) {
			t.Fatalf("GET %s leaked a credential: %s", path, body)
		}
	}

	// A rotated token that Vercel refuses doesn't replace the working one.
	c.must(http.StatusUnprocessableEntity, http.MethodPut, "/api/v1/connections/"+vc.ID+"/credential", map[string]string{"credential": "vcl_revoked"}, nil)
	c.must(http.StatusOK, http.MethodPost, "/api/v1/connections/"+vc.ID+"/check", nil, &vc)
	if vc.Status != "ready" {
		t.Fatalf("after a refused replacement %+v", vc)
	}

	c.must(http.StatusNoContent, http.MethodDelete, "/api/v1/connections/"+vc.ID, nil, nil)
	c.must(http.StatusNotFound, http.MethodGet, "/api/v1/connections/"+vc.ID, nil, nil)
	var list struct {
		Connections []connectionBody `json:"connections"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/connections", nil, &list)
	if len(list.Connections) != 1 || list.Connections[0].ID != gc.ID {
		t.Fatalf("connections %+v", list.Connections)
	}
}
