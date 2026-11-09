package deployments

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	ModeNative   = "native"
	ModeFallback = "fallback"
)

// ProviderEnvironment records where one of our environments deploys on a
// provider connection: the provider's own environment (native) or a shared
// one (fallback).
type ProviderEnvironment struct {
	OrgID, ProjectID, EnvironmentID string
	Provider                        string // "vercel" | "render"
	ConnectionID                    string
	Scope                           string // Vercel: the service (action) name. Render: "".
	Mode                            string // ModeNative | ModeFallback
	Permanent                       bool   // fallback the provider can never lift
	ProjectRef                      string // Vercel project id / Render project id ("" for a fallback)
	EnvironmentRef                  string // Vercel custom environment id, "production" or "preview" / Render environment id
	Reason                          string
	NotifiedAt                      *time.Time
	CreatedAt, UpdatedAt            time.Time
}

// ProviderEnvironmentStore keeps the per-environment native or fallback record.
type ProviderEnvironmentStore interface {
	ProviderEnvironment(ctx context.Context, orgID, environmentID, provider, connectionID, scope string) (*ProviderEnvironment, error)
	SaveProviderEnvironment(ctx context.Context, pe *ProviderEnvironment) error
	ProviderEnvironments(ctx context.Context, orgID, projectID string) ([]*ProviderEnvironment, error)
	SharingProject(ctx context.Context, orgID, provider, projectRef string) ([]*ProviderEnvironment, error)
	ClaimFallbackNotice(ctx context.Context, pe *ProviderEnvironment, at time.Time) (bool, error)
	ResetFallbacks(ctx context.Context, orgID, environmentID string) (reset int, permanent int, err error)
	DeleteProviderEnvironment(ctx context.Context, pe *ProviderEnvironment) error
	EnvironmentRole(ctx context.Context, orgID, projectID, environmentID string) (kind, name string, firstOfKind bool, err error)
}

const providerEnvironmentColumns = `org_id,project_id,environment_id,provider,connection_id,scope,mode,permanent,project_ref,environment_ref,reason,notified_at,created_at,updated_at`

type providerEnvironmentScanner interface{ Scan(...any) error }

func scanProviderEnvironment(r providerEnvironmentScanner) (*ProviderEnvironment, error) {
	var pe ProviderEnvironment
	err := r.Scan(&pe.OrgID, &pe.ProjectID, &pe.EnvironmentID, &pe.Provider, &pe.ConnectionID, &pe.Scope, &pe.Mode, &pe.Permanent, &pe.ProjectRef, &pe.EnvironmentRef, &pe.Reason, &pe.NotifiedAt, &pe.CreatedAt, &pe.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &pe, nil
}

// ProviderEnvironment returns the record for one key, or nil when there is none.
func (s *PgStore) ProviderEnvironment(ctx context.Context, orgID, environmentID, provider, connectionID, scope string) (*ProviderEnvironment, error) {
	pe, err := scanProviderEnvironment(s.db.QueryRow(ctx, `SELECT `+providerEnvironmentColumns+` FROM loomplane_provider_environments WHERE org_id=$1 AND environment_id=$2 AND provider=$3 AND connection_id=$4 AND scope=$5`, orgID, environmentID, provider, connectionID, scope))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return pe, err
}

// SaveProviderEnvironment writes the record, replacing what the key held. The
// first write's created_at and any notified_at are kept.
func (s *PgStore) SaveProviderEnvironment(ctx context.Context, pe *ProviderEnvironment) error {
	now := time.Now().UTC()
	if pe.CreatedAt.IsZero() {
		pe.CreatedAt = now
	}
	pe.UpdatedAt = now
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_provider_environments(`+providerEnvironmentColumns+`)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT (org_id,environment_id,provider,connection_id,scope) DO UPDATE SET
 project_id=EXCLUDED.project_id, mode=EXCLUDED.mode, permanent=EXCLUDED.permanent, project_ref=EXCLUDED.project_ref,
 environment_ref=EXCLUDED.environment_ref, reason=EXCLUDED.reason, updated_at=EXCLUDED.updated_at`,
		pe.OrgID, pe.ProjectID, pe.EnvironmentID, pe.Provider, pe.ConnectionID, pe.Scope, pe.Mode, pe.Permanent, pe.ProjectRef, pe.EnvironmentRef, pe.Reason, pe.NotifiedAt, pe.CreatedAt, pe.UpdatedAt)
	return err
}

func (s *PgStore) listProviderEnvironments(ctx context.Context, where string, args ...any) ([]*ProviderEnvironment, error) {
	rows, err := s.db.Query(ctx, `SELECT `+providerEnvironmentColumns+` FROM loomplane_provider_environments WHERE `+where+` ORDER BY environment_id,provider,connection_id,scope`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ProviderEnvironment
	for rows.Next() {
		pe, err := scanProviderEnvironment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pe)
	}
	return out, rows.Err()
}

// ProviderEnvironments lists a project's records.
func (s *PgStore) ProviderEnvironments(ctx context.Context, orgID, projectID string) ([]*ProviderEnvironment, error) {
	return s.listProviderEnvironments(ctx, `org_id=$1 AND project_id=$2`, orgID, projectID)
}

// SharingProject lists the native records that sit on one provider project,
// through any connection: a provider's project ids are unique, and two
// connections to one Vercel team or Render workspace reach the same project.
func (s *PgStore) SharingProject(ctx context.Context, orgID, provider, projectRef string) ([]*ProviderEnvironment, error) {
	return s.listProviderEnvironments(ctx, `org_id=$1 AND provider=$2 AND project_ref=$3 AND mode='native'`, orgID, provider, projectRef)
}

// ClaimFallbackNotice is true for the first caller on a row, so the owner is
// told about a fallback once.
func (s *PgStore) ClaimFallbackNotice(ctx context.Context, pe *ProviderEnvironment, at time.Time) (bool, error) {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_provider_environments SET notified_at=$6 WHERE org_id=$1 AND environment_id=$2 AND provider=$3 AND connection_id=$4 AND scope=$5 AND notified_at IS NULL`,
		pe.OrgID, pe.EnvironmentID, pe.Provider, pe.ConnectionID, pe.Scope, at)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	pe.NotifiedAt = &at
	return true, nil
}

// ResetFallbacks deletes an environment's fallback records the provider might
// lift, so the next deploy asks again. Permanent ones stay and are counted.
func (s *PgStore) ResetFallbacks(ctx context.Context, orgID, environmentID string) (reset, permanent int, err error) {
	res, err := s.db.Exec(ctx, `DELETE FROM loomplane_provider_environments WHERE org_id=$1 AND environment_id=$2 AND mode='fallback' AND NOT permanent`, orgID, environmentID)
	if err != nil {
		return 0, 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	reset = int(n)
	err = s.db.QueryRow(ctx, `SELECT count(*) FROM loomplane_provider_environments WHERE org_id=$1 AND environment_id=$2 AND mode='fallback' AND permanent`, orgID, environmentID).Scan(&permanent)
	return reset, permanent, err
}

// FellBack is true only when the key has a fallback record.
func (s *PgStore) FellBack(ctx context.Context, orgID, environmentID, provider, connectionID, scope string) (bool, error) {
	var fell bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_provider_environments WHERE org_id=$1 AND environment_id=$2 AND provider=$3 AND connection_id=$4 AND scope=$5 AND mode='fallback')`,
		orgID, environmentID, provider, connectionID, scope).Scan(&fell)
	return fell, err
}

// Recorded is true when the key has any record, native or fallback.
func (s *PgStore) Recorded(ctx context.Context, orgID, environmentID, provider, connectionID, scope string) (bool, error) {
	var found bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_provider_environments WHERE org_id=$1 AND environment_id=$2 AND provider=$3 AND connection_id=$4 AND scope=$5)`,
		orgID, environmentID, provider, connectionID, scope).Scan(&found)
	return found, err
}

// DeleteProviderEnvironment removes one record.
func (s *PgStore) DeleteProviderEnvironment(ctx context.Context, pe *ProviderEnvironment) error {
	_, err := s.db.Exec(ctx, `DELETE FROM loomplane_provider_environments WHERE org_id=$1 AND environment_id=$2 AND provider=$3 AND connection_id=$4 AND scope=$5`,
		pe.OrgID, pe.EnvironmentID, pe.Provider, pe.ConnectionID, pe.Scope)
	return err
}

// EnvironmentRole says what an environment is: its kind and name, and whether
// it is the first of that kind in its project (earliest created_at, id breaks ties).
func (s *PgStore) EnvironmentRole(ctx context.Context, orgID, projectID, environmentID string) (kind, name string, firstOfKind bool, err error) {
	err = s.db.QueryRow(ctx, `SELECT e.kind, e.name, NOT EXISTS(
 SELECT 1 FROM loomplane_environments o WHERE o.org_id=e.org_id AND o.project_id=e.project_id AND o.kind=e.kind AND o.id<>e.id
 AND (o.created_at<e.created_at OR (o.created_at=e.created_at AND o.id<e.id)))
FROM loomplane_environments e WHERE e.org_id=$1 AND e.project_id=$2 AND e.id=$3`, orgID, projectID, environmentID).Scan(&kind, &name, &firstOfKind)
	return kind, name, firstOfKind, err
}
