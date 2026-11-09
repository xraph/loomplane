//go:build integration

package agentchat_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	_ "github.com/xraph/grove/drivers/pgdriver/pgmigrate" // registers the Postgres migration executor
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/agentchat"
	"github.com/xraph/loomplane/internal/agentruns"
	"github.com/xraph/loomplane/internal/agenttest"
	"github.com/xraph/loomplane/internal/pglock"
	"github.com/xraph/loomplane/internal/security"
)

func openDB(t *testing.T, opts ...driver.Option) *pgdriver.PgDB {
	t.Helper()
	dsn := agenttest.FreshDatabase(t)
	pg := pgdriver.New()
	if err := pg.Open(context.Background(), dsn, opts...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return pg
}

// task is one row of loomplane_agent_tasks, written straight to the table.
type task struct {
	id, org, project, kind, session, status, by, input string
	at                                                 time.Time
}

func insert(t *testing.T, pg *pgdriver.PgDB, tk task) {
	t.Helper()
	_, err := pg.Exec(context.Background(), `INSERT INTO loomplane_agent_tasks (id, org_id, project_id, kind, run_id,
			session_id, status, requested_by, principal, input, max_tokens, event_seq, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, '{}', $9, 1000, 4, $10, $10)`,
		tk.id, tk.org, tk.project, tk.kind, "arun_"+tk.id, tk.session, tk.status, tk.by, tk.input, tk.at)
	if err != nil {
		t.Fatal(err)
	}
}

// A conversation is its session's chat tasks: the first gives its starter,
// project, title and start, the latest its status and last update, and an
// investigation sharing the session is never part of it.
func TestConversationStoreOverTasks(t *testing.T) {
	ctx := context.Background()
	pg := openDB(t)
	st := agentchat.NewPgStore(pg)
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	at := func(m int) time.Time { return base.Add(time.Duration(m) * time.Minute) }
	for _, tk := range []task{
		{"atask_1", "o", "proj_1", "chat", "s1", "completed", "usr_ada", "What is deployed in production right now?", at(1)},
		{"atask_2", "o", "proj_1", "chat", "s1", "completed", "usr_ada", "And staging?", at(2)},
		{"atask_3", "o", "proj_1", "chat", "s1", "running", "usr_ada", "Roll it back", at(3)},
		{"atask_4", "o", "", "chat", "s2", "completed", "usr_ada", "Hello", at(10)},
		{"atask_5", "o", "", "chat", "s3", "completed", "usr_bo", "Bo here", at(5)},
		{"atask_6", "o", "proj_1", "investigation", "s1", "failed", "", "Why did it fail?", at(20)},
		{"atask_7", "o2", "", "chat", "s9", "completed", "usr_bob", "Ledger", at(30)},
	} {
		insert(t, pg, tk)
	}

	list, err := st.List(ctx, "o", "usr_ada", agentchat.ListFilter{Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "s2" || list[1].ID != "s1" {
		t.Fatalf("Ada's list %+v", list)
	}
	s1 := list[1]
	if s1.Turns != 3 || s1.Title != "What is deployed in production right now?" || s1.Status != "running" ||
		s1.ProjectID != "proj_1" || s1.StartedBy != "usr_ada" || s1.LastTaskID != "atask_3" || s1.OrgID != "o" ||
		!s1.CreatedAt.Equal(at(1)) || !s1.UpdatedAt.Equal(at(3)) {
		t.Fatalf("s1 %+v", s1)
	}

	everyone, err := st.List(ctx, "o", "", agentchat.ListFilter{Limit: 30})
	if err != nil || len(everyone) != 3 || everyone[0].ID != "s2" || everyone[1].ID != "s3" || everyone[2].ID != "s1" {
		t.Fatalf("everyone %+v, %v", everyone, err)
	}
	inProject, err := st.List(ctx, "o", "", agentchat.ListFilter{ProjectID: "proj_1", Limit: 30})
	if err != nil || len(inProject) != 1 || inProject[0].ID != "s1" {
		t.Fatalf("in proj_1 %+v, %v", inProject, err)
	}
	page, err := st.List(ctx, "o", "", agentchat.ListFilter{Limit: 1})
	if err != nil || len(page) != 1 || page[0].ID != "s2" {
		t.Fatalf("first page %+v, %v", page, err)
	}
	page, err = st.List(ctx, "o", "", agentchat.ListFilter{Before: page[0].UpdatedAt, Limit: 30})
	if err != nil || len(page) != 2 || page[0].ID != "s3" || page[1].ID != "s1" {
		t.Fatalf("next page %+v, %v", page, err)
	}

	got, err := st.Conversation(ctx, "o", "s1")
	if err != nil || got.Turns != 3 || got.LastTaskID != "atask_3" || got.Status != "running" {
		t.Fatalf("conversation s1 %+v, %v", got, err)
	}
	if _, err = st.Conversation(ctx, "o2", "s1"); !errors.Is(err, agentchat.ErrNotFound) {
		t.Fatalf("o2 reads o's conversation: %v", err)
	}
	if _, err = st.Conversation(ctx, "o", "nope"); !errors.Is(err, agentchat.ErrNotFound) {
		t.Fatalf("unknown conversation: %v", err)
	}

	turns, more, err := st.Turns(ctx, "o", "s1", "atask_3", 1)
	if err != nil || !more || len(turns) != 1 || turns[0].TaskID != "atask_2" || turns[0].Input != "And staging?" ||
		turns[0].Status != "completed" || turns[0].EventSeq != 4 || !turns[0].CreatedAt.Equal(at(2)) {
		t.Fatalf("turns before atask_3: %+v, more %v, %v", turns, more, err)
	}
	turns, more, err = st.Turns(ctx, "o", "s1", "", 10)
	if err != nil || more || len(turns) != 3 || turns[0].TaskID != "atask_3" || turns[2].TaskID != "atask_1" {
		t.Fatalf("every turn: %+v, more %v, %v", turns, more, err)
	}
	if turns, _, err = st.Turns(ctx, "o2", "s1", "", 10); err != nil || len(turns) != 0 {
		t.Fatalf("o2 reads o's turns: %+v, %v", turns, err)
	}
	if turns, _, err = st.Turns(ctx, "o", "s1", "atask_6", 10); err != nil || len(turns) != 0 {
		t.Fatalf("before an investigation: %+v, %v", turns, err)
	}

	// The investigation's session alone is no conversation.
	insert(t, pg, task{"atask_8", "o", "", "investigation", "s7", "completed", "", "look", at(40)})
	if _, err = st.Conversation(ctx, "o", "s7"); !errors.Is(err, agentchat.ErrNotFound) {
		t.Fatalf("investigation as a conversation: %v", err)
	}
}

// The person's title wins over the first message's, pinning keeps when it
// was first pinned, a turn's page context reads back, and a deleted
// conversation is gone from every read while its tasks stay.
func TestConversationStoreChanges(t *testing.T) {
	ctx := context.Background()
	pg := openDB(t)
	st := agentchat.NewPgStore(pg)
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	at := func(m int) time.Time { return base.Add(time.Duration(m) * time.Minute) }
	for _, tk := range []task{
		{"atask_1", "o", "", "chat", "s1", "completed", "usr_ada", "What is deployed?", at(1)},
		{"atask_2", "o", "", "chat", "s1", "completed", "usr_ada", "And staging?", at(2)},
		{"atask_3", "o", "", "chat", "s2", "completed", "usr_ada", "Hello", at(3)},
	} {
		insert(t, pg, tk)
	}
	if _, err := pg.Exec(ctx, `UPDATE loomplane_agent_tasks SET page_context = '{"path": "/projects/proj_1", "label": "Project api", "kind": "project"}'
		WHERE id = 'atask_2'`); err != nil {
		t.Fatal(err)
	}

	title := "Deploy check"
	pin := true
	if err := st.Update(ctx, "o", "s1", agentchat.Change{Title: &title, Pinned: &pin, At: at(10)}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Conversation(ctx, "o", "s1")
	if err != nil || got.Title != "Deploy check" || !got.Pinned || got.PinnedAt == nil || !got.PinnedAt.Equal(at(10)) ||
		!got.UpdatedAt.Equal(at(2)) {
		t.Fatalf("after the rename %+v, %v", got, err)
	}
	// Pinning again keeps the first time; a change without a title keeps it.
	if err = st.Update(ctx, "o", "s1", agentchat.Change{Pinned: &pin, At: at(11)}); err != nil {
		t.Fatal(err)
	}
	if got, err = st.Conversation(ctx, "o", "s1"); err != nil || !got.PinnedAt.Equal(at(10)) || got.Title != "Deploy check" {
		t.Fatalf("pinned again %+v, %v", got, err)
	}
	unpin := false
	if err = st.Update(ctx, "o", "s1", agentchat.Change{Pinned: &unpin, At: at(12)}); err != nil {
		t.Fatal(err)
	}
	list, err := st.List(ctx, "o", "usr_ada", agentchat.ListFilter{Limit: 30})
	if err != nil || len(list) != 2 || list[0].ID != "s2" || list[1].ID != "s1" || list[1].Title != "Deploy check" ||
		list[1].Pinned || list[1].PinnedAt != nil || list[0].Title != "Hello" || list[0].Pinned {
		t.Fatalf("list %+v, %v", list, err)
	}
	// The other workspace's same session ID is untouched.
	if err = st.Update(ctx, "o2", "s1", agentchat.Change{Title: &title, At: at(13)}); err != nil {
		t.Fatal(err)
	}

	turns, _, err := st.Turns(ctx, "o", "s1", "", 10)
	if err != nil || len(turns) != 2 || turns[1].Context != nil ||
		!strings.Contains(string(turns[0].Context), `"label": "Project api"`) {
		t.Fatalf("turns %+v, %v", turns, err)
	}

	if err = st.Delete(ctx, "o", "s1", at(20)); err != nil {
		t.Fatal(err)
	}
	if _, err = st.Conversation(ctx, "o", "s1"); !errors.Is(err, agentchat.ErrNotFound) {
		t.Fatalf("a deleted conversation: %v", err)
	}
	if list, err = st.List(ctx, "o", "", agentchat.ListFilter{Limit: 30}); err != nil || len(list) != 1 || list[0].ID != "s2" {
		t.Fatalf("list after the delete %+v, %v", list, err)
	}
	if turns, _, err = st.Turns(ctx, "o", "s1", "", 10); err != nil || len(turns) != 0 {
		t.Fatalf("a deleted conversation's turns %+v, %v", turns, err)
	}
	// A change after the delete changes nothing, and deleting again keeps
	// when it was deleted.
	if err = st.Update(ctx, "o", "s1", agentchat.Change{Pinned: &pin, At: at(21)}); err != nil {
		t.Fatal(err)
	}
	if err = st.Delete(ctx, "o", "s1", at(22)); err != nil {
		t.Fatal(err)
	}
	var deleted time.Time
	var pinnedAt *time.Time
	if err = pg.QueryRow(ctx, `SELECT deleted_at, pinned_at FROM loomplane_agent_conversations WHERE org_id = 'o' AND session_id = 's1'`).
		Scan(&deleted, &pinnedAt); err != nil || !deleted.Equal(at(20)) || pinnedAt != nil {
		t.Fatalf("deleted at %v, pinned at %v, %v", deleted, pinnedAt, err)
	}
	var tasks int
	if err = pg.QueryRow(ctx, `SELECT count(*) FROM loomplane_agent_tasks WHERE session_id = 's1'`).Scan(&tasks); err != nil || tasks != 2 {
		t.Fatalf("%d tasks left, %v", tasks, err)
	}
}

// The migration's partial indexes exist, and running every migration
// again changes nothing.
func TestConversationIndexesExist(t *testing.T) {
	ctx := context.Background()
	pg := openDB(t)
	exec, err := migrate.NewExecutorFor(pg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = migrate.NewOrchestrator(exec, migrations.All()...).Migrate(ctx); err != nil {
		t.Fatalf("migrate again: %v", err)
	}
	for _, name := range []string{"loomplane_agent_tasks_chat_session", "loomplane_agent_tasks_chat_person"} {
		var def string
		err = pg.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE tablename = 'loomplane_agent_tasks' AND indexname = $1`, name).
			Scan(&def)
		if err != nil {
			t.Fatalf("index %s: %v", name, err)
		}
		if want := "WHERE (kind = 'chat'::text)"; !strings.Contains(def, want) {
			t.Fatalf("index %s is %q, want a partial index %s", name, def, want)
		}
	}
}

// sqlRuns starts a turn by writing its task straight to the table, as
// agentruns does, after a pause long enough for a second send to read the
// conversation in between.
type sqlRuns struct {
	pg     *pgdriver.PgDB
	n      atomic.Int64
	starts atomic.Int64
}

func (r *sqlRuns) Start(ctx context.Context, in agentruns.StartInput) (*agentruns.Task, error) {
	r.starts.Add(1)
	time.Sleep(100 * time.Millisecond)
	id := fmt.Sprintf("atask_send_%d", r.n.Add(1))
	now := time.Now().UTC()
	_, err := r.pg.Exec(ctx, `INSERT INTO loomplane_agent_tasks (id, org_id, project_id, kind, run_id,
			session_id, status, requested_by, principal, input, max_tokens, created_at, updated_at)
		VALUES ($1, $2, $3, 'chat', $4, $5, 'queued', $6, '{}', $7, 1000, $8, $8)`,
		id, in.OrgID, in.ProjectID, "arun_"+id, in.SessionID, in.OnBehalfOf.UserID, in.Input, now)
	if err != nil {
		return nil, err
	}
	return &agentruns.Task{ID: id, OrgID: in.OrgID, SessionID: in.SessionID, Status: agentruns.StatusQueued,
		Kind: agentruns.KindChat, CreatedAt: now, UpdatedAt: now}, nil
}

func (*sqlRuns) Cancel(context.Context, security.Principal, string) error { return nil }

func (*sqlRuns) Events(context.Context, security.Principal, string, int64, int) ([]agentruns.Event, error) {
	return nil, nil
}

// Two sends at once to an idle conversation, each through its own
// Service as two replicas would be, start one turn: the advisory lock
// makes the second read the first's task.
func TestConcurrentSendsStartOneOverPostgres(t *testing.T) {
	pg := openDB(t)
	insert(t, pg, task{"atask_1", "o", "", "chat", "s1", "completed", "usr_ada", "hello", time.Now().UTC().Add(-time.Minute)})
	runs := &sqlRuns{pg: pg}
	ada := security.Principal{Kind: security.KindUser, UserID: "usr_ada", OrgID: "o"}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		svc := agentchat.NewService(agentchat.Deps{Store: agentchat.NewPgStore(pg), Runs: runs, TryLock: pglock.TryFor(pg)})
		wg.Go(func() {
			_, errs[i] = svc.Send(context.Background(), ada, "s1", agentchat.SendInput{Message: fmt.Sprintf("go %d", i)})
		})
	}
	wg.Wait()
	sent, busy := 0, 0
	for i, err := range errs {
		switch {
		case err == nil:
			sent++
		case errors.Is(err, agentchat.ErrBusy):
			busy++
		default:
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if sent != 1 || busy != 1 || runs.starts.Load() != 1 {
		t.Fatalf("%d sent, %d busy, %d started", sent, busy, runs.starts.Load())
	}
	c, err := agentchat.NewPgStore(pg).Conversation(context.Background(), "o", "s1")
	if err != nil || c.Turns != 2 || c.Status != agentruns.StatusQueued {
		t.Fatalf("conversation %+v, %v", c, err)
	}
}

// A burst of sends across many conversations, more than the pool has
// connections, all get their turn: only a few hold the lock, and its
// connection, at once.
func TestASendBurstLeavesThePoolWorking(t *testing.T) {
	const pool, n = 6, 20
	pg := openDB(t, driver.WithPoolSize(pool))
	start := time.Now().UTC().Add(-time.Minute)
	for i := range n {
		insert(t, pg, task{fmt.Sprintf("atask_first_%d", i), "o", "", "chat", fmt.Sprintf("s%d", i), "completed",
			"usr_ada", "hello", start})
	}
	runs := &sqlRuns{pg: pg}
	svc := agentchat.NewService(agentchat.Deps{Store: agentchat.NewPgStore(pg), Runs: runs, TryLock: pglock.TryFor(pg)})
	ada := security.Principal{Kind: security.KindUser, UserID: "usr_ada", OrgID: "o"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			_, errs[i] = svc.Send(ctx, ada, fmt.Sprintf("s%d", i), agentchat.SendInput{Message: "next"})
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if got := runs.starts.Load(); got != n {
		t.Fatalf("started %d turns, want %d", got, n)
	}
}
