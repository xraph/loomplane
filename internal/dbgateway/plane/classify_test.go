package plane

import (
	"strings"
	"testing"
)

func TestClassifySQL(t *testing.T) {
	pin := Classification{Effect: EffectPinSession}
	stmt := Classification{Effect: EffectPinStatement}
	none := Classification{}

	tests := []struct {
		name string
		sql  string
		want Classification
	}{
		// LISTEN and UNLISTEN.
		{"listen", "LISTEN jobs", Classification{Effect: EffectListen, Channel: "jobs"}},
		{"listen folds unquoted case", "listen Jobs;", Classification{Effect: EffectListen, Channel: "jobs"}},
		{"listen keeps quoted case", `LISTEN "Jobs"`, Classification{Effect: EffectListen, Channel: "Jobs"}},
		{"listen unescapes doubled quotes", `LISTEN "a""b"`, Classification{Effect: EffectListen, Channel: `a"b`}},
		{"listen behind comments", "  \n -- c\n /* a /* nested */ b */ listen jobs", Classification{Effect: EffectListen, Channel: "jobs"}},
		{"listen with trailing comment", "LISTEN jobs; -- done", Classification{Effect: EffectListen, Channel: "jobs"}},
		{"unlisten channel", "UNLISTEN jobs", Classification{Effect: EffectUnlisten, Channel: "jobs"}},
		{"unlisten star", "UNLISTEN *", Classification{Effect: EffectUnlisten, Channel: "*", All: true}},
		{"unlisten quoted star names a channel", `UNLISTEN "*"`, Classification{Effect: EffectUnlisten, Channel: "*"}},
		{"listen quoted star names a channel", `LISTEN "*"`, Classification{Effect: EffectListen, Channel: "*"}},
		{"listen star is not a channel", "LISTEN *", pin},
		{"listen without channel", "LISTEN", pin},
		{"listen with junk after the channel", "LISTEN a b", pin},
		{"listen with a dotted name", "LISTEN a.b", pin},

		// SET.
		{"set to list", "SET search_path TO a,b", Classification{Effect: EffectSet, Param: "search_path", Value: "a,b"}},
		{"set session equals", "SET SESSION timezone = 'UTC'", Classification{Effect: EffectSet, Param: "timezone", Value: "'UTC'"}},
		{"set quoted mixed-case param", `set "TimeZone" to 'UTC';`, Classification{Effect: EffectSet, Param: "timezone", Value: "'UTC'"}},
		{"set time zone", "SET TIME ZONE 'UTC'", Classification{Effect: EffectSet, Param: "timezone", Value: "'UTC'"}},
		{"set time zone default", "SET TIME ZONE DEFAULT", Classification{Effect: EffectReset, Param: "timezone"}},
		{"set trailing comment dropped from value", "SET statement_timeout = 5 -- why\n", Classification{Effect: EffectSet, Param: "statement_timeout", Value: "5"}},
		{"set semicolon in string", "SET application_name = 'a;b'", Classification{Effect: EffectSet, Param: "application_name", Value: "'a;b'"}},
		{"set semicolon in comment", "SET application_name = 'x' /* ; */", Classification{Effect: EffectSet, Param: "application_name", Value: "'x'"}},
		{"set escape string", `SET application_name = E'a\';b'`, Classification{Effect: EffectSet, Param: "application_name", Value: `E'a\';b'`}},
		{"set to default resets", "SET search_path TO DEFAULT", Classification{Effect: EffectReset, Param: "search_path"}},
		{"set equals default resets", "set work_mem = default;", Classification{Effect: EffectReset, Param: "work_mem"}},
		{"set unknown to default pins", "SET my.custom TO DEFAULT", pin},
		{"set local", "SET LOCAL statement_timeout = 5", none},
		{"set transaction", "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE", none},
		{"set custom param", "SET my.custom = 1", pin},
		{"set session authorization", "SET SESSION AUTHORIZATION bob", pin},
		{"set role", "SET ROLE admin", pin},
		{"set session characteristics", "SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY", pin},
		{"set from current", "SET search_path FROM CURRENT", pin},
		{"set without a value", "SET search_path", pin},
		{"set with an empty value", "SET search_path TO ;", pin},

		// RESET and DISCARD.
		{"reset param", "RESET timezone", Classification{Effect: EffectReset, Param: "timezone"}},
		{"reset quoted param", `RESET "TimeZone";`, Classification{Effect: EffectReset, Param: "timezone"}},
		{"reset all", "RESET ALL", Classification{Effect: EffectReset, Param: "*"}},
		{"reset custom param", "RESET my.custom", pin},
		{"reset role", "RESET ROLE", pin},
		{"reset without a param", "RESET", pin},
		{"discard all", "DISCARD ALL", Classification{Effect: EffectDiscardAll}},
		{"discard all lowercase", "discard all;", Classification{Effect: EffectDiscardAll}},
		{"discard plans", "DISCARD PLANS", Classification{Effect: EffectDeallocate, Param: "*"}},
		{"discard temp", "DISCARD TEMP", none},
		{"discard sequences", "DISCARD SEQUENCES", none},
		{"deallocate", "DEALLOCATE foo", Classification{Effect: EffectDeallocate, Param: "foo"}},
		{"deallocate prepare", "DEALLOCATE PREPARE foo", Classification{Effect: EffectDeallocate, Param: "foo"}},
		{"deallocate a statement named prepare", "DEALLOCATE prepare", Classification{Effect: EffectDeallocate, Param: "prepare"}},
		{"deallocate folds unquoted case", "DEALLOCATE Foo;", Classification{Effect: EffectDeallocate, Param: "foo"}},
		{"deallocate keeps quoted case", `DEALLOCATE "Foo"`, Classification{Effect: EffectDeallocate, Param: "Foo"}},
		{"deallocate all", "DEALLOCATE ALL", Classification{Effect: EffectDeallocate, Param: "*"}},
		{"deallocate prepare all", "DEALLOCATE PREPARE ALL", Classification{Effect: EffectDeallocate, Param: "*"}},
		{"deallocate quoted all is a name", `DEALLOCATE "all"`, Classification{Effect: EffectDeallocate, Param: "all"}},
		{"deallocate without a name", "DEALLOCATE", pin},
		{"deallocate with junk after the name", "DEALLOCATE a b", pin},

		// Advisory locks.
		{"advisory lock", "SELECT pg_advisory_lock(1)", pin},
		{"advisory lock shared", "SELECT pg_advisory_lock_shared(1)", pin},
		{"try advisory lock", "SELECT pg_try_advisory_lock(1)", pin},
		{"try advisory lock shared", "SELECT pg_try_advisory_lock_shared(1)", pin},
		{"advisory unlock", "SELECT pg_advisory_unlock(1)", pin},
		{"advisory unlock shared", "SELECT pg_advisory_unlock_shared(1)", pin},
		{"advisory unlock all", "SELECT pg_advisory_unlock_all()", pin},
		{"advisory lock uppercase", "SELECT PG_ADVISORY_LOCK(1)", pin},
		{"advisory lock schema-qualified", "SELECT pg_catalog.pg_advisory_lock(1)", pin},
		{"advisory lock quoted", `SELECT "pg_advisory_lock"(1)`, pin},
		{"advisory lock in explain", "EXPLAIN ANALYZE SELECT pg_advisory_lock(1)", pin},
		{"advisory xact lock", "SELECT pg_advisory_xact_lock(1)", none},
		{"try advisory xact lock", "SELECT pg_try_advisory_xact_lock(1)", none},
		{"advisory name in a string", "SELECT 'pg_advisory_lock'", none},
		{"advisory name in an escape string", `SELECT E'pg_advisory_lock\''`, none},
		{"advisory name in a line comment", "SELECT 1 -- pg_advisory_lock(1)", none},
		{"advisory name in a block comment", "/* pg_advisory_lock */ SELECT 1", none},
		{"advisory name in a dollar string", "SELECT $$pg_advisory_lock$$", none},
		{"advisory name in a tagged dollar string", "SELECT $q$ pg_advisory_lock $q$", none},
		{"advisory lock after a statement", "SELECT 1; SELECT pg_advisory_lock(2)", pin},

		// Temporary objects.
		{"create temp table", "CREATE TEMP TABLE t(x int)", pin},
		{"create temporary table", "create temporary table t(x int)", pin},
		{"create global temporary table", "CREATE GLOBAL TEMPORARY TABLE t(x int)", pin},
		{"create local temp table", "CREATE LOCAL TEMP TABLE t(x int)", pin},
		{"create temp view", "CREATE TEMP VIEW v AS SELECT 1", pin},
		{"create or replace temp view", "CREATE OR REPLACE TEMP VIEW v AS SELECT 1", pin},
		{"create temp sequence", "CREATE TEMP SEQUENCE s", pin},
		{"create temp across a comment", "CREATE /* x */ TEMP TABLE t(x int)", pin},
		{"select into temp", "SELECT 1 INTO TEMP t", pin},
		{"create in pg_temp", "CREATE TABLE pg_temp.t(x int)", pin},
		{"temp as a table name prefix", "CREATE TABLE temp_stuff(x int)", none},
		{"temp as a column name", "CREATE TABLE t(temp int)", none},
		{"temp in a string", "SELECT 'CREATE TEMP TABLE t(x int)'", none},

		// Other session state.
		{"cursor with hold", "DECLARE c CURSOR WITH HOLD FOR SELECT 1", pin},
		{"cursor without hold", "DECLARE c CURSOR FOR SELECT 1", none},
		{"prepare transaction", "PREPARE TRANSACTION 'x'", pin},
		{"prepare statement", "PREPARE s AS SELECT 1", pin},
		{"set_config", "SELECT set_config('a','b',false)", pin},
		{"set_config in a string", "SELECT 'set_config'", none},
		{"do block", "DO $$ BEGIN PERFORM pg_advisory_lock(1); END $$", pin},
		{"do block without a lock", "DO $$ BEGIN NULL; END $$", pin},
		{"load", "LOAD 'auto_explain'", pin},

		// COPY.
		{"copy from stdin", "COPY t FROM STDIN", stmt},
		{"copy to stdout", "copy t to stdout;", stmt},
		{"copy that also locks", "COPY (SELECT pg_advisory_lock(1)) TO STDOUT", pin},

		// Plain statements and multi-statement text.
		{"select", "SELECT 1", none},
		{"select with params", "SELECT id, name FROM users WHERE id = $1 AND org = $2", none},
		{"keyword in a string", "SELECT 'LISTEN x'", none},
		{"with query", "WITH x AS (SELECT 1) SELECT * FROM x", none},
		{"parenthesised select", "(SELECT 1)", none},
		{"empty", "", none},
		{"whitespace", " \t\n", none},
		{"only a comment", "-- nothing", none},
		{"only a semicolon", ";", none},
		{"unterminated string", "SELECT 'oops", none},
		{"unterminated block comment", "SELECT 1 /* oops", none},
		{"unterminated dollar string", "SELECT $$ oops", none},
		{"two selects", "SELECT 1; SELECT 2", none},
		{"trailing semicolon", "SELECT 1;", none},
		{"trailing semicolon and comment", "SELECT 1; -- done", none},
		{"semicolon in a quoted identifier", `SELECT "a;b" FROM t`, none},
		{"semicolon in a dollar string", "SELECT $$a;b$$", none},
		{"semicolon in a tagged dollar string", "SELECT $tag$ ; $tag$", none},
		{"begin and commit", "BEGIN; SELECT 1; COMMIT", none},
		{"listen in a batch", "LISTEN a; LISTEN b", pin},
		{"set after a select", "SELECT 1; SET x = 1", pin},
		{"set local in a batch", "SET LOCAL x = 1; SELECT 1", pin},
		{"reset in a batch", "SELECT 1; RESET ALL", pin},
		{"unlisten in a batch", "SELECT 1; UNLISTEN *", pin},
		{"discard all in a batch", "SELECT 1; DISCARD ALL", pin},
		{"discard plans in a batch", "SELECT 1; DISCARD PLANS", pin},
		{"discard temp in a batch", "SELECT 1; DISCARD TEMP", none},
		{"deallocate in a batch", "SELECT 1; DEALLOCATE s", pin},

		// Gaps found by checking the lexer against Postgres.
		{"carriage return ends a line comment", "SELECT 1 -- x\r, pg_advisory_lock(1)", pin},
		{"carriage return ends a comment before a SET", "SELECT 1 -- x\r; SET search_path = evil", pin},
		{"carriage return ends a comment before a LISTEN", "SET search_path = a -- x\r; LISTEN q", pin},
		{"backslash quote in a plain string", "SELECT 'O\\'Brien'; SELECT pg_advisory_lock(7); SELECT 'x'", pin},
		{"backslash in a plain string that agrees", `SELECT 'a\d+'`, none},
		{"backslash in a plain string, set value", `SET application_name = 'a\b'`, Classification{Effect: EffectSet, Param: "application_name", Value: `'a\b'`}},
		{"update pg_settings", "UPDATE pg_settings SET setting = 'evil' WHERE name = 'search_path'", pin},
		{"update pg_catalog.pg_settings", "UPDATE pg_catalog.pg_settings SET setting = 'evil' WHERE name = 'search_path'", pin},
		{"select from pg_settings", "SELECT setting FROM pg_settings WHERE name = 'search_path'", none},
		{"update another table", "UPDATE t SET x = 1", none},
		{"escape string continues over a newline", "SELECT E'a'\n'\\'' , pg_advisory_lock(1) --'", pin},
		{"escape string continues past a comment", "SELECT E'a' -- x\n  -- y\n'\\'' , pg_advisory_lock(1) --'", pin},
		{"plain string continues over a newline", "SELECT 'a'\n'b', 1", none},
		{"unicode identifier with an escape", `SELECT U&"\0070g_advisory_lock"(1)`, pin},
		{"unicode identifier with an escape in set_config", `SELECT U&"set\005fconfig"('a','b',false)`, pin},
		{"unicode identifier with uescape", `SELECT U&"d!0061t" UESCAPE '!' FROM t`, pin},
		{"unicode identifier with uescape after a comment", "SELECT u&\"x\" /* c */ uescape '!' FROM t", pin},
		{"unicode identifier lowercase prefix", `SELECT u&"\0070g_advisory_lock"(1)`, pin},
		{"unicode identifier without escapes", `SELECT U&"data" FROM t`, none},
		{"query_to_xml", "SELECT query_to_xml('select pg_advisory_lock(1)', true, true, '')", pin},
		{"query_to_xmlschema", "SELECT query_to_xmlschema('select 1', true, true, '')", pin},
		{"query_to_xml_and_xmlschema", "SELECT query_to_xml_and_xmlschema('select 1', true, true, '')", pin},
		{"ts_stat", "SELECT * FROM ts_stat('select tsv from t')", pin},
		{"setseed", "SELECT setseed(0.5)", pin},
		{"dblink_connect", "SELECT dblink_connect('c', 'dbname=x')", pin},
		{"dblink_connect_u", "SELECT dblink_connect_u('c', 'dbname=x')", pin},
		{"set local.foo", "SET local.foo = 1", pin},
		{"set transaction.foo", "SET transaction.foo = 1", pin},
		{"set session.foo", "SET session.foo = 1", pin},
		{"set local with a space before the value", "SET LOCAL foo = 1", none},
		{"set time zone local", "SET TIME ZONE LOCAL", Classification{Effect: EffectReset, Param: "timezone"}},
		{"set time zone interval", "SET TIME ZONE INTERVAL '+00:00' HOUR TO MINUTE", pin},
		{"set time zone a number", "SET TIME ZONE -7", Classification{Effect: EffectSet, Param: "timezone", Value: "-7"}},
		{"prepare in a batch", "SELECT 1; PREPARE s AS SELECT 1", pin},
		{"copy in a batch", "SELECT 1; COPY t FROM STDIN", stmt},
		{"comment between statements", "SELECT 1; /* x */ SELECT 2", none},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifySQL(tt.sql); got != tt.want {
				t.Errorf("ClassifySQL(%q) = %+v, want %+v", tt.sql, got, tt.want)
			}
		})
	}
}

func TestClassifySQLKnownParams(t *testing.T) {
	for _, p := range KnownParams {
		set := ClassifySQL("SET " + strings.ToUpper(p) + " = 1")
		if want := (Classification{Effect: EffectSet, Param: p, Value: "1"}); set != want {
			t.Errorf("SET %s: got %+v, want %+v", p, set, want)
		}
		reset := ClassifySQL("RESET " + p)
		if want := (Classification{Effect: EffectReset, Param: p}); reset != want {
			t.Errorf("RESET %s: got %+v, want %+v", p, reset, want)
		}
	}
}

func TestClassifySQLDoesNotAllocateForPlainStatements(t *testing.T) {
	for _, sql := range []string{
		"SELECT id, name FROM users WHERE id = $1",
		"/* trace */ INSERT INTO t(a, b) VALUES ('x', $1); ",
		"BEGIN",
	} {
		if n := testing.AllocsPerRun(100, func() { _ = ClassifySQL(sql) }); n != 0 {
			t.Errorf("ClassifySQL(%q) allocates %v times, want 0", sql, n)
		}
	}
}

func TestClassifySQLLongStatement(t *testing.T) {
	long := "INSERT INTO t(a) VALUES " + strings.Repeat("('x;y'),", 500) + "('z')"
	if got := ClassifySQL(long); got != (Classification{}) {
		t.Errorf("long insert: got %+v, want none", got)
	}
	if got := ClassifySQL(long + "; SELECT pg_advisory_lock(1)"); got.Effect != EffectPinSession {
		t.Errorf("long insert then lock: got %+v, want PinSession", got)
	}
}

func BenchmarkClassifySQL(b *testing.B) {
	const sql = "SELECT u.id, u.name, o.total FROM users u JOIN orders o ON o.user_id = u.id WHERE u.id = $1 AND o.status = 'open'"
	b.ReportAllocs()
	for b.Loop() {
		_ = ClassifySQL(sql)
	}
}
