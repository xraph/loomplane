package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/authsome/account"
	"github.com/xraph/forge"
)

func TestSignupGate(t *testing.T) {
	lookupErr := errors.New("database is down")
	yes := func(context.Context, string) (bool, error) { return true, nil }
	no := func(context.Context, string) (bool, error) { return false, nil }
	broken := func(context.Context, string) (bool, error) { return false, lookupErr }
	on := func(context.Context) bool { return true }
	off := func(context.Context) bool { return false }

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
	} {
		t.Run(name, func(t *testing.T) {
			ctx := tc.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			var seen []string
			watch := func(f func(context.Context, string) (bool, error)) func(context.Context, string) (bool, error) {
				return func(ctx context.Context, email string) (bool, error) {
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

func TestRefuseSignUps(t *testing.T) {
	waitlistOn := false
	e := &Extension{gate: &signupGate{
		pending:    func(context.Context) bool { return false },
		waitlistOn: func(context.Context) bool { return waitlistOn },
		approved:   func(context.Context, string) (bool, error) { return false, nil },
		invited: func(_ context.Context, email string) (bool, error) {
			return email == "ada@example.com", nil
		},
	}}
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
