//go:build integration

package integration

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

type keyBody struct {
	Key struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Access string `json:"access"`
		Hint   string `json:"hint"`
	} `json:"key"`
	Secret string `json:"secret"`
}

func TestAPIKeys_actAsTheirMakerUntilRevoked(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	// The key attaches a repository as its maker, which checks GitHub can
	// read it.
	cfg.GitHub.APIURL = fakeGitHubRepos(t, map[string][]byte{"acme/mono": exampleTarball(t)}).URL
	cfg.Analysis.WorkDir = t.TempDir()
	r := start(t, cfg)
	defer r.stop()

	signUp := func(email string) *client {
		c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
		var auth struct {
			SessionToken string `json:"session_token"`
		}
		c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": email, "password": "Correct-horse-battery-9"}, &auth)
		c.token = auth.SessionToken
		return c
	}
	ada := signUp("ada@example.com")
	var org struct {
		ID string `json:"id"`
	}
	ada.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Engines", "slug": "engines"}, &org)
	ada.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)

	ada.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "", "access": "write"}, nil)
	ada.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "ci", "access": "admin"}, nil)
	ada.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "ci", "access": "write", "expires_in_days": 400}, nil)

	var write, read keyBody
	ada.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "ci", "access": "write"}, &write)
	ada.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "dashboards", "access": "read", "expires_in_days": 7}, &read)
	if !strings.HasPrefix(write.Secret, "lp_live_") || len(write.Secret) != len("lp_live_")+64 || write.Key.Hint != write.Secret[len(write.Secret)-4:] {
		t.Fatalf("key %+v", write)
	}
	as := func(secret string) *client {
		return &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey, token: secret}
	}

	// A write key acts as Ada, in her organization, with no session.
	ci := as(write.Secret)
	var sess struct {
		UserID string `json:"user_id"`
		OrgID  string `json:"org_id"`
	}
	ci.must(http.StatusOK, http.MethodGet, "/api/v1/session", nil, &sess)
	if sess.OrgID != org.ID || sess.UserID == "" {
		t.Fatalf("session as the key %+v", sess)
	}
	var proj struct {
		ID string `json:"id"`
	}
	ci.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)

	// A read key reads, and nothing else.
	viewer := as(read.Secret)
	var projects struct {
		Projects []struct {
			ID string `json:"id"`
		} `json:"projects"`
	}
	viewer.must(http.StatusOK, http.MethodGet, "/api/v1/projects", nil, &projects)
	if len(projects.Projects) != 1 || projects.Projects[0].ID != proj.ID {
		t.Fatalf("projects %+v", projects)
	}
	viewer.must(http.StatusForbidden, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Other"}, nil)
	viewer.must(http.StatusOK, http.MethodGet, "/api/v1/session", nil, &sess)

	// Keys are managed from a session, never with a key.
	ci.must(http.StatusForbidden, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "more", "access": "write"}, nil)
	ci.must(http.StatusForbidden, http.MethodDelete, "/api/v1/api-keys/"+read.Key.ID, nil, nil)

	// The list never shows a secret.
	if raw := ada.raw("/api/v1/api-keys"); strings.Contains(raw, write.Secret) || !strings.Contains(raw, write.Key.ID) {
		t.Fatalf("the list %s", raw)
	}

	// Something that looks like a key and isn't is refused, not anonymous.
	as("lp_live_"+strings.Repeat("0", 64)).must(http.StatusUnauthorized, http.MethodGet, "/api/v1/projects", nil, nil)

	// Revoking works at once.
	ada.must(http.StatusNoContent, http.MethodDelete, "/api/v1/api-keys/"+read.Key.ID, nil, nil)
	viewer.must(http.StatusUnauthorized, http.MethodGet, "/api/v1/projects", nil, nil)

	// Grace's key stops working when she's removed: it acts as her.
	var created struct {
		AcceptPath string `json:"accept_path"`
	}
	ada.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations", map[string]string{"email": "grace@example.com", "role": "developer"}, &created)
	u, err := url.Parse(created.AcceptPath)
	if err != nil {
		t.Fatal(err)
	}
	grace := signUp("grace@example.com")
	if code := grace.do(http.MethodPost, "/authsome/v1/orgs/invitations/accept", map[string]string{"token": u.Query().Get("token")}, nil); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("accept: status %d", code)
	}
	grace.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var graces keyBody
	grace.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "laptop", "access": "write"}, &graces)
	laptop := as(graces.Secret)
	laptop.must(http.StatusOK, http.MethodGet, "/api/v1/projects", nil, nil)

	// Grace only sees her own key; Ada, an owner, sees everyone's.
	var list struct {
		Keys []struct {
			ID string `json:"id"`
		} `json:"keys"`
	}
	grace.must(http.StatusOK, http.MethodGet, "/api/v1/api-keys", nil, &list)
	if len(list.Keys) != 1 || list.Keys[0].ID != graces.Key.ID {
		t.Fatalf("grace's keys %+v", list.Keys)
	}
	grace.must(http.StatusNotFound, http.MethodDelete, "/api/v1/api-keys/"+write.Key.ID, nil, nil)
	ada.must(http.StatusOK, http.MethodGet, "/api/v1/api-keys", nil, &list)
	if len(list.Keys) != 3 {
		t.Fatalf("ada sees %d keys", len(list.Keys))
	}

	var members struct {
		Members []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"members"`
	}
	ada.must(http.StatusOK, http.MethodGet, "/api/v1/members", nil, &members)
	for _, m := range members.Members {
		if m.Email == "grace@example.com" {
			if code := ada.do(http.MethodDelete, "/authsome/v1/orgs/"+org.ID+"/members/"+m.ID, nil, nil); code != http.StatusOK && code != http.StatusNoContent {
				t.Fatalf("remove grace: status %d", code)
			}
		}
	}
	laptop.must(http.StatusForbidden, http.MethodGet, "/api/v1/projects", nil, nil)
}
