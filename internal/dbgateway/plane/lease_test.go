package plane

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// gatewayCode is the SQLSTATE of a *GatewayError, or "" for anything else.
func gatewayCode(err error) string {
	var ge *GatewayError
	if errors.As(err, &ge) {
		return ge.Code
	}
	return ""
}

func TestWithLeaseRunsAndReleases(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))
	ran := false
	err := h.r.WithLease(context.Background(), hostFor("orders"), testRole, testPass, func(l *Lease) (LeaseState, error) {
		ran = true
		if n := h.handle("r1").sessions; n != 1 {
			t.Errorf("sessions during fn = %d, want the lease counted as one", n)
		}
		if _, err := roundTrip(context.Background(), l.Conn(), "SELECT 1"); err != nil {
			t.Errorf("query on the lease: %v", err)
		}
		return LeaseClean, nil
	})
	if err != nil || !ran {
		t.Fatalf("WithLease = %v, ran %v", err, ran)
	}
	pool := h.handle("r1").pool
	if s := pool.Stats(); s.Open != 1 || s.Idle != 1 {
		t.Fatalf("pool after a clean release = %+v, want one idle connection", s)
	}
	if n := h.handle("r1").sessions; n != 0 {
		t.Fatalf("sessions after WithLease = %d, want 0", n)
	}
	// The probe ran first, on the same pooled connection.
	if q := h.dialer.server(0).queries(); len(q) != 2 || q[0] != "SHOW max_connections" {
		t.Fatalf("backend queries = %q, want the probe then the query", q)
	}

	// role@label routes as Serve does.
	err = h.r.WithLease(context.Background(), "10.1.2.3", testRole+"@orders", testPass, func(*Lease) (LeaseState, error) {
		return LeaseClean, nil
	})
	if err != nil {
		t.Fatalf("WithLease by user@label = %v", err)
	}
}

func TestWithLeasePanicBreaksLease(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic did not reach the caller")
			}
		}()
		_ = h.r.WithLease(context.Background(), hostFor("orders"), testRole, testPass, func(*Lease) (LeaseState, error) {
			panic("boom")
		})
	}()
	if s := h.handle("r1").pool.Stats(); s.Open != 0 {
		t.Fatalf("pool after a panic = %+v, want the connection closed", s)
	}
	if n := h.handle("r1").sessions; n != 0 {
		t.Fatalf("sessions after a panic = %d, want 0", n)
	}
}

func TestWithLeaseErrors(t *testing.T) {
	never := func(*Lease) (LeaseState, error) {
		t.Error("fn ran")
		return LeaseClean, errors.New("fn ran")
	}
	ctx := context.Background()

	t.Run("unknown route", func(t *testing.T) {
		h := newRouterHarness(t, nil, nil)
		h.apply(h.route("r1", "orders"))
		for _, host := range []string{hostFor("billing"), "db.elsewhere.example.org"} {
			err := h.r.WithLease(ctx, host, testRole, testPass, never)
			if gatewayCode(err) != "08004" || !strings.Contains(err.Error(), "<label>."+string(testSuffix)) {
				t.Fatalf("host %s: err = %v, want 08004 naming the accepted forms", host, err)
			}
		}
	})
	t.Run("bad password", func(t *testing.T) {
		h := newRouterHarness(t, nil, nil)
		h.apply(h.route("r1", "orders"))
		for _, tc := range []struct{ role, pw string }{{testRole, "wrong"}, {"ghost", testPass}} {
			err := h.r.WithLease(ctx, hostFor("orders"), tc.role, tc.pw, never)
			if gatewayCode(err) != "28P01" || err.Error() != `password authentication failed for user "`+tc.role+`"` {
				t.Fatalf("role %s: err = %v, want 28P01", tc.role, err)
			}
		}
		if h.dialer.dials() != 0 {
			t.Fatal("a failed login dialed the backend")
		}
	})
	t.Run("draining router", func(t *testing.T) {
		h := newRouterHarness(t, nil, nil)
		h.apply(h.route("r1", "orders"))
		h.r.Drain(ctx)
		if err := h.r.WithLease(ctx, hostFor("orders"), testRole, testPass, never); gatewayCode(err) != "57P01" {
			t.Fatalf("err = %v, want 57P01", err)
		}
	})
	t.Run("draining route", func(t *testing.T) {
		h := newRouterHarness(t, nil, nil)
		rt := h.route("r1", "orders")
		rt.State = RouteStateDraining
		h.apply(rt)
		if err := h.r.WithLease(ctx, hostFor("orders"), testRole, testPass, never); gatewayCode(err) != "57P01" {
			t.Fatalf("err = %v, want 57P01", err)
		}
	})
	t.Run("paused past the limit", func(t *testing.T) {
		h := newRouterHarness(t, nil, nil)
		h.r.pauseLimit = 100 * time.Millisecond
		rt := h.route("r1", "orders")
		rt.State = RouteStatePaused
		h.apply(rt)
		if err := h.r.WithLease(ctx, hostFor("orders"), testRole, testPass, never); gatewayCode(err) != "57P01" {
			t.Fatalf("err = %v, want 57P01", err)
		}
	})
	t.Run("pool busy", func(t *testing.T) {
		// max_connections 6 leaves one connection for the pool.
		h := newRouterHarness(t, &fakeDialer{maxConns: "6"}, nil)
		h.apply(h.route("r1", "orders"))
		held, release := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- h.r.WithLease(ctx, hostFor("orders"), testRole, testPass, func(*Lease) (LeaseState, error) {
				close(held)
				<-release
				return LeaseClean, nil
			})
		}()
		<-held
		wctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		err := h.r.WithLease(wctx, hostFor("orders"), testRole, testPass, never)
		close(release)
		if gatewayCode(err) != "53300" {
			t.Fatalf("err = %v, want 53300", err)
		}
		if err := <-done; err != nil {
			t.Fatalf("holder: %v", err)
		}
	})
	t.Run("backend unreachable", func(t *testing.T) {
		h := newRouterHarness(t, nil, nil)
		h.dialer.setErr(errors.New("dial tcp 10.0.0.1:5432: connection refused"))
		h.apply(h.route("r1", "orders"))
		err := h.r.WithLease(ctx, hostFor("orders"), testRole, testPass, never)
		if gatewayCode(err) != "08006" || strings.Contains(err.Error(), "10.0.0.1") {
			t.Fatalf("err = %v, want 08006 without the backend address", err)
		}
	})
	t.Run("backend error passes through", func(t *testing.T) {
		h := newRouterHarness(t, nil, nil)
		h.apply(h.route("r1", "orders"))
		pgErr := &pgconn.PgError{Severity: "ERROR", Code: "42601", Message: "syntax error"}
		err := h.r.WithLease(ctx, hostFor("orders"), testRole, testPass, func(*Lease) (LeaseState, error) {
			return LeaseClean, pgErr
		})
		if err != pgErr { //nolint:errorlint // the test pins the error as unwrapped
			t.Fatalf("err = %v, want the backend's error unchanged", err)
		}
	})
}

// A request still running when the router drains past its limit is cut off:
// its connection's I/O ends, the lease is broken, and the error is 57P01.
func TestWithLeaseDrainCutsOff(t *testing.T) {
	h := newRouterHarness(t, &fakeDialer{hold: make(chan struct{})}, nil)
	t.Cleanup(func() { close(h.dialer.hold) })
	h.r.drainLimit = 100 * time.Millisecond
	h.apply(h.route("r1", "orders"))
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- h.r.WithLease(context.Background(), hostFor("orders"), testRole, testPass, func(l *Lease) (LeaseState, error) {
			close(started)
			if _, err := roundTrip(context.Background(), l.Conn(), "SELECT hold"); err != nil {
				return LeaseBroken, err
			}
			return LeaseClean, nil
		})
	}()
	<-started
	pool := h.handle("r1").pool
	h.r.Drain(context.Background())
	select {
	case err := <-done:
		if gatewayCode(err) != "57P01" {
			t.Fatalf("err = %v, want 57P01", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the drain did not cut the request off")
	}
	if s := pool.Stats(); s.Open != 0 {
		t.Fatalf("pool = %+v, want the connection closed", s)
	}
}
