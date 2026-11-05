package currentauthority

import (
	"context"
	"crypto/tls"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xraph/go-utils/metrics"
	"github.com/xraph/grove"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/observability"
)

type nativeTx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	Commit(context.Context) error
	Rollback(context.Context) error
}

type backend interface {
	begin(context.Context) (nativeTx, error)
	close() error
}

type nativeBackend struct {
	writer    *pgxpool.Pool
	reader    *pgdriver.PgDB
	readerDB  *grove.DB
	telemetry *observability.Telemetry
}

func (b *nativeBackend) begin(ctx context.Context) (nativeTx, error) {
	ctx, op := b.telemetry.StartSQL(ctx, "BEGIN")
	tx, err := b.writer.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite})
	op.End(err)
	return tx, err
}
func (b *nativeBackend) close() error {
	err := b.readerDB.Close()
	b.writer.Close()
	return err
}

// These pools remain private and don't enable the profile. Only the complete
// authoritative DSN is supported. Programmatic TLS, dialer or callback changes
// need another supported adapter; ConnString cannot serialize those changes.
// Installation must verify primary/database/schema/role/RLS equivalence.
func openPools(ctx context.Context, primaryDSN string, sink metrics.Metrics) (*nativeBackend, error) {
	if strings.TrimSpace(primaryDSN) == "" {
		return nil, ErrUnavailable
	}
	primary, err := pgxpool.ParseConfig(primaryDSN)
	if err != nil {
		return nil, ErrUnavailable
	}
	readDSN, err := readerDSN(primaryDSN)
	if err != nil {
		return nil, err
	}
	writer, err := pgxpool.NewWithConfig(ctx, writerConfig(primary))
	if err != nil {
		return nil, err
	}
	reader := pgdriver.New()
	if openErr := reader.Open(ctx, readDSN, driver.WithPoolSize(1), driver.WithQueryTimeout(AttemptLimit)); openErr != nil {
		writer.Close()
		return nil, errors.Join(ErrUnavailable, reader.Close())
	}
	readerDB, err := grove.Open(reader, grove.WithMetrics(sink))
	if err != nil {
		writer.Close()
		return nil, errors.Join(err, reader.Close())
	}
	telemetry := observability.NewTelemetry("postgresql", observability.WithStatusCode(func(err error) string {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return pgErr.Code
		}
		return ""
	}), observability.WithBenign(func(err error) bool { return errors.Is(err, pgx.ErrNoRows) }))
	telemetry.SetConnection(primary.ConnConfig.Database, primary.ConnConfig.Host, int(primary.ConnConfig.Port))
	telemetry.SetMetrics(sink)
	return &nativeBackend{writer: writer, reader: reader, readerDB: readerDB, telemetry: telemetry}, nil
}

func writerConfig(primary *pgxpool.Config) *pgxpool.Config {
	cfg := primary.Copy()
	cfg.MaxConns = 1
	cfg.MinConns = 0
	cfg.MinIdleConns = 0
	cfg.ConnConfig.ConnectTimeout = AttemptLimit
	cfg.MaxConnIdleTime = time.Minute
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "2000ms"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000ms"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "5000ms"
	return cfg
}

func readerDSN(primary string) (string, error) {
	derived, err := withDSNParams(primary, map[string]string{
		"pool_max_conns": "1", "pool_min_conns": "0", "pool_min_idle_conns": "0",
		"connect_timeout": "5", "pool_max_conn_idle_time": "1m",
		"lock_timeout": "2000ms", "statement_timeout": "5000ms",
		"idle_in_transaction_session_timeout": "5000ms", "default_transaction_read_only": "on",
	})
	if err != nil {
		return "", err
	}
	parsed, err := pgxpool.ParseConfig(derived)
	if err != nil || parsed.MaxConns != 1 || parsed.MinConns != 0 || parsed.MinIdleConns != 0 || parsed.ConnConfig.ConnectTimeout != AttemptLimit || parsed.MaxConnIdleTime != time.Minute {
		return "", ErrUnavailable
	}
	for key, value := range map[string]string{"default_transaction_read_only": "on", "lock_timeout": "2000ms", "statement_timeout": "5000ms", "idle_in_transaction_session_timeout": "5000ms"} {
		if parsed.ConnConfig.RuntimeParams[key] != value {
			return "", ErrUnavailable
		}
	}
	return derived, nil
}

// Preserve the original transport and runtime settings, overriding only the
// supplied keys. Validation errors deliberately omit the credential-bearing DSN.
func withDSNParams(original string, overrides map[string]string) (string, error) {
	primary, err := pgxpool.ParseConfig(original)
	if err != nil {
		return "", ErrUnavailable
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if strings.HasPrefix(original, "postgres://") || strings.HasPrefix(original, "postgresql://") {
		// libpq URI values use percent encoding, not form encoding. Preserve
		// existing bytes and ordering, including literal '+' and SSL aliases.
		separator := "?"
		queryAt := postgresURIQuery(original)
		if queryAt >= 0 {
			separator = "&"
			if queryAt == len(original)-1 || strings.HasSuffix(original, "&") {
				separator = ""
			}
		}
		updated := original
		for _, key := range keys {
			value := strings.ReplaceAll(url.QueryEscape(overrides[key]), "+", "%20")
			updated += separator + url.QueryEscape(key) + "=" + value
			separator = "&"
		}
		return validateDSNChange(primary, updated, overrides)
	}
	var updated strings.Builder
	updated.WriteString(original)
	for _, key := range keys {
		value := strings.ReplaceAll(overrides[key], "\\", "\\\\")
		value = strings.ReplaceAll(value, "'", "\\'")
		updated.WriteString(" " + key + "='" + value + "'")
	}
	return validateDSNChange(primary, updated.String(), overrides)
}

// Locate only the query delimiter. The installed libpq-compatible parser looks
// for userinfo up to the first slash, and permits punctuation in passwords and
// database names. Bracketed host contents are not query or path delimiters.
func postgresURIQuery(original string) int {
	offset := len("postgres://")
	if strings.HasPrefix(original, "postgresql://") {
		offset = len("postgresql://")
	}
	remainder := original[offset:]
	if at := strings.IndexAny(remainder, "@/"); at >= 0 && remainder[at] == '@' {
		offset += at + 1
		remainder = remainder[at+1:]
	}
	for i := 0; i < len(remainder); i++ {
		switch remainder[i] {
		case '[':
			if i == 0 || remainder[i-1] == ',' {
				end := strings.IndexByte(remainder[i+1:], ']')
				if end < 0 {
					return -1
				} // The public parser already rejected this shape.
				i += end + 1
			}
		case '?':
			return offset + i
		case '/':
			query := strings.IndexByte(remainder[i+1:], '?')
			if query < 0 {
				return -1
			}
			return offset + i + 1 + query
		}
	}
	return -1
}

func validateDSNChange(primary *pgxpool.Config, derived string, overrides map[string]string) (string, error) {
	parsed, err := pgxpool.ParseConfig(derived)
	if err != nil {
		return "", ErrUnavailable
	}
	a, b := primary.ConnConfig, parsed.ConnConfig
	if a.Host != b.Host || a.Port != b.Port || a.Database != b.Database || a.User != b.User || a.Password != b.Password || len(a.Fallbacks) != len(b.Fallbacks) || !sameTLSIdentity(a.TLSConfig, b.TLSConfig) {
		return "", ErrUnavailable
	}
	for i, expected := range a.Fallbacks {
		actual := b.Fallbacks[i]
		if expected.Host != actual.Host || expected.Port != actual.Port || !sameTLSIdentity(expected.TLSConfig, actual.TLSConfig) {
			return "", ErrUnavailable
		}
	}
	for key, value := range a.RuntimeParams {
		if _, changed := overrides[key]; !changed {
			if actual, ok := b.RuntimeParams[key]; !ok || actual != value {
				return "", ErrUnavailable
			}
		}
	}
	for key, value := range b.RuntimeParams {
		if _, changed := overrides[key]; !changed {
			if original, ok := a.RuntimeParams[key]; !ok || original != value {
				return "", ErrUnavailable
			}
		}
	}
	return derived, nil
}

func sameTLSIdentity(a, b *tls.Config) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ServerName == b.ServerName && a.InsecureSkipVerify == b.InsecureSkipVerify && a.MinVersion == b.MinVersion && a.MaxVersion == b.MaxVersion
}
