package dbgateway

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/xraph/loomplane/internal/dbgateway/plane"
)

var testSalt = []byte("0123456789abcdef")

// gatewayKeys are the private halves a deployed gateway holds.
type gatewayKeys struct {
	signing ed25519.PrivateKey
	sealing *ecdh.PrivateKey
}

func testGateway(t *testing.T, id string) (Gateway, gatewayKeys) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	seal, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	g := Gateway{
		OrgID: "org_1", ID: id, ProjectID: "proj_1", EnvironmentID: "env_1", Name: "main",
		Provider: "cloud-run", Region: "us-central1", Suffix: "us-central1.db.example.com",
		SigningPublicKey: pub, SealingPublicKey: seal.PublicKey(),
	}
	return g, gatewayKeys{signing: priv, sealing: seal}
}

// openCredential opens a route's backend credential the way the router does.
func openCredential(t *testing.T, key *ecdh.PrivateKey, rt plane.Route) (user, password string) {
	t.Helper()
	plain, err := plane.Open(key, rt.Backend.Credential, []byte("backend-credential:"+rt.ID))
	if err != nil {
		t.Fatalf("open the credential of %s: %v", rt.ID, err)
	}
	var c struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(plain, &c); err != nil {
		t.Fatalf("credential of %s isn't the router's JSON: %v", rt.ID, err)
	}
	return c.User, c.Password
}

// cleartextLogin runs plane.AuthenticateCleartext against v over a pipe, as a
// client that sends password when asked.
func cleartextLogin(t *testing.T, v plane.Verifier, password string) error {
	t.Helper()
	srv, cli := net.Pipe()
	defer srv.Close()
	defer cli.Close()
	_ = srv.SetDeadline(time.Now().Add(5 * time.Second))
	_ = cli.SetDeadline(time.Now().Add(5 * time.Second))
	be, fe := pgproto3.NewBackend(srv, srv), pgproto3.NewFrontend(cli, cli)
	done := make(chan error, 1)
	go func() { done <- plane.AuthenticateCleartext(be, v, nil) }()
	msg, err := fe.Receive()
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if _, ok := msg.(*pgproto3.AuthenticationCleartextPassword); !ok {
		t.Fatalf("got %T, want AuthenticationCleartextPassword", msg)
	}
	fe.Send(&pgproto3.PasswordMessage{Password: password})
	if err := fe.Flush(); err != nil {
		t.Fatal(err)
	}
	// On success the gateway's AuthenticationOk waits on the pipe for us to
	// read it; on failure it sends nothing.
	reply := make(chan pgproto3.BackendMessage, 1)
	go func() {
		m, rerr := fe.Receive()
		if rerr != nil {
			m = nil
		}
		reply <- m
	}()
	if err := <-done; err != nil {
		return err
	}
	if _, ok := (<-reply).(*pgproto3.AuthenticationOk); !ok {
		t.Fatal("no AuthenticationOk after a good password")
	}
	return nil
}

func TestCompileRoute(t *testing.T) {
	g, keys := testGateway(t, "dbgw_1")
	routes := []RouteRecord{{ID: "dbgr_1", Resource: "orders", State: "active", Salt: testSalt}}
	urls := map[string]string{"orders": "postgres://app:pw@db.do.example:25060/orders?sslmode=require"}

	s, skipped, err := Compile(g, routes, urls)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped %+v", skipped)
	}
	if s.GatewayID != g.ID || s.Version != 0 || !s.IssuedAt.IsZero() || len(s.Routes) != 1 {
		t.Fatalf("snapshot %+v", s)
	}
	rt := s.Routes[0]
	b := rt.Backend
	if b.Kind != "postgres" || b.Host != "db.do.example" || b.Port != 25060 || b.Database != "orders" ||
		b.SNI != "db.do.example" || b.SSLMode != "require" {
		t.Fatalf("backend %+v", b)
	}
	if rt.ID != "dbgr_1" || rt.Label != (plane.Label{DB: "orders"}) || rt.State != plane.RouteStateActive ||
		rt.IdleTimeout != 5*time.Minute || rt.Params == nil || len(rt.Params) != 0 {
		t.Fatalf("route %+v", rt)
	}
	if user, password := openCredential(t, keys.sealing, rt); user != "app" || password != "pw" {
		t.Fatalf("credential opens to %q/%q, want app/pw", user, password)
	}
	if len(rt.Verifiers) != 1 {
		t.Fatalf("verifiers %+v", rt.Verifiers)
	}
	v := rt.Verifiers[0]
	if v.Role != "app" || v.Iterations != 4096 || string(v.Salt) != string(testSalt) {
		t.Fatalf("verifier %+v", v)
	}
	if err := cleartextLogin(t, v, "pw"); err != nil {
		t.Fatalf("the backend's password doesn't log in: %v", err)
	}
	if err := cleartextLogin(t, v, "nope"); !errors.Is(err, plane.ErrAuth) {
		t.Fatalf("a wrong password: %v, want ErrAuth", err)
	}
	// Sealed to the gateway's key and its route: another route's context
	// doesn't open it.
	if _, err := plane.Open(keys.sealing, b.Credential, []byte("backend-credential:dbgr_2")); err == nil {
		t.Fatal("the credential opened under another route's context")
	}
}

func TestCompileAddressForms(t *testing.T) {
	g, keys := testGateway(t, "dbgw_1")
	for _, tc := range []struct {
		name, url            string
		host, db, ssl        string
		port                 int
		wantUser, wantSecret string
	}{
		{"postgresql scheme and default port", "postgresql://app:pw@db.example/orders", "db.example", "orders", "require", 5432, "app", "pw"},
		{"sslmode disable", "postgres://app:pw@10.0.0.4:5433/orders?sslmode=disable", "10.0.0.4", "orders", "disable", 5433, "app", "pw"},
		{"sslmode verify-full", "postgres://app:pw@db.example/orders?sslmode=verify-full", "db.example", "orders", "verify-full", 5432, "app", "pw"},
		{"escaped password", "postgres://app:p%40ss%3Aw%2Frd@db.example/orders", "db.example", "orders", "require", 5432, "app", "p@ss:w/rd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, skipped, err := Compile(g, []RouteRecord{{ID: "dbgr_1", Resource: "orders", State: "active", Salt: testSalt}},
				map[string]string{"orders": tc.url})
			if err != nil || len(skipped) != 0 || len(s.Routes) != 1 {
				t.Fatalf("routes %+v skipped %+v: %v", s.Routes, skipped, err)
			}
			b := s.Routes[0].Backend
			if b.Host != tc.host || b.SNI != tc.host || b.Port != tc.port || b.Database != tc.db || b.SSLMode != tc.ssl {
				t.Fatalf("backend %+v", b)
			}
			if user, password := openCredential(t, keys.sealing, s.Routes[0]); user != tc.wantUser || password != tc.wantSecret {
				t.Fatalf("credential %q/%q", user, password)
			}
		})
	}
}

func TestCompileExcludesUnreachable(t *testing.T) {
	g, _ := testGateway(t, "dbgw_1")
	routes := []RouteRecord{
		{ID: "dbgr_1", Resource: "orders", State: "active", Salt: testSalt},
		{ID: "dbgr_2", Resource: "billing", State: "active", Salt: testSalt},
	}
	s, skipped, err := Compile(g, routes, map[string]string{"orders": "postgres://app:pw@db.example/orders"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Routes) != 1 || s.Routes[0].ID != "dbgr_1" {
		t.Fatalf("routes %+v", s.Routes)
	}
	if len(skipped) != 1 || skipped[0] != (Skipped{Resource: "billing", Reason: "no address stored yet"}) {
		t.Fatalf("skipped %+v", skipped)
	}
}

// Every address the gateway can't serve is skipped with a reason, and the
// reason never carries the password or the address.
func TestCompileSkipsUnsupportedAddresses(t *testing.T) {
	g, _ := testGateway(t, "dbgw_1")
	const secret = "s3cret-pw"
	for _, tc := range []struct{ name, resource, url string }{
		{"unix socket in the query", "orders", "postgres://app:" + secret + "@/orders?host=/var/run/postgresql"},
		{"unix socket as the host", "orders", "postgres://app:" + secret + "@%2Fvar%2Frun%2Fpostgresql/orders"},
		{"several hosts", "orders", "postgres://app:" + secret + "@h1:5432,h2:5432/orders"},
		{"no host", "orders", "postgres://app:" + secret + "@/orders"},
		{"no database", "orders", "postgres://app:" + secret + "@db.example/"},
		{"unsupported sslmode", "orders", "postgres://app:" + secret + "@db.example/orders?sslmode=prefer"},
		{"another scheme", "orders", "mysql://app:" + secret + "@db.example/orders"},
		{"port out of range", "orders", "postgres://app:" + secret + "@db.example:99999/orders"},
		{"not a URL", "orders", "postgres://app:" + secret + "@db.example:port/orders\x7f"},
		{"no user", "orders", "postgres://db.example/orders"},
		{"no password", "orders", "postgres://app@db.example/orders"},
		{"a name that isn't a label", "Orders_DB", "postgres://app:" + secret + "@db.example/orders"},
		{"a name that reads as a branch", "orders--main", "postgres://app:" + secret + "@db.example/orders"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, skipped, err := Compile(g, []RouteRecord{{ID: "dbgr_1", Resource: tc.resource, State: "active", Salt: testSalt}},
				map[string]string{tc.resource: tc.url})
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if s.Routes == nil || len(s.Routes) != 0 {
				t.Fatalf("routes %+v, want none (and not nil)", s.Routes)
			}
			if len(skipped) != 1 || skipped[0].Resource != tc.resource || skipped[0].Reason == "" {
				t.Fatalf("skipped %+v", skipped)
			}
			if r := skipped[0].Reason; strings.Contains(r, secret) || strings.Contains(r, tc.url) || strings.Contains(r, "://") {
				t.Fatalf("reason %q echoes the address", r)
			}
		})
	}
}

func TestCompileSortsRoutesAndKeepsState(t *testing.T) {
	g, _ := testGateway(t, "dbgw_1")
	routes := []RouteRecord{
		{ID: "dbgr_b", Resource: "billing", State: "paused", Salt: testSalt},
		{ID: "dbgr_a", Resource: "orders", State: "active", Salt: testSalt},
	}
	urls := map[string]string{"orders": "postgres://app:pw@db.example/orders", "billing": "postgres://app:pw@db.example/billing"}
	s, skipped, err := Compile(g, routes, urls)
	if err != nil || len(skipped) != 0 || len(s.Routes) != 2 {
		t.Fatalf("routes %+v skipped %+v: %v", s.Routes, skipped, err)
	}
	if s.Routes[0].ID != "dbgr_a" || s.Routes[1].ID != "dbgr_b" {
		t.Fatalf("routes not sorted by ID: %s, %s", s.Routes[0].ID, s.Routes[1].ID)
	}
	if s.Routes[1].State != plane.RouteStatePaused {
		t.Fatalf("paused route state %q", s.Routes[1].State)
	}
}

func TestCompileNeedsTheSealingKey(t *testing.T) {
	g, _ := testGateway(t, "dbgw_1")
	g.SealingPublicKey = nil
	_, _, err := Compile(g, []RouteRecord{{ID: "dbgr_1", Resource: "orders", State: "active", Salt: testSalt}},
		map[string]string{"orders": "postgres://app:s3cret-pw@db.example/orders"})
	if err == nil {
		t.Fatal("compiled without a key to seal the credential to")
	}
	if strings.Contains(err.Error(), "s3cret-pw") {
		t.Fatalf("error %q echoes the password", err)
	}
}
