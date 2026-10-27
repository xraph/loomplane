package cloudprofiles

import (
	"context"
	"errors"

	"github.com/xraph/loomplane/internal/security"
)

// Usage is what a Cloud carries now: the environments that chose it, and the
// running services it placed, by target or by its default.
type Usage struct {
	CloudID      string             `json:"cloud_id"`
	Environments []EnvironmentUsage `json:"environments"`
}

// EnvironmentUsage is one environment that uses the Cloud, or still runs
// services it placed after choosing another.
type EnvironmentUsage struct {
	ProjectID       string `json:"project_id"`
	ProjectName     string `json:"project_name"`
	EnvironmentID   string `json:"environment_id"`
	EnvironmentName string `json:"environment_name"`
	// Assigned is false when the environment chose another Cloud, or none,
	// and services this one placed are still running there.
	Assigned   bool             `json:"assigned"`
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

// CloudUsage joins assignments with the placement each live service binding
// recorded when it was applied.
func (s *PgStore) CloudUsage(ctx context.Context, org, project, cloud string) (*Usage, error) {
	out := &Usage{CloudID: cloud, Environments: []EnvironmentUsage{}}
	rows, err := s.db.Query(ctx, `SELECT u.project_id,p.name,u.environment_id,e.name,bool_or(u.assigned)
 FROM (
  SELECT project_id,environment_id,true AS assigned FROM loomplane_cloud_assignments WHERE org_id=$1 AND cloud_id=$2 AND ($3='' OR project_id=$3)
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
		if err = rows.Scan(&e.ProjectID, &e.ProjectName, &e.EnvironmentID, &e.EnvironmentName, &e.Assigned); err != nil {
			rows.Close()
			return nil, err
		}
		e.Placements = []PlacementUsage{}
		index[e.EnvironmentID] = len(out.Environments)
		out.Environments = append(out.Environments, e)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
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
		if err = rows.Scan(&env, &placed.TargetID, &placed.Default, &placed.Services); err != nil {
			return nil, err
		}
		if i, ok := index[env]; ok {
			out.Environments[i].Placements = append(out.Environments[i].Placements, placed)
		}
	}
	return out, rows.Err()
}
