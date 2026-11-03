package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.jetify.com/typeid/v2"
)

// ErrWorkspaceNotFound means the organization hasn't connected that
// Bitbucket workspace.
var ErrWorkspaceNotFound = errors.New("that Bitbucket workspace isn't connected to this organization")

// ErrHookNotFound means there's no push webhook for that repository.
var ErrHookNotFound = errors.New("there's no push webhook for that Bitbucket repository")

// ErrHookExists means the organization already has a push webhook for the
// repository.
var ErrHookExists = errors.New("this organization already has a push webhook for that Bitbucket repository")

// A workspace connection is ready, or needs someone to connect it again
// because Bitbucket stopped honouring its sign-in.
const (
	BitbucketReady           = "ready"
	BitbucketReconnectNeeded = "reconnect_needed"
)

// A push webhook is on while Loomplane keeps it, and off when it couldn't
// be created or has been removed, with the reason beside it.
const (
	HookOn  = "on"
	HookOff = "off"
)

// BitbucketConnection is a Bitbucket workspace an organization has
// connected. It holds no token: the refresh token is in Vault.
type BitbucketConnection struct {
	ID    string
	OrgID string
	// WorkspaceUUID is Bitbucket's stable ID for the workspace, braces
	// included. The slug can change.
	WorkspaceUUID string
	WorkspaceSlug string
	WorkspaceName string
	AvatarURL     string
	// BitbucketUserUUID and BitbucketUserName are the account that signed in.
	BitbucketUserUUID string
	BitbucketUserName string
	Status            string
	// Message says why the status is reconnect_needed. It's empty when ready.
	Message string
	// CreatedBy is the Loomplane member whose sign-in is stored. Whoever
	// reconnects takes it over, so it stays a member who can still be asked.
	CreatedBy string
	// GrantGeneration goes up each time the row is saved again, such as by a
	// reconnect. An access token cached under an older generation came from
	// a sign-in the row no longer holds. The store sets it.
	GrantGeneration int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// BitbucketHook is the push webhook on one repository.
type BitbucketHook struct {
	HookID string
	OrgID  string
	// ConnectionID is the BitbucketConnection whose sign-in created the hook.
	ConnectionID string
	// Repository is the key sources use, such as bitbucket.org/acme/api.
	Repository string
	// BitbucketHookUUID is empty when the hook is off.
	BitbucketHookUUID string
	Status            string
	// Reason says why the hook is off.
	Reason    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// HookStore keeps the push webhooks, one per organization and repository.
type HookStore interface {
	// SaveBitbucketHook returns ErrHookExists when the organization already
	// has a hook for the repository, whatever its case.
	SaveBitbucketHook(ctx context.Context, h *BitbucketHook) error
	// GetBitbucketHook returns ErrHookNotFound for an unknown ID.
	GetBitbucketHook(ctx context.Context, hookID string) (*BitbucketHook, error)
	// UpdateBitbucketHook changes a hook's connection, Bitbucket UUID, status,
	// reason and update time in place, such as turning an off hook on. It
	// returns ErrHookNotFound for an unknown ID.
	UpdateBitbucketHook(ctx context.Context, h *BitbucketHook) error
	// UpdateBitbucketHookIfOff is UpdateBitbucketHook only while the row
	// holds no Bitbucket hook, so of two tries to turn one hook on, one
	// wins. It reports false when the row is gone or another try got
	// there first.
	UpdateBitbucketHookIfOff(ctx context.Context, h *BitbucketHook) (bool, error)
	// BitbucketHookFor returns ErrHookNotFound when the repository has none.
	BitbucketHookFor(ctx context.Context, orgID, repository string) (*BitbucketHook, error)
	BitbucketHooksIn(ctx context.Context, orgID, connectionID string) ([]*BitbucketHook, error)
	// DeleteBitbucketHook returns ErrHookNotFound when there was nothing to
	// delete.
	DeleteBitbucketHook(ctx context.Context, hookID string) error
}

// BitbucketStore persists connected workspaces and their webhooks, per
// organization. *PgStore is the real one.
type BitbucketStore interface {
	HookStore
	// SaveBitbucketWorkspace connects a workspace, or refreshes it if the
	// organization already has it, keeping its ID and CreatedAt and taking
	// c.CreatedBy as the member whose sign-in is now stored. A new row's
	// GrantGeneration is 1, and each later save adds one. It sets c.ID,
	// c.CreatedAt and c.GrantGeneration to what the row holds.
	SaveBitbucketWorkspace(ctx context.Context, c *BitbucketConnection) error
	ListBitbucketWorkspaces(ctx context.Context, orgID string) ([]*BitbucketConnection, error)
	// GetBitbucketWorkspace returns ErrWorkspaceNotFound for another
	// organization's workspace too.
	GetBitbucketWorkspace(ctx context.Context, orgID, id string) (*BitbucketConnection, error)
	// DeleteBitbucketWorkspace returns ErrWorkspaceNotFound when there was
	// nothing to delete.
	DeleteBitbucketWorkspace(ctx context.Context, orgID, id string) error
	// SetBitbucketWorkspaceStatus returns ErrWorkspaceNotFound when the
	// organization has no such workspace.
	SetBitbucketWorkspaceStatus(ctx context.Context, orgID, id, status, message string) error
	// WithBitbucketLock runs fn holding the workspace's grant lock, so two
	// token refreshes or reconnects never race over its Vault entry. The
	// lock lasts until fn returns, across every Loomplane process.
	WithBitbucketLock(ctx context.Context, orgID, id string, fn func() error) error
	// BitbucketConnectorIsMember reports whether the user is still an active
	// member of the organization.
	BitbucketConnectorIsMember(ctx context.Context, orgID, userID string) (bool, error)
}

var _ BitbucketStore = (*PgStore)(nil)

const workspaceColumns = `id, org_id, workspace_uuid, workspace_slug, workspace_name, avatar_url,
	bitbucket_user_uuid, bitbucket_user_name, status, message, created_by, grant_generation, created_at, updated_at`

func scanWorkspace(row pgx.Row) (*BitbucketConnection, error) {
	var c BitbucketConnection
	err := row.Scan(&c.ID, &c.OrgID, &c.WorkspaceUUID, &c.WorkspaceSlug, &c.WorkspaceName, &c.AvatarURL,
		&c.BitbucketUserUUID, &c.BitbucketUserName, &c.Status, &c.Message, &c.CreatedBy, &c.GrantGeneration, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
	return &c, nil
}

// SaveBitbucketWorkspace implements BitbucketStore.
func (s *PgStore) SaveBitbucketWorkspace(ctx context.Context, c *BitbucketConnection) error {
	id := c.ID
	if id == "" {
		tid, err := typeid.Generate("bbws")
		if err != nil {
			return fmt.Errorf("generate Bitbucket workspace id: %w", err)
		}
		id = tid.String()
	}
	err := s.db.QueryRow(ctx, `INSERT INTO loomplane_bitbucket_workspaces
		(id, org_id, workspace_uuid, workspace_slug, workspace_name, avatar_url,
		 bitbucket_user_uuid, bitbucket_user_name, status, message, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (org_id, workspace_uuid) DO UPDATE SET
			workspace_slug = EXCLUDED.workspace_slug, workspace_name = EXCLUDED.workspace_name,
			avatar_url = EXCLUDED.avatar_url, bitbucket_user_uuid = EXCLUDED.bitbucket_user_uuid,
			bitbucket_user_name = EXCLUDED.bitbucket_user_name, status = EXCLUDED.status,
			message = EXCLUDED.message, created_by = EXCLUDED.created_by, updated_at = EXCLUDED.updated_at,
			grant_generation = loomplane_bitbucket_workspaces.grant_generation + 1
		RETURNING id, created_at, grant_generation`,
		id, c.OrgID, c.WorkspaceUUID, c.WorkspaceSlug, c.WorkspaceName, c.AvatarURL,
		c.BitbucketUserUUID, c.BitbucketUserName, c.Status, c.Message, c.CreatedBy, c.CreatedAt, c.UpdatedAt,
	).Scan(&c.ID, &c.CreatedAt, &c.GrantGeneration)
	if err != nil {
		return fmt.Errorf("save Bitbucket workspace: %w", err)
	}
	c.CreatedAt = c.CreatedAt.UTC()
	return nil
}

// ListBitbucketWorkspaces returns the organization's workspaces, oldest first.
func (s *PgStore) ListBitbucketWorkspaces(ctx context.Context, orgID string) ([]*BitbucketConnection, error) {
	rows, err := s.db.Query(ctx, `SELECT `+workspaceColumns+` FROM loomplane_bitbucket_workspaces
		WHERE org_id = $1 ORDER BY created_at, id`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list Bitbucket workspaces: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*BitbucketConnection{}
	for rows.Next() {
		c, err := scanWorkspace(rows)
		if err != nil {
			return nil, fmt.Errorf("scan Bitbucket workspace: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Bitbucket workspaces: %w", err)
	}
	return out, nil
}

// GetBitbucketWorkspace implements BitbucketStore.
func (s *PgStore) GetBitbucketWorkspace(ctx context.Context, orgID, id string) (*BitbucketConnection, error) {
	c, err := scanWorkspace(s.db.QueryRow(ctx, `SELECT `+workspaceColumns+` FROM loomplane_bitbucket_workspaces
		WHERE org_id = $1 AND id = $2`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorkspaceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get Bitbucket workspace: %w", err)
	}
	return c, nil
}

// DeleteBitbucketWorkspace implements BitbucketStore.
func (s *PgStore) DeleteBitbucketWorkspace(ctx context.Context, orgID, id string) error {
	res, err := s.db.Exec(ctx, `DELETE FROM loomplane_bitbucket_workspaces WHERE org_id = $1 AND id = $2`, orgID, id)
	if err != nil {
		return fmt.Errorf("delete Bitbucket workspace: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrWorkspaceNotFound
	}
	return nil
}

// SetBitbucketWorkspaceStatus implements BitbucketStore.
func (s *PgStore) SetBitbucketWorkspaceStatus(ctx context.Context, orgID, id, status, message string) error {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_bitbucket_workspaces SET status = $3, message = $4, updated_at = now()
		WHERE org_id = $1 AND id = $2`, orgID, id, status, message)
	if err != nil {
		return fmt.Errorf("set Bitbucket workspace status: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrWorkspaceNotFound
	}
	return nil
}

// WithBitbucketLock implements BitbucketStore.
func (s *PgStore) WithBitbucketLock(ctx context.Context, orgID, id string, fn func() error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("release Bitbucket grant lock: %w", rollbackErr))
		}
	}()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "bitbucket-grant/"+orgID+"/"+id); err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	return tx.Commit()
}

// BitbucketConnectorIsMember implements BitbucketStore.
func (s *PgStore) BitbucketConnectorIsMember(ctx context.Context, orgID, userID string) (bool, error) {
	var member bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM authsome_members m JOIN authsome_users u ON u.id = m.user_id
		WHERE m.org_id = $1 AND m.user_id = $2 AND u.deleted_at IS NULL AND NOT u.banned)`, orgID, userID).Scan(&member)
	if err != nil {
		return false, fmt.Errorf("check organization membership: %w", err)
	}
	return member, nil
}

const hookColumns = `hook_id, org_id, connection_id, repository, bitbucket_hook_uuid, status, reason, created_at, updated_at`

func scanHook(row pgx.Row) (*BitbucketHook, error) {
	var h BitbucketHook
	err := row.Scan(&h.HookID, &h.OrgID, &h.ConnectionID, &h.Repository, &h.BitbucketHookUUID, &h.Status, &h.Reason, &h.CreatedAt, &h.UpdatedAt)
	if err != nil {
		return nil, err
	}
	h.CreatedAt, h.UpdatedAt = h.CreatedAt.UTC(), h.UpdatedAt.UTC()
	return &h, nil
}

// SaveBitbucketHook implements HookStore.
func (s *PgStore) SaveBitbucketHook(ctx context.Context, h *BitbucketHook) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_bitbucket_webhooks (`+hookColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		h.HookID, h.OrgID, h.ConnectionID, h.Repository, h.BitbucketHookUUID, h.Status, h.Reason, h.CreatedAt, h.UpdatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrHookExists
	}
	if err != nil {
		return fmt.Errorf("save Bitbucket webhook: %w", err)
	}
	return nil
}

// GetBitbucketHook implements HookStore.
func (s *PgStore) GetBitbucketHook(ctx context.Context, hookID string) (*BitbucketHook, error) {
	h, err := scanHook(s.db.QueryRow(ctx, `SELECT `+hookColumns+` FROM loomplane_bitbucket_webhooks WHERE hook_id = $1`, hookID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrHookNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get Bitbucket webhook: %w", err)
	}
	return h, nil
}

// UpdateBitbucketHook implements HookStore.
func (s *PgStore) UpdateBitbucketHook(ctx context.Context, h *BitbucketHook) error {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_bitbucket_webhooks
		SET connection_id = $2, bitbucket_hook_uuid = $3, status = $4, reason = $5, updated_at = $6
		WHERE hook_id = $1`, h.HookID, h.ConnectionID, h.BitbucketHookUUID, h.Status, h.Reason, h.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update Bitbucket webhook: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrHookNotFound
	}
	return nil
}

// UpdateBitbucketHookIfOff implements HookStore.
func (s *PgStore) UpdateBitbucketHookIfOff(ctx context.Context, h *BitbucketHook) (bool, error) {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_bitbucket_webhooks
		SET connection_id = $2, bitbucket_hook_uuid = $3, status = $4, reason = $5, updated_at = $6
		WHERE hook_id = $1 AND bitbucket_hook_uuid = ''`, h.HookID, h.ConnectionID, h.BitbucketHookUUID, h.Status, h.Reason, h.UpdatedAt)
	if err != nil {
		return false, fmt.Errorf("update Bitbucket webhook: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	return n == 1, nil
}

// BitbucketHookFor implements HookStore. It compares the repository
// without regard to case, as the unique index does.
func (s *PgStore) BitbucketHookFor(ctx context.Context, orgID, repository string) (*BitbucketHook, error) {
	h, err := scanHook(s.db.QueryRow(ctx, `SELECT `+hookColumns+` FROM loomplane_bitbucket_webhooks
		WHERE org_id = $1 AND lower(repository) = lower($2)`, orgID, repository))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrHookNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find Bitbucket webhook: %w", err)
	}
	return h, nil
}

// BitbucketHooksIn returns the hooks a workspace's sign-in created, oldest first.
func (s *PgStore) BitbucketHooksIn(ctx context.Context, orgID, connectionID string) ([]*BitbucketHook, error) {
	rows, err := s.db.Query(ctx, `SELECT `+hookColumns+` FROM loomplane_bitbucket_webhooks
		WHERE org_id = $1 AND connection_id = $2 ORDER BY created_at, hook_id`, orgID, connectionID)
	if err != nil {
		return nil, fmt.Errorf("list Bitbucket webhooks: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*BitbucketHook{}
	for rows.Next() {
		h, err := scanHook(rows)
		if err != nil {
			return nil, fmt.Errorf("scan Bitbucket webhook: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Bitbucket webhooks: %w", err)
	}
	return out, nil
}

// DeleteBitbucketHook implements HookStore.
func (s *PgStore) DeleteBitbucketHook(ctx context.Context, hookID string) error {
	res, err := s.db.Exec(ctx, `DELETE FROM loomplane_bitbucket_webhooks WHERE hook_id = $1`, hookID)
	if err != nil {
		return fmt.Errorf("delete Bitbucket webhook: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrHookNotFound
	}
	return nil
}
