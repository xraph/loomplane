//go:build integration

package integration

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
	pgmigrate "github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/repositories"
)

const gitlabMigration = "20261210000100"

// gitlabStore migrates a fresh database and returns the store over it, the
// pool, and a plain connection for checking what the store wrote.
func gitlabStore(t *testing.T) (*repositories.GitLabPgStore, *pgdriver.PgDB, *pgx.Conn) {
	t.Helper()
	ctx := t.Context()
	dsn := freshDatabase(t)
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	db := pgdriver.New()
	if err := db.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return repositories.NewGitLabPgStore(db), db, conn
}

func tablesPresent(t *testing.T, conn *pgx.Conn) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.tables
		WHERE table_name IN ('loomplane_gitlab_hosts','loomplane_gitlab_sign_ins','loomplane_gitlab_connections')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestGitLabStore_migrationUpAndDown(t *testing.T) {
	_, db, conn := gitlabStore(t)
	ctx := t.Context()
	if n := tablesPresent(t, conn); n != 3 {
		t.Fatalf("%d GitLab tables after the migration, want 3", n)
	}
	m := federationMigration(t, gitlabMigration)
	if err := m.Down(ctx, pgmigrate.New(db)); err != nil {
		t.Fatal(err)
	}
	if n := tablesPresent(t, conn); n != 0 {
		t.Fatalf("%d GitLab tables left after down", n)
	}
	if err := m.Up(ctx, pgmigrate.New(db)); err != nil {
		t.Fatal(err)
	}
	if err := m.Up(ctx, pgmigrate.New(db)); err != nil {
		t.Fatal("the migration cannot replay: ", err)
	}
	if n := tablesPresent(t, conn); n != 3 {
		t.Fatalf("%d GitLab tables after up again, want 3", n)
	}
}

func TestGitLabStore_signInIsSpentOnce(t *testing.T) {
	s, _, _ := gitlabStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	in := &repositories.GitLabSignIn{
		State: "state-1", OrgID: "org-a", UserID: "usr-1", Host: "gitlab.com", Verifier: "v-1",
		ExpiresAt: now.Add(10 * time.Minute), CreatedAt: now,
	}
	if err := s.SaveSignIn(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.TakeSignIn(ctx, "state-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.OrgID != "org-a" || got.UserID != "usr-1" || got.Host != "gitlab.com" || got.Verifier != "v-1" ||
		!got.ExpiresAt.Equal(in.ExpiresAt) || !got.CreatedAt.Equal(now) {
		t.Fatalf("took %+v, saved %+v", got, in)
	}
	if _, err = s.TakeSignIn(ctx, "state-1", now); !errors.Is(err, repositories.ErrBadState) {
		t.Fatalf("second take = %v, want ErrBadState", err)
	}
	if _, err = s.TakeSignIn(ctx, "never-saved", now); !errors.Is(err, repositories.ErrBadState) {
		t.Fatalf("unknown state = %v, want ErrBadState", err)
	}

	// An expired one is refused, and is gone afterwards.
	in.State = "state-2"
	if err = s.SaveSignIn(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TakeSignIn(ctx, "state-2", in.ExpiresAt.Add(time.Second)); !errors.Is(err, repositories.ErrBadState) {
		t.Fatalf("expired take = %v, want ErrBadState", err)
	}
	if _, err = s.TakeSignIn(ctx, "state-2", now); !errors.Is(err, repositories.ErrBadState) {
		t.Fatalf("take after expiry = %v, want ErrBadState", err)
	}

	// Two takers race for one sign-in: exactly one wins.
	in.State = "state-3"
	if err = s.SaveSignIn(ctx, in); err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, terr := s.TakeSignIn(ctx, "state-3", now); terr == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d takers spent one sign-in", wins.Load())
	}
}

func TestGitLabStore_connectionUpsertKeepsID(t *testing.T) {
	s, _, _ := gitlabStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	c := &repositories.GitLabConnection{
		OrgID: "org-a", Host: "gitlab.com", Kind: "oauth", GitLabUserID: 42, Username: "rex", AvatarURL: "https://a/1.png",
		Version: "v1", CreatedBy: "usr-1", CreatedAt: now,
	}
	id, err := s.SaveConnection(ctx, c)
	if err != nil || id == "" || id[:4] != "glc_" {
		t.Fatalf("SaveConnection = %q, %v; want a glc_ ID", id, err)
	}
	if err = s.MarkLapsed(ctx, "org-a", id, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetConnection(ctx, id)
	if err != nil || got.LapsedAt == nil || !got.LapsedAt.Equal(now) {
		t.Fatalf("lapsed connection %+v, %v", got, err)
	}

	again := *c
	again.ID = ""
	again.Username, again.Version, again.WebhookSet = "rex-renamed", "v2", true
	id2, err := s.SaveConnection(ctx, &again)
	if err != nil || id2 != id {
		t.Fatalf("second save = %q, %v; want the kept ID %q", id2, err, id)
	}
	got, err = s.GetConnection(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.LapsedAt != nil || got.Username != "rex-renamed" || got.Version != "v2" || !got.WebhookSet ||
		got.GitLabUserID != 42 || got.Kind != "oauth" || got.CreatedBy != "usr-1" || !got.CreatedAt.Equal(now) {
		t.Fatalf("upserted %+v", got)
	}

	// Another GitLab user, another host and another organization each get
	// their own connection.
	other := *c
	other.ID, other.GitLabUserID = "", 43
	id3, err := s.SaveConnection(ctx, &other)
	if err != nil || id3 == id {
		t.Fatalf("another user = %q, %v", id3, err)
	}
	other.ID, other.Host = "", "git.corp.example"
	id4, err := s.SaveConnection(ctx, &other)
	if err != nil || id4 == id3 {
		t.Fatalf("another host = %q, %v", id4, err)
	}
	other.ID, other.OrgID = "", "org-b"
	if _, err = s.SaveConnection(ctx, &other); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListConnections(ctx, "org-a")
	if err != nil || len(list) != 3 {
		t.Fatalf("org-a's connections %d, %v", len(list), err)
	}
	if err = s.DeleteConnection(ctx, "org-b", id); !errors.Is(err, repositories.ErrGitLabConnectionNotFound) {
		t.Fatalf("deleting another organization's connection = %v", err)
	}
	if err = s.DeleteConnection(ctx, "org-a", id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetConnection(ctx, id); !errors.Is(err, repositories.ErrGitLabConnectionNotFound) {
		t.Fatalf("get after delete = %v", err)
	}
	if err = s.MarkLapsed(ctx, "org-a", id, now); !errors.Is(err, repositories.ErrGitLabConnectionNotFound) {
		t.Fatalf("lapse after delete = %v", err)
	}
}

func TestGitLabStore_hostInUseCantBeDeleted(t *testing.T) {
	s, _, _ := gitlabStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	h := &repositories.GitLabHost{
		OrgID: "org-a", WebURL: "https://git.a.com", Host: "git.a.com", ClientID: "client", CreatedBy: "usr-1", CreatedAt: now,
	}
	if err := s.SaveHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	if h.ID == "" || h.ID[:4] != "glh_" {
		t.Fatalf("SaveHost left the ID %q, want a glh_ one", h.ID)
	}
	dup := *h
	dup.ID = ""
	if err := s.SaveHost(ctx, &dup); !errors.Is(err, repositories.ErrGitLabHostExists) {
		t.Fatalf("saving the host twice = %v, want ErrGitLabHostExists", err)
	}
	// The same host for another organization is a different registration.
	other := *h
	other.ID, other.OrgID = "", "org-b"
	if err := s.SaveHost(ctx, &other); err != nil {
		t.Fatal(err)
	}

	named, err := s.HostsNamed(ctx, "git.a.com")
	if err != nil || len(named) != 2 {
		t.Fatalf("HostsNamed = %d, %v", len(named), err)
	}
	mine, err := s.ListHosts(ctx, "org-a")
	if err != nil || len(mine) != 1 || mine[0].ID != h.ID || mine[0].ClientID != "client" || mine[0].Instance {
		t.Fatalf("ListHosts = %+v, %v", mine, err)
	}

	c := &repositories.GitLabConnection{
		OrgID: "org-a", Host: "git.a.com", HostID: h.ID, Kind: "oauth", GitLabUserID: 7, Username: "rex", CreatedBy: "usr-1", CreatedAt: now,
	}
	cid, err := s.SaveConnection(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteHost(ctx, "org-a", h.ID); !errors.Is(err, repositories.ErrGitLabHostInUse) {
		t.Fatalf("deleting a host in use = %v, want ErrGitLabHostInUse", err)
	}
	if err = s.DeleteHost(ctx, "org-b", h.ID); !errors.Is(err, repositories.ErrGitLabHostNotFound) {
		t.Fatalf("deleting another organization's host = %v, want ErrGitLabHostNotFound", err)
	}
	if err = s.DeleteConnection(ctx, "org-a", cid); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteHost(ctx, "org-a", h.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteHost(ctx, "org-a", h.ID); !errors.Is(err, repositories.ErrGitLabHostNotFound) {
		t.Fatalf("deleting it again = %v, want ErrGitLabHostNotFound", err)
	}
}

func TestGitLabStore_grantLockSerializes(t *testing.T) {
	s, _, _ := gitlabStore(t)
	ctx := t.Context()
	var inside, overlaps, runs atomic.Int32
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.WithGrantLock(ctx, "org-a", "glc_1", func() error {
				if inside.Add(1) != 1 {
					overlaps.Add(1)
				}
				time.Sleep(50 * time.Millisecond)
				inside.Add(-1)
				runs.Add(1)
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if overlaps.Load() != 0 || runs.Load() != 4 {
		t.Fatalf("%d overlaps in %d runs", overlaps.Load(), runs.Load())
	}

	// A different connection has a lock of its own, and an error from fn
	// comes back and releases the lock.
	boom := errors.New("boom")
	if err := s.WithGrantLock(ctx, "org-a", "glc_2", func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("WithGrantLock = %v, want fn's error", err)
	}
	if err := s.WithGrantLock(ctx, "org-a", "glc_2", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
