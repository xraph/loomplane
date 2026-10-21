// Package recovery owns deployed-service recovery records, separate from
// control-plane exports and from the file store that holds backup bytes.
package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

var ErrNotFound = errors.New("service backup not found")
var ErrConflict = errors.New("backup request conflicts with an existing operation")
var ErrInvalid = errors.New("invalid service backup request")
var requestKey = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,128}$`)

// Backup reports upload success separately from evidence of a tested restore.
type Backup struct {
	ID            string             `json:"id" forge:"id"`
	OrgID         string             `json:"org_id"`
	ProjectID     string             `json:"project_id"`
	EnvironmentID string             `json:"environment_id"`
	Name          string             `json:"name"`
	Status        string             `json:"status"`
	Attempt       int                `json:"attempt"`
	Bytes         int64              `json:"bytes"`
	SHA256        string             `json:"sha256,omitempty"`
	Error         string             `json:"error,omitempty"`
	CreatedBy     string             `json:"created_by"`
	CreatedAt     time.Time          `json:"created_at"`
	CompletedAt   *time.Time         `json:"completed_at,omitempty"`
	RestoredAt    *time.Time         `json:"restored_at,omitempty"`
	RequestID     string             `json:"-"`
	Source        planning.Binding   `json:"-"`
	Object        backupfiles.Object `json:"-"`
	Token         string             `json:"-"`
}

func (b *Backup) objectKey() string {
	return fmt.Sprintf("%s/%s/%s/%s/%s.dump", b.OrgID, b.ProjectID, b.EnvironmentID, b.ID, b.Token)
}

type BackupCreateInput struct {
	Environment string `json:"environment"`
	Resource    string `json:"resource"`
	RequestID   string `json:"request_id"`
}

type Store interface {
	Create(context.Context, *Backup) (*Backup, error)
	Get(context.Context, string, string) (*Backup, error)
	List(context.Context, string, string) ([]Backup, error)
	Claim(context.Context, string, string, string) (*Backup, error)
	Finish(context.Context, *Backup, backupfiles.Object, error) error
	Retry(context.Context, string, string) error
	Pending(context.Context) ([]Backup, error)
}

type Authorizer interface {
	Require(context.Context, security.Principal, security.Permission, string) error
}
type Projects interface {
	GetProject(context.Context, string, string) (*projects.Project, error)
	ListEnvironments(context.Context, string, string) ([]*projects.Environment, error)
}
type Bindings interface {
	ListBindings(context.Context, string, string) ([]*planning.Binding, error)
}
type Backend interface {
	BackupDatabase(context.Context, security.Principal, *planning.Binding, *backupfiles.Store, string) (backupfiles.Object, error)
}
type Service struct {
	Retirements       RetirementStore
	EnqueueRetirement func(context.Context, string, string) error
	Policies          PolicyStore
	Restores          RestoreStore
	RestoreBackend    RestoreBackend
	EnqueueRestore    func(context.Context, string, string) error
	Store             Store
	Authz             Authorizer
	Projects          Projects
	Bindings          Bindings
	Backend           Backend
	Files             *backupfiles.Store
	Enqueue           func(context.Context, string, string) error
	Audit             projects.Auditor
}

func (s *Service) Create(ctx context.Context, p security.Principal, project string, in BackupCreateInput) (*Backup, error) {
	if err := s.Authz.Require(ctx, p, security.BackupManage, project); err != nil {
		return nil, err
	}
	if _, err := s.Projects.GetProject(ctx, p.OrgID, project); err != nil {
		return nil, err
	}
	if !requestKey.MatchString(in.RequestID) {
		return nil, fmt.Errorf("%w: request_id must be 8-128 letters, digits, underscores or hyphens", ErrInvalid)
	}
	envs, err := s.Projects.ListEnvironments(ctx, p.OrgID, project)
	if err != nil {
		return nil, err
	}
	environment := ""
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
	var source *planning.Binding
	for _, binding := range bindings {
		if binding.Subject == planning.ResourceSubject && binding.Name == in.Resource {
			source = binding
		}
	}
	if source == nil || source.ProjectID != project {
		return nil, ErrNotFound
	}
	var cfg planning.ResourceConfig
	if json.Unmarshal(source.Config, &cfg) != nil || !supportsRecovery(&cfg) {
		return nil, fmt.Errorf("%w: the resource has no supported database backup adapter", ErrInvalid)
	}
	key, err := typeid.Generate("backup")
	if err != nil {
		return nil, err
	}
	record, err := s.Store.Create(ctx, &Backup{ID: key.String(), OrgID: p.OrgID, ProjectID: project, EnvironmentID: environment, Name: source.Name, Source: *source, RequestID: in.RequestID, Status: "queued", CreatedBy: p.UserID, CreatedAt: time.Now().UTC()})
	if err != nil {
		return nil, err
	}
	if record.Status == "queued" {
		if checkErr := s.Enqueue(ctx, p.OrgID, record.ID); checkErr != nil {
			return record, checkErr
		}
	}
	if s.Audit != nil {
		s.Audit.Record(ctx, projects.AuditEvent{Action: "service.backup.request", Severity: "info", ProjectID: project, Principal: p, Attributes: map[string]any{"backup_id": record.ID, "environment_id": record.EnvironmentID, "resource": record.Name}})
	}
	return record, nil
}

func (s *Service) Get(ctx context.Context, p security.Principal, id string) (*Backup, error) {
	record, err := s.Store.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if checkErr := s.Authz.Require(ctx, p, security.BackupRead, record.ProjectID); checkErr != nil {
		return nil, checkErr
	}
	return record, nil
}
func (s *Service) List(ctx context.Context, p security.Principal, project string) ([]Backup, error) {
	if err := s.Authz.Require(ctx, p, security.BackupRead, project); err != nil {
		return nil, err
	}
	if _, err := s.Projects.GetProject(ctx, p.OrgID, project); err != nil {
		return nil, err
	}
	return s.Store.List(ctx, p.OrgID, project)
}
func (s *Service) Retry(ctx context.Context, p security.Principal, id string) (*Backup, error) {
	record, err := s.Store.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if checkErr := s.Authz.Require(ctx, p, security.BackupManage, record.ProjectID); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := s.Store.Retry(ctx, p.OrgID, id); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := s.Enqueue(ctx, p.OrgID, id); checkErr != nil {
		return nil, checkErr
	}
	return s.Store.Get(ctx, p.OrgID, id)
}

// Run is a durable worker entry point. Each claim has its own file key; a late
// worker can neither overwrite a later attempt nor publish its recovery point.
func (s *Service) Run(ctx context.Context, org, id string) error {
	token, err := typeid.Generate("attempt")
	if err != nil {
		return err
	}
	record, err := s.Store.Claim(ctx, org, id, token.String())
	if errors.Is(err, ErrConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Minute)
	defer cancel()
	principal := security.Principal{OrgID: org, UserID: record.CreatedBy}
	if err = s.Authz.Require(ctx, principal, security.BackupManage, record.ProjectID); err != nil {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer stop()
		return s.Store.Finish(cleanup, record, backupfiles.Object{}, err)
	}
	bindings, err := s.Bindings.ListBindings(ctx, org, record.EnvironmentID)
	if err == nil {
		current := false
		for _, binding := range bindings {
			if binding.ID == record.Source.ID && binding.ExternalID == record.Source.ExternalID && binding.ConnectionID == record.Source.ConnectionID && binding.ProjectID == record.ProjectID {
				current = true
			}
		}
		if !current {
			err = errors.New("source binding changed before backup; request a new backup")
		}
	}
	var object backupfiles.Object
	if err == nil {
		object, err = s.Backend.BackupDatabase(ctx, principal, &record.Source, s.Files, record.objectKey())
	}
	cleanup, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancelCleanup()
	return errors.Join(err, s.Store.Finish(cleanup, record, object, err))
}

// Reconcile repairs the gap between committing a request and enqueueing it,
// and retries claims whose worker disappeared. Native jobs have a shorter timeout.
func (s *Service) Reconcile(ctx context.Context) error {
	var failures []error
	pending, err := s.Store.Pending(ctx)
	if err != nil {
		failures = append(failures, err)
	}
	for _, record := range pending {
		if err = s.Enqueue(ctx, record.OrgID, record.ID); err != nil {
			failures = append(failures, err)
		}
	}
	if s.Policies != nil {
		failures = append(failures, s.reconcilePolicies(ctx))
	}
	if s.Restores != nil {
		failures = append(failures, s.reconcileRestores(ctx))
	}
	if s.Retirements != nil {
		failures = append(failures, s.reconcileRetirements(ctx))
	}
	return errors.Join(failures...)
}
