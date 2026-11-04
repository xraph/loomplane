package currentauthority

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/xraph/grove/drivers/pgdriver"
)

type connectionProfile struct {
	SystemID         string         `json:"system_id"`
	DatabaseID       uint32         `json:"database_id"`
	Database         string         `json:"database"`
	Role             string         `json:"role"`
	SessionRole      string         `json:"session_role"`
	Schema           string         `json:"schema"`
	SearchPath       string         `json:"search_path"`
	Primary          bool           `json:"primary"`
	ReadOnly         bool           `json:"read_only"`
	LockTimeout      int            `json:"lock_timeout"`
	StatementTimeout int            `json:"statement_timeout"`
	IdleTimeout      int            `json:"idle_timeout"`
	Tables           []tableProfile `json:"tables"`
}

type tableProfile struct {
	Name        string            `json:"name"`
	OID         uint32            `json:"oid"`
	Resolved    uint32            `json:"resolved"`
	Kind        string            `json:"kind"`
	Readable    bool              `json:"readable"`
	RLS         bool              `json:"rls"`
	ForcedRLS   bool              `json:"forced_rls"`
	Triggers    int               `json:"triggers"`
	Rules       int               `json:"rules"`
	Inheritance int               `json:"inheritance"`
	Generated   int               `json:"generated"`
	Temporal    map[string]string `json:"temporal"`
}

type profileScanner interface{ Scan(...any) error }
type profileQuery func(context.Context, string, ...any) profileScanner
type profileScanFunc func(...any) error

func (scan profileScanFunc) Scan(dest ...any) error { return scan(dest...) }

// inspectConnections records actual sessions without installing a profile. It
// needs permission to read the server system identifier; an uninspectable or
// unsupported database stays unavailable. The fixed DSN-only pool constructor
// supplies the reader's one-connection ceiling, which pgdriver has no getter for.
func inspectConnections(ctx context.Context, main *pgdriver.PgDB, owned *nativeBackend, schema string) (*connectionProfile, error) {
	if main == nil || owned == nil || owned.writer == nil || owned.reader == nil || schema == "" || owned.writer.Config().MaxConns != 1 {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, AttemptLimit)
	defer cancel()
	queries := []profileQuery{
		func(ctx context.Context, q string, args ...any) profileScanner { return main.QueryRow(ctx, q, args...) },
		func(ctx context.Context, q string, args ...any) profileScanner {
			ctx, op := owned.telemetry.StartSQL(ctx, q)
			return profileScanFunc(func(dest ...any) error {
				err := owned.writer.QueryRow(ctx, q, args...).Scan(dest...)
				op.End(err)
				return err
			})
		},
		func(ctx context.Context, q string, args ...any) profileScanner {
			return owned.reader.QueryRow(ctx, q, args...)
		},
	}
	var baseline *connectionProfile
	for i, query := range queries {
		current, err := inspectConnection(ctx, query, schema)
		if err != nil {
			return nil, fmt.Errorf("inspect connection %d: %w", i, err)
		}
		if i == 0 {
			if current.ReadOnly {
				return nil, ErrUnavailable
			}
			baseline = &current
			continue
		}
		if i == 1 && current.ReadOnly {
			return nil, ErrUnavailable
		}
		if i == 2 && !current.ReadOnly {
			return nil, ErrUnavailable
		}
		if current.LockTimeout != 2000 || current.StatementTimeout != 5000 || current.IdleTimeout != 5000 {
			return nil, ErrUnavailable
		}
		// Connection bounds differ from main; identity, visibility and relations may not.
		if !sameDatabaseProfile(*baseline, current) {
			return nil, fmt.Errorf("%w: connection %d does not match main", ErrUnavailable, i)
		}
	}
	return baseline, nil
}

func inspectConnection(ctx context.Context, query profileQuery, schema string) (connectionProfile, error) {
	ctx, cancel := context.WithTimeout(ctx, AttemptLimit)
	defer cancel()
	var current connectionProfile
	var encoded []byte
	if err := query(ctx, connectionProfileSQL, schema, authorityTables[:]).Scan(&encoded); err != nil {
		return current, fmt.Errorf("%w: inspect session: %w", ErrUnavailable, err)
	}
	if err := json.Unmarshal(encoded, &current); err != nil {
		return current, fmt.Errorf("%w: decode session profile: %w", ErrUnavailable, err)
	}
	if err := current.validate(schema); err != nil {
		return current, err
	}
	return current, nil
}

func sameDatabaseProfile(baseline, current connectionProfile) bool {
	current.ReadOnly = baseline.ReadOnly
	current.LockTimeout = baseline.LockTimeout
	current.StatementTimeout = baseline.StatementTimeout
	current.IdleTimeout = baseline.IdleTimeout
	return reflect.DeepEqual(baseline, current)
}

func (p *connectionProfile) validate(schema string) error {
	if !p.Primary || p.SystemID == "" || p.DatabaseID == 0 || p.Database == "" || p.Role == "" || p.Role != p.SessionRole || p.Schema != schema || len(p.Tables) != len(authorityTables) {
		return fmt.Errorf("%w: unsupported session identity or incomplete table inventory", ErrUnavailable)
	}
	temporal := map[string]map[string]string{
		"warden_assignments": {"expires_at": "timestamp with time zone"},
		"warden_policies":    {"not_before": "timestamp with time zone", "not_after": "timestamp with time zone", "is_active": "boolean"},
		"keysmith_keys":      {"expires_at": "timestamp with time zone"},
		"authsome_users":     {"ban_expires": "timestamp with time zone", "banned": "boolean"},
	}
	for i, table := range p.Tables {
		if table.Name != authorityTables[i] || table.OID == 0 || table.OID != table.Resolved || table.Kind != "r" || !table.Readable || table.RLS || table.ForcedRLS || table.Triggers != 0 || table.Rules != 0 || table.Inheritance != 0 || table.Generated != 0 {
			return fmt.Errorf("%w: unsupported identity or visibility for %s", ErrUnavailable, table.Name)
		}
		for column, typ := range temporal[table.Name] {
			if table.Temporal[column] != typ {
				return fmt.Errorf("%w: unsupported temporal column %s.%s", ErrUnavailable, table.Name, column)
			}
		}
	}
	return nil
}

const connectionProfileSQL = `SELECT pg_catalog.jsonb_build_object(
 'system_id',(SELECT system_identifier::text FROM pg_catalog.pg_control_system()),
 'database_id',(SELECT oid::bigint FROM pg_catalog.pg_database WHERE datname=pg_catalog.current_database()),
 'database',pg_catalog.current_database(),'role',current_user,'session_role',session_user,
 'schema',pg_catalog.current_schema(),'search_path',pg_catalog.current_setting('search_path'),
 'primary',NOT pg_catalog.pg_is_in_recovery(),
 'read_only',pg_catalog.current_setting('transaction_read_only')::boolean,
 'lock_timeout',(SELECT setting::int FROM pg_catalog.pg_settings WHERE name='lock_timeout'),
 'statement_timeout',(SELECT setting::int FROM pg_catalog.pg_settings WHERE name='statement_timeout'),
 'idle_timeout',(SELECT setting::int FROM pg_catalog.pg_settings WHERE name='idle_in_transaction_session_timeout'),
 'tables',(SELECT pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object(
  'name',n.name,'oid',c.oid::bigint,'resolved',pg_catalog.to_regclass(n.name)::oid::bigint,'kind',c.relkind,
  'readable',pg_catalog.has_table_privilege(c.oid,'SELECT'),'rls',c.relrowsecurity,'forced_rls',c.relforcerowsecurity,
  'triggers',(SELECT count(*) FROM pg_catalog.pg_trigger WHERE tgrelid=c.oid AND NOT tgisinternal),
  'rules',(SELECT count(*) FROM pg_catalog.pg_rewrite WHERE ev_class=c.oid),
  'inheritance',(SELECT count(*) FROM pg_catalog.pg_inherits WHERE inhrelid=c.oid OR inhparent=c.oid),
  'generated',(SELECT count(*) FROM pg_catalog.pg_attribute WHERE attrelid=c.oid AND attgenerated<>'' AND NOT attisdropped),
  'temporal',(SELECT pg_catalog.jsonb_object_agg(a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod)) FROM pg_catalog.pg_attribute a
   WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped AND a.attname IN ('expires_at','not_before','not_after','is_active','ban_expires','banned'))
 ) ORDER BY n.ordinal) FROM pg_catalog.unnest($2::text[]) WITH ORDINALITY n(name,ordinal)
 JOIN pg_catalog.pg_namespace ns ON ns.nspname=$1
 JOIN pg_catalog.pg_class c ON c.relnamespace=ns.oid AND c.relname=n.name))`
