package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var _ AppStore = (*PgStore)(nil)

const appColumns = `cloud, client_id, slug, updated_by, created_at, updated_at`

func scanApp(row pgx.Row) (*StoredApp, error) {
	var a StoredApp
	if err := row.Scan(&a.Cloud, &a.ClientID, &a.Slug, &a.UpdatedBy, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	a.CreatedAt, a.UpdatedAt = a.CreatedAt.UTC(), a.UpdatedAt.UTC()
	return &a, nil
}

// GetApp implements AppStore.
func (s *PgStore) GetApp(ctx context.Context, cloud string) (*StoredApp, error) {
	a, err := scanApp(s.db.QueryRow(ctx, `SELECT `+appColumns+` FROM loomplane_platform_oauth_apps WHERE cloud = $1`, cloud))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get sign-in app: %w", err)
	}
	return a, nil
}

// PutApp implements AppStore. It keeps the first created_at. Saving an app
// leaves any problem on the row for the caller to clear.
func (s *PgStore) PutApp(ctx context.Context, a *StoredApp) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_platform_oauth_apps (`+appColumns+`) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (cloud) DO UPDATE SET client_id = EXCLUDED.client_id, slug = EXCLUDED.slug, updated_by = EXCLUDED.updated_by, updated_at = EXCLUDED.updated_at`,
		a.Cloud, a.ClientID, a.Slug, a.UpdatedBy, a.CreatedAt, a.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save sign-in app: %w", err)
	}
	return nil
}

// DeleteApp implements AppStore.
func (s *PgStore) DeleteApp(ctx context.Context, cloud string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM loomplane_platform_oauth_apps WHERE cloud = $1`, cloud); err != nil {
		return fmt.Errorf("delete sign-in app: %w", err)
	}
	return nil
}

// ListApps implements AppStore.
func (s *PgStore) ListApps(ctx context.Context) ([]*StoredApp, error) {
	rows, err := s.db.Query(ctx, `SELECT `+appColumns+` FROM loomplane_platform_oauth_apps ORDER BY cloud`)
	if err != nil {
		return nil, fmt.Errorf("list sign-in apps: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*StoredApp{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, fmt.Errorf("scan sign-in app: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sign-in apps: %w", err)
	}
	return out, nil
}

// PutSignIn implements AppStore.
func (s *PgStore) PutSignIn(ctx context.Context, p *PendingSignIn) error {
	if p.Settings == nil {
		p.Settings = map[string]string{}
	}
	settings, err := json.Marshal(p.Settings)
	if err != nil {
		return fmt.Errorf("encode sign-in settings: %w", err)
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_oauth_sign_ins (state, org_id, user_id, cloud, name, connection_id, settings, verifier, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		p.State, p.OrgID, p.UserID, p.Cloud, p.Name, p.ConnectionID, settings, p.Verifier, p.ExpiresAt, p.CreatedAt)
	if err != nil {
		return fmt.Errorf("save pending sign-in: %w", err)
	}
	return nil
}

// TakeSignIn implements AppStore with one DELETE, so two callbacks with the
// same state can't both win.
func (s *PgStore) TakeSignIn(ctx context.Context, state string) (*PendingSignIn, error) {
	var p PendingSignIn
	var settings []byte
	err := s.db.QueryRow(ctx, `DELETE FROM loomplane_oauth_sign_ins WHERE state = $1
		RETURNING state, org_id, user_id, cloud, name, connection_id, settings, verifier, expires_at, created_at`, state).
		Scan(&p.State, &p.OrgID, &p.UserID, &p.Cloud, &p.Name, &p.ConnectionID, &settings, &p.Verifier, &p.ExpiresAt, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("take pending sign-in: %w", err)
	}
	if err = json.Unmarshal(settings, &p.Settings); err != nil {
		return nil, fmt.Errorf("decode sign-in settings: %w", err)
	}
	p.ExpiresAt, p.CreatedAt = p.ExpiresAt.UTC(), p.CreatedAt.UTC()
	return &p, nil
}

// SweepSignIns implements AppStore.
func (s *PgStore) SweepSignIns(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM loomplane_oauth_sign_ins WHERE expires_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("sweep pending sign-ins: %w", err)
	}
	return tag.RowsAffected()
}

// SetAppProblem implements AppStore. A cloud with no app row updates
// nothing.
func (s *PgStore) SetAppProblem(ctx context.Context, cloud, message string, at time.Time) error {
	if _, err := s.db.Exec(ctx, `UPDATE loomplane_platform_oauth_apps SET problem = $2, problem_at = $3 WHERE cloud = $1`, cloud, message, at); err != nil {
		return fmt.Errorf("save sign-in app problem: %w", err)
	}
	return nil
}

// ClearAppProblem implements AppStore.
func (s *PgStore) ClearAppProblem(ctx context.Context, cloud string) error {
	if _, err := s.db.Exec(ctx, `UPDATE loomplane_platform_oauth_apps SET problem = '', problem_at = NULL WHERE cloud = $1 AND problem <> ''`, cloud); err != nil {
		return fmt.Errorf("clear sign-in app problem: %w", err)
	}
	return nil
}

// AppProblems implements AppStore.
func (s *PgStore) AppProblems(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.Query(ctx, `SELECT cloud, problem FROM loomplane_platform_oauth_apps WHERE problem <> ''`)
	if err != nil {
		return nil, fmt.Errorf("list sign-in app problems: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := map[string]string{}
	for rows.Next() {
		var cloud, message string
		if err := rows.Scan(&cloud, &message); err != nil {
			return nil, fmt.Errorf("scan sign-in app problem: %w", err)
		}
		out[cloud] = message
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sign-in app problems: %w", err)
	}
	return out, nil
}
