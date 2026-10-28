package models

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// Manifests reads and edits a project's manifest as a principal.
// *manifests.Service is the real one.
type Manifests interface {
	Current(ctx context.Context, p security.Principal, projectID string) (*manifests.Revision, error)
	Edit(ctx context.Context, p security.Principal, projectID string, in manifests.EditInput) (*manifests.Revision, bool, error)
}

// Planner plans an environment as a principal. *planning.Service is the
// real one.
type Planner interface {
	Create(ctx context.Context, p security.Principal, projectID string, in planning.CreatePlanInput) (*planning.Plan, error)
}

// Projects finds a workspace's projects and environments. *projects.PgStore
// is the real one.
type Projects interface {
	GetProject(ctx context.Context, orgID, id string) (*projects.Project, error)
	GetEnvironment(ctx context.Context, orgID, projectID, id string) (*projects.Environment, error)
}

// TokenGuard runs fn under the workspace's Hugging Face token lock, with
// the token's revision (0 when none is set). *aisettings.Service is the
// real one. A gated model's deployment row is written inside it, so a
// token delete, which checks GatedDeployments under the same lock, can't
// miss it.
type TokenGuard interface {
	WithHuggingFaceToken(ctx context.Context, orgID string, fn func(ctx context.Context, revision int64) error) error
}

// Auditor records changes. projects.Auditor, as used across Loomplane.
type Auditor = projects.Auditor

var (
	_ TokenGuard             = (*aisettings.Service)(nil)
	_ aisettings.GatedModels = (*Service)(nil)
	_ Projects               = (*projects.PgStore)(nil)
	_ Manifests              = (*manifests.Service)(nil)
	_ Planner                = (*planning.Service)(nil)
	_ projects.Reaper        = (*Reaper)(nil)
)

// SetGateways binds what's told when a project's models change: after a
// move or a removal is planned. Nil unbinds it. W1's gateway sync is the
// real one; until then it's noGateways.
func (s *Service) SetGateways(g Gateways) {
	if g == nil {
		g = noGateways{}
	}
	s.gatewaysMu.Lock()
	defer s.gatewaysMu.Unlock()
	s.gateways = g
}

// modelsChanged tells the project's gateways its models changed. It never
// fails what called it.
func (s *Service) modelsChanged(ctx context.Context, orgID, projectID string) {
	s.gatewaysMu.RLock()
	g := s.gateways
	s.gatewaysMu.RUnlock()
	g.ModelsChanged(context.WithoutCancel(ctx), orgID, projectID)
}

// environment returns one of the project's environments.
func (s *Service) environment(ctx context.Context, orgID, projectID, environmentID string) (*projects.Environment, error) {
	if s.d.Projects == nil {
		return nil, errors.New("models: projects aren't configured")
	}
	return s.d.Projects.GetEnvironment(ctx, orgID, projectID, environmentID)
}

// WriteNeed is the permission that changes the models of an environment:
// ai:manage in a production environment, ai:edit in any other.
func (s *Service) WriteNeed(ctx context.Context, orgID, projectID, environmentID string) (security.Permission, error) {
	env, err := s.environment(ctx, orgID, projectID, environmentID)
	if err != nil {
		return security.Permission{}, err
	}
	return writeNeed(env), nil
}

func writeNeed(env *projects.Environment) security.Permission {
	if env.Kind == projects.KindProduction {
		return security.AIManage
	}
	return security.AIEdit
}

// GatedDeployments implements aisettings.GatedModels: the workspace's
// model deployments whose weights are gated on Hugging Face, so they
// download with its token, as "llama-8b in shop/production". A deployment
// still waiting for its server counts: it will download once the server is
// there.
func (s *Service) GatedDeployments(ctx context.Context, orgID string) ([]string, error) {
	if s.d.Store == nil {
		return nil, nil
	}
	rows, err := s.d.Store.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range rows {
		// A row whose environment is gone is an orphan the Reaper missed:
		// nothing will download for it, so it can't hold the token.
		gone, err := s.environmentGone(ctx, d)
		if err != nil {
			return nil, err
		}
		if gone {
			continue
		}
		v, err := s.lookup(ctx, orgID, d.EntryID)
		if errors.Is(err, ErrEntryNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if v.entry.Gated {
			out = append(out, s.label(ctx, d))
		}
	}
	slices.Sort(out)
	return out, nil
}

// environmentGone reports whether d's environment, or its project, has
// been deleted.
func (s *Service) environmentGone(ctx context.Context, d *Deployment) (bool, error) {
	_, err := s.environment(ctx, d.OrgID, d.ProjectID, d.EnvironmentID)
	switch {
	case errors.Is(err, projects.ErrEnvironmentNotFound), errors.Is(err, projects.ErrNotFound):
		return true, nil
	case err != nil:
		return false, err
	}
	return false, nil
}

// label names a deployment as "<resource> in <project>/<environment>",
// by slug and name, or by ID when they can't be read.
func (s *Service) label(ctx context.Context, d *Deployment) string {
	project, environment := d.ProjectID, d.EnvironmentID
	if s.d.Projects != nil {
		if p, err := s.d.Projects.GetProject(ctx, d.OrgID, d.ProjectID); err == nil {
			project = p.Slug
		}
		if e, err := s.d.Projects.GetEnvironment(ctx, d.OrgID, d.ProjectID, d.EnvironmentID); err == nil {
			environment = e.Name
		}
	}
	return fmt.Sprintf("%s in %s/%s", d.Resource, project, environment)
}

// Reaper deletes the model deployment rows of a project or environment
// being deleted. The rows have no foreign key to either, so without it the
// reconcile cron would keep finding them. It implements projects.Reaper.
type Reaper struct {
	Store Store
	// Warn logs a deployment that was waiting for a GPU server, whose
	// server Loomplane may still be creating, and a step that failed. It
	// may be nil.
	Warn func(msg string, err error)
}

// Reap implements projects.Reaper. It reads the rows before the deletion,
// and says now which of them were waiting for a server, so a GPU server
// that's still being created, and costs money once it's up, isn't
// forgotten with them. Once the deletion has happened it lists the rows
// again and deletes what it finds, so a row written between the two reads
// goes too.
func (r *Reaper) Reap(ctx context.Context, orgID, projectID, environmentID string) func(context.Context) {
	rows, err := r.Store.ListProject(ctx, orgID, projectID)
	if err != nil {
		r.warn("read the model deployments of a project or environment being deleted before the deletion", err)
	}
	for _, d := range rows {
		if environmentID != "" && d.EnvironmentID != environmentID {
			continue
		}
		r.warnWaiting(d)
	}
	return func(ctx context.Context) {
		rows, err := r.Store.ListProject(ctx, orgID, projectID)
		if err != nil {
			r.warn("read the model deployments of a deleted project or environment, so their rows stay", err)
			return
		}
		for _, d := range rows {
			if environmentID != "" && d.EnvironmentID != environmentID {
				continue
			}
			if err := r.Store.Delete(ctx, d.OrgID, d.ProjectID, d.EnvironmentID, d.Resource); err != nil && !errors.Is(err, ErrNotFound) {
				r.warn(fmt.Sprintf("delete the model deployment %s of a deleted environment", d.Resource), err)
			}
		}
	}
}

// warnWaiting says that d was waiting for a GPU server, when it was.
func (r *Reaper) warnWaiting(d *Deployment) {
	if d.ServerOperationID == "" || (d.Phase != PhaseWaitingForServer && d.Phase != PhaseNeedsAttention) {
		return
	}
	server := d.ServerName
	if server == "" {
		server = "its GPU server"
	}
	r.warn(fmt.Sprintf("%s was waiting for a GPU server when its environment was deleted", d.Resource),
		fmt.Errorf("loomplane was creating %s for %s (cloud %s, operation %s), and the deletion doesn't stop it. "+
			"Once it's up it shows on the cloud's servers and costs money until you delete it",
			server, d.Resource, d.ServerCloudID, d.ServerOperationID))
}

func (r *Reaper) warn(msg string, err error) {
	if r.Warn != nil {
		r.Warn(msg, err)
	}
}
