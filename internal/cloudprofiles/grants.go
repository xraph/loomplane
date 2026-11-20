package cloudprofiles

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// ProjectGrant allows new placements on listed targets. An empty list allows all
// targets in this Cloud. Revocation retains the row and advances its revision.
// Default marks the project's default Cloud; at most one live grant per
// project carries it, and changing it is a reviewed Cloud change.
type ProjectGrant struct {
	OrgID     string   `json:"org_id"`
	CloudID   string   `json:"cloud_id"`
	ProjectID string   `json:"project_id"`
	TargetIDs []string `json:"target_ids"`
	Revision  int64    `json:"revision"`
	Revoked   bool     `json:"revoked"`
	Default   bool     `json:"default"`
}

// GrantStore persists which projects may use a Cloud. Environments play no
// part: every environment of a granted project may use it.
type GrantStore interface {
	SetProjectGrant(ctx context.Context, orgID, cloudID, projectID string, targetIDs []string, revoked bool) (*ProjectGrant, error)
	GetProjectGrant(context.Context, string, string, string) (*ProjectGrant, error)
}

// ListOrganization lists organization-owned Clouds for infrastructure managers.
func (s *Service) ListOrganization(ctx context.Context, p security.Principal) ([]*Profile, error) {
	if err := s.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID); err != nil {
		return nil, err
	}
	return s.store.ListProfiles(ctx, p.OrgID, "")
}

// CreateOrganization creates a Cloud without granting a project access.
func (s *Service) CreateOrganization(ctx context.Context, p security.Principal, in CreateInput) (*Profile, error) {
	return s.Create(ctx, p, "", in)
}

// GetOrganization reads the organization's Cloud configuration.
func (s *Service) GetOrganization(ctx context.Context, p security.Principal, id string) (*Profile, error) {
	if err := s.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID); err != nil {
		return nil, err
	}
	return s.store.GetProfile(ctx, p.OrgID, "", id)
}

// GrantProject replaces the project's allowed target set.
func (s *Service) GrantProject(ctx context.Context, p security.Principal, cloudID, projectID string, targetIDs []string) (*ProjectGrant, error) {
	return s.setGrant(ctx, p, cloudID, projectID, targetIDs, false)
}

// RevokeProject blocks fresh placement without deleting deployed workloads.
func (s *Service) RevokeProject(ctx context.Context, p security.Principal, cloudID, projectID string) (*ProjectGrant, error) {
	return s.setGrant(ctx, p, cloudID, projectID, nil, true)
}
func (s *Service) setGrant(ctx context.Context, p security.Principal, cloudID, projectID string, targetIDs []string, revoked bool) (*ProjectGrant, error) {
	if _, err := s.GetOrganization(ctx, p, cloudID); err != nil {
		return nil, err
	}
	project, err := s.projects.GetProject(ctx, p.OrgID, projectID)
	if err != nil {
		return nil, err
	}
	if project == nil || project.OrgID != p.OrgID {
		return nil, projects.ErrNotFound
	}
	st, ok := s.store.(GrantStore)
	if !ok {
		return nil, ErrInvalid
	}
	targets := append([]string{}, targetIDs...)
	slices.Sort(targets)
	targets = slices.Compact(targets)
	grant, err := st.SetProjectGrant(ctx, p.OrgID, cloudID, projectID, targets, revoked)
	if err != nil {
		return nil, err
	}
	s.record(ctx, p, projectID, "cloud.grant", map[string]any{"cloud_id": cloudID, "revision": grant.Revision, "revoked": revoked, "default": grant.Default})
	return grant, nil
}

// LockOrganization orders Cloud acceptance, metadata changes and grant updates.
// It bounds this wait, and every later lock wait in tx, by LockWaitLimit.
func LockOrganization(ctx context.Context, tx driver.Tx, org string) error {
	if err := BoundLockWaits(ctx, tx); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "cloud-organization/"+org)
	return LockWaitError(err, "the organization lock")
}

// LockWaitLimit is the longest a transaction waits for a lock once it holds
// or asks for the organization lock, or while it admits a direct server
// apply. Those transactions only read and write the database, so even a
// queue of them clears in well under a second. A wait this long is a lock
// cycle that runs through a goroutine waiting on another connection, which
// Postgres can't detect. The wait fails instead of hanging the organization.
const LockWaitLimit = 60 * time.Second

// BoundLockWaits sets LockWaitLimit as tx's lock_timeout.
func BoundLockWaits(ctx context.Context, tx driver.Tx) error {
	_, err := tx.Exec(ctx, `SELECT set_config('lock_timeout',$1,true)`, fmt.Sprintf("%dms", LockWaitLimit.Milliseconds()))
	return err
}

// LockWaitError names the lock a wait gave up on after LockWaitLimit.
// Other errors pass through unchanged.
func LockWaitError(err error, lock string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
		return fmt.Errorf("waited %s for %s and gave up; another transaction held it that long, which usually means a lock cycle: %w", LockWaitLimit, lock, err)
	}
	return err
}

func (s *PgStore) GetProjectGrant(ctx context.Context, org, cloud, project string) (*ProjectGrant, error) {
	g := &ProjectGrant{OrgID: org, CloudID: cloud, ProjectID: project}
	err := s.db.QueryRow(ctx, `SELECT target_ids,revision,revoked,is_default FROM loomplane_cloud_project_grants WHERE org_id=$1 AND cloud_id=$2 AND project_id=$3`, org, cloud, project).Scan(&g.TargetIDs, &g.Revision, &g.Revoked, &g.Default)
	return g, storeError(err)
}
func (s *PgStore) SetProjectGrant(ctx context.Context, org, cloud, project string, targets []string, revoked bool) (*ProjectGrant, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollbackSync(tx)
	if lockErr := LockOrganization(ctx, tx, org); lockErr != nil {
		return nil, lockErr
	}
	for _, id := range targets {
		var found string
		if err = tx.QueryRow(ctx, `SELECT id FROM loomplane_cloud_targets WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, org, cloud, id).Scan(&found); err != nil {
			return nil, storeError(err)
		}
	}
	if targets == nil {
		targets = []string{}
	}
	// A project's first live grant becomes its default Cloud: nothing runs
	// there yet, so there is nothing to review. Not while a shared change on
	// the Cloud is unfinished, though: that change reaches only the
	// environments it reviewed, so the project waits and makes it its default
	// once the change settles. Revoking a grant ends its default; the
	// project's services keep running where they are.
	g := &ProjectGrant{OrgID: org, CloudID: cloud, ProjectID: project, TargetIDs: targets, Revoked: revoked}
	err = tx.QueryRow(ctx, `INSERT INTO loomplane_cloud_project_grants(org_id,cloud_id,project_id,target_ids,revoked,is_default)
 VALUES($1,$2,$3,$4,$5,NOT $5 AND NOT EXISTS(SELECT 1 FROM loomplane_cloud_project_grants WHERE org_id=$1 AND project_id=$3 AND is_default AND NOT revoked)
  AND NOT EXISTS(SELECT 1 FROM loomplane_cloud_syncs WHERE org_id=$1 AND cloud_id=$2 AND (target_id<>'' OR body->>'default'='true') AND status NOT IN ('succeeded','reverted')))
 ON CONFLICT(org_id,cloud_id,project_id) DO UPDATE SET target_ids=EXCLUDED.target_ids,revoked=EXCLUDED.revoked,is_default=loomplane_cloud_project_grants.is_default AND NOT EXCLUDED.revoked,revision=loomplane_cloud_project_grants.revision+1,updated_at=now() RETURNING revision,is_default`, org, cloud, project, targets, revoked).Scan(&g.Revision, &g.Default)
	if err != nil {
		return nil, storeError(err)
	}
	return g, tx.Commit()
}

// EnvironmentsUsing lists every environment, in every project, a shared
// change to cloud reaches: each environment of a project whose default
// Cloud it is, and each environment running a service it placed.
func (s *PgStore) EnvironmentsUsing(ctx context.Context, org, cloud string) ([]*projects.Environment, error) {
	rows, err := s.db.Query(ctx, environmentsUsingSQL, org, cloud)
	if err != nil {
		return nil, storeError(err)
	}
	defer rows.Close()
	out := []*projects.Environment{}
	for rows.Next() {
		e := &projects.Environment{OrgID: org}
		if scanErr := rows.Scan(&e.ID, &e.ProjectID, &e.Name); scanErr != nil {
			return nil, scanErr
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// environmentsUsingSQL is shared with acceptance, which compares the
// reviewed environments against it inside the accepting transaction.
const environmentsUsingSQL = `SELECT e.id,e.project_id,e.name FROM loomplane_environments e
 WHERE e.org_id=$1 AND (
  EXISTS(SELECT 1 FROM loomplane_cloud_project_grants g WHERE g.org_id=e.org_id AND g.project_id=e.project_id AND g.cloud_id=$2 AND g.is_default AND NOT g.revoked)
  OR EXISTS(SELECT 1 FROM loomplane_bindings b WHERE b.org_id=e.org_id AND b.environment_id=e.id AND b.subject='service' AND b.config->'cloud_placement'->>'cloud_id'=$2))
 ORDER BY e.project_id,e.id`

// SyncForParticipant is an internal execution lookup, not a project API view.
func (s *PgStore) SyncForParticipant(ctx context.Context, org, project, env, id string) (*Sync, *Snapshot, error) {
	sync, snapshot, err := readSync(s.db.QueryRow(ctx, `SELECT body,snapshot,version FROM loomplane_cloud_syncs WHERE org_id=$1 AND id=$2`, org, id))
	if err != nil {
		return nil, nil, err
	}
	for _, p := range snapshot.Environments {
		if p.EnvironmentID == env && ParticipantProject(p.ProjectID, sync.ProjectID) == project {
			return sync, snapshot, nil
		}
	}
	return nil, nil, ErrNotFound
}

// ValidateGrants rechecks fresh placement authority. Cleanup uses retained inputs.
func (s *PgStore) ValidateGrants(ctx context.Context, org string, grants []ProjectGrant) error {
	for _, pin := range grants {
		g, err := s.GetProjectGrant(ctx, org, pin.CloudID, pin.ProjectID)
		if err != nil {
			return err
		}
		if g.Revoked || g.Revision != pin.Revision {
			return ErrConflict
		}
	}
	return nil
}

// ListProjectGrants returns both active and revoked grants to the Cloud manager.
func (s *Service) ListProjectGrants(ctx context.Context, p security.Principal, cloud string) ([]*ProjectGrant, error) {
	if _, err := s.GetOrganization(ctx, p, cloud); err != nil {
		return nil, err
	}
	st, ok := s.store.(interface {
		ListProjectGrants(context.Context, string, string) ([]*ProjectGrant, error)
	})
	if !ok {
		return nil, ErrInvalid
	}
	return st.ListProjectGrants(ctx, p.OrgID, cloud)
}
func (s *PgStore) ListProjectGrants(ctx context.Context, org, cloud string) ([]*ProjectGrant, error) {
	rows, err := s.db.Query(ctx, `SELECT project_id,target_ids,revision,revoked,is_default FROM loomplane_cloud_project_grants WHERE org_id=$1 AND cloud_id=$2 ORDER BY project_id`, org, cloud)
	if err != nil {
		return nil, storeError(err)
	}
	defer rows.Close()
	out := []*ProjectGrant{}
	for rows.Next() {
		g := &ProjectGrant{OrgID: org, CloudID: cloud}
		if scanErr := rows.Scan(&g.ProjectID, &g.TargetIDs, &g.Revision, &g.Revoked, &g.Default); scanErr != nil {
			return nil, scanErr
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
