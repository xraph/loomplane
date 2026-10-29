//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/authsome/organization"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/config"
)

const (
	pendingRefusal  = "Loomplane isn't set up yet. Open it in a browser to create the first account."
	waitlistRefusal = "Sign-up needs an invitation or an approved waitlist entry."
)

// refusal is the body of a refused sign-up.
type refusal struct {
	Error string `json:"error"`
	Type  string `json:"type"`
}

// signUp posts to authsome's own sign-up route, the way the sign-up form
// does, and returns the status and the refusal when there is one.
func signUp(c *setupClient, email string) (int, refusal) {
	c.t.Helper()
	status, raw, _ := (&setupClient{t: c.t, base: c.base, key: c.key}).send(http.MethodPost, "/authsome/v1/signup",
		map[string]string{"email": email, "password": "Correct-horse-battery-9"})
	var out refusal
	if status >= 300 {
		if err := json.Unmarshal(raw, &out); err != nil {
			c.t.Fatalf("sign up %s: %d %q isn't JSON: %v", email, status, raw, err)
		}
	}
	return status, out
}

// clientWaitlist is what authsome's client config, which its sign-up and
// waitlist forms read, says about the waitlist.
func clientWaitlist(c *setupClient) bool {
	c.t.Helper()
	var out struct {
		Waitlist *struct {
			Enabled bool `json:"enabled"`
		} `json:"waitlist"`
	}
	c.get("/authsome/v1/client-config?key="+url.QueryEscape(c.key), &out)
	if out.Waitlist == nil {
		c.t.Fatal("authsome's client config has no waitlist section")
	}
	return out.Waitlist.Enabled
}

func execSQL(t *testing.T, dsn, query string, args ...any) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tag, err := conn.Exec(ctx, query, args...)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if tag.RowsAffected() == 0 {
		t.Fatalf("%s %v changed nothing", query, args)
	}
}

func TestSignupGate_pendingRefusesEveryone(t *testing.T) {
	on, off := true, false
	for name, waitlist := range map[string]*bool{"waitlist off": &off, "waitlist on": &on, "waitlist unset": nil} {
		t.Run(name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Identity.Waitlist = waitlist
			r, c := freshSetup(t, &cfg)
			defer r.stop()

			status, why := signUp(c, "mallory@example.com")
			if status != http.StatusForbidden || why.Error != pendingRefusal || why.Type != "setup_pending" {
				t.Fatalf("sign-up before setup: %d %+v, want 403 %q", status, why, pendingRefusal)
			}
			if n := countRows(t, cfg.Database.URL, liveUsers); n != 0 {
				t.Fatalf("%d users after a refused sign-up, want 0", n)
			}
			// Setup still works, and is then the only account.
			if code, body := c.setup(ada); code != http.StatusOK {
				t.Fatalf("setup: %d %s", code, body)
			}
			if n := countRows(t, cfg.Database.URL, liveUsers); n != 1 {
				t.Fatalf("%d users after setup, want 1", n)
			}
		})
	}
}

func TestSignupGate_waitlistOn(t *testing.T) {
	cfg := config.Defaults()
	r, c := freshSetup(t, &cfg)
	defer r.stop()
	if code, body := c.setup(ada); code != http.StatusOK {
		t.Fatalf("setup: %d %s", code, body)
	}
	if !clientWaitlist(c) {
		t.Fatal("authsome's client config says the waitlist is off after setup turned it on")
	}

	// A stranger is turned away.
	if status, why := signUp(c, "mallory@example.com"); status != http.StatusForbidden || why.Error != waitlistRefusal || why.Type != "waitlist_required" {
		t.Fatalf("a stranger's sign-up: %d %+v, want 403 %q", status, why, waitlistRefusal)
	}

	// Joining the waitlist isn't enough; an approved entry is.
	status, raw, _ := c.send(http.MethodPost, "/authsome/v1/waitlist/join", map[string]string{"email": "Grace@Example.com"})
	if status >= 300 {
		t.Fatalf("join the waitlist: %d %s", status, raw)
	}
	if status, why := signUp(c, "grace@example.com"); status != http.StatusForbidden || why.Error != waitlistRefusal {
		t.Fatalf("sign-up with a pending waitlist entry: %d %+v, want 403", status, why)
	}
	execSQL(t, cfg.Database.URL, `UPDATE authsome_waitlist_entries SET status = 'approved' WHERE email = $1`, "grace@example.com")
	if status, why := signUp(c, "Grace@Example.com"); status != http.StatusCreated {
		t.Fatalf("sign-up with an approved waitlist entry: %d %+v, want 201", status, why)
	}

	// Someone Ada invites may sign up, until the invitation expires.
	for _, email := range []string{"Hopper@Example.com", "late@example.com"} {
		if status, raw, _ := c.send(http.MethodPost, "/api/v1/invitations", map[string]string{"email": email}); status != http.StatusCreated {
			t.Fatalf("invite %s: %d %s", email, status, raw)
		}
	}
	if status, why := signUp(c, "hopper@example.com"); status != http.StatusCreated {
		t.Fatalf("an invited person's sign-up: %d %+v, want 201", status, why)
	}
	execSQL(t, cfg.Database.URL, `UPDATE authsome_invitations SET expires_at = now() - interval '1 hour' WHERE email = $1`, "late@example.com")
	if status, why := signUp(c, "late@example.com"); status != http.StatusForbidden || why.Error != waitlistRefusal {
		t.Fatalf("sign-up on an expired invitation: %d %+v, want 403", status, why)
	}

	if n := countRows(t, cfg.Database.URL, liveUsers); n != 3 {
		t.Fatalf("%d users, want Ada, Grace and Hopper", n)
	}
}

func TestSignupGate_waitlistOff(t *testing.T) {
	cfg := config.Defaults()
	off := false
	cfg.Identity.Waitlist = &off
	r, c := freshSetup(t, &cfg)
	defer r.stop()
	if code, body := c.setup(ada); code != http.StatusOK {
		t.Fatalf("setup: %d %s", code, body)
	}
	if clientWaitlist(c) {
		t.Fatal("authsome's client config says the waitlist is on while the override keeps it off")
	}
	if status, why := signUp(c, "grace@example.com"); status != http.StatusCreated {
		t.Fatalf("a stranger's sign-up with the waitlist off: %d %+v, want 201", status, why)
	}
}

// Setup's user exists before setup has made its organization or stored the
// waitlist. A sign-up in that window would see an instance that has users
// and no waitlist, and if setup then rolled back, the stranger would be
// left as the only account. Sign-up is refused until setup has finished.
func TestSignupGate_refusedWhileSetupRuns(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	held, release := make(chan struct{}), make(chan error)
	var calls atomic.Int32
	// No waitlist override, as on a deploy that leaves it unset.
	r := boot(t, cfg, func(app forge.App) {
		ext, err := app.GetExtension(identity.Name)
		if err != nil {
			t.Fatal(err)
		}
		ext.(*identity.Extension).SetSetupOrgHook(func(ctx context.Context, o *organization.Organization, create func(context.Context, *organization.Organization) error) error {
			if err := create(ctx, o); err != nil {
				return err
			}
			if calls.Add(1) > 1 {
				return nil
			}
			close(held)
			return <-release
		})
	})
	defer r.stop()
	c := &setupClient{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}

	finished := make(chan int, 1)
	go func() {
		status, _, _ := (&setupClient{t: t, base: c.base, key: c.key}).send(http.MethodPost, "/api/v1/setup", ada)
		finished <- status
	}()
	select {
	case <-held:
	case <-time.After(time.Minute):
		t.Fatal("setup never reached its organization step")
	}
	if n := countRows(t, cfg.Database.URL, liveUsers); n != 1 {
		t.Fatalf("%d users while setup is held, want Ada alone", n)
	}
	status, why := signUp(c, "mallory@example.com")
	release <- errors.New("injected organization failure")
	if status != http.StatusForbidden || why.Error != pendingRefusal || why.Type != "setup_pending" {
		t.Fatalf("sign-up while setup runs: %d %+v, want 403 %q", status, why, pendingRefusal)
	}
	if code := <-finished; code != http.StatusInternalServerError {
		t.Fatalf("the held setup: %d, want 500", code)
	}
	if n := countRows(t, cfg.Database.URL, liveUsers); n != 0 {
		t.Fatalf("%d users after setup rolled back, want 0", n)
	}

	// Once setup has finished, sign-up is judged on the finished instance,
	// where setup turned the waitlist on.
	if code, body := c.setup(ada); code != http.StatusOK {
		t.Fatalf("setup after the rollback: %d %s", code, body)
	}
	if status, why := signUp(c, "mallory@example.com"); status != http.StatusForbidden || why.Error != waitlistRefusal {
		t.Fatalf("sign-up after setup: %d %+v, want 403 %q", status, why, waitlistRefusal)
	}
}
