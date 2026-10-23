package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// Policy schedules full logical backups. Retention keeps all points until a
// separately qualified deletion policy is available.
type Policy struct {
	ID            string    `json:"id" forge:"id"`
	OrgID         string    `json:"org_id"`
	ProjectID     string    `json:"project_id"`
	EnvironmentID string    `json:"environment_id"`
	Environment   string    `json:"environment"`
	Resource      string    `json:"resource"`
	EveryHours    int       `json:"every_hours"`
	Enabled       bool      `json:"enabled"`
	NextAt        time.Time `json:"next_at"`
	LastError     string    `json:"last_error,omitempty"`
	CreatedBy     string    `json:"created_by"`
}
type PolicyInput struct {
	Environment string `json:"environment"`
	Resource    string `json:"resource"`
	EveryHours  int    `json:"every_hours"`
	Enabled     bool   `json:"enabled"`
}
type PolicyStore interface {
	PutPolicy(context.Context, *Policy) (*Policy, error)
	ListPolicies(context.Context, string, string) ([]Policy, error)
	DuePolicies(context.Context) ([]Policy, error)
	AdvancePolicy(context.Context, *Policy, error) error
}

func (s *Service) SetPolicy(ctx context.Context, p security.Principal, project string, in PolicyInput) (*Policy, error) {
	if err := s.Authz.Require(ctx, p, security.BackupManage, project); err != nil {
		return nil, err
	}
	if _, err := s.Projects.GetProject(ctx, p.OrgID, project); err != nil {
		return nil, err
	}
	if in.EveryHours < 1 || in.EveryHours > 168 {
		return nil, ErrInvalid
	}
	envs, err := s.Projects.ListEnvironments(ctx, p.OrgID, project)
	if err != nil {
		return nil, err
	}
	var environment string
	for _, env := range envs {
		if env.Name == in.Environment {
			environment = env.ID
		}
	}
	if environment == "" {
		return nil, ErrNotFound
	}
	bindings, err := s.Bindings.ListBindings(ctx, p.OrgID, environment)
	if err != nil {
		return nil, err
	}
	found := false
	var refused error
	for _, b := range bindings {
		if b.ProjectID == project && b.Subject == planning.ResourceSubject && b.Name == in.Resource {
			var cfg planning.ResourceConfig
			if json.Unmarshal(b.Config, &cfg) == nil {
				if refused = refuseRecovery(b.Name, &cfg); refused == nil {
					found = true
				}
			}
		}
	}
	if !found {
		if named(refused) {
			return nil, refused
		}
		return nil, ErrInvalid
	}
	key, err := typeid.Generate("policy")
	if err != nil {
		return nil, err
	}
	r, err := s.Policies.PutPolicy(ctx, &Policy{ID: key.String(), OrgID: p.OrgID, ProjectID: project, EnvironmentID: environment, Environment: in.Environment, Resource: in.Resource, EveryHours: in.EveryHours, Enabled: in.Enabled, CreatedBy: p.UserID, NextAt: time.Now().UTC().Add(time.Duration(in.EveryHours) * time.Hour)})
	if err == nil && s.Audit != nil {
		s.Audit.Record(ctx, projects.AuditEvent{Action: "service.backup.policy", Severity: "info", ProjectID: project, Principal: p, Attributes: map[string]any{"policy_id": r.ID, "enabled": r.Enabled, "every_hours": r.EveryHours}})
	}
	return r, err
}
func (s *Service) ListPolicies(ctx context.Context, p security.Principal, project string) ([]Policy, error) {
	if err := s.Authz.Require(ctx, p, security.BackupRead, project); err != nil {
		return nil, err
	}
	if _, err := s.Projects.GetProject(ctx, p.OrgID, project); err != nil {
		return nil, err
	}
	return s.Policies.ListPolicies(ctx, p.OrgID, project)
}
func (s *Service) reconcilePolicies(ctx context.Context) error {
	policies, err := s.Policies.DuePolicies(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, policy := range policies {
		// All schedulers use the same persisted due time as their idempotency key.
		request := fmt.Sprintf("schedule_%s_%d", policy.ID, policy.NextAt.Unix())
		_, createErr := s.Create(ctx, security.Principal{OrgID: policy.OrgID, UserID: policy.CreatedBy}, policy.ProjectID, BackupCreateInput{Environment: policy.Environment, Resource: policy.Resource, RequestID: request})
		if advanceErr := s.Policies.AdvancePolicy(ctx, &policy, createErr); advanceErr != nil {
			failures = append(failures, advanceErr)
		}
	}
	return errors.Join(failures...)
}
