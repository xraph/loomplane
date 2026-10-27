package catalogconnect

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/xraph/loomplane/internal/recipes"
)

// fakePg answers the prober's queries as a server would.
type fakePg struct {
	version   string
	available []string
	asked     []string
	closed    bool
	// preload is SHOW shared_preload_libraries; preloadErr refuses it.
	preload    string
	preloadErr error
	// super and createRole are the connection role's rolsuper and
	// rolcreaterole.
	super, createRole bool
	queried           []string
}

func (f *fakePg) ServerVersion(context.Context) (string, error) { return f.version, nil }

func (f *fakePg) AvailableExtensions(_ context.Context, names []string) ([]string, error) {
	f.asked = names
	var out []string
	for _, n := range names {
		if slices.Contains(f.available, n) {
			out = append(out, n)
		}
	}
	return out, nil
}

func (f *fakePg) PreloadLibraries(context.Context) (string, error) {
	f.queried = append(f.queried, "preload")
	return f.preload, f.preloadErr
}

func (f *fakePg) RoleRights(context.Context) (bool, bool, error) {
	f.queried = append(f.queried, "role")
	return f.super, f.createRole, nil
}

func (f *fakePg) Close(context.Context) error {
	f.closed = true
	return nil
}

func TestPostgresProberReportsVersionAndMissingExtensions(t *testing.T) {
	for _, tc := range []struct {
		version, min string
		want         Report
	}{
		{"16.4", "", Report{Version: "16.4", Missing: []string{"timescaledb"}}},
		{"16.4 (Debian 16.4-1.pgdg120+1)", "16", Report{Version: "16.4", Missing: []string{"timescaledb"}}},
		{"16.4", "17", Report{Version: "16.4", Missing: []string{"timescaledb"}, Problems: []string{"PostgreSQL 16.4 is older than 17"}}},
		{"17.2", "17", Report{Version: "17.2", Missing: []string{"timescaledb"}}},
	} {
		pg := &fakePg{version: tc.version, available: []string{"vector", "postgis"}}
		var dsn string
		prober := PostgresProber{connect: func(_ context.Context, cfg *pgx.ConnConfig) (pgConn, error) {
			dsn = cfg.ConnString()
			return pg, nil
		}}
		got, err := prober.Probe(t.Context(), refuse, testDSN, recipes.Requirement{
			Engine: "postgres", MinVersion: tc.min, Extensions: []string{"vector", "timescaledb"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(*got, tc.want) {
			t.Fatalf("%s/%s: got %+v, want %+v", tc.version, tc.min, *got, tc.want)
		}
		if dsn != testDSN || !pg.closed || !slices.Equal(pg.asked, []string{"vector", "timescaledb"}) {
			t.Fatalf("connected to %q, closed %v, asked %v", dsn, pg.closed, pg.asked)
		}
	}
}

func refuse(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("connection refused")
}

// fakeRedis serves RESP on one end of a pipe and records each command.
func fakeRedis(t *testing.T, password, version string) (DialFunc, *[]string, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var commands, dialed []string
	dial := func(_ context.Context, network, address string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, network+" "+address)
		mu.Unlock()
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			r := bufio.NewReader(server)
			authed := password == ""
			for {
				args, err := readCommand(r)
				if err != nil {
					return
				}
				mu.Lock()
				commands = append(commands, strings.Join(args, " "))
				mu.Unlock()
				var reply string
				switch strings.ToUpper(args[0]) {
				case "AUTH":
					if args[len(args)-1] != password {
						reply = "-WRONGPASS invalid username-password pair or user is disabled.\r\n"
						break
					}
					authed, reply = true, "+OK\r\n"
				case "PING":
					reply = "+PONG\r\n"
					if !authed {
						reply = "-NOAUTH Authentication required.\r\n"
					}
				case "INFO":
					body := "# Server\r\nredis_version:" + version + "\r\nredis_mode:standalone\r\n"
					reply = "$" + strconv.Itoa(len(body)) + "\r\n" + body + "\r\n"
				default:
					reply = "-ERR unknown command\r\n"
				}
				if _, err = server.Write([]byte(reply)); err != nil {
					return
				}
			}
		}()
		return client, nil
	}
	return dial, &commands, &dialed
}

func readCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, n)
	for range n {
		if _, err = r.ReadString('\n'); err != nil {
			return nil, err
		}
		arg, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		args = append(args, strings.TrimSuffix(arg, "\r\n"))
	}
	return args, nil
}

func TestRedisProberChecksPingAndVersion(t *testing.T) {
	dial, commands, dialed := fakeRedis(t, "hunter2", "7.2.5")
	dsn := "redis://:hunter2@cache.example:6380/0"
	got, err := RedisProber{}.Probe(t.Context(), dial, dsn, recipes.Requirement{Engine: "redis", MinVersion: "7"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, Report{Version: "7.2.5"}) {
		t.Fatalf("report: %+v", *got)
	}
	if want := []string{"AUTH hunter2", "PING", "INFO server"}; !slices.Equal(*commands, want) {
		t.Fatalf("commands: %q", *commands)
	}
	if !slices.Equal(*dialed, []string{"tcp cache.example:6380"}) {
		t.Fatalf("dialed: %q", *dialed)
	}

	got, err = RedisProber{}.Probe(t.Context(), dial, dsn, recipes.Requirement{Engine: "redis", MinVersion: "8.2"})
	if err != nil || !slices.Equal(got.Problems, []string{"Redis 7.2.5 is older than 8.2"}) {
		t.Fatalf("old version: %+v %v", got, err)
	}

	// An ACL user authenticates with its name.
	dial, commands, _ = fakeRedis(t, "s3cret", "8.2.1")
	if _, err = (RedisProber{}).Probe(t.Context(), dial, "redis://app:s3cret@cache.example", recipes.Requirement{}); err != nil {
		t.Fatal(err)
	}
	if (*commands)[0] != "AUTH app s3cret" {
		t.Fatalf("ACL auth: %q", *commands)
	}
	// Without a password there's no AUTH.
	dial, commands, _ = fakeRedis(t, "", "8.2.1")
	if _, err = (RedisProber{}).Probe(t.Context(), dial, "redis://cache.example", recipes.Requirement{}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*commands, []string{"PING", "INFO server"}) {
		t.Fatalf("no password: %q", *commands)
	}

	// A refused password is the server's answer, without the password.
	dial, _, _ = fakeRedis(t, "right", "7.2.5")
	_, err = RedisProber{}.Probe(t.Context(), dial, "redis://:wrong-password@cache.example", recipes.Requirement{})
	if err == nil || strings.Contains(err.Error(), "wrong-password") || !strings.Contains(err.Error(), "WRONGPASS") {
		t.Fatalf("refused password: %v", err)
	}
}

func TestCheckerUsesTheGivenDialer(t *testing.T) {
	svc := NewService(newMemStore(), memSecrets{}, roleAuthz{})
	pg, err := svc.Create(t.Context(), admin, CreateInput{Name: "prod-pg", Engine: "postgres", DSN: testDSN})
	if err != nil {
		t.Fatal(err)
	}
	checker := &Checker{Endpoints: svc, Probers: map[string]Prober{"postgres": PostgresProber{}, "redis": RedisProber{}}}
	var dialed []string
	dial := func(_ context.Context, network, address string) (net.Conn, error) {
		dialed = append(dialed, network+" "+address)
		return nil, errors.New("connect: connection refused")
	}
	got, err := checker.Check(t.Context(), "org", pg.ID, recipes.Requirement{Engine: "postgres"}, dial)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"db.example:5432 isn't reachable from the target: connect: connection refused"}
	if !slices.Equal(got.Problems, want) || got.Version != "" {
		t.Fatalf("report: %+v", got)
	}
	if len(dialed) == 0 || slices.ContainsFunc(dialed, func(d string) bool { return d != "tcp db.example:5432" }) {
		t.Fatalf("dialed: %q", dialed)
	}
	text := fmt.Sprint(got)
	if strings.Contains(text, "u:p@") || strings.Contains(text, testDSN) || strings.Contains(text, "user=") {
		t.Fatalf("the report names the DSN: %s", text)
	}

	redisDial, _, redisDialed := fakeRedis(t, "hunter2", "7.2.5")
	cache, err := svc.Create(t.Context(), admin, CreateInput{Name: "cache", Engine: "redis", DSN: "redis://:hunter2@cache.example"})
	if err != nil {
		t.Fatal(err)
	}
	got, err = checker.Check(t.Context(), "org", cache.ID, recipes.Requirement{Engine: "redis", MinVersion: "7"}, redisDial)
	if err != nil || got.Version != "7.2.5" || len(got.Problems) != 0 || !slices.Equal(*redisDialed, []string{"tcp cache.example:6379"}) {
		t.Fatalf("redis: %+v %v %q", got, err, *redisDialed)
	}
	got, err = checker.Check(t.Context(), "org", cache.ID, recipes.Requirement{Engine: "postgres"}, redisDial)
	if err != nil || !slices.Equal(got.Problems, []string{"cache is a Redis endpoint, and this needs PostgreSQL"}) {
		t.Fatalf("engine mismatch: %+v %v", got, err)
	}
	if _, err = checker.Check(t.Context(), "org", "endpoint_missing", recipes.Requirement{}, redisDial); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing endpoint: %v", err)
	}
}

// writeKeyPair writes a self-signed certificate and its key as PEM files.
func writeKeyPair(t *testing.T, certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "loomplane"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresProberSendsOnlyTheDSNsCredentials(t *testing.T) {
	// Loomplane's own libpq environment, ~/.pgpass and ~/.postgresql
	// client certificate and root CA.
	home := t.TempDir()
	certs := filepath.Join(home, ".postgresql")
	if err := os.MkdirAll(certs, 0o700); err != nil {
		t.Fatal(err)
	}
	cert, key, root := filepath.Join(certs, "postgresql.crt"), filepath.Join(certs, "postgresql.key"), filepath.Join(certs, "root.crt")
	writeKeyPair(t, cert, key)
	writeKeyPair(t, root, filepath.Join(home, "root.key"))
	if err := os.WriteFile(filepath.Join(home, ".pgpass"), []byte("*:*:*:*:from-pgpass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PGPASSWORD", "from-env")
	t.Setenv("PGSSLCERT", cert)
	t.Setenv("PGSSLKEY", key)
	t.Setenv("PGSSLROOTCERT", root)
	t.Setenv("PGAPPNAME", "from-env")
	t.Setenv("PGTARGETSESSIONATTRS", "read-write")
	t.Setenv("PGCHANNELBINDING", "require")

	for _, tc := range []struct{ dsn, password, tls string }{
		{"postgres://u@db.example/app?sslmode=require", "", TLSRequire},
		{"postgres://u:p@db.example:6432/app?sslmode=verify-full&application_name=fabriq", "p", TLSVerifyFull},
		{"postgres://u@db.example/app?sslmode=disable", "", TLSDisable},
	} {
		var cfg *pgx.ConnConfig
		prober := PostgresProber{connect: func(_ context.Context, c *pgx.ConnConfig) (pgConn, error) {
			cfg = c
			return &fakePg{version: "17.2"}, nil
		}}
		if _, err := prober.Probe(t.Context(), refuse, tc.dsn, recipes.Requirement{}); err != nil {
			t.Fatalf("%s: %v", tc.dsn, err)
		}
		if cfg.Password != tc.password || cfg.User != "u" || cfg.Database != "app" || cfg.Host != "db.example" || len(cfg.Fallbacks) != 0 {
			t.Fatalf("%s: user %q password %q database %q host %q fallbacks %d", tc.dsn, cfg.User, cfg.Password, cfg.Database, cfg.Host, len(cfg.Fallbacks))
		}
		if cfg.ValidateConnect != nil || cfg.ChannelBinding != "prefer" || cfg.RuntimeParams["application_name"] == "from-env" {
			t.Fatalf("%s: environment settings applied: %+v %q", tc.dsn, cfg.RuntimeParams, cfg.ChannelBinding)
		}
		switch tc.tls {
		case TLSDisable:
			if cfg.TLSConfig != nil {
				t.Fatalf("%s: TLS on", tc.dsn)
			}
		default:
			c := cfg.TLSConfig
			if c == nil || len(c.Certificates) != 0 || c.GetClientCertificate != nil || c.RootCAs != nil {
				t.Fatalf("%s: TLS config carries Loomplane's certificates: %+v", tc.dsn, c)
			}
			if tc.tls == TLSRequire && !c.InsecureSkipVerify {
				t.Fatalf("%s: require verifies", tc.dsn)
			}
			if tc.tls == TLSVerifyFull && (c.InsecureSkipVerify || c.VerifyPeerCertificate != nil || c.ServerName != "db.example") {
				t.Fatalf("%s: verify-full doesn't verify the host: %+v", tc.dsn, c)
			}
		}
	}
}

func TestCheckerLooksUpEndpointsWithoutTheDSN(t *testing.T) {
	svc := NewService(newMemStore(), memSecrets{}, roleAuthz{})
	pg, err := svc.Create(t.Context(), admin, CreateInput{Name: "prod-pg", Engine: "postgres", DSN: testDSN})
	if err != nil {
		t.Fatal(err)
	}
	checker := &Checker{Endpoints: svc}
	got, err := checker.Endpoint(t.Context(), "org", pg.ID)
	if err != nil || got.ID != pg.ID || got.Engine != EnginePostgres || got.Name != "prod-pg" {
		t.Fatalf("endpoint: %+v %v", got, err)
	}
	if _, err = checker.Endpoint(t.Context(), "org", "endpoint_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing endpoint: %v", err)
	}
	if _, err = checker.Endpoint(t.Context(), "other-org", pg.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another organization's endpoint: %v", err)
	}
}

// fabriqLike is a kind that connects Postgres as its own role, the way
// Fabriq's server DSN does.
func fabriqLike() recipes.Kind {
	return recipes.Kind{Name: "fabriq", Title: "Fabriq", Components: []recipes.Component{{
		Name: "server",
		Env: []recipes.EnvVar{
			{Name: "FABRIQ_HTTP_ADDR", Value: ":8081"},
			{Name: "FABRIQ_POSTGRES_DSN", Template: "postgres://fabriq_app:${secret:app-password}@${dep:postgres:hostport}/${dep:postgres:database}?${dep:postgres:query}"},
		},
	}}}
}

func TestPostgresProberChecksPreloadAndRole(t *testing.T) {
	ctx := WithConsumer(t.Context(), ConsumerOf(fabriqLike(), "postgres"))
	req := recipes.Requirement{Engine: "postgres", MinVersion: "16", Preload: []string{"timescaledb"}, CreateRole: true}
	probe := func(pg *fakePg, req recipes.Requirement) *Report {
		t.Helper()
		prober := PostgresProber{connect: func(context.Context, *pgx.ConnConfig) (pgConn, error) { return pg, nil }}
		got, err := prober.Probe(ctx, refuse, testDSN, req)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	got := probe(&fakePg{version: "16.4", preload: "pg_stat_statements"}, req)
	want := []string{"timescaledb isn't in shared_preload_libraries", "the connection's role can't create roles, and Fabriq creates fabriq_app"}
	if got.Version != "16.4" || !slices.Equal(got.Problems, want) || len(got.Missing) != 0 {
		t.Fatalf("report: %+v", *got)
	}

	// The setting is a list, quoted or not, and either right lets the
	// role create roles.
	for _, pg := range []*fakePg{
		{version: "16.4", preload: `pg_stat_statements, "timescaledb"`, super: true},
		{version: "16.4", preload: "timescaledb", createRole: true},
	} {
		if got = probe(pg, req); len(got.Problems) != 0 {
			t.Fatalf("%q: %+v", pg.preload, *got)
		}
	}

	// A role that can't read the setting can't show the library is there.
	denied := &pgconn.PgError{Code: "42501", Message: `permission denied to examine "shared_preload_libraries"`}
	got = probe(&fakePg{version: "16.4", preloadErr: denied, createRole: true}, req)
	if !slices.Equal(got.Problems, []string{"the connection's role can't read shared_preload_libraries, so Loomplane can't tell whether timescaledb is there; grant it pg_read_all_settings"}) {
		t.Fatalf("unreadable setting: %+v", *got)
	}

	// Without either requirement neither is asked.
	pg := &fakePg{version: "16.4"}
	if got = probe(pg, recipes.Requirement{Engine: "postgres"}); len(got.Problems) != 0 || len(pg.queried) != 0 {
		t.Fatalf("asked %v: %+v", pg.queried, *got)
	}

	// A kind whose own DSN doesn't name its role is named by its title.
	ctx = WithConsumer(t.Context(), ConsumerOf(recipes.Kind{Name: "other", Title: "Other"}, "postgres"))
	if got = probe(&fakePg{version: "16.4"}, recipes.Requirement{Engine: "postgres", CreateRole: true}); !slices.Equal(got.Problems, []string{"the connection's role can't create roles, and Other creates a role of its own"}) {
		t.Fatalf("no template role: %+v", *got)
	}
}

func TestProbersRefuseDisallowedTLS(t *testing.T) {
	ctx := WithConsumer(t.Context(), ConsumerOf(fabriqLike(), "redis"))
	store, secrets := newMemStore(), memSecrets{}
	svc := NewService(store, secrets, roleAuthz{})
	checker := &Checker{Endpoints: svc, Probers: map[string]Prober{"postgres": PostgresProber{}, "redis": RedisProber{}}}

	// rediss:// is stored as verify-full, so the require endpoint is
	// written to the store directly.
	store.rows["endpoint_tls"] = Endpoint{ID: "endpoint_tls", OrgID: "org", Name: "cache", Engine: "redis", Host: "cache.example", Port: 6379, TLS: TLSRequire}
	secrets[DSNKey("org", "endpoint_tls")] = []byte("redis://:hunter2@cache.example")
	dial, _, _ := fakeRedis(t, "hunter2", "7.2.5")
	redisOnly := recipes.Requirement{Engine: "redis", MinVersion: "7", TLS: []string{TLSDisable}}
	got, err := checker.Check(ctx, "org", "endpoint_tls", redisOnly, dial)
	if err != nil || got.Version != "7.2.5" || !slices.Equal(got.Problems, []string{"Fabriq can't reach Redis over TLS yet"}) {
		t.Fatalf("redis over TLS: %+v %v", got, err)
	}
	// A verify-full endpoint is refused the same way, and one without TLS
	// passes.
	got, err = checker.CheckDSN(ctx, "rediss://:hunter2@cache.example", redisOnly, refuse)
	if err != nil || !slices.Contains(got.Problems, "Fabriq can't reach Redis over TLS yet") {
		t.Fatalf("rediss: %+v %v", got, err)
	}
	got, err = checker.CheckDSN(ctx, "redis://:hunter2@cache.example", redisOnly, dial)
	if err != nil || len(got.Problems) != 0 {
		t.Fatalf("plain redis: %+v %v", got, err)
	}

	// Postgres reads its mode from sslmode, and an empty list allows all.
	pg := &fakePg{version: "17.2"}
	checker.Probers["postgres"] = PostgresProber{connect: func(context.Context, *pgx.ConnConfig) (pgConn, error) { return pg, nil }}
	ctx = WithConsumer(t.Context(), ConsumerOf(fabriqLike(), "postgres"))
	verified := recipes.Requirement{Engine: "postgres", TLS: []string{TLSVerifyFull}}
	got, err = checker.CheckDSN(ctx, "postgres://u@db.example/app?sslmode=disable", verified, refuse)
	if err != nil || !slices.Equal(got.Problems, []string{"Fabriq reaches PostgreSQL with TLS verify-full, and this endpoint uses disable"}) {
		t.Fatalf("postgres without TLS: %+v %v", got, err)
	}
	for _, mode := range []string{TLSDisable, TLSRequire, TLSVerifyFull} {
		got, err = checker.CheckDSN(ctx, "postgres://u@db.example/app?sslmode="+mode, recipes.Requirement{Engine: "postgres"}, refuse)
		if err != nil || len(got.Problems) != 0 {
			t.Fatalf("%s with no TLS rule: %+v %v", mode, got, err)
		}
	}
}

// countingStore and countingSecrets fail the test on any call.
type countingStore struct{ t *testing.T }

func (s countingStore) Create(context.Context, *Endpoint) error {
	s.t.Error("store: Create")
	return nil
}

func (s countingStore) Get(context.Context, string, string) (*Endpoint, error) {
	s.t.Error("store: Get")
	return nil, ErrNotFound
}

func (s countingStore) List(context.Context, string) ([]*Endpoint, error) {
	s.t.Error("store: List")
	return nil, nil
}

func (s countingStore) Delete(context.Context, string, string) error {
	s.t.Error("store: Delete")
	return nil
}

type countingSecrets struct{ t *testing.T }

func (s countingSecrets) Put(context.Context, string, []byte) error {
	s.t.Error("secrets: Put")
	return nil
}

func (s countingSecrets) Get(context.Context, string) ([]byte, error) {
	s.t.Error("secrets: Get")
	return nil, nil
}

func (s countingSecrets) Delete(context.Context, string) error {
	s.t.Error("secrets: Delete")
	return nil
}

func TestCheckDSNNeverStores(t *testing.T) {
	checker := &Checker{
		Endpoints: NewService(countingStore{t}, countingSecrets{t}, roleAuthz{}),
		Probers:   map[string]Prober{"postgres": PostgresProber{}, "redis": RedisProber{}},
	}
	const dsn = "postgres://app:hunter2-Secret@db.example:5432/app?sslmode=require"
	leaks := func(v any) bool {
		text := fmt.Sprintf("%+v", v)
		return strings.Contains(text, "hunter2") || strings.Contains(text, dsn)
	}

	// An unreachable endpoint is a problem naming its address.
	var dialed []string
	dial := func(_ context.Context, network, address string) (net.Conn, error) {
		dialed = append(dialed, network+" "+address)
		return nil, errors.New("dial " + address + " for postgres://app:hunter2-Secret@db.example: connection refused")
	}
	got, err := checker.CheckDSN(t.Context(), dsn, recipes.Requirement{Engine: "postgres", MinVersion: "16"}, dial)
	if err != nil || len(got.Problems) != 1 || !strings.HasPrefix(got.Problems[0], "db.example:5432 isn't reachable from the target") || leaks(got) {
		t.Fatalf("unreachable: %+v %v", got, err)
	}
	if len(dialed) == 0 || dialed[0] != "tcp db.example:5432" {
		t.Fatalf("dialed: %q", dialed)
	}

	// A refused password is the server's answer, without the password.
	redisDial, _, _ := fakeRedis(t, "right", "7.2.5")
	got, err = checker.CheckDSN(t.Context(), "redis://:hunter2-Secret@cache.example", recipes.Requirement{Engine: "redis"}, redisDial)
	if err != nil || len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "WRONGPASS") || leaks(got) {
		t.Fatalf("refused: %+v %v", got, err)
	}

	// A DSN that can't be read, or isn't the engine's, is ErrInvalid and
	// isn't repeated.
	for _, tc := range []struct {
		dsn    string
		engine string
	}{
		{"postgres://app:hunter2-Secret@db.example/app", "postgres"},
		{"mysql://app:hunter2-Secret@db.example/app", "postgres"},
		{"postgres://app:hunter2-Secret@db.example/app?sslmode=require", "redis"},
	} {
		_, err = checker.CheckDSN(t.Context(), tc.dsn, recipes.Requirement{Engine: tc.engine}, dial)
		if !errors.Is(err, ErrInvalid) || leaks(err) {
			t.Fatalf("%s as %s: %v", tc.dsn, tc.engine, err)
		}
	}
	if _, err = checker.CheckDSN(t.Context(), dsn, recipes.Requirement{Engine: "mysql"}, dial); !errors.Is(err, ErrInvalid) || leaks(err) {
		t.Fatalf("mysql requirement: %v", err)
	}
	if _, err = checker.CheckDSN(t.Context(), dsn, recipes.Requirement{Engine: "postgres"}, nil); err == nil || leaks(err) {
		t.Fatalf("no dialer: %v", err)
	}
}

func TestPostgresRequireSendsTheHostName(t *testing.T) {
	for _, tc := range []struct{ dsn, serverName string }{
		{"postgres://u@ep-cool-name.neon.example/app?sslmode=require", "ep-cool-name.neon.example"},
		{"postgres://u@10.0.0.5/app?sslmode=require", ""},
		{"postgres://u@[2001:db8::5]/app?sslmode=require", ""},
	} {
		var cfg *pgx.ConnConfig
		prober := PostgresProber{connect: func(_ context.Context, c *pgx.ConnConfig) (pgConn, error) {
			cfg = c
			return &fakePg{version: "17.2"}, nil
		}}
		if _, err := prober.Probe(t.Context(), refuse, tc.dsn, recipes.Requirement{}); err != nil {
			t.Fatalf("%s: %v", tc.dsn, err)
		}
		if c := cfg.TLSConfig; c == nil || !c.InsecureSkipVerify || c.ServerName != tc.serverName {
			t.Fatalf("%s: TLS config %+v", tc.dsn, c)
		}
	}
}

func TestReportResult(t *testing.T) {
	for _, tc := range []struct {
		report Report
		want   CheckResult
	}{
		{Report{Version: "16.4"}, CheckResult{OK: true, Version: "16.4"}},
		{Report{Version: "16.4", Missing: []string{"vector"}}, CheckResult{Version: "16.4", Missing: []string{"vector"}}},
		{Report{Problems: []string{"db.example:5432 isn't reachable from the target: i/o timeout"}}, CheckResult{Problems: []string{"db.example:5432 isn't reachable from the target: i/o timeout"}}},
		// A check that found nothing at all didn't reach a usable endpoint.
		{Report{}, CheckResult{Problems: []string{"the endpoint didn't say which version it runs"}}},
	} {
		if got := tc.report.Result(); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%+v: got %+v, want %+v", tc.report, got, tc.want)
		}
	}
}
