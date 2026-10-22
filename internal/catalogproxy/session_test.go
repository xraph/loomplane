package catalogproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/security"
)

var sessionURL = regexp.MustCompile(`^/catalog-admin/([A-Za-z0-9_-]{43})/admin$`)

func TestOpenNeedsInfrastructureModify(t *testing.T) {
	h := newHarness(t, sampleTarget())
	ctx := context.Background()
	if _, err := h.svc.Open(ctx, security.Principal{OrgID: "org", UserID: "developer"}, appRef); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a developer opened the admin: %v", err)
	}
	if n := len(h.store.all()); n != 0 {
		t.Fatalf("a refused open kept %d sessions", n)
	}
	s, err := h.svc.Open(ctx, admin, appRef)
	if err != nil {
		t.Fatal(err)
	}
	m := sessionURL.FindStringSubmatch(s.URL)
	if m == nil {
		t.Fatalf("session URL %q", s.URL)
	}
	if !s.ExpiresAt.Equal(h.clock.Now().Add(30 * time.Minute)) {
		t.Fatalf("expires at %v", s.ExpiresAt)
	}
	if got := h.authz.checks[len(h.authz.checks)-1]; got != "org admin infrastructure:modify proj" {
		t.Fatalf("checked %q", got)
	}
	rows := h.store.all()
	if len(rows) != 1 {
		t.Fatalf("%d sessions", len(rows))
	}
	sum := sha256.Sum256([]byte(m[1]))
	r := rows[0]
	if r.TokenHash != hex.EncodeToString(sum[:]) || strings.Contains(r.TokenHash, m[1]) {
		t.Fatalf("stored %q, want the token's SHA-256", r.TokenHash)
	}
	want := Record{TokenHash: r.TokenHash, OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Resource: "app", Kind: "sample", UserID: "admin",
		CreatedAt: h.clock.Now(), LastUsedAt: h.clock.Now(), ExpiresAt: s.ExpiresAt}
	if r != want {
		t.Fatalf("stored %+v, want %+v", r, want)
	}
	// Two opens are two sessions.
	other, err := h.svc.Open(ctx, admin, appRef)
	if err != nil || other.URL == s.URL {
		t.Fatalf("second open: %+v %v", other, err)
	}
	// A resource the environment doesn't have.
	if _, err = h.svc.Open(ctx, admin, deployments.ResourceRef{ProjectID: "proj", EnvironmentID: "env", Resource: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing resource: %v", err)
	}
	// Nor does another project's environment.
	if _, err = h.svc.Open(ctx, admin, deployments.ResourceRef{ProjectID: "other", EnvironmentID: "env", Resource: "app"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another project: %v", err)
	}
}

func TestOpenRefusesKindsWithoutATarget(t *testing.T) {
	h := newHarness(t, sampleTarget())
	_, err := h.svc.Open(context.Background(), admin, deployments.ResourceRef{ProjectID: "proj", EnvironmentID: "env", Resource: "db"})
	if !errors.Is(err, ErrInvalid) || err.Error() != "postgres has no admin to open" {
		t.Fatalf("got %v", err)
	}
	if n := len(h.store.all()); n != 0 {
		t.Fatalf("kept %d sessions", n)
	}
}

func TestProxyExpiresIdleAndAbsolute(t *testing.T) {
	const expired = "This admin link expired. Open it again from Loomplane."
	t.Run("idle", func(t *testing.T) {
		h := newHarness(t, sampleTarget())
		base := h.open(t)
		h.clock.Add(29 * time.Minute)
		if w := h.do(http.MethodGet, base+"/admin", nil); w.Code != http.StatusOK {
			t.Fatalf("29 min idle: %d", w.Code)
		}
		h.clock.Add(31 * time.Minute)
		w := h.do(http.MethodGet, base+"/admin", nil)
		if w.Code != http.StatusGone || strings.TrimSpace(w.Body.String()) != expired {
			t.Fatalf("31 min idle: %d %q", w.Code, w.Body.String())
		}
		if n := len(h.up.seen()); n != 1 {
			t.Fatalf("the component got %d requests", n)
		}
	})
	t.Run("absolute", func(t *testing.T) {
		h := newHarness(t, sampleTarget())
		base := h.open(t)
		for elapsed := 20 * time.Minute; elapsed < 8*time.Hour; elapsed += 20 * time.Minute {
			h.clock.Add(20 * time.Minute)
			if w := h.do(http.MethodGet, base+"/admin", nil); w.Code != http.StatusOK {
				t.Fatalf("after %v of activity: %d %q", elapsed, w.Code, w.Body.String())
			}
		}
		h.clock.Add(19 * time.Minute) // 7h59m
		if w := h.do(http.MethodGet, base+"/admin", nil); w.Code != http.StatusOK {
			t.Fatalf("at 7h59m: %d", w.Code)
		}
		h.clock.Add(2 * time.Minute) // 8h01m
		w := h.do(http.MethodGet, base+"/admin", nil)
		if w.Code != http.StatusGone || strings.TrimSpace(w.Body.String()) != expired {
			t.Fatalf("at 8h01m: %d %q", w.Code, w.Body.String())
		}
	})
}

func TestProxyStopsAfterPermissionRevoked(t *testing.T) {
	h := newHarness(t, sampleTarget())
	base := h.open(t)
	opened := h.authz.count()
	h.authz.deny("admin")
	h.clock.Add(30 * time.Second)
	if w := h.do(http.MethodGet, base+"/admin", nil); w.Code != http.StatusOK {
		t.Fatalf("within RecheckEvery: %d", w.Code)
	}
	if h.authz.count() != opened {
		t.Fatal("rechecked within RecheckEvery")
	}
	h.clock.Add(31 * time.Second)
	if w := h.do(http.MethodGet, base+"/admin", nil); w.Code != http.StatusForbidden {
		t.Fatalf("after RecheckEvery: %d %q", w.Code, w.Body.String())
	}
	if n := len(h.up.seen()); n != 1 {
		t.Fatalf("the component got %d requests", n)
	}
	if got := h.authz.checks[len(h.authz.checks)-1]; got != "org admin infrastructure:modify proj" {
		t.Fatalf("rechecked %q", got)
	}
}

func TestProxyRechecksAtMostOncePerMinute(t *testing.T) {
	h := newHarness(t, sampleTarget())
	base := h.open(t)
	opened := h.authz.count()
	for range 5 {
		h.clock.Add(25 * time.Second)
		if w := h.do(http.MethodGet, base+"/admin", nil); w.Code != http.StatusOK {
			t.Fatalf("got %d", w.Code)
		}
	}
	// 125 s of activity: rechecked at 75 s only.
	if got := h.authz.count() - opened; got != 1 {
		t.Fatalf("rechecked %d times", got)
	}
}

func TestRevokeResourceEndsSessions(t *testing.T) {
	h := newHarness(t, sampleTarget())
	base := h.open(t)
	if w := h.do(http.MethodGet, base+"/admin", nil); w.Code != http.StatusOK {
		t.Fatalf("before: %d", w.Code)
	}
	if err := h.svc.RevokeResource(context.Background(), "org", "env", "app"); err != nil {
		t.Fatal(err)
	}
	if w := h.do(http.MethodGet, base+"/admin", nil); w.Code != http.StatusNotFound {
		t.Fatalf("after: %d", w.Code)
	}
	if n := len(h.up.seen()); n != 1 {
		t.Fatalf("the component got %d requests", n)
	}
}

func TestRegisterRefusesUnsafeTargets(t *testing.T) {
	for name, target := range map[string]Target{
		"no component":           {Port: "admin", Root: "/admin", AllowedPrefixes: []string{"/admin"}},
		"no prefixes":            {Component: "api", Port: "admin", Root: "/admin"},
		"whole component":        {Component: "api", Port: "admin", Root: "/", AllowedPrefixes: []string{"/"}},
		"relative prefix":        {Component: "api", Port: "admin", Root: "/admin", AllowedPrefixes: []string{"admin"}},
		"dot prefix":             {Component: "api", Port: "admin", Root: "/admin/../x", AllowedPrefixes: []string{"/admin/../x"}},
		"root outside":           {Component: "api", Port: "admin", Root: "/other", AllowedPrefixes: []string{"/admin"}},
		"strip outside a prefix": {Component: "api", Port: "admin", Root: "/admin", AllowedPrefixes: []string{"/admin"}, StripPrefix: "/adm"},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("registered")
				}
			}()
			Register("unsafe-"+strings.ReplaceAll(name, " ", "-"), target)
		})
	}
}
