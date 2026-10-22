package deployments

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
)

// guardFixture is a DialGuard whose lookups and local addresses are fixed.
type guardFixture struct {
	lookups int
	fail    bool
	now     time.Time
}

func (f *guardFixture) guard() *DialGuard {
	return &DialGuard{
		Denied:   []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")},
		Services: []string{"db.internal:5432", ":8780", "fly-provider:9000"},
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			f.lookups++
			if f.fail {
				return nil, errors.New("no such host")
			}
			switch host {
			case "db.internal":
				return []netip.Addr{netip.MustParseAddr("10.1.2.3"), netip.MustParseAddr("fd00::5")}, nil
			case "fly-provider":
				return []netip.Addr{netip.MustParseAddr("::ffff:10.1.2.9")}, nil
			}
			return nil, errors.New("no such host")
		},
		Local: func() ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("127.0.0.1")}, nil
		},
		now: func() time.Time { return f.now },
	}
}

func TestDialGuardRefusesLoomplanesOwnServices(t *testing.T) {
	f := &guardFixture{now: time.Unix(1_700_000_000, 0)}
	g := f.guard()
	for addr, want := range map[string]bool{
		// Its database, at its port, in every form.
		"10.1.2.3:5432": false, "[::ffff:10.1.2.3]:5432": false, "[64:ff9b::a01:203]:5432": false, "[64:ff9b:1::a01:203]:5432": false, "[fd00::5]:5432": false,
		// The same hosts at other ports are someone's endpoints.
		"10.1.2.3:5433": true, "[fd00::5]:6379": true,
		// The API listens on every address of this host.
		"192.0.2.10:8780": false, "192.0.2.10:8781": true,
		// A provider plugin.
		"10.1.2.9:9000": false, "10.1.2.9:9001": true,
		// An operator's range, at any port and as NAT64.
		"198.51.100.7:443": false, "[64:ff9b::c633:6407]:5432": false,
		// Everything built in still holds.
		"127.0.0.1:5432": false, "0.0.0.7:80": false, "[64:ff9b::a9fe:a9fe]:80": false,
		"203.0.113.1:5432": true,
	} {
		if got := g.allows(t.Context(), netip.MustParseAddrPort(addr)); got != want {
			t.Fatalf("%s: got %t, want %t", addr, got, want)
		}
	}
	// A nil guard refuses only what's built in.
	var none *DialGuard
	if !none.allows(t.Context(), netip.MustParseAddrPort("10.1.2.3:5432")) || none.allows(t.Context(), netip.MustParseAddrPort("[64:ff9b::7f00:1]:80")) {
		t.Fatal("a nil guard")
	}
}

func TestDialGuardCachesLookupsBriefly(t *testing.T) {
	f := &guardFixture{now: time.Unix(1_700_000_000, 0)}
	g := f.guard()
	db := netip.MustParseAddrPort("10.1.2.3:5432")
	for range 3 {
		if g.allows(t.Context(), db) {
			t.Fatal("dialed the database")
		}
	}
	if f.lookups != 2 {
		t.Fatalf("looked up %d times, want once per named service", f.lookups)
	}
	// Once it's stale it's looked up again; a failed lookup keeps what
	// was known.
	f.now = f.now.Add(2 * dialGuardTTL)
	f.fail = true
	if g.allows(t.Context(), db) {
		t.Fatal("a failed lookup forgot the database")
	}
	if f.lookups != 4 {
		t.Fatalf("looked up %d times after the cache went stale", f.lookups)
	}
}

func TestClusterChecksRefuseLoomplanesOwnServices(t *testing.T) {
	creds := dialCredentials{
		"conn_cluster": {ID: "conn_cluster", OrgID: "org", Cloud: connections.Kubernetes, Status: connections.StatusReady, Details: map[string]string{}},
	}
	f := &guardFixture{now: time.Now()}
	target := &OwnedTarget{Cloud: connections.Kubernetes, Credentials: creds, DialGuard: f.guard()}
	dial, _, err := target.TargetDial(t.Context(), "org", "conn_cluster")
	if err != nil {
		t.Fatal(err)
	}
	// The guard refuses before connecting, so nothing is dialed.
	for _, addr := range []string{"10.1.2.3:5432", "198.51.100.9:6379", "192.0.2.10:8780"} {
		conn, dialErr := dial(t.Context(), "tcp", addr)
		if dialErr == nil {
			_ = conn.Close()
			t.Fatalf("%s: dialed", addr)
		}
		if host, _, _ := strings.Cut(addr, ":"); dialErr.Error() != host+" resolves to an address Loomplane doesn't connect to from its own network" {
			t.Fatalf("%s: %v", addr, dialErr)
		}
	}
}

func TestDialGuardServesWhatItKnowsWhileItLooksUpAgain(t *testing.T) {
	f := &guardFixture{now: time.Unix(1_700_000_000, 0)}
	g := f.guard()
	db := netip.MustParseAddrPort("10.1.2.3:5432")
	if g.allows(t.Context(), db) {
		t.Fatal("dialed the database")
	}
	// Once what it knows is stale, one check looks the services up again
	// and stalls on DNS; another isn't held up meanwhile.
	f.now = f.now.Add(2 * dialGuardTTL)
	lookup, entered, release := g.Lookup, make(chan struct{}), make(chan struct{})
	var once sync.Once
	g.Lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		once.Do(func() { close(entered) })
		<-release
		return lookup(ctx, host)
	}
	refreshed := make(chan bool)
	go func() { refreshed <- g.allows(context.Background(), db) }()
	<-entered
	other := make(chan bool)
	go func() { other <- g.allows(context.Background(), db) }()
	select {
	case allowed := <-other:
		if allowed {
			t.Fatal("a check during the lookup dialed the database")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a check waited on another's DNS lookup")
	}
	close(release)
	if <-refreshed {
		t.Fatal("the refreshing check dialed the database")
	}
}
