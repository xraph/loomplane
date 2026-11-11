package currentauthority

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

var errFleetSchemaUnsupported = fmt.Errorf("%w: unsupported Fleet schema", ErrUnavailable)

type fleetTrigger struct {
	Table string `json:"table"`
	projectTrigger
}

type fleetFunctionContract struct {
	Body       string   `json:"body"`
	Language   string   `json:"language"`
	Arguments  []string `json:"arguments"`
	Names      []string `json:"names"`
	Return     string   `json:"return"`
	Volatility string   `json:"volatility"`
	Source     string   `json:"source"`
}

type fleetSchemaContract struct {
	NamespaceEvidenceVersion int                                `json:"namespace_evidence_version"`
	Columns                  map[string]map[string]schemaColumn `json:"columns"`
	Indexes                  []schemaIndex                      `json:"indexes"`
	ForeignKeys              []schemaForeignKey                 `json:"foreign_keys"`
	Triggers                 []fleetTrigger                     `json:"triggers"`
	Functions                map[string]fleetFunctionContract   `json:"functions"`
	Checks                   map[string]map[string]string       `json:"checks"`
	WriteEvents              map[string]int                     `json:"write_events"`
}

type fleetRelation struct {
	tableProfile
	Owner       uint32 `json:"owner"`
	UnsafeTypes bool   `json:"unsafe_types"`
}

type fleetCheck struct {
	Table      string `json:"table"`
	Name       string `json:"name"`
	Definition string `json:"definition"`
	Valid      bool   `json:"valid"`
	Local      bool   `json:"local"`
	Inherited  int    `json:"inherited"`
}

type fleetCatalog struct {
	CatalogFirst      bool              `json:"catalog_first"`
	Checks            []fleetCheck      `json:"checks"`
	Relations         []fleetRelation   `json:"relations"`
	Shape             ordinarySchema    `json:"shape"`
	Triggers          []fleetTrigger    `json:"triggers"`
	Functions         []projectFunction `json:"functions"`
	UnsafeExpressions int               `json:"unsafe_expressions"`
	ShadowFunctions   int               `json:"shadow_functions"`
	ShadowOperators   int               `json:"shadow_operators"`
}

// inspectFleetSchema verifies the finite namespace path. The five capacity
// relations are read-only schema-version checks. Triggers on those unrelated
// writer paths are not Fleet authorization dependencies.
func inspectFleetSchema(ctx context.Context, query profileQuery, schema string, owner uint32) error {
	if schema == "" || owner == 0 || query == nil {
		return ErrUnavailable
	}
	contract, err := fleetContract()
	if err != nil {
		return err
	}
	// The installed path requires immutable per-attempt namespace evidence.
	// A catalog contract predating that journal cannot activate Fleet authority.
	if contract.NamespaceEvidenceVersion != 1 {
		return fmt.Errorf("%w: namespace attempt evidence contract is not qualified", errFleetSchemaUnsupported)
	}
	tables := make([]string, 0, len(contract.Columns))
	functions := make([]string, 0, len(contract.Functions))
	for name := range contract.Columns {
		tables = append(tables, name)
	}
	for name := range contract.Functions {
		functions = append(functions, name)
	}
	sort.Strings(tables)
	sort.Strings(functions)
	writes, err := json.Marshal(contract.WriteEvents)
	if err != nil {
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, AttemptLimit)
	defer cancel()
	var encoded []byte
	if err := query(ctx, fleetSchemaSQL, schema, tables, functions, string(writes)).Scan(&encoded); err != nil {
		return fmt.Errorf("%w: Fleet schema: %w", ErrUnavailable, err)
	}
	var actual fleetCatalog
	if err := json.Unmarshal(encoded, &actual); err != nil {
		return fmt.Errorf("%w: decode Fleet schema", ErrUnavailable)
	}
	if err := actual.validateFleet(schema, owner, contract); err != nil {
		return errors.Join(errFleetSchemaUnsupported, err)
	}
	return nil
}

func fleetContract() (fleetSchemaContract, error) {
	var contract fleetSchemaContract
	if err := json.Unmarshal([]byte(fleetSchemaContractJSON), &contract); err != nil {
		return contract, ErrUnavailable
	}
	return contract, nil
}

func (p fleetCatalog) validateFleet(schema string, owner uint32, want fleetSchemaContract) error {
	var failures []error
	if owner == 0 || schema == "" || !p.CatalogFirst || len(p.Relations) != len(want.Columns) || p.UnsafeExpressions != 0 || p.ShadowFunctions != 0 || p.ShadowOperators != 0 {
		failures = append(failures, ErrUnavailable)
	}
	seen := map[string]bool{}
	for _, table := range p.Relations {
		if _, ok := want.Columns[table.Name]; !ok || seen[table.Name] || table.OID == 0 || table.OID != table.Resolved || table.Owner != owner || table.Kind != "r" || !table.Readable || table.RLS || table.ForcedRLS || table.Rules != 0 || table.Inheritance != 0 || table.Generated != 0 || table.UnsafeTypes {
			failures = append(failures, fmt.Errorf("%w: Fleet relation %s", ErrUnavailable, table.Name))
		}
		seen[table.Name] = true
	}
	for table, columns := range want.Columns {
		for name, column := range columns {
			if got, ok := p.Shape.Columns[table][name]; !ok || got != column {
				failures = append(failures, fmt.Errorf("%w: Fleet column %s.%s: got %+v, need %+v", ErrUnavailable, table, name, got, column))
			}
		}
	}
	for _, index := range want.Indexes {
		if !p.Shape.hasIndex(index.Table, index.Columns, index.Primary) {
			failures = append(failures, fmt.Errorf("%w: Fleet unique key %s %v", ErrUnavailable, index.Table, index.Columns))
		}
	}
	for _, fk := range want.ForeignKeys {
		found := false
		for _, got := range p.Shape.ForeignKeys {
			if got.Table == fk.Table && reflect.DeepEqual(got.Columns, fk.Columns) && got.Target == fk.Target && got.TargetSchema == schema && reflect.DeepEqual(got.TargetColumns, fk.TargetColumns) && got.Valid && !got.Deferred {
				found = true
				break
			}
		}
		if !found {
			failures = append(failures, fmt.Errorf("%w: Fleet foreign key %s %v", ErrUnavailable, fk.Table, fk.Columns))
		}
	}
	for table, checks := range want.Checks {
		for name, definition := range checks {
			found := false
			for _, check := range p.Checks {
				if check.Table == table && check.Name == name && check.Definition == definition && check.Valid && check.Local && check.Inherited == 0 {
					found = true
					break
				}
			}
			if !found {
				failures = append(failures, fmt.Errorf("%w: Fleet check %s.%s", ErrUnavailable, table, name))
			}
		}
	}
	if len(p.Triggers) != len(want.Triggers) {
		failures = append(failures, fmt.Errorf("%w: Fleet trigger inventory", ErrUnavailable))
	}
	expectedTriggers := map[string]fleetTrigger{}
	for _, trigger := range want.Triggers {
		trigger.Enabled = "O"
		trigger.FunctionSchema = schema
		trigger.Columns = []string{}
		expectedTriggers[trigger.Table+"/"+trigger.Name] = trigger
	}
	for _, trigger := range p.Triggers {
		key := trigger.Table + "/" + trigger.Name
		expected, ok := expectedTriggers[key]
		if !ok || !reflect.DeepEqual(expected, trigger) {
			failures = append(failures, fmt.Errorf("%w: Fleet trigger %s", ErrUnavailable, key))
		}
		delete(expectedTriggers, key)
	}
	if len(p.Functions) != len(want.Functions) {
		failures = append(failures, fmt.Errorf("%w: Fleet function inventory", ErrUnavailable))
	}
	seen = map[string]bool{}
	for _, function := range p.Functions {
		expected, ok := want.Functions[function.Name]
		sum := sha256.Sum256([]byte(function.Body))
		if !ok || seen[function.Name] || hex.EncodeToString(sum[:]) != expected.Body || function.Schema != schema || function.Owner != owner || function.Language != expected.Language || !sameStrings(function.Arguments, expected.Arguments) || !sameStrings(function.Names, expected.Names) || function.Return != expected.Return || len(function.Config) != 0 || function.Security || function.Strict || function.Leakproof || function.Volatility != expected.Volatility || function.Parallel != "u" || function.Kind != "f" || function.Extras {
			failures = append(failures, fmt.Errorf("%w: Fleet function %s", ErrUnavailable, function.Name))
		}
		seen[function.Name] = true
	}
	return errors.Join(failures...)
}
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const fleetSchemaSQL = `WITH tables AS (
 SELECT c.* FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=ANY($2::text[])
), functions AS (
 SELECT p.*,n.nspname,l.lanname FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace JOIN pg_catalog.pg_language l ON l.oid=p.prolang WHERE n.nspname=$1 AND p.proname=ANY($3::text[])
), writes AS (
 SELECT key AS name,value::integer AS events FROM pg_catalog.jsonb_each_text($4::jsonb)
), expressions AS (
 SELECT 'pg_catalog.pg_attrdef'::pg_catalog.regclass AS classid,d.oid FROM pg_catalog.pg_attrdef d JOIN tables c ON c.oid=d.adrelid JOIN writes w ON w.name=c.relname
 UNION ALL
 SELECT 'pg_catalog.pg_constraint'::pg_catalog.regclass,k.oid FROM pg_catalog.pg_constraint k JOIN tables c ON c.oid=k.conrelid JOIN writes w ON w.name=c.relname WHERE k.contype='c'
)
SELECT pg_catalog.jsonb_build_object(
 'catalog_first',(pg_catalog.current_schemas(true))[1]='pg_catalog',
 'checks',(SELECT coalesce(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object('table',c.relname,'name',k.conname,'definition',pg_catalog.pg_get_constraintdef(k.oid,false),'valid',k.convalidated,'local',k.conislocal,'inherited',k.coninhcount)),'[]'::jsonb) FROM tables c JOIN pg_catalog.pg_constraint k ON k.conrelid=c.oid WHERE k.contype='c'),
 'relations',(SELECT pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object(
  'name',c.relname,'oid',c.oid::bigint,'resolved',pg_catalog.to_regclass(c.relname)::oid::bigint,'owner',c.relowner::bigint,'kind',c.relkind,
  'readable',pg_catalog.has_table_privilege(c.oid,'SELECT'),'rls',c.relrowsecurity,'forced_rls',c.relforcerowsecurity,
  'rules',(SELECT count(*) FROM pg_catalog.pg_rewrite WHERE ev_class=c.oid),
  'inheritance',(SELECT count(*) FROM pg_catalog.pg_inherits WHERE inhrelid=c.oid OR inhparent=c.oid),
  'generated',(SELECT count(*) FROM pg_catalog.pg_attribute WHERE attrelid=c.oid AND attgenerated<>'' AND NOT attisdropped),
  'unsafe_types',EXISTS(SELECT 1 FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_type t ON t.oid=a.atttypid JOIN pg_catalog.pg_namespace n ON n.oid=t.typnamespace WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped AND n.nspname<>'pg_catalog') OR EXISTS(SELECT 1 FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_collation cl ON cl.oid=a.attcollation JOIN pg_catalog.pg_namespace n ON n.oid=cl.collnamespace WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped AND n.nspname<>'pg_catalog')
 ) ORDER BY c.relname) FROM tables c),
 'shape',pg_catalog.jsonb_build_object(
  'columns',(SELECT pg_catalog.jsonb_object_agg(c.relname,(SELECT pg_catalog.jsonb_object_agg(a.attname,pg_catalog.jsonb_build_object('type',pg_catalog.format_type(a.atttypid,a.atttypmod),'required',a.attnotnull)) FROM pg_catalog.pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped)) FROM tables c),
  'indexes',(SELECT coalesce(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object('table',c.relname,'primary',i.indisprimary,
   'columns',(SELECT pg_catalog.jsonb_agg(a.attname ORDER BY x.ordinal) FROM pg_catalog.unnest(i.indkey::smallint[]) WITH ORDINALITY x(num,ordinal) JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum=x.num WHERE x.ordinal<=i.indnkeyatts)
  )),'[]'::jsonb) FROM tables c JOIN pg_catalog.pg_index i ON i.indrelid=c.oid WHERE i.indisunique AND i.indisvalid AND i.indisready AND i.indimmediate AND i.indpred IS NULL AND i.indexprs IS NULL AND NOT EXISTS(SELECT 1 FROM pg_catalog.unnest(i.indclass::oid[]) x(oid) JOIN pg_catalog.pg_opclass op ON op.oid=x.oid JOIN pg_catalog.pg_namespace n ON n.oid=op.opcnamespace WHERE n.nspname<>'pg_catalog')),
  'foreign_keys',(SELECT coalesce(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object('table',c.relname,
   'columns',(SELECT pg_catalog.jsonb_agg(a.attname ORDER BY x.ordinal) FROM pg_catalog.unnest(k.conkey) WITH ORDINALITY x(num,ordinal) JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum=x.num),
   'target',r.relname,'target_schema',n.nspname,
   'target_columns',(SELECT pg_catalog.jsonb_agg(a.attname ORDER BY x.ordinal) FROM pg_catalog.unnest(k.confkey) WITH ORDINALITY x(num,ordinal) JOIN pg_catalog.pg_attribute a ON a.attrelid=r.oid AND a.attnum=x.num),
   'valid',k.convalidated,'deferred',k.condeferrable OR k.condeferred
  )),'[]'::jsonb) FROM tables c JOIN pg_catalog.pg_constraint k ON k.conrelid=c.oid AND k.contype='f' JOIN pg_catalog.pg_class r ON r.oid=k.confrelid JOIN pg_catalog.pg_namespace n ON n.oid=r.relnamespace)
 ),
 'triggers',(SELECT coalesce(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object(
  'table',c.relname,'name',t.tgname,'type',t.tgtype,'enabled',t.tgenabled,
  'columns',coalesce((SELECT pg_catalog.jsonb_agg(a.attname ORDER BY x.ordinal) FROM pg_catalog.unnest(t.tgattr::smallint[]) WITH ORDINALITY x(num,ordinal) JOIN pg_catalog.pg_attribute a ON a.attrelid=t.tgrelid AND a.attnum=x.num),'[]'::jsonb),
  'arguments',pg_catalog.encode(t.tgargs,'hex'),'function',p.proname,'function_schema',n.nspname,
  'extras',t.tgqual IS NOT NULL OR t.tgconstraint<>0 OR t.tgdeferrable OR t.tginitdeferred OR t.tgoldtable IS NOT NULL OR t.tgnewtable IS NOT NULL
 )),'[]'::jsonb) FROM pg_catalog.pg_trigger t JOIN tables c ON c.oid=t.tgrelid JOIN writes w ON w.name=c.relname JOIN pg_catalog.pg_proc p ON p.oid=t.tgfoid JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE NOT t.tgisinternal AND (t.tgtype::integer & w.events)<>0),
 'functions',(SELECT coalesce(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object(
  'name',f.proname,'schema',f.nspname,'owner',f.proowner::bigint,'language',f.lanname,
  'arguments',coalesce((SELECT pg_catalog.jsonb_agg(pg_catalog.format_type(x.typ,NULL) ORDER BY x.ordinal) FROM pg_catalog.unnest(f.proargtypes::oid[]) WITH ORDINALITY x(typ,ordinal)),'[]'::jsonb),
  'names',f.proargnames,'return',pg_catalog.format_type(f.prorettype,NULL),'body',f.prosrc,'config',f.proconfig,
  'security',f.prosecdef,'strict',f.proisstrict,'leakproof',f.proleakproof,'volatility',f.provolatile,'parallel',f.proparallel,'kind',f.prokind,
  'extras',NOT pg_catalog.pg_function_is_visible(f.oid) OR f.proretset OR f.provariadic<>0 OR f.pronargdefaults<>0 OR f.prosupport<>0 OR f.probin IS NOT NULL OR f.prosqlbody IS NOT NULL OR f.proargmodes IS NOT NULL OR f.proallargtypes IS NOT NULL OR pg_catalog.cardinality(f.protrftypes)>0
 )),'[]'::jsonb) FROM functions f),
 'unsafe_expressions',(SELECT count(*) FROM expressions e JOIN pg_catalog.pg_depend d ON d.classid=e.classid AND d.objid=e.oid JOIN pg_catalog.pg_proc p ON d.refclassid='pg_catalog.pg_proc'::pg_catalog.regclass AND p.oid=d.refobjid JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname<>'pg_catalog' AND NOT(n.nspname=$1 AND p.proname=ANY($3::text[]))),
 'shadow_functions',(SELECT count(*) FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname<>'pg_catalog' AND pg_catalog.pg_function_is_visible(p.oid) AND p.proname IN ('pg_advisory_xact_lock','hashtextextended','current_setting','to_jsonb','jsonb_build_object','json_build_object','jsonb_typeof','octet_length','pg_notify','cardinality','unnest','length','max','count')),
 'shadow_operators',(SELECT count(*) FROM pg_catalog.pg_operator o JOIN pg_catalog.pg_namespace n ON n.oid=o.oprnamespace WHERE n.nspname<>'pg_catalog' AND pg_catalog.pg_operator_is_visible(o.oid) AND o.oprname IN ('=','<>','<','>','<=','>=','||','->','->>','?','?&','-','+','~','!~'))
)`
