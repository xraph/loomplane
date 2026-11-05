//go:build integration

package identity

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	authid "github.com/xraph/authsome/id"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/keysmith"
	keyid "github.com/xraph/keysmith/id"
	"github.com/xraph/keysmith/scope"
	kspg "github.com/xraph/keysmith/store/postgres"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/security"
)

func TestApprovalAdmissionPostgresKeyFacts(t *testing.T) {
	dsn := os.Getenv("LOOMPLANE_CURRENT_AUTHORITY_TEST_DSN")
	if dsn == "" {
		t.Skip("set LOOMPLANE_CURRENT_AUTHORITY_TEST_DSN for isolated PostgreSQL 55471")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || cfg.ConnConfig.Port != 55471 || (cfg.ConnConfig.Host != "localhost" && cfg.ConnConfig.Host != "127.0.0.1") {
		t.Fatal("requires isolated PostgreSQL 55471")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("fixture requires URL DSN")
	}
	schema := fmt.Sprintf("approval_source_%d", time.Now().UnixNano())
	params := parsed.Query()
	params.Set("search_path", schema)
	params.Set("pool_max_conns", "1")
	params.Set("pool_min_conns", "0")
	params.Set("pool_min_idle_conns", "0")
	parsed.RawQuery = params.Encode()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	db := pgdriver.New()
	if err = db.Open(ctx, parsed.String(), driver.WithPoolSize(1)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	exec := func(q string, args ...any) {
		t.Helper()
		if _, execErr := db.Exec(ctx, q, args...); execErr != nil {
			t.Fatal(execErr)
		}
	}
	exec("CREATE SCHEMA " + pgx.Identifier{schema}.Sanitize())
	t.Cleanup(func() {
		cleanup, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		if _, dropErr := db.Exec(cleanup, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); dropErr != nil {
			t.Error(dropErr)
		}
	})
	reader := &approvalKeyReader{db: db}
	reader.prepare(ctx)
	if reader.schema.Load() != nil {
		t.Fatal("missing tables admitted")
	}
	store := kspg.New(db)
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	reader.prepare(ctx)
	if reader.schema.Load() == nil {
		t.Fatal("installed schema unavailable")
	}
	engine, err := keysmith.NewEngine(keysmith.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	app := authid.NewAppID().String()
	actor := security.Principal{UserID: authid.NewUserID().String(), OrgID: authid.NewOrgID().String()}
	service := apikeys.New(engine, app, admissionAllow{}, nil)
	created, err := service.Create(ctx, actor, apikeys.NewKey{Name: "approval", Access: apikeys.Write})
	if err != nil {
		t.Fatal(err)
	}
	actor.KeyID = created.Key.ID
	authenticated, err := service.Authenticate(ctx, created.Secret)
	if err != nil || !sameOrdinary(authenticated, actor) {
		t.Fatal("actual key authentication failed", err)
	}
	k, scoped, err := reader.read(ctx, actor.KeyID)
	if err != nil || scoped || apikeys.ValidateSavedKeyRecordAt(actor, app, k, time.Now()) != nil {
		t.Fatalf("saved key read failed: scoped=%v err=%v", scoped, err)
	}
	if k.KeyHash != "" || k.Name != "" || k.Hint != "" {
		t.Fatal("unneeded key facts retained")
	}
	kid, err := keyid.ParseKeyID(actor.KeyID)
	if err != nil {
		t.Fatal(err)
	}
	sc := &scope.Scope{ID: keyid.NewScopeID(), TenantID: actor.OrgID, Name: "deploy", CreatedAt: time.Now()}
	if err = store.Scopes().Create(ctx, sc); err != nil {
		t.Fatal(err)
	}
	if err = store.Scopes().AssignToKey(ctx, kid, []string{sc.Name}); err != nil {
		t.Fatal(err)
	}
	_, scoped, err = reader.read(ctx, actor.KeyID)
	if err != nil || !scoped {
		t.Fatal("persisted scope link missed", err)
	}
	// Keep the real installed schema, then remove only this fixture's foreign
	// key to prove orphan links cannot disappear through an inner join.
	var constraint string
	if err = db.QueryRow(ctx, `SELECT c.conname FROM pg_catalog.pg_constraint c WHERE c.conrelid OPERATOR(pg_catalog.=) $1::pg_catalog.regclass AND c.confrelid OPERATOR(pg_catalog.=) $2::pg_catalog.regclass AND c.contype OPERATOR(pg_catalog.=) 'f'`, pgx.Identifier{schema, "keysmith_key_scopes"}.Sanitize(), pgx.Identifier{schema, "keysmith_scopes"}.Sanitize()).Scan(&constraint); err != nil {
		t.Fatal(err)
	}
	exec("ALTER TABLE " + pgx.Identifier{schema, "keysmith_key_scopes"}.Sanitize() + " DROP CONSTRAINT " + pgx.Identifier{constraint}.Sanitize())
	exec("DELETE FROM "+pgx.Identifier{schema, "keysmith_scopes"}.Sanitize()+" WHERE id=$1", sc.ID.String())
	_, scoped, err = reader.read(ctx, actor.KeyID)
	if err != nil || !scoped {
		t.Fatal("orphan scope link missed", err)
	}
	exec("DELETE FROM " + pgx.Identifier{schema, "keysmith_key_scopes"}.Sanitize())
	for _, change := range []string{"state='suspended'", "revoked_at=pg_catalog.now()", "expires_at=pg_catalog.now()-interval '1 second'", "metadata='{\"access\":\"read\"}'::jsonb", "metadata='{\"access\":\"write\",\"kind\":\"fleet\"}'::jsonb"} {
		exec("UPDATE "+pgx.Identifier{schema, "keysmith_keys"}.Sanitize()+" SET "+change+" WHERE id=$1", actor.KeyID)
		k, _, err = reader.read(ctx, actor.KeyID)
		if err != nil || apikeys.ValidateSavedKeyRecordAt(actor, app, k, time.Now()) == nil {
			t.Fatalf("predicate not copied for %s: %v", change, err)
		}
		exec("UPDATE "+pgx.Identifier{schema, "keysmith_keys"}.Sanitize()+" SET state='active',revoked_at=NULL,expires_at=pg_catalog.now()+interval '1 hour',metadata='{\"access\":\"write\"}'::jsonb WHERE id=$1", actor.KeyID)
	}
	if _, _, err = reader.read(ctx, keyid.NewKeyID().String()); err == nil {
		t.Fatal("missing key accepted")
	}
	exec("SET search_path TO pg_catalog")
	if _, _, err = reader.read(ctx, actor.KeyID); err == nil {
		t.Fatal("changed schema accepted")
	}
	reader.prepare(ctx)
	if reader.schema.Load() != nil {
		t.Fatal("wrong installed resolution accepted")
	}
	exec("SET search_path TO " + pgx.Identifier{schema}.Sanitize())
	reader.prepare(ctx)
	if reader.schema.Load() == nil {
		t.Fatal("schema recovery failed")
	}
	exec("ALTER TABLE " + pgx.Identifier{schema, "keysmith_key_scopes"}.Sanitize() + " RENAME TO removed_scope_links")
	if _, _, err = reader.read(ctx, actor.KeyID); err == nil {
		t.Fatal("query failure accepted")
	}
	reader.prepare(ctx)
	if reader.schema.Load() != nil {
		t.Fatal("missing scope table accepted")
	}
	if strings.Contains(schema, "public") {
		t.Fatal("fixture used application schema")
	}
}
