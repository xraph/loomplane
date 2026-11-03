//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/config"
)

// fakeHooks is Bitbucket's webhook list for acme/api.
type fakeHooks struct {
	mu      sync.Mutex
	hooks   map[string]fakeHook // by UUID
	deleted []string
	n       int
}

type fakeHook struct {
	URL    string   `json:"url"`
	Secret string   `json:"secret"`
	Events []string `json:"events"`
	Active bool     `json:"active"`
}

func (f *fakeHooks) routes(mux *http.ServeMux, guarded func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("POST /repositories/acme/api/hooks", guarded(func(w http.ResponseWriter, r *http.Request) {
		var h fakeHook
		if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.n++
		uuid := fmt.Sprintf("{00000000-0000-4000-8000-%012d}", f.n)
		f.hooks[uuid] = h
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"uuid":"` + uuid + `"}`))
	}))
	mux.HandleFunc("DELETE /repositories/acme/api/hooks/{uuid}", guarded(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		uuid := r.PathValue("uuid")
		if _, ok := f.hooks[uuid]; !ok {
			http.NotFound(w, r)
			return
		}
		delete(f.hooks, uuid)
		f.deleted = append(f.deleted, uuid)
		w.WriteHeader(http.StatusNoContent)
	}))
}

// secretFor is the secret Bitbucket was given for the hook at hookID.
func (f *fakeHooks) secretFor(t *testing.T, hookID string) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, h := range f.hooks {
		if strings.HasSuffix(h.URL, "/api/v1/bitbucket/webhook/"+hookID) {
			return h.Secret
		}
	}
	t.Fatalf("Bitbucket has no hook for %s", hookID)
	return ""
}

func TestBitbucketWebhook_aPushReanalyzesOnlyItsOrganization(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.Analysis.WorkDir = t.TempDir()
	// Bitbucket only delivers to https. Deliveries below go to the test
	// server directly, at the path the hook was given.
	cfg.PublicURL = "https://loomplane.example.test"
	hooks := &fakeHooks{hooks: map[string]fakeHook{}}
	bb := fakePrivateBitbucket(t, hooks.routes)
	cfg.Bitbucket.APIURL, cfg.Bitbucket.WebURL = bb.URL, bb.URL
	cfg.Bitbucket.ClientID, cfg.Bitbucket.ClientSecret = bbConsumerKey, bbConsumerSecret
	// No worker runs, so the analyses a push starts stay queued.
	r := start(t, cfg)
	defer r.stop()

	type org struct {
		app     *bitbucketApp
		id      string
		project string
		hookID  string
	}
	join := func(email, slug string) *org {
		c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
		account(t, c, email)
		var o struct {
			ID string `json:"id"`
		}
		c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": slug, "slug": slug}, &o)
		c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": o.ID}, nil)
		a := &bitbucketApp{t: t, c: c, cfg: cfg}
		a.connect()
		return &org{app: a, id: o.ID, project: a.attach()}
	}
	orgA := join("ada@example.com", "engines")
	orgB := join("grace@example.com", "compilers")

	conn, err := pgx.Connect(context.Background(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	for _, o := range []*org{orgA, orgB} {
		if err := conn.QueryRow(context.Background(), `SELECT hook_id FROM loomplane_bitbucket_webhooks
			WHERE org_id = $1 AND repository = 'bitbucket.org/acme/api' AND status = 'on'`, o.id).Scan(&o.hookID); err != nil {
			t.Fatalf("%s's hook: %v", o.id, err)
		}
	}
	if len(hooks.hooks) != 2 {
		t.Fatalf("Bitbucket has %d hooks, want one per organization", len(hooks.hooks))
	}
	for _, h := range hooks.hooks {
		if len(h.Events) != 1 || h.Events[0] != "repo:push" || !h.Active || !strings.HasPrefix(h.URL, "https://loomplane.example.test/api/v1/bitbucket/webhook/") {
			t.Fatalf("hook %+v", h)
		}
	}
	// The listing says pushes re-analyze the source.
	var list struct {
		Sources []struct {
			Repository string `json:"repository"`
			Push       *struct {
				On     bool   `json:"on"`
				Reason string `json:"reason"`
			} `json:"push"`
		} `json:"sources"`
	}
	orgA.app.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+orgA.project+"/sources", nil, &list)
	if len(list.Sources) != 1 || list.Sources[0].Push == nil || !list.Sources[0].Push.On {
		t.Fatalf("sources %+v", list.Sources)
	}

	secretA, secretB := hooks.secretFor(t, orgA.hookID), hooks.secretFor(t, orgB.hookID)
	if secretA == secretB {
		t.Fatal("two hooks share a secret")
	}
	body := []byte(`{"repository":{"full_name":"acme/api","uuid":"{repo}"},"push":{"changes":[{"new":{"type":"branch","name":"trunk","target":{"hash":"` + exampleSHA + `"}},"old":null}]}}`)
	deliver := func(hookID, secret string, payload []byte) (int, int) {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write(payload)
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, r.srv.URL+"/api/v1/bitbucket/webhook/"+hookID, bytes.NewReader(payload))
		req.Header.Set("X-Event-Key", "repo:push")
		req.Header.Set("X-Hub-Signature", "sha256="+hex.EncodeToString(m.Sum(nil)))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out struct {
			Started int `json:"analyses_started"`
		}
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out.Started
	}
	analyses := func(o *org) []analysisBody {
		var l struct {
			Analyses []analysisBody `json:"analyses"`
		}
		o.app.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+o.project+"/analyses", nil, &l)
		return l.Analyses
	}

	// B's secret proves nothing on A's address.
	if code, _ := deliver(orgA.hookID, secretB, body); code != http.StatusUnauthorized {
		t.Fatalf("A's hook with B's secret: %d", code)
	}
	if code, n := deliver(orgA.hookID, secretA, body); code != http.StatusAccepted || n != 1 {
		t.Fatalf("signed push to A's hook: %d, %d", code, n)
	}
	if a := analyses(orgA); len(a) != 1 || a[0].Status != "queued" {
		t.Fatalf("A's analyses %+v", a)
	}
	if a := analyses(orgB); len(a) != 0 {
		t.Fatalf("B's project heard A's push: %+v", a)
	}
	var by string
	if err := conn.QueryRow(context.Background(), `SELECT created_by FROM loomplane_analyses WHERE project_id = $1`, orgA.project).Scan(&by); err != nil || by != "bitbucket-push" {
		t.Fatalf("created by %q, %v", by, err)
	}
	if code, _ := deliver("bbhook_nothing", secretA, body); code != http.StatusNotFound {
		t.Fatalf("an unknown hook: %d", code)
	}

	// Disconnecting A removes its hook from Bitbucket and its address.
	var st bitbucketStatusBody
	orgA.app.must(http.StatusOK, http.MethodGet, "/api/v1/bitbucket", nil, &st)
	orgA.app.must(http.StatusNoContent, http.MethodDelete, "/api/v1/bitbucket/workspaces/"+st.Workspaces[0].ID, nil, nil)
	if code, _ := deliver(orgA.hookID, secretA, body); code != http.StatusNotFound {
		t.Fatalf("A's hook after disconnecting: %d", code)
	}
	if len(hooks.deleted) != 1 || len(hooks.hooks) != 1 || hooks.secretFor(t, orgB.hookID) != secretB {
		t.Fatalf("Bitbucket's hooks after A disconnected: deleted %v, left %d", hooks.deleted, len(hooks.hooks))
	}
	// No response carried a secret.
	for _, o := range []*org{orgA, orgB} {
		for _, seen := range o.app.seen {
			for _, secret := range []string{secretA, secretB, bbAccessToken, bbRefreshToken} {
				if strings.Contains(seen, secret) {
					t.Fatalf("a response holds a secret: %s", seen)
				}
			}
		}
	}
}
