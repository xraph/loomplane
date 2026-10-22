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

	"github.com/xraph/loomplane/internal/recipes"
)

// fakePg answers the prober's queries as a server would.
type fakePg struct {
	version   string
	available []string
	asked     []string
	closed    bool
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
