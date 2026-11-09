package currentauthority

import (
	"context"
	"crypto/tls"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xraph/grove/drivers/pgdriver"
	wardenpg "github.com/xraph/warden/store/postgres"
)

func TestReaderDSNPreservesFullURLAndKeywordSettings(t *testing.T) {
	cases := []string{
		"postgresql://u%27ser:p%5Cass%20word@localhost:55471/db?sslmode=prefer&host=localhost,127.0.0.1&port=55471,55472&search_path=one%2Ctwo&application_name=authority%27s+reader&pool_max_conns=7&pool_min_conns=3&pool_min_idle_conns=2&target_session_attrs=read-write",
		`host=localhost,127.0.0.1 port=55471,55472 dbname=db user='u\'ser' password='p\\ass word' sslmode=verify-full search_path='one,two' application_name='authority\'s reader' pool_max_conns=7 pool_min_conns=3 pool_min_idle_conns=2 target_session_attrs=read-write`,
	}
	for i, dsn := range cases {
		t.Run([]string{"url", "keyword"}[i], func(t *testing.T) {
			primary, err := pgxpool.ParseConfig(dsn)
			if err != nil {
				t.Fatal("invalid fixture DSN")
			}
			reader, err := readerDSN(dsn)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(reader, dsn) {
				t.Fatal("original connection string changed")
			}
			parsed, err := pgxpool.ParseConfig(reader)
			if err != nil {
				t.Fatal("invalid derived reader configuration")
			}
			a, b := primary.ConnConfig, parsed.ConnConfig
			if a.Host != b.Host || a.Port != b.Port || a.Database != b.Database || a.User != b.User || a.Password != b.Password {
				t.Fatal("reader changed connection identity")
			}
			assertTLSSettings(t, a.TLSConfig, b.TLSConfig)
			if len(a.Fallbacks) != len(b.Fallbacks) {
				t.Fatal("fallback count changed")
			}
			for n, fallback := range a.Fallbacks {
				actual := b.Fallbacks[n]
				if fallback.Host != actual.Host || fallback.Port != actual.Port {
					t.Fatal("fallback ordering changed")
				}
				assertTLSSettings(t, fallback.TLSConfig, actual.TLSConfig)
			}
			for _, key := range []string{"application_name", "search_path"} {
				if a.RuntimeParams[key] != b.RuntimeParams[key] {
					t.Fatalf("reader changed %s", key)
				}
			}
			if parsed.MaxConns != 1 || parsed.MinConns != 0 || parsed.MinIdleConns != 0 || b.ConnectTimeout != AttemptLimit || parsed.MaxConnIdleTime != time.Minute {
				t.Fatal("reader bounds missing")
			}
			expected := map[string]string{"default_transaction_read_only": "on", "lock_timeout": "2000ms", "statement_timeout": "5000ms", "idle_in_transaction_session_timeout": "5000ms"}
			for key, value := range expected {
				if b.RuntimeParams[key] != value {
					t.Fatalf("reader missing %s", key)
				}
			}
			if primary.MaxConns != 7 || primary.MinConns != 3 || primary.MinIdleConns != 2 || primary.ConnConfig.RuntimeParams["default_transaction_read_only"] != "" {
				t.Fatal("primary configuration mutated")
			}
		})
	}
}

func assertTLSSettings(t *testing.T, expected, actual *tls.Config) {
	t.Helper()
	if expected == nil || actual == nil {
		if expected != actual {
			t.Fatal("TLS availability changed")
		}
		return
	}
	if expected.ServerName != actual.ServerName || expected.InsecureSkipVerify != actual.InsecureSkipVerify || expected.MinVersion != actual.MinVersion || expected.MaxVersion != actual.MaxVersion || !reflect.DeepEqual(expected.RootCAs, actual.RootCAs) || !reflect.DeepEqual(expected.Certificates, actual.Certificates) {
		t.Fatal("TLS settings changed")
	}
}

func TestReaderDSNOverridesQuoteValuesAndRejectMalformedInput(t *testing.T) {
	for _, dsn := range []string{"postgres://u:p@localhost:55471/db?sslmode=disable", "host=localhost port=55471 user=u password=p dbname=db sslmode=disable"} {
		const value = "one's \\ reader"
		changed, err := withDSNParams(dsn, map[string]string{"application_name": value})
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := pgxpool.ParseConfig(changed)
		if err != nil {
			t.Fatal("quoted override did not parse")
		}
		if parsed.ConnConfig.RuntimeParams["application_name"] != value {
			t.Fatalf("quoted runtime parameter changed: got %q, want %q", parsed.ConnConfig.RuntimeParams["application_name"], value)
		}
	}
	for _, dsn := range []string{"postgres://u:secret@%invalid/db", "host='unterminated"} {
		if _, err := readerDSN(dsn); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("malformed DSN: %v", err)
		}
	}
	if _, err := openPools(context.Background(), "", nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("empty authoritative DSN: %v", err)
	}
}

func TestPostgresReaderUsesExistingWardenStoreAndOneConnection(t *testing.T) {
	f := newPGFixture(t)
	if pgdriver.Unwrap(f.native.readerDB) != f.native.reader {
		t.Fatal("Grove did not retain the owned reader")
	}
	store := wardenpg.New(f.native.readerDB)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	connection, err := f.native.reader.AcquireConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Release()
	var readonly string
	if readErr := connection.QueryRow(ctx, "SHOW transaction_read_only").Scan(&readonly); readErr != nil {
		t.Fatal(readErr)
	}
	if readonly != "on" {
		t.Fatal("Warden reader is writable")
	}
	queued, cancelQueued := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancelQueued()
	extra, err := f.native.reader.AcquireConn(queued)
	if err == nil {
		extra.Release()
		t.Fatal("reader opened a second connection")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reader queue: %v", err)
	}
}

type closeFailureBackend struct {
	backend
	failure error
}

func (b closeFailureBackend) close() error { return errors.Join(b.backend.close(), b.failure) }

func TestPostgresReaderCloseErrorRemainsObservable(t *testing.T) {
	f := newPGFixture(t)
	failure := errors.New("injected reader close failure")
	f.coordinator.backend = closeFailureBackend{backend: f.native, failure: failure}
	t.Cleanup(func() { f.coordinator.mu.Lock(); f.coordinator.cleanupErr = nil; f.coordinator.mu.Unlock() })
	for i := 0; i < 2; i++ {
		if err := f.coordinator.Close(); !errors.Is(err, failure) {
			t.Fatalf("Close %d: %v", i, err)
		}
		if err := f.coordinator.Shutdown(context.Background()); !errors.Is(err, failure) {
			t.Fatalf("Shutdown %d: %v", i, err)
		}
	}
}

func TestReaderURIQueryBoundaryPreservesIdentity(t *testing.T) {
	cases := map[string]string{
		"question in password":          "postgres://u:p?x@localhost/db",
		"ampersand in database":         "postgres://u:p@localhost/db&",
		"both with ipv6":                "postgres://u:p?x@[::1]/db&",
		"userinfo without path":         "postgres://u:p?x@localhost",
		"literal query question":        "postgres://u:p?x@localhost/db&?application_name=literal+plus?",
		"empty real query":              "postgres://u:p?x@localhost/db&?",
		"trailing real query ampersand": "postgres://u:p?x@localhost/db&?application_name=literal+plus&",
		"ordered SSL aliases":           "postgres://u:p?x@localhost/db&?sslmode=disable&ssl=true&sslmode=prefer&application_name=literal+plus",
	}
	for name, dsn := range cases {
		t.Run(name, func(t *testing.T) {
			primary, err := pgxpool.ParseConfig(dsn)
			if err != nil {
				t.Fatal("installed pgx rejected fixture URI")
			}
			derived, err := readerDSN(dsn)
			if err != nil {
				t.Fatal(err)
			}
			reader, err := pgxpool.ParseConfig(derived)
			if err != nil {
				t.Fatal("installed pgx rejected derived URI")
			}
			a, b := primary.ConnConfig, reader.ConnConfig
			if a.User != b.User || a.Password != b.Password || a.Database != b.Database || a.Host != b.Host || a.Port != b.Port {
				t.Fatal("reader changed parsed connection identity")
			}
			assertTLSSettings(t, a.TLSConfig, b.TLSConfig)
			if len(a.Fallbacks) != len(b.Fallbacks) {
				t.Fatal("reader changed fallback count")
			}
			for i, expected := range a.Fallbacks {
				actual := b.Fallbacks[i]
				if expected.Host != actual.Host || expected.Port != actual.Port {
					t.Fatal("reader changed fallback identity")
				}
				assertTLSSettings(t, expected.TLSConfig, actual.TLSConfig)
			}
			if b.RuntimeParams["application_name"] != a.RuntimeParams["application_name"] {
				t.Fatal("literal plus query parameter changed")
			}
			if reader.MaxConns != 1 || reader.MinConns != 0 || reader.MinIdleConns != 0 || b.ConnectTimeout != AttemptLimit || reader.MaxConnIdleTime != time.Minute {
				t.Fatal("reader bounds omitted")
			}
			for key, value := range map[string]string{"default_transaction_read_only": "on", "lock_timeout": "2000ms", "statement_timeout": "5000ms", "idle_in_transaction_session_timeout": "5000ms"} {
				if b.RuntimeParams[key] != value {
					t.Fatalf("reader omitted %s", key)
				}
			}
		})
	}
}
