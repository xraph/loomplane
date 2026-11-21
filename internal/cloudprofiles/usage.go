package cloudprofiles

import (
	"context"
	"errors"

	"github.com/xraph/loomplane/internal/security"
)

// Usage is what a Cloud carries now: the environments of projects whose
// default Cloud it is, and the running services it placed, by target or by
// its default placement.
type Usage struct {
	CloudID      string             `json:"cloud_id"`
	Environments []EnvironmentUsage `json:"environments"`
}

// EnvironmentUsage is one environment that uses the Cloud, through its
// project's default or through services that name it, or still runs
// services it placed.
type EnvironmentUsage struct {
	ProjectID       string `json:"project_id"`
	ProjectName     string `json:"project_name"`
	EnvironmentID   string `json:"environment_id"`
	EnvironmentName string `json:"environment_name"`
	// Default is true when the Cloud is the project's default Cloud. False
	// means only services placed on it, or still running there, tie the
	// environment to it.
	Default    bool             `json:"default"`
	Placements []PlacementUsage `json:"placements"`
}

// PlacementUsage names the running services one target, or the default, placed.
type PlacementUsage struct {
	TargetID string   `json:"target_id,omitempty"`
	Default  bool     `json:"default,omitempty"`
	Services []string `json:"services"`
}

// UsageStore reads a Cloud's environments and placed services. A nonempty
// project limits both to that project.
type UsageStore interface {
	CloudUsage(ctx context.Context, orgID, projectID, cloudID string) (*Usage, error)
}

// Usage reports who uses a Cloud and what it placed, in the caller's view.
func (s *Service) Usage(ctx context.Context, p security.Principal, projectID, id string) (*Usage, error) {
	if err := s.scope(ctx, p, projectID, security.CloudAccountRead); err != nil {
		return nil, err
	}
	// The read proves the Cloud exists in this view: a project sees only
	// Clouds granted to it.
	if _, err := s.store.GetProfile(ctx, p.OrgID, projectID, id); err != nil {
		return nil, err
	}
	st, ok := s.store.(UsageStore)
	if !ok {
		return nil, errors.New("cloud usage unavailable")
	}
	return st.CloudUsage(ctx, p.OrgID, projectID, id)
}

var _ UsageStore = (*PgStore)(nil)

// CloudUsage joins the projects whose default the Cloud is with the
// placement each live service binding recorded when it was applied.
func (s *PgStore) CloudUsage(ctx context.Context, org, project, cloud string) (*Usage, error) {
	out := &Usage{CloudID: cloud, Environments: []EnvironmentUsage{}}
	rows, err := s.db.Query(ctx, `SELECT u.project_id,p.name,u.environment_id,e.name,bool_or(u.is_default)
 FROM (
  SELECT e.project_id,e.id AS environment_id,true AS is_default FROM loomplane_environments e
  JOIN loomplane_cloud_project_grants g ON g.org_id=e.org_id AND g.project_id=e.project_id AND g.cloud_id=$2 AND g.is_default AND NOT g.revoked
  WHERE e.org_id=$1 AND ($3='' OR e.project_id=$3)
  UNION ALL
  SELECT project_id,environment_id,false FROM loomplane_bindings WHERE org_id=$1 AND subject='service' AND config->'cloud_placement'->>'cloud_id'=$2 AND ($3='' OR project_id=$3)
 ) u
 JOIN loomplane_projects p ON p.org_id=$1 AND p.id=u.project_id
 JOIN loomplane_environments e ON e.org_id=$1 AND e.id=u.environment_id
 GROUP BY u.project_id,p.name,u.environment_id,e.name
 ORDER BY p.name,e.name,u.environment_id`, org, cloud, project)
	if err != nil {
		return nil, err
	}
	index := map[string]int{}
	for rows.Next() {
		var e EnvironmentUsage
		if scanErr := rows.Scan(&e.ProjectID, &e.ProjectName, &e.EnvironmentID, &e.EnvironmentName, &e.Default); scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		e.Placements = []PlacementUsage{}
		index[e.EnvironmentID] = len(out.Environments)
		out.Environments = append(out.Environments, e)
	}
	rows.Close()
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	rows, err = s.db.Query(ctx, `SELECT environment_id,coalesce(config->'cloud_placement'->>'target_id',''),coalesce((config->'cloud_placement'->>'default')::boolean,false),array_agg(name ORDER BY name)
 FROM loomplane_bindings
 WHERE org_id=$1 AND subject='service' AND config->'cloud_placement'->>'cloud_id'=$2 AND ($3='' OR project_id=$3)
 GROUP BY 1,2,3 ORDER BY 1,2`, org, cloud, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var env string
		var placed PlacementUsage
		if scanErr := rows.Scan(&env, &placed.TargetID, &placed.Default, &placed.Services); scanErr != nil {
			return nil, scanErr
		}
		if i, ok := index[env]; ok {
			out.Environments[i].Placements = append(out.Environments[i].Placements, placed)
		}
	}
	return out, rows.Err()
}
