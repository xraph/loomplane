package deployments

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/loomplane/internal/page"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// Authorizer decides whether a principal may act.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// Retrier starts a reopened deployment's steps again.
// *orchestration.Deployments is the real one.
type Retrier interface {
	Retry(ctx context.Context, orgID, deploymentID, retryID string, steps [][]string) error
}

// Projects looks projects up.
type Projects interface {
	GetProject(ctx context.Context, orgID, id string) (*projects.Project, error)
}

// Auditor records retries. projects' Auditor is the real one.
type Auditor interface {
	Record(ctx context.Context, e projects.AuditEvent)
}

// Service is the deployment API: what deployments did, and retrying one
// that failed.
type Service struct {
	store Store
	authz Authorizer
	retry Retrier
	audit Auditor
	now   func() time.Time
	// Projects checks a project is the caller's organization's before its
	// deployments are listed. It may be nil, in tests.
	Projects Projects
}

// NewService returns a Service. audit may be nil.
func NewService(store Store, authz Authorizer, retry Retrier, audit Auditor) *Service {
	return &Service{store: store, authz: authz, retry: retry, audit: audit, now: time.Now}
}

// ErrNotFailed is a retry of a deployment that hasn't failed.
var ErrNotFailed = errors.New("only a failed deployment can be retried")

// Retry runs a failed deployment's failed and skipped steps again, from
// the first that failed, with the plan that was approved. Steps that
// succeeded stay as they are. Approving is what lets someone act on the
// clouds, so retrying needs the same role.
func (s *Service) Retry(ctx context.Context, p security.Principal, id string) (*Deployment, error) {
	if err := s.authz.Require(ctx, p, security.DeploymentApprove, ""); err != nil {
		return nil, err
	}
	d, err := s.store.GetDeployment(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	ok, err := s.store.Reopen(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFailed
	}
	retryID := fmt.Sprintf("%s.retry.%d", d.ID, d.Retries+1)
	if err := s.retry.Retry(ctx, p.OrgID, d.ID, retryID, d.Order()); err != nil {
		// Put it back, so it can be retried again.
		ferr := s.store.Finish(context.WithoutCancel(ctx), p.OrgID, d.ID, StatusFailed, "The retry couldn't start: "+err.Error(), s.now().UTC())
		return nil, errors.Join(err, ferr)
	}
	if s.audit != nil {
		s.audit.Record(ctx, projects.AuditEvent{
			Action: "deployment.retry", Severity: "warning", ProjectID: d.ProjectID, Principal: p,
			Attributes: map[string]any{"deployment_id": d.ID, "plan_id": d.PlanID, "environment": d.Environment, "retry": d.Retries + 1},
		})
	}
	return s.store.GetDeployment(ctx, p.OrgID, id)
}

// List returns a page of a project's deployments started before the
// cursor, newest first, and where the next page starts: the zero cursor
// when this is the last.
func (s *Service) List(ctx context.Context, p security.Principal, projectID string, before page.Cursor, limit int) ([]*Deployment, page.Cursor, error) {
	if err := s.authz.Require(ctx, p, security.DeploymentRead, projectID); err != nil {
		return nil, page.Cursor{}, err
	}
	// Another organization's project is not found, not an empty list: the
	// answer mustn't differ by whether the project exists.
	if s.Projects != nil {
		if _, err := s.Projects.GetProject(ctx, p.OrgID, projectID); err != nil {
			return nil, page.Cursor{}, err
		}
	}
	// One more than a page says whether there's another.
	list, err := s.store.ListDeployments(ctx, p.OrgID, projectID, before, limit+1)
	if err != nil {
		return nil, page.Cursor{}, err
	}
	list, next := page.Trim(list, limit, func(d *Deployment) page.Cursor { return page.Cursor{At: d.StartedAt, ID: d.ID} })
	return list, next, nil
}

// Get returns one deployment.
func (s *Service) Get(ctx context.Context, p security.Principal, id string) (*Deployment, error) {
	if err := s.authz.Require(ctx, p, security.DeploymentRead, ""); err != nil {
		return nil, err
	}
	return s.store.GetDeployment(ctx, p.OrgID, id)
}

// ForPlan returns the deployment an approved plan started.
func (s *Service) ForPlan(ctx context.Context, p security.Principal, planID string) (*Deployment, error) {
	if err := s.authz.Require(ctx, p, security.DeploymentRead, ""); err != nil {
		return nil, err
	}
	return s.store.ForPlan(ctx, p.OrgID, planID)
}
