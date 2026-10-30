package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/authsome/account"
	authmw "github.com/xraph/authsome/middleware"
	"github.com/xraph/authsome/ratelimit"
	"github.com/xraph/forge"
)

func TestSignupGate(t *testing.T) {
	lookupErr := errors.New("database is down")
	yes := func(context.Context, string) (bool, error) { return true, nil }
	no := func(context.Context, string) (bool, error) { return false, nil }
	broken := func(context.Context, string) (bool, error) { return false, lookupErr }
	on := func(context.Context) bool { return true }
	off := func(context.Context) bool { return false }
	lockErr := errors.New("lock timed out")
	// full is a set of check slots that are all taken.
	full := func() chan struct{} {
		slots := make(chan struct{}, 1)
		slots <- struct{}{}
		return slots
	}
	setupRunning := func(context.Context, func(context.Context) error) (bool, error) { return false, nil }
	lockBroken := func(context.Context, func(context.Context) error) (bool, error) { return false, lockErr }

	for name, tc := range map[string]struct {
		ctx      context.Context
		gate     signupGate
		want     error
		wantSeen string
	}{
		"setup's own sign-up while pending": {
			ctx:  withSetup(context.Background()),
			gate: signupGate{pending: on, waitlistOn: on, approved: no, invited: no},
		},
		"pending": {
			gate: signupGate{pending: on, waitlistOn: off, approved: yes, invited: yes},
			want: errSetupPending,
		},
		"waitlist off": {
			gate: signupGate{pending: off, waitlistOn: off, approved: no, invited: no},
		},
		"invited": {
			gate:     signupGate{pending: off, waitlistOn: on, approved: no, invited: yes},
			wantSeen: "ada@example.com",
		},
		"approved": {
			gate:     signupGate{pending: off, waitlistOn: on, approved: yes, invited: no},
			wantSeen: "ada@example.com",
		},
		"neither invited nor approved": {
			gate: signupGate{pending: off, waitlistOn: on, approved: no, invited: no},
			want: errWaitlist,
		},
		"invitation lookup fails": {
			gate: signupGate{pending: off, waitlistOn: on, approved: no, invited: broken},
			want: lookupErr,
		},
		"waitlist lookup fails": {
			gate: signupGate{pending: off, waitlistOn: on, approved: broken, invited: no},
			want: lookupErr,
		},
		"setup running, waitlist off": {
			gate: signupGate{pending: off, waitlistOn: off, approved: yes, invited: yes, outsideSetup: setupRunning},
			want: errSetupPending,
		},
		"setup running, invited": {
			gate: signupGate{pending: off, waitlistOn: on, approved: yes, invited: yes, outsideSetup: setupRunning},
			want: errSetupPending,
		},
		"setup lock fails": {
			gate: signupGate{pending: off, waitlistOn: off, approved: yes, invited: yes, outsideSetup: lockBroken},
			want: lockErr,
		},
		"setup's own sign-up while setup holds the lock": {
			ctx:  withSetup(context.Background()),
			gate: signupGate{pending: on, waitlistOn: on, approved: no, invited: no, outsideSetup: setupRunning, slots: full()},
		},
		"no free check before the timeout, waitlist off": {
			gate: signupGate{pending: off, waitlistOn: off, approved: yes, invited: yes, slots: full(), timeout: 10 * time.Millisecond},
			want: context.DeadlineExceeded,
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := tc.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			if tc.gate.outsideSetup == nil {
				tc.gate.outsideSetup = noSetup
			}
			if tc.gate.slots == nil {
				tc.gate.slots = make(chan struct{}, 1)
			}
			if tc.gate.timeout == 0 {
				tc.gate.timeout = time.Second
			}
			// Every look at the instance happens while setup is kept out.
			inLock := false
			lock := tc.gate.outsideSetup
			tc.gate.outsideSetup = func(ctx context.Context, judge func(context.Context) error) (bool, error) {
				return lock(ctx, func(ctx context.Context) error {
					inLock = true
					defer func() { inLock = false }()
					return judge(ctx)
				})
			}
			pending := tc.gate.pending
			tc.gate.pending = func(ctx context.Context) bool {
				if !inLock {
					t.Error("counted users outside the setup lock")
				}
				return pending(ctx)
			}
			var seen []string
			watch := func(f func(context.Context, string) (bool, error)) func(context.Context, string) (bool, error) {
				return func(ctx context.Context, email string) (bool, error) {
					if !inLock {
						t.Error("looked the address up outside the setup lock")
					}
					seen = append(seen, email)
					return f(ctx, email)
				}
			}
			tc.gate.approved, tc.gate.invited = watch(tc.gate.approved), watch(tc.gate.invited)
			err := tc.gate.OnBeforeSignUp(ctx, &account.SignUpRequest{Email: " Ada@Example.com "})
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("OnBeforeSignUp = %v, want %v", err, tc.want)
			}
			for _, email := range seen {
				if email != "ada@example.com" {
					t.Fatalf("looked up %q, want the address lowercased and trimmed", email)
				}
			}
			if tc.wantSeen != "" && len(seen) == 0 {
				t.Fatal("allowed without looking the address up")
			}
		})
	}
}

// noSetup stands in for the setup lock when no setup is running.
func noSetup(ctx context.Context, judge func(context.Context) error) (bool, error) {
	return true, judge(ctx)
}

func TestRefuseSignUps(t *testing.T) {
	waitlistOn := false
	e := &Extension{BaseExtension: forge.NewBaseExtension(Name, "test", "test"), gate: &signupGate{
		outsideSetup: noSetup,
		slots:        make(chan struct{}, 1),
		timeout:      time.Second,
		pending:      func(context.Context) bool { return false },
		waitlistOn:   func(context.Context) bool { return waitlistOn },
		approved:     func(context.Context, string) (bool, error) { return false, nil },
		invited: func(_ context.Context, email string) (bool, error) {
			if email == "broken@example.com" {
				return false, errors.New("database is down")
			}
			return email == "ada@example.com", nil
		},
	}}
	e.SetLogger(forge.NewNoopLogger())
	r := forge.NewRouter()
	r.Use(e.refuseSignUps)
	// The handler sees the body the middleware read.
	if err := r.POST(authsomeAPIPath+"/signup", func(c forge.Context) error {
		var in struct {
			Email string `json:"email"`
		}
		if err := c.BindJSON(&in); err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, in)
	}); err != nil {
		t.Fatal(err)
	}
	post := func(body string) (int, string) {
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, authsomeAPIPath+"/signup", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.Handler().ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}

	if code, body := post(`{"email":"grace@example.com"}`); code != http.StatusCreated || !strings.Contains(body, "grace@example.com") {
		t.Fatalf("waitlist off: %d %s, want 201 with the body intact", code, body)
	}
	waitlistOn = true
	code, body := post(`{"email":"grace@example.com"}`)
	var refused struct {
		Error string `json:"error"`
		Type  string `json:"type"`
	}
	if err := json.Unmarshal([]byte(body), &refused); err != nil || code != http.StatusForbidden ||
		refused.Error != errWaitlist.message || refused.Type != "waitlist_required" {
		t.Fatalf("a stranger with the waitlist on: %d %s, want 403 %q", code, body, errWaitlist.message)
	}
	if code, body := post(`{"email":"Ada@Example.com"}`); code != http.StatusCreated || !strings.Contains(body, "Ada@Example.com") {
		t.Fatalf("an invited address: %d %s, want 201 with the body intact", code, body)
	}
	// A check that can't finish refuses here, without reaching authsome.
	if code, body := post(`{"email":"broken@example.com"}`); code != http.StatusServiceUnavailable || strings.Contains(body, "broken@example.com") {
		t.Fatalf("a check that failed: %d %s, want 503", code, body)
	}
}

// The route's own limit, which authsome puts on its sign-up route, runs
// after refuseSignUps. signupLimit runs the same limit on the same budget
// first, so a flood from one address is throttled before the gate looks
// anything up, and a sign-up the gate passes is counted once.
func TestRefuseSignUps_rateLimitFirst(t *testing.T) {
	var judged atomic.Int32
	e := &Extension{BaseExtension: forge.NewBaseExtension(Name, "test", "test"), gate: &signupGate{
		outsideSetup: noSetup,
		slots:        make(chan struct{}, 1),
		timeout:      time.Second,
		pending:      func(context.Context) bool { return false },
		waitlistOn:   func(context.Context) bool { judged.Add(1); return true },
		approved:     func(context.Context, string) (bool, error) { return false, nil },
		invited: func(_ context.Context, email string) (bool, error) {
			return strings.HasSuffix(email, "@invited.example.com"), nil
		},
	}}
	e.SetLogger(forge.NewNoopLogger())
	const limit = 3
	limiter := routeLimiter{Limiter: ratelimit.NewMemoryLimiter()}
	cfg := authmw.RateLimitConfig{Limit: limit, Window: time.Minute}
	e.signupLimit = authmw.RateLimit(limiter, cfg)
	r := forge.NewRouter()
	r.Use(rateLimitScope)
	r.Use(e.refuseSignUps)
	if err := r.POST(authsomeAPIPath+"/signup", func(c forge.Context) error {
		return c.JSON(http.StatusCreated, map[string]bool{"ok": true})
	}, forge.WithMiddleware(authmw.RateLimit(limiter, cfg))); err != nil {
		t.Fatal(err)
	}
	post := func(from, email string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, authsomeAPIPath+"/signup", strings.NewReader(`{"email":"`+email+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = from + ":1234"
		r.Handler().ServeHTTP(w, req)
		return w.Code
	}

	for i := range limit + 3 {
		want := http.StatusForbidden
		if i >= limit {
			want = http.StatusTooManyRequests
		}
		if got := post("203.0.113.7", "bot@example.com"); got != want {
			t.Fatalf("sign-up %d from one address: %d, want %d", i+1, got, want)
		}
	}
	if n := judged.Load(); n != limit {
		t.Fatalf("the gate judged %d sign-ups of a flood, want %d", n, limit)
	}
	if got := post("198.51.100.9", "grace@example.com"); got != http.StatusForbidden {
		t.Fatalf("another address during the flood: %d, want 403", got)
	}
	// Counted once each, so all three fit the budget.
	for i := range limit {
		if got := post("192.0.2.10", "ada@invited.example.com"); got != http.StatusCreated {
			t.Fatalf("invited sign-up %d from one address: %d, want 201", i+1, got)
		}
	}
	if got := post("192.0.2.10", "ada@invited.example.com"); got != http.StatusTooManyRequests {
		t.Fatalf("a sign-up past the limit: %d, want 429", got)
	}
}

func TestSignupCheckSlots(t *testing.T) {
	for pool, want := range map[int]int{0: 1, 1: 1, 4: 1, 8: 2, 16: 4, 100: 8} {
		if got := signupCheckSlots(pool); got != want {
			t.Errorf("signupCheckSlots(%d) = %d, want %d", pool, got, want)
		}
	}
}

func TestSignupGateName(t *testing.T) {
	if got := (&signupGate{}).Name(); got != "loomplane-signup-gate" {
		t.Fatalf("Name() = %q", got)
	}
}

func TestSignupRefusals(t *testing.T) {
	for err, want := range map[*signupRefusalError]string{
		errSetupPending: "Loomplane isn't set up yet. Open it in a browser to create the first account.",
		errWaitlist:     "Sign-up needs an invitation or an approved waitlist entry.",
	} {
		if err.Error() != want || err.StatusCode() != http.StatusForbidden {
			t.Errorf("%q: status %d, want 403 %q", err.Error(), err.StatusCode(), want)
		}
		body, ok := err.ResponseBody().(map[string]any)
		if !ok || body["error"] != want || body["code"] != http.StatusForbidden || body["type"] == "" {
			t.Errorf("%q: body %v", want, err.ResponseBody())
		}
	}
}
