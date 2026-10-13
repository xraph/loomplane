//go:build integration

package realtime

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
)

func eventDatabase(t *testing.T) (*PgStore, *pgx.Conn, string) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for durable event tests")
	}
	ctx := t.Context()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("loomplane_events_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	dsn = u.String()
	if err = ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	if err = pg.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return NewPgStore(pg), conn, dsn
}

func TestPgStore_serializesCommitCursorsAndRollsBackStateEvents(t *testing.T) {
	store, conn, _ := eventDatabase(t)
	ctx := t.Context()
	if _, err := conn.Exec(ctx, `INSERT INTO loomplane_projects (id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES ('proj_1','org_1','Before','before','active','usr_1',now(),now())`); err != nil {
		t.Fatal(err)
	}
	before, err := store.HighWater(ctx, "org_1")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE loomplane_projects SET name='Committed first' WHERE id='proj_1'`); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, appendErr := store.Append(ctx, "org_1", "fixture.changed", []byte(`{"id":"second"}`))
		result <- appendErr
	}()
	// An independent writer waits on the cursor held by the state write.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second writer did not wait for the cursor")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	events, found, err := store.Since(ctx, "org_1", before)
	if err != nil || !found || len(events) != 2 || events[0].Event != "project.updated" || events[1].Event != "fixture.changed" {
		t.Fatalf("commit order: %+v %v %v", events, found, err)
	}
	after, err := store.HighWater(ctx, "org_1")
	if err != nil {
		t.Fatal(err)
	}
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_projects SET name='Rolled back' WHERE id='proj_1'`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	events, found, err = store.Since(ctx, "org_1", after)
	if err != nil || !found || len(events) != 0 {
		t.Fatalf("rolled-back state event retained: %+v %v %v", events, found, err)
	}
	if _, found, err = store.Since(ctx, "org_2", after); err != nil || found {
		t.Fatalf("foreign cursor accepted: %v %v", found, err)
	}
}

func TestPgStore_deduplicatesProviderIDsAndRetainsRepeatedLines(t *testing.T) {
	store, _, _ := eventDatabase(t)
	ctx := t.Context()
	input := LogInput{OrgID: "org_1", ProjectID: "proj_1", DeploymentID: "dep_1", ActionID: "service:api", Attempt: "1", ProviderID: "line-a", Stream: "stdout", Message: "repeated private-token", Time: time.Now()}
	redactor := NewRedactor([]string{"private-token"})
	for _, id := range []string{"line-a", "line-a", "line-b"} {
		input.ProviderID = id
		if _, err := store.AppendLog(ctx, input, redactor); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.Logs(ctx, "org_1", "dep_1", "", 100)
	if err != nil || len(page.Logs) != 2 || page.Logs[0].Message != page.Logs[1].Message || strings.Contains(page.Logs[0].Message, "private-token") {
		t.Fatalf("dedup/repeated output: %+v %v", page, err)
	}
	other, err := store.Logs(ctx, "org_2", "dep_1", "", 100)
	if err != nil || len(other.Logs) != 0 {
		t.Fatalf("foreign logs: %+v %v", other, err)
	}
	input.ProviderID = "long"
	input.Message = strings.Repeat("x", MaxLineBytes+1)
	line, err := store.AppendLog(ctx, input, redactor)
	if err != nil || !line.Truncated || len(line.Message) > MaxLineBytes {
		t.Fatalf("line cap: %+v %v", line, err)
	}
}

func TestPgStore_attemptBudgetAndRetentionProduceExplicitGaps(t *testing.T) {
	store, conn, _ := eventDatabase(t)
	ctx := t.Context()
	input := LogInput{OrgID: "org_1", ProjectID: "proj_1", DeploymentID: "dep_1", ActionID: "service:api", Attempt: "1", ProviderID: "initial", Stream: "stdout", Message: "initial"}
	line, err := store.AppendLog(ctx, input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `UPDATE loomplane_build_log_budgets SET bytes=$1 WHERE org_id='org_1'`, MaxAttemptBytes); err != nil {
		t.Fatal(err)
	}
	input.ProviderID = "limit"
	input.Message = "later output"
	marker, err := store.AppendLog(ctx, input, nil)
	if err != nil || marker == nil || !marker.Truncated || marker.Stream != "system" {
		t.Fatalf("attempt marker: %+v %v", marker, err)
	}
	input.ProviderID = "ignored"
	if later, appendErr := store.AppendLog(ctx, input, nil); appendErr != nil || later != nil {
		t.Fatalf("attempt kept output after limit: %+v %v", later, appendErr)
	}
	input.Attempt = "2"
	input.ProviderID = "new-attempt"
	if next, appendErr := store.AppendLog(ctx, input, nil); appendErr != nil || next == nil || next.Truncated {
		t.Fatalf("retry inherited budget: %+v %v", next, appendErr)
	}
	if _, err = conn.Exec(ctx, `UPDATE loomplane_events SET created_at=now()-interval '8 days' WHERE org_id='org_1' AND sequence<=2`); err != nil {
		t.Fatal(err)
	}
	if err = store.Prune(ctx, time.Now().Add(-Retention)); err != nil {
		t.Fatal(err)
	}
	if events, found, replayErr := store.Since(ctx, "org_1", line.ID); replayErr != nil || found || len(events) != 0 {
		t.Fatalf("expired resume silently succeeded: %+v %v %v", events, found, replayErr)
	}
	page, err := store.Logs(ctx, "org_1", "dep_1", line.ID, 100)
	if err != nil || !page.Gap || len(page.Logs) != 1 || page.Logs[0].Attempt != "2" {
		t.Fatalf("retained logs after gap: %+v %v", page, err)
	}
}

func TestPgStore_retainsCollectorCursorWithoutPublishingIt(t *testing.T) {
	store, _, _ := eventDatabase(t)
	ref := LogInput{OrgID: "org_1", ProjectID: "proj_1", DeploymentID: "dep_1", ActionID: "service:api", Attempt: "1", OperationID: "build-1"}
	if err := store.SaveCheckpoint(t.Context(), ref, "opaque-provider-cursor", "unavailable", "access denied private-token", NewRedactor([]string{"private-token"})); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := store.Checkpoint(t.Context(), ref)
	if err != nil || checkpoint.Cursor != "opaque-provider-cursor" || strings.Contains(checkpoint.Message, "private-token") {
		t.Fatalf("collector checkpoint: %+v %v", checkpoint, err)
	}
	events, found, err := store.Since(t.Context(), "org_1", "org_1:0")
	if err != nil || !found || len(events) != 1 || strings.Contains(string(events[0].Data), "opaque-provider-cursor") || events[0].Event != "build.collection" {
		t.Fatalf("published private checkpoint or lost state: %+v %v", events, err)
	}
}
