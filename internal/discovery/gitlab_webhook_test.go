package discovery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/repositories"
)

var glNow = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

// glSecret is the 32 bytes a webhook secret is made of.
var glSecret = []byte("0123456789abcdef0123456789abcdef")

// glSigned returns the headers GitLab 19 signs a delivery with, at ts.
func glSigned(secret, body []byte, ts time.Time, extra ...string) http.Header {
	const id = "msg_1"
	stamp := strconv.FormatInt(ts.Unix(), 10)
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(id + "." + stamp + "."))
	m.Write(body)
	sig := "v1," + base64.StdEncoding.EncodeToString(m.Sum(nil))
	for _, e := range extra {
		sig = e + " " + sig
	}
	h := http.Header{}
	h.Set("Webhook-Id", id)
	h.Set("Webhook-Timestamp", stamp)
	h.Set("Webhook-Signature", sig)
	return h
}

func TestVerifyGitLab_signingToken(t *testing.T) {
	body := []byte(`{"object_kind":"push"}`)
	// The right entry among two, in either position.
	other := "v1," + base64.StdEncoding.EncodeToString([]byte("not the signature"))
	for _, h := range []http.Header{
		glSigned(glSecret, body, glNow),
		glSigned(glSecret, body, glNow, other),
		glSigned(glSecret, body, glNow.Add(-4*time.Minute)),
		glSigned(glSecret, body, glNow.Add(4*time.Minute)),
	} {
		if err := VerifyGitLab(h, body, glSecret, glNow); err != nil {
			t.Fatalf("%v: %v", h, err)
		}
	}
	// A signature made with another secret, over another body, or in
	// another version isn't one.
	bad := glSigned([]byte("another secret, 32 bytes long!!!"), body, glNow)
	if err := VerifyGitLab(bad, body, glSecret, glNow); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("wrong secret: %v", err)
	}
	if err := VerifyGitLab(glSigned(glSecret, body, glNow), []byte(`{"object_kind":"tag_push"}`), glSecret, glNow); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("another body: %v", err)
	}
	v2 := glSigned(glSecret, body, glNow)
	v2.Set("Webhook-Signature", "v2,"+v2.Get("Webhook-Signature")[3:])
	if err := VerifyGitLab(v2, body, glSecret, glNow); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("v2 entry: %v", err)
	}
	// Missing pieces, and a secret Loomplane doesn't have.
	for _, name := range []string{"Webhook-Id", "Webhook-Timestamp"} {
		h := glSigned(glSecret, body, glNow)
		h.Del(name)
		if err := VerifyGitLab(h, body, glSecret, glNow); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("without %s: %v", name, err)
		}
	}
	h := glSigned(glSecret, body, glNow)
	h.Set("Webhook-Timestamp", "yesterday")
	if err := VerifyGitLab(h, body, glSecret, glNow); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a timestamp that isn't seconds: %v", err)
	}
	if err := VerifyGitLab(glSigned(nil, body, glNow), body, nil, glNow); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("no secret: %v", err)
	}
}

func TestVerifyGitLab_replayedTimestamp(t *testing.T) {
	body := []byte(`{}`)
	for _, ago := range []time.Duration{6 * time.Minute, -6 * time.Minute, 24 * time.Hour} {
		if err := VerifyGitLab(glSigned(glSecret, body, glNow.Add(-ago)), body, glSecret, glNow); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("a delivery signed %s ago: %v", ago, err)
		}
	}
	// A valid secret token doesn't rescue a stale signature: the signature
	// wins when it's there.
	h := glSigned(glSecret, body, glNow.Add(-6*time.Minute))
	h.Set("X-Gitlab-Token", base64.RawURLEncoding.EncodeToString(glSecret))
	if err := VerifyGitLab(h, body, glSecret, glNow); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("stale signature with a good token: %v", err)
	}
}

func TestVerifyGitLab_secretToken(t *testing.T) {
	body := []byte(`{}`)
	token := base64.RawURLEncoding.EncodeToString(glSecret)
	h := http.Header{}
	h.Set("X-Gitlab-Token", token)
	if err := VerifyGitLab(h, body, glSecret, glNow); err != nil {
		t.Fatalf("matching token: %v", err)
	}
	for name, tok := range map[string]string{
		"wrong":     base64.RawURLEncoding.EncodeToString([]byte("another secret, 32 bytes long!!!")),
		"standard":  base64.StdEncoding.EncodeToString(glSecret),
		"truncated": token[:len(token)-1],
		"empty":     "",
	} {
		bad := http.Header{}
		if tok != "" {
			bad.Set("X-Gitlab-Token", tok)
		}
		if err := VerifyGitLab(bad, body, glSecret, glNow); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("%s token: %v", name, err)
		}
	}
	if err := VerifyGitLab(http.Header{}, body, glSecret, glNow); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("neither header: %v", err)
	}
	// With no secret on file, a token header proves nothing.
	if err := VerifyGitLab(h, body, nil, glNow); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("no secret: %v", err)
	}
}

func glPush(path, ref, branch, after string) []byte {
	return fmt.Appendf(nil, `{"object_kind":"push","ref":%q,"after":%q,
		"project":{"path_with_namespace":%q,"default_branch":%q}}`, ref, after, path, branch)
}

const glSHA = "da1560886d4f094c3e6c9ef40349f7d38b5d27d7"

func glHeaders(secret, body []byte, event string) http.Header {
	h := glSigned(secret, body, glNow)
	h.Set("X-Gitlab-Event", event)
	return h
}

type glHooks struct {
	hooks   *GitLabWebhooks
	started []string
}

func newGLHooks(orgID string, sources ...*Source) *glHooks {
	g := &glHooks{}
	g.hooks = &GitLabWebhooks{
		Target: func(_ context.Context, id string) (string, string, []byte, error) {
			if id != "glc_1" {
				return "", "", nil, repositories.ErrGitLabConnectionNotFound
			}
			return orgID, "gitlab.com", glSecret, nil
		},
		Sources: func(_ context.Context, key string) ([]*Source, error) {
			var out []*Source
			for _, s := range sources {
				if strings.EqualFold(s.Repository, key) {
					out = append(out, s)
				}
			}
			return out, nil
		},
		Start: func(_ context.Context, src *Source) (*Analysis, error) {
			g.started = append(g.started, src.ID)
			return &Analysis{}, nil
		},
		Now: func() time.Time { return glNow },
	}
	return g
}

func TestGitLabHandle_onlyThisConnectionsOrg(t *testing.T) {
	g := newGLHooks("org-a",
		&Source{ID: "src_a", OrgID: "org-a", ProjectID: "p1", Alias: "api", Repository: "gitlab.com/grp/api"},
		&Source{ID: "src_a2", OrgID: "org-a", ProjectID: "p2", Alias: "api", Repository: "gitlab.com/grp/api", Ref: "main"},
		&Source{ID: "src_b", OrgID: "org-b", ProjectID: "p3", Alias: "api", Repository: "gitlab.com/grp/api"},
		// Another branch didn't move.
		&Source{ID: "src_rel", OrgID: "org-a", ProjectID: "p4", Alias: "api", Repository: "gitlab.com/grp/api", Ref: "release"},
		// Another project on the host.
		&Source{ID: "src_other", OrgID: "org-a", ProjectID: "p5", Alias: "web", Repository: "gitlab.com/grp/web"},
	)
	body := glPush("grp/api", "refs/heads/main", "main", glSHA)
	n, err := g.hooks.Handle(context.Background(), "glc_1", glHeaders(glSecret, body, "Push Hook"), body)
	if err != nil || n != 2 || !slices.Equal(g.started, []string{"src_a", "src_a2"}) {
		t.Fatalf("started %d %v, %v; want org-a's two", n, g.started, err)
	}

	// The same push through org-b's connection starts org-b's alone.
	g = newGLHooks("org-b",
		&Source{ID: "src_a", OrgID: "org-a", Alias: "api", Repository: "gitlab.com/grp/api"},
		&Source{ID: "src_b", OrgID: "org-b", Alias: "api", Repository: "gitlab.com/grp/api"},
	)
	n, err = g.hooks.Handle(context.Background(), "glc_1", glHeaders(glSecret, body, "Push Hook"), body)
	if err != nil || n != 1 || !slices.Equal(g.started, []string{"src_b"}) {
		t.Fatalf("started %d %v, %v; want org-b's", n, g.started, err)
	}

	// A connection with no organization hears for nobody.
	g = newGLHooks("", &Source{ID: "src_blank", OrgID: "", Alias: "api", Repository: "gitlab.com/grp/api"})
	if n, err = g.hooks.Handle(context.Background(), "glc_1", glHeaders(glSecret, body, "Push Hook"), body); err != nil || n != 0 {
		t.Fatalf("a connection without an organization started %d, %v", n, err)
	}
}

func TestGitLabHandle_matchesPathCaseInsensitively(t *testing.T) {
	g := newGLHooks("org-a", &Source{ID: "src_a", OrgID: "org-a", Alias: "api", Repository: "gitlab.com/acme/api"})
	body := glPush("Acme/API", "refs/heads/main", "main", glSHA)
	n, err := g.hooks.Handle(context.Background(), "glc_1", glHeaders(glSecret, body, "Push Hook"), body)
	if err != nil || n != 1 {
		t.Fatalf("started %d, %v; want 1", n, err)
	}
}

func TestGitLabHandle_ignoresOtherBranchesDeletesAndEvents(t *testing.T) {
	src := &Source{ID: "src_a", OrgID: "org-a", Alias: "api", Repository: "gitlab.com/grp/api"}
	zeros := "0000000000000000000000000000000000000000"
	for name, c := range map[string]struct {
		event string
		body  []byte
	}{
		"another branch":    {"Push Hook", glPush("grp/api", "refs/heads/feature", "main", glSHA)},
		"a tag":             {"Push Hook", glPush("grp/api", "refs/tags/main", "main", glSHA)},
		"a branch deletion": {"Push Hook", glPush("grp/api", "refs/heads/main", "main", zeros)},
		"a tag push event":  {"Tag Push Hook", glPush("grp/api", "refs/heads/main", "main", glSHA)},
		"a merge request":   {"Merge Request Hook", glPush("grp/api", "refs/heads/main", "main", glSHA)},
		"no event":          {"", glPush("grp/api", "refs/heads/main", "main", glSHA)},
		"no default branch": {"Push Hook", glPush("grp/api", "refs/heads/", "", glSHA)},
		"no project path":   {"Push Hook", glPush("", "refs/heads/main", "main", glSHA)},
	} {
		g := newGLHooks("org-a", src)
		n, err := g.hooks.Handle(context.Background(), "glc_1", glHeaders(glSecret, c.body, c.event), c.body)
		if err != nil || n != 0 || len(g.started) != 0 {
			t.Fatalf("%s: started %d %v, %v", name, n, g.started, err)
		}
	}
	// A body that isn't JSON is an error, not a silent drop, once signed.
	g := newGLHooks("org-a", src)
	bad := []byte(`not json`)
	if _, err := g.hooks.Handle(context.Background(), "glc_1", glHeaders(glSecret, bad, "Push Hook"), bad); err == nil || errors.Is(err, ErrBadSignature) {
		t.Fatalf("a body that isn't JSON: %v", err)
	}
}

func TestGitLabHandle_renamedProjectStartsNothing(t *testing.T) {
	g := newGLHooks("org-a", &Source{ID: "src_a", OrgID: "org-a", Alias: "api", Repository: "gitlab.com/old/api"})
	body := glPush("new/api", "refs/heads/main", "main", glSHA)
	n, err := g.hooks.Handle(context.Background(), "glc_1", glHeaders(glSecret, body, "Push Hook"), body)
	if err != nil || n != 0 || len(g.started) != 0 {
		t.Fatalf("started %d %v, %v; want none", n, g.started, err)
	}
}

func TestGitLabHandle_refusesWhatItCantTrust(t *testing.T) {
	g := newGLHooks("org-a", &Source{ID: "src_a", OrgID: "org-a", Alias: "api", Repository: "gitlab.com/grp/api"})
	body := glPush("grp/api", "refs/heads/main", "main", glSHA)

	// A connection Loomplane doesn't have.
	if _, err := g.hooks.Handle(context.Background(), "glc_gone", glHeaders(glSecret, body, "Push Hook"), body); !errors.Is(err, repositories.ErrGitLabConnectionNotFound) {
		t.Fatalf("unknown connection: %v", err)
	}
	// A signature made with the wrong secret, and none at all.
	wrong := glHeaders([]byte("another secret, 32 bytes long!!!"), body, "Push Hook")
	if _, err := g.hooks.Handle(context.Background(), "glc_1", wrong, body); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("wrong secret: %v", err)
	}
	if _, err := g.hooks.Handle(context.Background(), "glc_1", http.Header{"X-Gitlab-Event": {"Push Hook"}}, body); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("unsigned: %v", err)
	}
	// A body changed after signing.
	signed := glHeaders(glSecret, body, "Push Hook")
	other := glPush("grp/api", "refs/heads/main", "main", "1111111111111111111111111111111111111111")
	if _, err := g.hooks.Handle(context.Background(), "glc_1", signed, other); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered body: %v", err)
	}
	if len(g.started) != 0 {
		t.Fatalf("started %v from refused deliveries", g.started)
	}
}

func TestGitLabHandle_oneSourceFailingDoesntStopTheRest(t *testing.T) {
	g := newGLHooks("org-a",
		&Source{ID: "src_1", OrgID: "org-a", ProjectID: "p1", Alias: "api", Repository: "gitlab.com/grp/api"},
		&Source{ID: "src_2", OrgID: "org-a", ProjectID: "p2", Alias: "api", Repository: "gitlab.com/grp/api"},
		&Source{ID: "src_busy", OrgID: "org-a", ProjectID: "p3", Alias: "api", Repository: "gitlab.com/grp/api"},
	)
	start := g.hooks.Start
	g.hooks.Start = func(ctx context.Context, src *Source) (*Analysis, error) {
		switch src.ID {
		case "src_1":
			return nil, errors.New("queue is down")
		case "src_busy":
			return nil, ErrAlreadyActive
		}
		return start(ctx, src)
	}
	body := glPush("grp/api", "refs/heads/main", "main", glSHA)
	n, err := g.hooks.Handle(context.Background(), "glc_1", glHeaders(glSecret, body, "Push Hook"), body)
	if n != 1 || err == nil || !slices.Equal(g.started, []string{"src_2"}) {
		t.Fatalf("started %d %v, %v", n, g.started, err)
	}
}
