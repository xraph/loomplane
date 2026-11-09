package nodes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/authsome/ratelimit"
)

// fromAddr sends an unauthenticated request from addr.
func (r *rig) fromAddr(t *testing.T, method, path, body, addr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	if body != "" {
		req = httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	}
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = addr
	out := httptest.NewRecorder()
	r.router.ServeHTTP(out, req)
	return out
}

func TestJoinIsLimitedPerAddress(t *testing.T) {
	r := newRig(t)
	r.ext.limiter = ratelimit.NewMemoryLimiter()
	r.ext.cfg.Nodes.JoinRateLimit = 2
	for i := range 2 {
		if out := r.fromAddr(t, http.MethodPost, "/api/v1/nodes/join", `not json`, "203.0.113.7:4000"); out.Code != http.StatusBadRequest {
			t.Fatalf("join %d: %d %s", i, out.Code, out.Body)
		}
	}
	out := r.fromAddr(t, http.MethodPost, "/api/v1/nodes/join", joinBody(t, nil), "203.0.113.7:4001")
	if out.Code != http.StatusTooManyRequests || out.Header().Get("Retry-After") == "" {
		t.Fatalf("third join: %d %s", out.Code, out.Body)
	}
	if r.tokens.spent != 0 {
		t.Fatal("a join over the limit reached the token")
	}
	// Another address has its own budget, and challenges count apart.
	if out = r.fromAddr(t, http.MethodPost, "/api/v1/nodes/join", joinBody(t, nil), "198.51.100.1:4000"); out.Code != http.StatusCreated {
		t.Fatalf("join from another address: %d %s", out.Code, out.Body)
	}
	if out = r.fromAddr(t, http.MethodGet, "/node/v1/challenge?node=node_1", "", "203.0.113.7:4002"); out.Code != http.StatusOK {
		t.Fatalf("challenge after joins: %d %s", out.Code, out.Body)
	}
}

func TestChallengeIsLimitedPerAddress(t *testing.T) {
	r := newRig(t)
	r.ext.limiter = ratelimit.NewMemoryLimiter()
	r.ext.cfg.Nodes.ChallengeRateLimit = 3
	for i := range 3 {
		if out := r.fromAddr(t, http.MethodGet, "/node/v1/challenge?node=node_1", "", "203.0.113.7:4000"); out.Code != http.StatusOK {
			t.Fatalf("challenge %d: %d %s", i, out.Code, out.Body)
		}
	}
	if out := r.fromAddr(t, http.MethodGet, "/node/v1/challenge?node=node_1", "", "203.0.113.7:4000"); out.Code != http.StatusTooManyRequests {
		t.Fatalf("fourth challenge: %d %s", out.Code, out.Body)
	}
}

// A limit of zero is off, and so is a missing limiter.
func TestNodeLimitsOff(t *testing.T) {
	r := newRig(t)
	r.ext.limiter = ratelimit.NewMemoryLimiter()
	r.ext.cfg.Nodes.ChallengeRateLimit = 0
	for range 20 {
		if out := r.fromAddr(t, http.MethodGet, "/node/v1/challenge?node=node_1", "", "203.0.113.7:4000"); out.Code != http.StatusOK {
			t.Fatalf("limit 0: %d", out.Code)
		}
	}
	r.ext.limiter = nil
	r.ext.cfg.Nodes.ChallengeRateLimit = 1
	for range 3 {
		if out := r.fromAddr(t, http.MethodGet, "/node/v1/challenge?node=node_1", "", "203.0.113.7:4000"); out.Code != http.StatusOK {
			t.Fatalf("no limiter: %d", out.Code)
		}
	}
}

type brokenLimiter struct{}

func (brokenLimiter) Allow(context.Context, string, int, time.Duration) (bool, error) {
	return false, errors.New("kv down")
}

func (brokenLimiter) Remaining(context.Context, string, int, time.Duration) (int, error) {
	return 0, errors.New("kv down")
}

// A limiter that can't answer refuses, so an outage is not an open door.
func TestNodeLimitsFailClosed(t *testing.T) {
	r := newRig(t)
	r.ext.limiter = brokenLimiter{}
	r.ext.cfg.Nodes.JoinRateLimit = 10
	if out := r.fromAddr(t, http.MethodPost, "/api/v1/nodes/join", joinBody(t, nil), "203.0.113.7:4000"); out.Code != http.StatusServiceUnavailable || r.tokens.spent != 0 {
		t.Fatalf("join with the limiter down: %d", out.Code)
	}
}
