package catalogconnect

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/fleets"
)

var ErrInstanceAccess = errors.New("instance credential scope could not be verified")

// InstanceResourcePins verifies the saved physical resource and current provider
// connection before credentials are opened. It must not infer identity from a host.
type InstanceResourcePins interface {
	VerifyInstanceResourcePins(context.Context, fleets.ResourceBinding) error
}

// InstanceBucketPolicies reads effective policy from an installed provider.
// Providers without a credential-policy reader remain unsupported.
type InstanceBucketPolicies interface {
	VerifyInstanceBucketPolicy(context.Context, fleets.ResourceBinding, BucketAccess) (fleets.BucketAccessScope, error)
}

// InstanceAccessVerifier reuses the endpoint vault and explicit target dialer.
type InstanceAccessVerifier struct {
	Endpoints *Service
	Pins      InstanceResourcePins
	Dial      func(context.Context, fleets.ResourceBinding) (DialFunc, error)
	Buckets   InstanceBucketPolicies
}

func (v *InstanceAccessVerifier) VerifyInstanceResourceAccess(ctx context.Context, b fleets.ResourceBinding) (fleets.ResourceAccessObservation, error) {
	empty := fleets.ResourceAccessObservation{}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if v == nil || v.Endpoints == nil || v.Pins == nil || v.Dial == nil {
		return empty, ErrInstanceAccess
	}
	err := v.Pins.VerifyInstanceResourcePins(ctx, b)
	if cancelled := ctx.Err(); cancelled != nil {
		return empty, cancelled
	}
	if err != nil {
		return empty, ErrInstanceAccess
	}
	endpoint, dsn, err := v.Endpoints.Resolve(ctx, b.Scope.OrgID, b.Credential.EndpointID)
	if cancelled := ctx.Err(); cancelled != nil {
		return empty, cancelled
	}
	if err != nil || endpoint == nil || int64(endpoint.Revision) != b.Credential.Version {
		return empty, ErrInstanceAccess
	}
	parsed := &Endpoint{Engine: endpoint.Engine}
	if parseDSN(parsed, dsn) != nil || parsed.Host != endpoint.Host || parsed.Port != endpoint.Port || parsed.Username != endpoint.Username || parsed.Database != endpoint.Database || parsed.TLS != endpoint.TLS {
		return empty, ErrInstanceAccess
	}
	now := time.Now().UTC()
	out := fleets.ResourceAccessObservation{ResourceID: b.ResourceID, Incarnation: b.Incarnation, InstanceID: b.InstanceID, ConnectionGeneration: b.ConnectionGeneration, Credential: b.Credential, PrincipalID: b.PrincipalID, ScopeID: b.AccessScope, CheckedAt: now, ExpiresAt: now.Add(time.Minute)}
	switch endpoint.Engine {
	case EnginePostgres:
		database, schema, ok := strings.Cut(b.AccessScope, "/")
		if !ok || strings.Contains(schema, "/") || database != endpoint.Database || schema == "" || b.PrincipalID != endpoint.Username || endpoint.TLS != TLSVerifyFull {
			return empty, ErrInstanceAccess
		}
		dial, dialErr := v.Dial(ctx, b)
		if cancelled := ctx.Err(); cancelled != nil {
			return empty, cancelled
		}
		if dialErr != nil || dial == nil {
			return empty, ErrInstanceAccess
		}
		scope, probeErr := probeInstancePostgres(ctx, dial, dsn, database, schema, b.PrincipalID)
		if cancelled := ctx.Err(); cancelled != nil {
			return empty, cancelled
		}
		if probeErr != nil {
			return empty, ErrInstanceAccess
		}
		out.Protocol = "postgres-role-v2"
		out.Database = scope
	case EngineS3, EngineGCS, EngineR2:
		if v.Buckets == nil {
			return empty, ErrInstanceAccess
		}
		access, parseErr := ParseBucketAccess(dsn)
		if parseErr != nil || access.Bucket != b.AccessScope {
			return empty, ErrInstanceAccess
		}
		policy, policyErr := v.Buckets.VerifyInstanceBucketPolicy(ctx, b, access)
		if cancelled := ctx.Err(); cancelled != nil {
			return empty, cancelled
		}
		if policyErr != nil {
			return empty, ErrInstanceAccess
		}
		out.Protocol = "bucket-policy-v1"
		out.Bucket = &policy
	default:
		return empty, ErrInstanceAccess
	}
	if cancelled := ctx.Err(); cancelled != nil {
		return empty, cancelled
	}
	current, err := v.Endpoints.Get(ctx, b.Scope.OrgID, b.Credential.EndpointID)
	if cancelled := ctx.Err(); cancelled != nil {
		return empty, cancelled
	}
	if err != nil || current == nil || *current != *endpoint {
		return empty, ErrInstanceAccess
	}
	err = v.Pins.VerifyInstanceResourcePins(ctx, b)
	if cancelled := ctx.Err(); cancelled != nil {
		return empty, cancelled
	}
	if err != nil {
		return empty, ErrInstanceAccess
	}
	return out, nil
}

func probeInstancePostgres(ctx context.Context, dial DialFunc, dsn, database, schema, principal string) (*fleets.DatabaseAccessScope, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, ErrInstanceAccess
	}
	if err = pinConfig(cfg, dsn); err != nil {
		return nil, ErrInstanceAccess
	}
	cfg.RuntimeParams = map[string]string{"search_path": "pg_catalog", "default_transaction_read_only": "on"}
	cfg.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	cfg.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		if cancelled := ctx.Err(); cancelled != nil {
			return nil, cancelled
		}
		return dial(ctx, network, address)
	}
	cfg.ConnectTimeout = probeTimeout
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, ErrInstanceAccess
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	out := &fleets.DatabaseAccessScope{Database: database, Schema: schema}
	var user, session, actualDB string
	err = conn.QueryRow(ctx, instancePostgresScopeSQL, schema).Scan(&user, &session, &actualDB, &out.Login, &out.Superuser, &out.CreateRole, &out.CreateDatabase, &out.Replication, &out.BypassRLS, &out.OtherRoles, &out.OtherDatabases, &out.OtherSchemas, &out.PrivilegedFunctions, &out.CanRead, &out.CanWrite, &out.CreateSchemas, &out.ForeignServerAccess, &out.IndirectObjects)
	if err != nil || user != principal || session != principal || actualDB != database {
		return nil, ErrInstanceAccess
	}
	return out, nil
}

// Privilege functions include inherited and PUBLIC grants. Any usable definer
// function outside the system catalog blocks this protocol's isolation claim.
const instancePostgresScopeSQL = `SELECT current_user::text,session_user::text,current_database()::text,
 r.rolcanlogin,r.rolsuper,r.rolcreaterole,r.rolcreatedb,r.rolreplication,r.rolbypassrls,
 ARRAY(SELECT x.rolname::text FROM pg_roles x WHERE x.rolname<>current_user AND pg_has_role(current_user,x.oid,'MEMBER') LIMIT 2),
 ARRAY(SELECT d.datname::text FROM pg_database d WHERE d.datname<>current_database() AND (d.datdba=r.oid OR d.datallowconn AND has_database_privilege(current_user,d.oid,'CONNECT')) LIMIT 2),
 ARRAY(SELECT n.nspname::text FROM pg_namespace n WHERE n.nspname<>$1 AND n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND n.nspname NOT LIKE 'pg_temp_%' AND (
  n.nspowner=r.oid OR has_schema_privilege(current_user,n.oid,'USAGE') OR has_schema_privilege(current_user,n.oid,'CREATE')
  OR EXISTS(SELECT 1 FROM pg_proc p WHERE p.pronamespace=n.oid AND (p.proowner=r.oid OR has_function_privilege(current_user,p.oid,'EXECUTE')))
  OR EXISTS(SELECT 1 FROM pg_class c WHERE c.relnamespace=n.oid AND (c.relowner=r.oid OR CASE
   WHEN c.relkind='S' THEN has_sequence_privilege(current_user,c.oid,'USAGE,SELECT,UPDATE')
   WHEN c.relkind IN ('r','p','v','m','f') THEN
    has_table_privilege(current_user,c.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
    OR has_any_column_privilege(current_user,c.oid,'SELECT,INSERT,UPDATE,REFERENCES')
    OR CASE WHEN current_setting('server_version_num')::int>=170000 THEN has_table_privilege(current_user,c.oid,'MAINTAIN') ELSE false END
   ELSE false END))
 ) LIMIT 2),
 EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE p.prosecdef AND n.nspname<>'pg_catalog' AND has_function_privilege(current_user,p.oid,'EXECUTE')),
 EXISTS(SELECT 1 FROM pg_namespace n WHERE n.nspname=$1 AND has_schema_privilege(current_user,n.oid,'USAGE')) AND NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','p','v','m','f') AND NOT has_table_privilege(current_user,c.oid,'SELECT')),
 EXISTS(SELECT 1 FROM pg_namespace n WHERE n.nspname=$1 AND has_schema_privilege(current_user,n.oid,'CREATE')) AND NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','p') AND NOT (has_table_privilege(current_user,c.oid,'INSERT') AND has_table_privilege(current_user,c.oid,'UPDATE') AND has_table_privilege(current_user,c.oid,'DELETE'))),
 has_database_privilege(current_user,current_database(),'CREATE') OR EXISTS(SELECT 1 FROM pg_database d WHERE d.datname=current_database() AND d.datdba=r.oid),
 EXISTS(SELECT 1 FROM pg_foreign_server s WHERE s.srvowner=r.oid OR has_server_privilege(current_user,s.oid,'USAGE'))
 OR EXISTS(SELECT 1 FROM pg_foreign_data_wrapper f WHERE f.fdwowner=r.oid OR has_foreign_data_wrapper_privilege(current_user,f.oid,'USAGE')),
 EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND (
  c.relkind='f'
  OR c.relkind IN ('v','m') AND c.relowner<>r.oid
  OR c.relowner<>r.oid AND EXISTS(SELECT 1 FROM pg_rewrite rules WHERE rules.ev_class=c.oid)
  OR EXISTS(SELECT 1 FROM pg_trigger t JOIN pg_proc p ON p.oid=t.tgfoid
   WHERE t.tgrelid=c.oid AND NOT t.tgisinternal AND p.prosecdef AND p.proowner<>r.oid)
  OR EXISTS(SELECT 1 FROM pg_inherits i JOIN pg_class peer
   ON peer.oid=CASE WHEN i.inhparent=c.oid THEN i.inhrelid ELSE i.inhparent END
   WHERE c.oid IN (i.inhparent,i.inhrelid) AND peer.relnamespace<>c.relnamespace)
  OR EXISTS(SELECT 1 FROM pg_constraint k JOIN pg_class peer
   ON peer.oid=CASE WHEN k.conrelid=c.oid THEN k.confrelid ELSE k.conrelid END
   WHERE k.contype='f' AND c.oid IN (k.conrelid,k.confrelid)
    AND (peer.relnamespace<>c.relnamespace OR c.relowner<>r.oid OR peer.relowner<>r.oid))
 ))
 FROM pg_roles r WHERE r.rolname=current_user`
