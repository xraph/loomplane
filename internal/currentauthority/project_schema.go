package currentauthority

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
)

type projectTrigger struct {
	Name           string   `json:"name"`
	Type           int      `json:"type"`
	Enabled        string   `json:"enabled"`
	Columns        []string `json:"columns"`
	Arguments      string   `json:"arguments"`
	Function       string   `json:"function"`
	FunctionSchema string   `json:"function_schema"`
	Extras         bool     `json:"extras"`
}

type projectFunction struct {
	Name       string   `json:"name"`
	Schema     string   `json:"schema"`
	Owner      uint32   `json:"owner"`
	Language   string   `json:"language"`
	Arguments  []string `json:"arguments"`
	Names      []string `json:"names"`
	Return     string   `json:"return"`
	Body       string   `json:"body"`
	Config     []string `json:"config"`
	Security   bool     `json:"security"`
	Strict     bool     `json:"strict"`
	Leakproof  bool     `json:"leakproof"`
	Volatility string   `json:"volatility"`
	Parallel   string   `json:"parallel"`
	Kind       string   `json:"kind"`
	Extras     bool     `json:"extras"`
}

type schemaColumn struct {
	Type     string `json:"type"`
	Required bool   `json:"required"`
}
type schemaIndex struct {
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
	Primary bool     `json:"primary"`
}
type schemaForeignKey struct {
	Table         string   `json:"table"`
	Columns       []string `json:"columns"`
	Target        string   `json:"target"`
	TargetSchema  string   `json:"target_schema"`
	TargetColumns []string `json:"target_columns"`
	Valid         bool     `json:"valid"`
	Deferred      bool     `json:"deferred"`
}
type ordinarySchema struct {
	ProjectOwner uint32                             `json:"project_owner"`
	Triggers     []projectTrigger                   `json:"triggers"`
	Functions    []projectFunction                  `json:"functions"`
	Columns      map[string]map[string]schemaColumn `json:"columns"`
	Indexes      []schemaIndex                      `json:"indexes"`
	ForeignKeys  []schemaForeignKey                 `json:"foreign_keys"`
}

// inspectOrdinarySchema requires an independently supplied trusted migration
// role. Never infer that trust from the functions or table being inspected.
// Runtime migration/function replacement must drain/invalidate installed profiles.
func inspectOrdinarySchema(ctx context.Context, query profileQuery, schema string, migrationRole uint32) error {
	if migrationRole == 0 {
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, AttemptLimit)
	defer cancel()
	var raw []byte
	if err := query(ctx, ordinarySchemaSQL, schema, authorityTables[:]).Scan(&raw); err != nil {
		return fmt.Errorf("%w: ordinary schema: %w", ErrUnavailable, err)
	}
	var actual ordinarySchema
	if err := json.Unmarshal(raw, &actual); err != nil {
		return ErrUnavailable
	}
	return actual.validate(schema, migrationRole)
}

func (p ordinarySchema) validate(schema string, migrationRole uint32) error {
	if migrationRole == 0 || p.ProjectOwner != migrationRole || len(p.Triggers) != 4 || len(p.Functions) != 5 {
		return ErrUnavailable
	}
	expected := map[string]projectTrigger{
		"loomplane_state_event":           {Name: "loomplane_state_event", Type: 29, Enabled: "O", Columns: []string{}, Arguments: "70726f6a65637400", Function: "loomplane_state_event_trigger", FunctionSchema: schema},
		"loomplane_domain_project_delete": {Name: "loomplane_domain_project_delete", Type: 11, Enabled: "O", Columns: []string{}, Function: "loomplane_domain_delete_guard", FunctionSchema: schema},
		"loomplane_fleet_project_mode":    {Name: "loomplane_fleet_project_mode", Type: 19, Enabled: "O", Columns: []string{"deployment_mode"}, Function: "loomplane_fleet_project_mode", FunctionSchema: schema},
		"variable_source_gate":            {Name: "variable_source_gate", Type: 30, Enabled: "O", Columns: []string{}, Function: "loomplane_variable_source_gate", FunctionSchema: schema},
	}
	for _, t := range p.Triggers {
		want, ok := expected[t.Name]
		if !ok || !reflect.DeepEqual(want, t) {
			return fmt.Errorf("%w: project trigger %s", ErrUnavailable, t.Name)
		}
		delete(expected, t.Name)
	}
	seen := map[string]bool{}
	for _, f := range p.Functions {
		body, ok := projectFunctionBodies[f.Name]
		sum := sha256.Sum256([]byte(f.Body))
		if !ok || seen[f.Name] || hex.EncodeToString(sum[:]) != body || f.Schema != schema || f.Owner != migrationRole || f.Language != "plpgsql" || len(f.Config) != 0 || f.Security || f.Strict || f.Leakproof || f.Volatility != "v" || f.Parallel != "u" || f.Kind != "f" || f.Extras {
			return fmt.Errorf("%w: project function %s", ErrUnavailable, f.Name)
		}
		seen[f.Name] = true
		if f.Name == "loomplane_append_event" {
			if f.Return != "bigint" || !reflect.DeepEqual(f.Arguments, []string{"text", "text", "text", "text", "jsonb"}) || !reflect.DeepEqual(f.Names, []string{"org", "project", "event_type", "entity", "payload"}) {
				return ErrUnavailable
			}
		} else if f.Return != "trigger" || len(f.Arguments) != 0 || len(f.Names) != 0 {
			return ErrUnavailable
		}
	}
	for table, columns := range ordinaryColumns {
		for name, want := range columns {
			if got, ok := p.Columns[table][name]; !ok || got != want {
				return fmt.Errorf("%w: ordinary column %s.%s", ErrUnavailable, table, name)
			}
		}
	}
	for _, table := range []string{"authsome_users", "authsome_organizations", "authsome_members", "keysmith_keys", "keysmith_scopes", "loomplane_projects", "loomplane_teams", "loomplane_team_grants"} {
		if !p.hasIndex(table, []string{"id"}, true) {
			return fmt.Errorf("%w: identity primary key %s", ErrUnavailable, table)
		}
	}
	if !p.hasIndex("authsome_members", []string{"user_id", "org_id"}, false) && !p.hasIndex("authsome_members", []string{"org_id", "user_id"}, false) {
		return fmt.Errorf("%w: membership cardinality", ErrUnavailable)
	}
	if !p.hasIndex("keysmith_key_scopes", []string{"key_id", "scope_id"}, true) {
		return ErrUnavailable
	}
	for column, target := range map[string]string{"key_id": "keysmith_keys", "scope_id": "keysmith_scopes"} {
		found := false
		for _, fk := range p.ForeignKeys {
			if fk.Table == "keysmith_key_scopes" && reflect.DeepEqual(fk.Columns, []string{column}) && fk.TargetSchema == schema && fk.Target == target && reflect.DeepEqual(fk.TargetColumns, []string{"id"}) && fk.Valid && !fk.Deferred {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%w: key scope foreign key %s", ErrUnavailable, column)
		}
	}
	return nil
}

func (p ordinarySchema) hasIndex(table string, columns []string, primary bool) bool {
	for _, i := range p.Indexes {
		if i.Table == table && (!primary || i.Primary) && reflect.DeepEqual(i.Columns, columns) {
			return true
		}
	}
	return false
}

var ordinaryColumns = map[string]map[string]schemaColumn{
	"authsome_users":         {"id": {"text", true}, "app_id": {"text", true}, "env_id": {"text", true}, "email_verified": {"boolean", true}, "banned": {"boolean", true}, "ban_expires": {"timestamp with time zone", false}, "deleted_at": {"timestamp with time zone", false}},
	"authsome_organizations": {"id": {"text", true}, "app_id": {"text", true}},
	"authsome_members":       {"id": {"text", true}, "org_id": {"text", true}, "user_id": {"text", true}, "role": {"text", true}},
	"keysmith_keys":          {"id": {"text", true}, "app_id": {"text", true}, "tenant_id": {"text", true}, "state": {"text", true}, "metadata": {"jsonb", true}, "created_by": {"text", false}, "policy_id": {"text", false}, "revoked_at": {"timestamp with time zone", false}, "expires_at": {"timestamp with time zone", false}},
	"keysmith_key_scopes":    {"key_id": {"text", true}, "scope_id": {"text", true}},
	"keysmith_scopes":        {"id": {"text", true}},
	"loomplane_projects":     {"id": {"text", true}, "org_id": {"text", true}, "deployment_mode": {"text", true}},
	"loomplane_teams":        {"id": {"text", true}, "org_id": {"text", true}},
	"loomplane_team_members": {"team_id": {"text", true}, "org_id": {"text", true}, "user_id": {"text", true}, "member_id": {"text", true}},
	"loomplane_team_grants":  {"id": {"text", true}, "org_id": {"text", true}, "team_id": {"text", true}, "role": {"text", true}, "project_id": {"text", false}},
}

const ordinarySchemaSQL = `WITH tables AS (
 SELECT c.* FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=ANY($2::text[])
), functions AS (
 SELECT p.*,n.nspname,l.lanname FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace JOIN pg_catalog.pg_language l ON l.oid=p.prolang
 WHERE n.nspname=$1 AND p.proname IN ('loomplane_state_event_trigger','loomplane_append_event','loomplane_domain_delete_guard','loomplane_fleet_project_mode','loomplane_variable_source_gate')
)
SELECT pg_catalog.jsonb_build_object(
 'project_owner',(SELECT relowner::bigint FROM tables WHERE relname='loomplane_projects'),
 'triggers',(SELECT coalesce(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object(
  'name',t.tgname,'type',t.tgtype,'enabled',t.tgenabled,
  'columns',coalesce((SELECT pg_catalog.jsonb_agg(a.attname ORDER BY x.ordinal) FROM pg_catalog.unnest(t.tgattr::smallint[]) WITH ORDINALITY x(num,ordinal) JOIN pg_catalog.pg_attribute a ON a.attrelid=t.tgrelid AND a.attnum=x.num),'[]'::jsonb),
  'arguments',pg_catalog.encode(t.tgargs,'hex'),'function',p.proname,'function_schema',n.nspname,
  'extras',t.tgqual IS NOT NULL OR t.tgconstraint<>0 OR t.tgdeferrable OR t.tginitdeferred OR t.tgoldtable IS NOT NULL OR t.tgnewtable IS NOT NULL
 )),'[]'::jsonb) FROM pg_catalog.pg_trigger t JOIN tables c ON c.oid=t.tgrelid JOIN pg_catalog.pg_proc p ON p.oid=t.tgfoid JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE c.relname='loomplane_projects' AND NOT t.tgisinternal),
 'functions',(SELECT coalesce(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object(
  'name',f.proname,'schema',f.nspname,'owner',f.proowner::bigint,'language',f.lanname,
  'arguments',coalesce((SELECT pg_catalog.jsonb_agg(pg_catalog.format_type(x.typ,NULL) ORDER BY x.ordinal) FROM pg_catalog.unnest(f.proargtypes::oid[]) WITH ORDINALITY x(typ,ordinal)),'[]'::jsonb),
  'names',f.proargnames,'return',pg_catalog.format_type(f.prorettype,NULL),'body',f.prosrc,'config',f.proconfig,
  'security',f.prosecdef,'strict',f.proisstrict,'leakproof',f.proleakproof,'volatility',f.provolatile,'parallel',f.proparallel,'kind',f.prokind,
  'extras',f.proretset OR f.provariadic<>0 OR f.pronargdefaults<>0 OR f.prosupport<>0 OR f.probin IS NOT NULL OR f.prosqlbody IS NOT NULL OR f.proargmodes IS NOT NULL OR f.proallargtypes IS NOT NULL OR pg_catalog.cardinality(f.protrftypes)>0
 )),'[]'::jsonb) FROM functions f),
 'columns',(SELECT pg_catalog.jsonb_object_agg(c.relname,(SELECT pg_catalog.jsonb_object_agg(a.attname,pg_catalog.jsonb_build_object('type',pg_catalog.format_type(a.atttypid,a.atttypmod),'required',a.attnotnull)) FROM pg_catalog.pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped)) FROM tables c),
 'indexes',(SELECT coalesce(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object('table',c.relname,'primary',i.indisprimary,
  'columns',(SELECT pg_catalog.jsonb_agg(a.attname ORDER BY x.ordinal) FROM pg_catalog.unnest(i.indkey::smallint[]) WITH ORDINALITY x(num,ordinal) JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum=x.num WHERE x.ordinal<=i.indnkeyatts)
 )),'[]'::jsonb) FROM tables c JOIN pg_catalog.pg_index i ON i.indrelid=c.oid WHERE i.indisunique AND i.indisvalid AND i.indisready AND i.indimmediate AND i.indpred IS NULL AND i.indexprs IS NULL),
 'foreign_keys',(SELECT coalesce(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object('table',c.relname,
  'columns',(SELECT pg_catalog.jsonb_agg(a.attname ORDER BY x.ordinal) FROM pg_catalog.unnest(k.conkey) WITH ORDINALITY x(num,ordinal) JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum=x.num),
  'target',r.relname,'target_schema',n.nspname,
  'target_columns',(SELECT pg_catalog.jsonb_agg(a.attname ORDER BY x.ordinal) FROM pg_catalog.unnest(k.confkey) WITH ORDINALITY x(num,ordinal) JOIN pg_catalog.pg_attribute a ON a.attrelid=r.oid AND a.attnum=x.num),
  'valid',k.convalidated,'deferred',k.condeferrable OR k.condeferred
 )),'[]'::jsonb) FROM tables c JOIN pg_catalog.pg_constraint k ON k.conrelid=c.oid AND k.contype='f' JOIN pg_catalog.pg_class r ON r.oid=k.confrelid JOIN pg_catalog.pg_namespace n ON n.oid=r.relnamespace))`
