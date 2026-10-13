//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/config"
)

func TestWebhook_aPushReanalyzesTheProjectsThatTrackIt(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.WebhookSecret = "webhook-secret-0123456789"
	r := start(t, cfg)
	defer r.stop()

	org := func(email, slug string) (*client, string, string) {
		c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
		var auth struct {
			SessionToken string `json:"session_token"`
		}
		c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": email, "password": "Correct-horse-battery-9"}, &auth)
		c.token = auth.SessionToken
		var o struct {
			ID string `json:"id"`
		}
		c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": slug, "slug": slug}, &o)
		c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": o.ID}, nil)
		var p struct {
			ID string `json:"id"`
		}
		c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono.git"}, &p)
		return c, o.ID, p.ID
	}
	ada, adaOrg, adaProj := org("ada@example.com", "engines")
	grace, _, graceProj := org("grace@example.com", "compilers")

	// Ada's organization connected installation 42; Grace's didn't.
	conn, err := pgx.Connect(context.Background(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(context.Background(), `INSERT INTO loomplane_github_installations (org_id, installation_id, account, created_by, created_at) VALUES ($1, 42, 'acme', 'test', now())`, adaOrg); err != nil {
		t.Fatal(err)
	}

	deliver := func(event string, payload any, secret string) (int, int) {
		body, _ := json.Marshal(payload)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, r.srv.URL+"/api/v1/github/webhook", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", event)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
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
	push := func(ref string, installation int64) map[string]any {
		p := map[string]any{
			"ref": ref, "after": "abc123",
			"repository": map[string]any{"full_name": "acme/mono", "default_branch": "main", "private": false},
		}
		if installation != 0 {
			p["installation"] = map[string]any{"id": installation}
		}
		return p
	}
	analyses := func(c *client, projectID string) []analysisBody {
		var list struct {
			Analyses []analysisBody `json:"analyses"`
		}
		c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+projectID+"/analyses", nil, &list)
		return list.Analyses
	}

	if code, _ := deliver("push", push("refs/heads/main", 42), "wrong-secret"); code != http.StatusUnauthorized {
		t.Fatalf("a forged delivery got %d", code)
	}
	if code, n := deliver("ping", map[string]any{"zen": "hi"}, cfg.GitHub.WebhookSecret); code != http.StatusAccepted || n != 0 {
		t.Fatalf("ping: %d, %d", code, n)
	}
	if _, n := deliver("push", push("refs/heads/feature", 42), cfg.GitHub.WebhookSecret); n != 0 {
		t.Fatalf("a push to another branch started %d", n)
	}

	// Through the installation, only Ada's organization hears about it.
	if code, n := deliver("push", push("refs/heads/main", 42), cfg.GitHub.WebhookSecret); code != http.StatusAccepted || n != 1 {
		t.Fatalf("push through the installation: %d, %d", code, n)
	}
	if a := analyses(ada, adaProj); len(a) != 1 || a[0].Status != "queued" {
		t.Fatalf("ada's analyses %+v", a)
	}
	if a := analyses(grace, graceProj); len(a) != 0 {
		t.Fatalf("grace's project heard a push through someone else's installation: %+v", a)
	}

	// A public repository's own webhook reaches every project on it; Ada's
	// already has an analysis under way.
	if _, n := deliver("push", push("refs/heads/main", 0), cfg.GitHub.WebhookSecret); n != 1 {
		t.Fatalf("public push started %d", n)
	}
	if a := analyses(grace, graceProj); len(a) != 1 {
		t.Fatalf("grace's analyses %+v", a)
	}
}
