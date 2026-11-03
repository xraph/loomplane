package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/drivers/pgdriver"
	"go.jetify.com/typeid/v2"
)

const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
)

// GitLabPgStore is the Postgres GitLabStore. The schema is migration group
// "loomplane" (database/migrations).
type GitLabPgStore struct {
	db *pgdriver.PgDB
}

// NewGitLabPgStore returns a GitLabStore over db.
func NewGitLabPgStore(db *pgdriver.PgDB) *GitLabPgStore { return &GitLabPgStore{db: db} }

var _ GitLabStore = (*GitLabPgStore)(nil)

func newGitLabID(prefix string) (string, error) {
	id, err := typeid.Generate(prefix)
	if err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return id.String(), nil
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// SaveHost implements GitLabStore.
func (s *GitLabPgStore) SaveHost(ctx context.Context, h *GitLabHost) error {
	if h.ID == "" {
		id, err := newGitLabID("glh")
		if err != nil {
			return err
		}
		h.ID = id
	}
	h.Host = canonicalGitLabHost(h.Host)
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_gitlab_hosts
		(id, org_id, web_url, host, client_id, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		h.ID, h.OrgID, h.WebURL, h.Host, h.ClientID, h.CreatedBy, h.CreatedAt)
	if pgCode(err) == pgUniqueViolation {
		return ErrGitLabHostExists
	}
	if err != nil {
		return fmt.Errorf("save GitLab host: %w", err)
	}
	return nil
}

const gitLabHostColumns = `id, org_id, web_url, host, client_id, created_by, created_at`

func (s *GitLabPgStore) queryHosts(ctx context.Context, where string, arg any) ([]*GitLabHost, error) {
	rows, err := s.db.Query(ctx, `SELECT `+gitLabHostColumns+` FROM loomplane_gitlab_hosts WHERE `+where+` ORDER BY created_at, id`, arg)
	if err != nil {
		return nil, fmt.Errorf("list GitLab hosts: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*GitLabHost{}
	for rows.Next() {
		var h GitLabHost
		if err := rows.Scan(&h.ID, &h.OrgID, &h.WebURL, &h.Host, &h.ClientID, &h.CreatedBy, &h.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan GitLab host: %w", err)
		}
		h.CreatedAt = h.CreatedAt.UTC()
		out = append(out, &h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list GitLab hosts: %w", err)
	}
	return out, nil
}

// ListHosts implements GitLabStore.
func (s *GitLabPgStore) ListHosts(ctx context.Context, orgID string) ([]*GitLabHost, error) {
	return s.queryHosts(ctx, `org_id = $1`, orgID)
}

// HostsNamed implements GitLabStore.
func (s *GitLabPgStore) HostsNamed(ctx context.Context, host string) ([]*GitLabHost, error) {
	return s.queryHosts(ctx, `host = $1`, canonicalGitLabHost(host))
}

// DeleteHost implements GitLabStore. The connections' foreign key refuses
// the delete while any uses the host, so nothing can slip in between a check
// and the delete.
func (s *GitLabPgStore) DeleteHost(ctx context.Context, orgID, id string) error {
	res, err := s.db.Exec(ctx, `DELETE FROM loomplane_gitlab_hosts WHERE org_id = $1 AND id = $2`, orgID, id)
	if pgCode(err) == pgForeignKeyViolation {
		return ErrGitLabHostInUse
	}
	if err != nil {
		return fmt.Errorf("delete GitLab host: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrGitLabHostNotFound
	}
	return nil
}

// SaveSignIn implements GitLabStore.
func (s *GitLabPgStore) SaveSignIn(ctx context.Context, in *GitLabSignIn) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_gitlab_sign_ins
		(state, org_id, user_id, host, verifier, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		in.State, in.OrgID, in.UserID, canonicalGitLabHost(in.Host), in.Verifier, in.ExpiresAt, in.CreatedAt)
	if err != nil {
		return fmt.Errorf("save GitLab sign-in: %w", err)
	}
	return nil
}

// TakeSignIn implements GitLabStore. The delete is the take: a state
// presented twice finds nothing the second time, and an expired one is
// deleted without being returned.
func (s *GitLabPgStore) TakeSignIn(ctx context.Context, state string, now time.Time) (*GitLabSignIn, error) {
	var in GitLabSignIn
	err := s.db.QueryRow(ctx, `DELETE FROM loomplane_gitlab_sign_ins WHERE state = $1
		RETURNING state, org_id, user_id, host, verifier, expires_at, created_at`, state).
		Scan(&in.State, &in.OrgID, &in.UserID, &in.Host, &in.Verifier, &in.ExpiresAt, &in.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBadState
	}
	if err != nil {
		return nil, fmt.Errorf("take GitLab sign-in: %w", err)
	}
	in.ExpiresAt, in.CreatedAt = in.ExpiresAt.UTC(), in.CreatedAt.UTC()
	if !now.Before(in.ExpiresAt) {
		return nil, ErrBadState
	}
	return &in, nil
}

// SaveConnection implements GitLabStore. What a reconnect refreshes is the
// account's name, picture and GitLab release, and it clears a lapse. Who
// first connected it, when, and whether its webhook is set stay: signing in
// again leaves the hook on GitLab and its secret in Vault. A new connection
// has no webhook.
func (s *GitLabPgStore) SaveConnection(ctx context.Context, c *GitLabConnection) (string, error) {
	id := c.ID
	if id == "" {
		var err error
		if id, err = newGitLabID("glc"); err != nil {
			return "", err
		}
	}
	kind := c.Kind
	if kind == "" {
		kind = GitLabKindOAuth
	}
	var hostID *string
	if c.HostID != "" {
		hostID = &c.HostID
	}
	var kept string
	err := s.db.QueryRow(ctx, `INSERT INTO loomplane_gitlab_connections
		(id, org_id, host, host_id, kind, gitlab_user_id, username, avatar_url, version, created_by, created_at, lapsed_at, webhook_set)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NULL, FALSE)
		ON CONFLICT (org_id, host, gitlab_user_id) DO UPDATE SET
			host_id = EXCLUDED.host_id, kind = EXCLUDED.kind, username = EXCLUDED.username,
			avatar_url = EXCLUDED.avatar_url, version = EXCLUDED.version, lapsed_at = NULL
		RETURNING id`,
		id, c.OrgID, canonicalGitLabHost(c.Host), hostID, kind, c.GitLabUserID, c.Username, c.AvatarURL, c.Version,
		c.CreatedBy, c.CreatedAt).Scan(&kept)
	if err != nil {
		return "", fmt.Errorf("save GitLab connection: %w", err)
	}
	return kept, nil
}

const gitLabConnectionColumns = `id, org_id, host, host_id, kind, gitlab_user_id, username, avatar_url, version,
	created_by, created_at, lapsed_at, webhook_set`

func scanGitLabConnection(row pgx.Row) (*GitLabConnection, error) {
	var c GitLabConnection
	var hostID *string
	if err := row.Scan(&c.ID, &c.OrgID, &c.Host, &hostID, &c.Kind, &c.GitLabUserID, &c.Username, &c.AvatarURL,
		&c.Version, &c.CreatedBy, &c.CreatedAt, &c.LapsedAt, &c.WebhookSet); err != nil {
		return nil, err
	}
	if hostID != nil {
		c.HostID = *hostID
	}
	c.CreatedAt = c.CreatedAt.UTC()
	if c.LapsedAt != nil {
		at := c.LapsedAt.UTC()
		c.LapsedAt = &at
	}
	return &c, nil
}

// ListConnections implements GitLabStore.
func (s *GitLabPgStore) ListConnections(ctx context.Context, orgID string) ([]*GitLabConnection, error) {
	rows, err := s.db.Query(ctx, `SELECT `+gitLabConnectionColumns+` FROM loomplane_gitlab_connections
		WHERE org_id = $1 ORDER BY created_at, id`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list GitLab connections: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*GitLabConnection{}
	for rows.Next() {
		c, err := scanGitLabConnection(rows)
		if err != nil {
			return nil, fmt.Errorf("scan GitLab connection: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list GitLab connections: %w", err)
	}
	return out, nil
}

// GetConnection implements GitLabStore.
func (s *GitLabPgStore) GetConnection(ctx context.Context, id string) (*GitLabConnection, error) {
	c, err := scanGitLabConnection(s.db.QueryRow(ctx, `SELECT `+gitLabConnectionColumns+`
		FROM loomplane_gitlab_connections WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrGitLabConnectionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get GitLab connection: %w", err)
	}
	return c, nil
}

// MarkLapsed implements GitLabStore.
func (s *GitLabPgStore) MarkLapsed(ctx context.Context, orgID, id string, at time.Time) error {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_gitlab_connections SET lapsed_at = $3 WHERE org_id = $1 AND id = $2`, orgID, id, at)
	if err != nil {
		return fmt.Errorf("mark GitLab connection lapsed: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrGitLabConnectionNotFound
	}
	return nil
}

// SetWebhookSet implements GitLabStore.
func (s *GitLabPgStore) SetWebhookSet(ctx context.Context, orgID, id string) error {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_gitlab_connections SET webhook_set = TRUE WHERE org_id = $1 AND id = $2`, orgID, id)
	if err != nil {
		return fmt.Errorf("mark GitLab connection's webhook set: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrGitLabConnectionNotFound
	}
	return nil
}

// DeleteConnection implements GitLabStore.
func (s *GitLabPgStore) DeleteConnection(ctx context.Context, orgID, id string) error {
	res, err := s.db.Exec(ctx, `DELETE FROM loomplane_gitlab_connections WHERE org_id = $1 AND id = $2`, orgID, id)
	if err != nil {
		return fmt.Errorf("delete GitLab connection: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrGitLabConnectionNotFound
	}
	return nil
}

// WithGrantLock implements GitLabStore. The lock lasts until fn returns,
// across every Loomplane process.
func (s *GitLabPgStore) WithGrantLock(ctx context.Context, orgID, id string, fn func() error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("release GitLab grant lock: %w", rollbackErr))
		}
	}()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "gitlab-grant/"+orgID+"/"+id); err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	return tx.Commit()
}
