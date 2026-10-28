//go:build integration

package aisettings

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
	_ "github.com/xraph/grove/drivers/pgdriver/pgmigrate" // registers the Postgres migration executor
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
)

// The migrations this package's tables come from, by version and name.
var settingsMigrations = map[string]string{
	"20261125000100": "ai_settings",
	"20261126000100": "ai_huggingface_token",
}

// scratchStore is a PgStore over a database of its own, dropped when the
// test ends, with the AI settings migrations applied twice each (a rerun
// must be harmless).
func scratchStore(t *testing.T) (*PgStore, *pgdriver.PgDB) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the AI settings store test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("aisettings_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Fatal("LOOMPLANE_TEST_DATABASE_URL must be a PostgreSQL URL")
	}
	u.Path = "/" + name
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	exec, err := migrate.NewExecutorFor(pg)
	if err != nil {
		t.Fatal(err)
	}
	applied := 0
	for _, m := range migrations.Group().Migrations() { // in version order
		name, ok := settingsMigrations[m.Version]
		if !ok {
			continue
		}
		if m.Name != name {
			t.Fatalf("migration %s is %q, want %q", m.Version, m.Name, name)
		}
		for range 2 {
			if err = m.Up(ctx, exec); err != nil {
				t.Fatal(err)
			}
		}
		applied++
	}
	if applied != len(settingsMigrations) {
		t.Fatalf("applied %d of the %d AI settings migrations", applied, len(settingsMigrations))
	}
	return NewPgStore(pg), pg
}

func TestSettingsDefaultsForANewWorkspace(t *testing.T) {
	st, _ := scratchStore(t)
	got, err := st.Settings(t.Context(), "org_new")
	if err != nil {
		t.Fatal(err)
	}
	want := &Settings{OrgID: "org_new", MonthlyBudgetUSD: "0.00", Embeddings: Embeddings{
		Mode: EmbeddingsLocal, Model: "granite-embedding-107m-multilingual", Dimensions: 384,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("defaults:\n got %+v\nwant %+v", got, want)
	}
	// Reading stores nothing.
	if again, _ := st.Settings(t.Context(), "org_new"); again.Revision != 0 {
		t.Fatalf("revision after a read: %d", again.Revision)
	}
}

func TestProviderKeyRoundTrip(t *testing.T) {
	st, pg := scratchStore(t)
	ctx := t.Context()
	k := &ProviderKey{
		OrgID: "org_a", Name: "local-llm", Type: "opencompat",
		Fields: map[string]string{"base_url": "https://llm.internal.example/v1"},
		Models: []OpenCompatModel{
			{ID: "qwen3-8b", Task: "chat", ContextWindow: 32768, MaxOutput: 8192, InputPerMillion: "0", OutputPerMillion: "0"},
			{ID: "bge-m3", Task: "embedding", Dimensions: 1024, InputPerMillion: "0.02", OutputPerMillion: "0", EmbeddingPerMillion: "0.02"},
		},
		KeyHint: "wxyz", Projects: []string{"proj_1", "proj_2"}, CreatedBy: "usr_admin", UpdatedBy: "usr_admin",
	}
	if err := st.SaveProviderKey(ctx, k); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(k.ID, "aikey_") || k.Revision != 1 || k.CreatedAt.IsZero() || !k.UpdatedAt.Equal(k.CreatedAt) {
		t.Fatalf("saved key: id %q revision %d created %v updated %v", k.ID, k.Revision, k.CreatedAt, k.UpdatedAt)
	}
	got, err := st.ProviderKey(ctx, "org_a", "local-llm")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, k) {
		t.Fatalf("read back:\n got %+v\nwant %+v", got, k)
	}

	// A second save updates the same row and bumps its revision. The stored
	// ID and creator stay, even when the caller sends others.
	firstID, created := k.ID, k.CreatedAt
	update := &ProviderKey{
		ID: "aikey_someoneelse", OrgID: "org_a", Name: "local-llm", Type: "opencompat",
		Fields: map[string]string{"base_url": "https://llm2.internal.example/v1"}, Models: k.Models,
		KeyHint: "abcd", Projects: []string{}, CreatedBy: "usr_other", UpdatedBy: "usr_other",
	}
	if err = st.SaveProviderKey(ctx, update); err != nil {
		t.Fatal(err)
	}
	if update.ID != firstID || update.Revision != 2 || update.CreatedBy != "usr_admin" || update.UpdatedBy != "usr_other" ||
		!update.CreatedAt.Equal(created) || update.UpdatedAt.Before(created) {
		t.Fatalf("updated key: %+v", update)
	}
	keys, err := st.ProviderKeys(ctx, "org_a")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || !reflect.DeepEqual(keys[0], update) {
		t.Fatalf("keys after an update: %+v", keys)
	}
	if keys[0].Projects == nil {
		t.Fatal("projects read back as nil, want an empty list")
	}

	// (org, name) is unique in the table itself.
	_, err = pg.Exec(ctx, `INSERT INTO loomplane_ai_provider_keys (id, org_id, name, type, created_by, updated_by)
		VALUES ('aikey_dup', 'org_a', 'local-llm', 'openai', 'u', 'u')`)
	if err == nil {
		t.Fatal("a second key with the same org and name was stored")
	}

	// Another workspace's key with the same name is its own.
	other := &ProviderKey{OrgID: "org_b", Name: "local-llm", Type: "anthropic", CreatedBy: "usr_b", UpdatedBy: "usr_b"}
	if err = st.SaveProviderKey(ctx, other); err != nil {
		t.Fatal(err)
	}
	if other.ID == firstID || other.Revision != 1 || other.Projects == nil {
		t.Fatalf("other workspace's key: %+v", other)
	}
	if keys, _ = st.ProviderKeys(ctx, "org_a"); len(keys) != 1 || keys[0].Type != "opencompat" {
		t.Fatalf("org_a keys after org_b saved one: %+v", keys)
	}

	if err = st.DeleteProviderKey(ctx, "org_a", "local-llm"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ProviderKey(ctx, "org_a", "local-llm"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted key: %v", err)
	}
	if err = st.DeleteProviderKey(ctx, "org_a", "local-llm"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if _, err = st.ProviderKey(ctx, "org_b", "local-llm"); err != nil {
		t.Fatalf("org_b's key after org_a's delete: %v", err)
	}
}

func TestCreateProviderKeyNeverOverwrites(t *testing.T) {
	st, _ := scratchStore(t)
	ctx := t.Context()
	first := &ProviderKey{ID: "aikey_first", OrgID: "org_a", Name: "claude", Type: "anthropic", KeyHint: "wxyz", CreatedBy: "usr_a", UpdatedBy: "usr_a"}
	if err := st.CreateProviderKey(ctx, first); err != nil {
		t.Fatal(err)
	}
	if first.ID != "aikey_first" || first.Revision != 1 {
		t.Fatalf("created = %+v", first)
	}
	second := &ProviderKey{ID: "aikey_second", OrgID: "org_a", Name: "claude", Type: "openai", KeyHint: "3333", CreatedBy: "usr_b", UpdatedBy: "usr_b"}
	if err := st.CreateProviderKey(ctx, second); !errors.Is(err, ErrStale) {
		t.Fatalf("second create err = %v, want ErrStale", err)
	}
	got, err := st.ProviderKey(ctx, "org_a", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "aikey_first" || got.Type != "anthropic" || got.KeyHint != "wxyz" || got.Revision != 1 || got.UpdatedBy != "usr_a" {
		t.Errorf("the refused create changed the key: %+v", got)
	}
	// Another workspace may use the name.
	other := &ProviderKey{OrgID: "org_b", Name: "claude", Type: "openai", CreatedBy: "usr_b", UpdatedBy: "usr_b"}
	if err := st.CreateProviderKey(ctx, other); err != nil || !strings.HasPrefix(other.ID, "aikey_") {
		t.Fatalf("other org create: %v, %+v", err, other)
	}
}

func allocation(scope, usd string) *Allocation {
	return &Allocation{Scope: scope, ProjectID: "proj_shop", EnvironmentID: "env_" + scope, Label: "shop / " + scope, MonthlyBudgetUSD: usd}
}

func TestSetAllocationRefusesOverBudget(t *testing.T) {
	st, _ := scratchStore(t)
	ctx := t.Context()

	// A workspace with no budget has nothing to allocate.
	err := st.SetAllocation(ctx, "org_a", allocation("gateway:env_staging/ai", "10"))
	if !errors.Is(err, ErrOverBudget) || !strings.Contains(err.Error(), "0.00") {
		t.Fatalf("allocation with no budget: %v", err)
	}

	if _, err = st.SetBudget(ctx, "org_a", "100", "usr_admin"); err != nil {
		t.Fatal(err)
	}
	staging := allocation("gateway:env_staging/ai", "60")
	if err = st.SetAllocation(ctx, "org_a", staging); err != nil {
		t.Fatal(err)
	}
	if staging.MonthlyBudgetUSD != "60.00" || staging.UpdatedAt.IsZero() {
		t.Fatalf("stored allocation: %+v", staging)
	}
	err = st.SetAllocation(ctx, "org_a", allocation("gateway:env_production/ai", "50"))
	if !errors.Is(err, ErrOverBudget) {
		t.Fatalf("over budget: %v", err)
	}
	if !strings.Contains(err.Error(), "40.00") {
		t.Fatalf("error doesn't name what's left: %v", err)
	}

	// Raising an allocation counts its new amount, not old plus new.
	if err = st.SetAllocation(ctx, "org_a", allocation("gateway:env_staging/ai", "100")); err != nil {
		t.Fatalf("raising an allocation to the whole budget: %v", err)
	}
	got, err := st.Allocations(ctx, "org_a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Scope != "gateway:env_staging/ai" || got[0].MonthlyBudgetUSD != "100.00" ||
		got[0].ProjectID != "proj_shop" || got[0].EnvironmentID != "env_gateway:env_staging/ai" || got[0].Label != "shop / gateway:env_staging/ai" {
		t.Fatalf("allocations: %+v", got)
	}

	// Deleting frees the budget, and deleting again is harmless.
	for range 2 {
		if err = st.DeleteAllocation(ctx, "org_a", "gateway:env_staging/ai"); err != nil {
			t.Fatal(err)
		}
	}
	if err = st.SetAllocation(ctx, "org_a", allocation("gateway:env_production/ai", "50")); err != nil {
		t.Fatalf("allocation after a delete: %v", err)
	}
}

func TestConcurrentAllocationsNeverExceedTheBudget(t *testing.T) {
	st, _ := scratchStore(t)
	ctx := t.Context()
	if _, err := st.SetBudget(ctx, "org_a", "100", "usr_admin"); err != nil {
		t.Fatal(err)
	}
	var (
		wg              sync.WaitGroup
		mu              sync.Mutex
		ok, over, other int
	)
	for i := range 20 {
		wg.Go(func() {
			err := st.SetAllocation(ctx, "org_a", allocation(fmt.Sprintf("gateway:env_%02d/ai", i), "10"))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrOverBudget):
				over++
			default:
				other++
				t.Errorf("allocation %d: %v", i, err)
			}
		})
	}
	wg.Wait()
	if ok != 10 || over != 10 || other != 0 {
		t.Fatalf("%d succeeded, %d over budget, %d failed otherwise", ok, over, other)
	}
	got, err := st.Allocations(ctx, "org_a")
	if err != nil {
		t.Fatal(err)
	}
	var cents int
	for _, a := range got {
		var whole, frac int
		if _, err = fmt.Sscanf(a.MonthlyBudgetUSD, "%d.%d", &whole, &frac); err != nil {
			t.Fatal(err)
		}
		cents += whole*100 + frac
	}
	if len(got) != 10 || cents != 10000 {
		t.Fatalf("%d allocations summing to %d cents", len(got), cents)
	}
}

func TestSetBudgetBelowAllocations(t *testing.T) {
	st, _ := scratchStore(t)
	ctx := t.Context()
	if _, err := st.SetBudget(ctx, "org_a", "100", "usr_admin"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAllocation(ctx, "org_a", allocation("gateway:env_staging/ai", "60")); err != nil {
		t.Fatal(err)
	}
	_, err := st.SetBudget(ctx, "org_a", "50", "usr_admin")
	if !errors.Is(err, ErrUnderAllocated) || !strings.Contains(err.Error(), "60.00") {
		t.Fatalf("budget below allocations: %v", err)
	}
	if s, _ := st.Settings(ctx, "org_a"); s.MonthlyBudgetUSD != "100.00" {
		t.Fatalf("a refused budget was stored: %+v", s)
	}
	s, err := st.SetBudget(ctx, "org_a", "60", "usr_other")
	if err != nil {
		t.Fatal(err)
	}
	if s.MonthlyBudgetUSD != "60.00" || s.UpdatedBy != "usr_other" || s.Revision != 3 || s.Embeddings != LocalEmbeddings() {
		t.Fatalf("budget at the allocations: %+v", s)
	}
}

func TestSaveSettingsIsOptimistic(t *testing.T) {
	st, _ := scratchStore(t)
	ctx := t.Context()
	read, err := st.Settings(ctx, "org_a")
	if err != nil {
		t.Fatal(err)
	}
	voyage := Embeddings{Mode: EmbeddingsProvider, Provider: "voyage", Model: "voyage-3", Dimensions: 1024}
	first := &Settings{OrgID: "org_a", Embeddings: voyage, UpdatedBy: "usr_a"}
	if err = st.SaveSettings(ctx, first, read.Revision); err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || first.MonthlyBudgetUSD != "0.00" || first.UpdatedAt.IsZero() {
		t.Fatalf("first save: %+v", first)
	}
	second := &Settings{OrgID: "org_a", Embeddings: LocalEmbeddings(), UpdatedBy: "usr_b"}
	if err = st.SaveSettings(ctx, second, read.Revision); !errors.Is(err, ErrStale) {
		t.Fatalf("second save with the same revision: %v", err)
	}
	got, err := st.Settings(ctx, "org_a")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, first) {
		t.Fatalf("stored:\n got %+v\nwant %+v", got, first)
	}

	// A budget change is a new revision too, and a save leaves the budget
	// alone whatever it carries.
	if _, err = st.SetBudget(ctx, "org_a", "25.5", "usr_admin"); err != nil {
		t.Fatal(err)
	}
	second.MonthlyBudgetUSD = "9999"
	if err = st.SaveSettings(ctx, second, got.Revision); !errors.Is(err, ErrStale) {
		t.Fatalf("save after a budget change: %v", err)
	}
	if err = st.SaveSettings(ctx, second, got.Revision+1); err != nil {
		t.Fatal(err)
	}
	if second.Revision != 3 || second.MonthlyBudgetUSD != "25.50" || second.Embeddings != LocalEmbeddings() || second.UpdatedBy != "usr_b" {
		t.Fatalf("save after a budget change: %+v", second)
	}
}

func TestHuggingFaceTokenMetadata(t *testing.T) {
	st, pg := scratchStore(t)
	ctx := t.Context()
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return at }

	// A workspace with no row gets one, with the token's first revision and
	// the settings' defaults.
	hf, err := st.SetHuggingFaceToken(ctx, "org_a", "hf_\u2026wxyz", "rex")
	if err != nil {
		t.Fatal(err)
	}
	want := &HuggingFaceToken{Hint: "hf_\u2026wxyz", Account: "rex", SetAt: at, Revision: 1}
	if !reflect.DeepEqual(hf, want) {
		t.Fatalf("set:\n got %+v\nwant %+v", hf, want)
	}
	got, err := st.Settings(ctx, "org_a")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.HuggingFace, want) || got.Revision != 1 || got.MonthlyBudgetUSD != "0.00" {
		t.Fatalf("settings after set: %+v (token %+v)", got, got.HuggingFace)
	}

	// Setting and clearing bump the token's revision, never the settings'.
	if hf, err = st.SetHuggingFaceToken(ctx, "org_a", "hf_\u20269876", "rex-org"); err != nil || hf.Revision != 2 || hf.Account != "rex-org" {
		t.Fatalf("second set = %+v, %v", hf, err)
	}
	if err = st.ClearHuggingFaceToken(ctx, "org_a"); err != nil {
		t.Fatal(err)
	}
	if got, err = st.Settings(ctx, "org_a"); err != nil || got.HuggingFace != nil || got.Revision != 1 {
		t.Fatalf("settings after clear: %+v, %v", got, err)
	}
	if hf, err = st.SetHuggingFaceToken(ctx, "org_a", "hf_\u2026abcd", "rex"); err != nil || hf.Revision != 4 {
		t.Fatalf("set after clear = %+v, %v; want revision 4", hf, err)
	}
	// Clearing a workspace with no row writes nothing.
	if err = st.ClearHuggingFaceToken(ctx, "org_none"); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_ai_settings WHERE org_id = 'org_none'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rows for org_none: %d, %v", rows, err)
	}
	// The settings writes leave the token alone.
	if _, err = st.SetBudget(ctx, "org_a", "10", "usr_a"); err != nil {
		t.Fatal(err)
	}
	if got, err = st.Settings(ctx, "org_a"); err != nil || got.HuggingFace == nil || got.HuggingFace.Revision != 4 {
		t.Fatalf("token after a budget change: %+v, %v", got.HuggingFace, err)
	}
}

// One workspace's token lock holders run one at a time, and another
// workspace's don't wait for them. The holder's own writes to the settings
// row, on other connections, don't wait on the lock either.
func TestHuggingFaceTokenLockSerializesAWorkspace(t *testing.T) {
	ctx := context.Background()
	s, _ := scratchStore(t)

	held := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- s.LockHuggingFaceToken(ctx, "org_a", func(ctx context.Context) error {
			// A write to the row while holding the lock.
			if _, err := s.SetHuggingFaceToken(ctx, "org_a", "hf_…aaaa", "rex"); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	second := make(chan struct{})
	go func() {
		_ = s.LockHuggingFaceToken(ctx, "org_a", func(context.Context) error {
			close(second)
			return nil
		})
	}()
	other := make(chan error, 1)
	go func() { other <- s.LockHuggingFaceToken(ctx, "org_b", func(context.Context) error { return nil }) }()
	select {
	case err := <-other:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("another workspace waited for org_a's lock")
	}
	select {
	case <-second:
		t.Fatal("a second holder ran while org_a's lock was held")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	select {
	case <-second:
	case <-time.After(5 * time.Second):
		t.Fatal("the second holder never ran")
	}

	// An error from fn comes back, and the lock is let go.
	boom := errors.New("boom")
	if err := s.LockHuggingFaceToken(ctx, "org_a", func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("fn's error: %v", err)
	}
	if err := s.LockHuggingFaceToken(ctx, "org_a", func(context.Context) error { return nil }); err != nil {
		t.Errorf("after an error: %v", err)
	}
}
