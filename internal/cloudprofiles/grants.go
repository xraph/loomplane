package cloudprofiles

import (
	"context"
	"slices"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// ProjectGrant allows new placements on listed targets. An empty list allows all
// targets in this Cloud. Revocation retains the row and advances its revision.
type ProjectGrant struct {
	OrgID     string   `json:"org_id"`
	CloudID   string   `json:"cloud_id"`
	ProjectID string   `json:"project_id"`
	TargetIDs []string `json:"target_ids"`
	Revision  int64    `json:"revision"`
	Revoked   bool     `json:"revoked"`
}

// GrantStore persists authorization independently of environment assignments.
type GrantStore interface {
	SetProjectGrant(context.Context, string, string, string, []string, bool) (int64, error)
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
	revision, err := st.SetProjectGrant(ctx, p.OrgID, cloudID, projectID, targets, revoked)
	if err != nil {
		return nil, err
	}
	s.record(ctx, p, projectID, "cloud.grant", map[string]any{"cloud_id": cloudID, "revision": revision, "revoked": revoked})
	return &ProjectGrant{OrgID: p.OrgID, CloudID: cloudID, ProjectID: projectID, TargetIDs: targets, Revision: revision, Revoked: revoked}, nil
}

// LockOrganization orders Cloud acceptance, metadata changes and grant updates.
func LockOrganization(ctx context.Context, tx driver.Tx, org string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "cloud-organization/"+org)
	return err
}

func (s *PgStore) GetProjectGrant(ctx context.Context, org, cloud, project string) (*ProjectGrant, error) {
	g := &ProjectGrant{OrgID: org, CloudID: cloud, ProjectID: project}
	err := s.db.QueryRow(ctx, `SELECT target_ids,revision,revoked FROM loomplane_cloud_project_grants WHERE org_id=$1 AND cloud_id=$2 AND project_id=$3`, org, cloud, project).Scan(&g.TargetIDs, &g.Revision, &g.Revoked)
	return g, storeError(err)
}
func (s *PgStore) SetProjectGrant(ctx context.Context, org, cloud, project string, targets []string, revoked bool) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer rollbackSync(tx)
	if lockErr := LockOrganization(ctx, tx, org); lockErr != nil {
		return 0, lockErr
	}
	for _, id := range targets {
		var found string
		if err = tx.QueryRow(ctx, `SELECT id FROM loomplane_cloud_targets WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, org, cloud, id).Scan(&found); err != nil {
			return 0, storeError(err)
		}
	}
	if targets == nil {
		targets = []string{}
	}
	var revision int64
	err = tx.QueryRow(ctx, `INSERT INTO loomplane_cloud_project_grants(org_id,cloud_id,project_id,target_ids,revoked) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(org_id,cloud_id,project_id) DO UPDATE SET target_ids=EXCLUDED.target_ids,revoked=EXCLUDED.revoked,revision=loomplane_cloud_project_grants.revision+1,updated_at=now() RETURNING revision`, org, cloud, project, targets, revoked).Scan(&revision)
	if err != nil {
		return 0, storeError(err)
	}
	return revision, tx.Commit()
}

// AssignedEnvironments includes every project affected by an organization change.
func (s *PgStore) AssignedEnvironments(ctx context.Context, org, cloud string) ([]*projects.Environment, error) {
	rows, err := s.db.Query(ctx, `SELECT e.id,e.project_id,e.name FROM loomplane_environments e JOIN loomplane_cloud_assignments a ON a.org_id=e.org_id AND a.project_id=e.project_id AND a.environment_id=e.id WHERE a.org_id=$1 AND a.cloud_id=$2 ORDER BY e.project_id,e.id`, org, cloud)
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
	rows, err := s.db.Query(ctx, `SELECT project_id,target_ids,revision,revoked FROM loomplane_cloud_project_grants WHERE org_id=$1 AND cloud_id=$2 ORDER BY project_id`, org, cloud)
	if err != nil {
		return nil, storeError(err)
	}
	defer rows.Close()
	out := []*ProjectGrant{}
	for rows.Next() {
		g := &ProjectGrant{OrgID: org, CloudID: cloud}
		if scanErr := rows.Scan(&g.ProjectID, &g.TargetIDs, &g.Revision, &g.Revoked); scanErr != nil {
			return nil, scanErr
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
