package recovery

import (
	"context"
	"errors"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// Restore is an isolated, temporary recovery copy. It never changes a binding.
type Restore struct {
	ID         string     `json:"id" forge:"id"`
	OrgID      string     `json:"org_id"`
	ProjectID  string     `json:"project_id"`
	BackupID   string     `json:"backup_id"`
	Status     string     `json:"status"`
	ExternalID string     `json:"external_id,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	DurationMS int64      `json:"duration_ms"`
	RequestID  string     `json:"-"`
}
type RestoreInput struct {
	RequestID string `json:"request_id"`
}
type RestoreStore interface {
	CreateRestore(context.Context, *Restore) (*Restore, error)
	GetRestore(context.Context, string, string) (*Restore, error)
	ListRestores(context.Context, string, string) ([]Restore, error)
	ClaimRestore(context.Context, string, string) (*Restore, error)
	FinishRestore(context.Context, *Restore, string, time.Duration, error) error
	ClaimCleanup(context.Context, string, string) (*Restore, error)
	FinishCleanup(context.Context, *Restore, error) error
	PendingRestores(context.Context) ([]Restore, error)
	ExpiredRestores(context.Context) ([]Restore, error)
}
type RestoreBackend interface {
	RestoreDatabase(context.Context, security.Principal, *planning.Binding, *backupfiles.Store, backupfiles.Object, string) (string, error)
	DeleteRecovery(context.Context, security.Principal, *planning.Binding, string) error
}

func (s *Service) Restore(ctx context.Context, p security.Principal, backupID string, in RestoreInput) (*Restore, error) {
	b, err := s.Store.Get(ctx, p.OrgID, backupID)
	if err != nil {
		return nil, err
	}
	if checkErr := s.Authz.Require(ctx, p, security.RestoreManage, b.ProjectID); checkErr != nil {
		return nil, checkErr
	}
	if b.Status != "succeeded" || !requestKey.MatchString(in.RequestID) {
		return nil, ErrInvalid
	}
	key, err := typeid.Generate("restore")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	r, err := s.Restores.CreateRestore(ctx, &Restore{ID: key.String(), OrgID: p.OrgID, ProjectID: b.ProjectID, BackupID: b.ID, Status: "queued", RequestID: in.RequestID, CreatedBy: p.UserID, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)})
	if err != nil {
		return nil, err
	}
	if r.Status == "queued" {
		if checkErr := s.EnqueueRestore(ctx, p.OrgID, r.ID); checkErr != nil {
			return r, checkErr
		}
	}
	if s.Audit != nil {
		s.Audit.Record(ctx, projects.AuditEvent{Action: "service.restore.request", Severity: "info", ProjectID: r.ProjectID, Principal: p, Attributes: map[string]any{"restore_id": r.ID, "backup_id": b.ID}})
	}
	return r, nil
}
func (s *Service) GetRestore(ctx context.Context, p security.Principal, id string) (*Restore, error) {
	r, err := s.Restores.GetRestore(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if checkErr := s.Authz.Require(ctx, p, security.BackupRead, r.ProjectID); checkErr != nil {
		return nil, checkErr
	}
	return r, nil
}
func (s *Service) ListRestores(ctx context.Context, p security.Principal, project string) ([]Restore, error) {
	if err := s.Authz.Require(ctx, p, security.BackupRead, project); err != nil {
		return nil, err
	}
	if _, err := s.Projects.GetProject(ctx, p.OrgID, project); err != nil {
		return nil, err
	}
	return s.Restores.ListRestores(ctx, p.OrgID, project)
}
func (s *Service) RunRestore(ctx context.Context, org, id string) error {
	r, err := s.Restores.ClaimRestore(ctx, org, id)
	if errors.Is(err, ErrConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Minute)
	defer cancel()
	started := time.Now()
	p := security.Principal{OrgID: org, UserID: r.CreatedBy}
	err = s.Authz.Require(ctx, p, security.RestoreManage, r.ProjectID)
	var external string
	if err == nil {
		var b *Backup
		b, err = s.Store.Get(ctx, org, r.BackupID)
		if err == nil && (b.Status != "succeeded" || b.ProjectID != r.ProjectID) {
			err = ErrConflict
		}
		if err == nil {
			external, err = s.RestoreBackend.RestoreDatabase(ctx, p, &b.Source, s.Files, b.Object, r.ID)
		}
	}
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer stop()
	return errors.Join(err, s.Restores.FinishRestore(cleanup, r, external, time.Since(started), err))
}
func (s *Service) DeleteRestore(ctx context.Context, p security.Principal, id string) error {
	r, err := s.Restores.GetRestore(ctx, p.OrgID, id)
	if err != nil {
		return err
	}
	if checkErr := s.Authz.Require(ctx, p, security.RestoreManage, r.ProjectID); checkErr != nil {
		return checkErr
	}
	if r.Status == "deleted" {
		return nil
	}
	return s.cleanupRestore(ctx, p, r)
}
func (s *Service) cleanupRestore(ctx context.Context, p security.Principal, r *Restore) error {
	r, err := s.Restores.ClaimCleanup(ctx, r.OrgID, r.ID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	b, err := s.Store.Get(ctx, r.OrgID, r.BackupID)
	if err == nil {
		err = s.RestoreBackend.DeleteRecovery(ctx, p, &b.Source, r.ID)
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer stop()
	return errors.Join(err, s.Restores.FinishCleanup(finish, r, err))
}
func (s *Service) reconcileRestores(ctx context.Context) error {
	pending, err := s.Restores.PendingRestores(ctx)
	if err != nil {
		return err
	}
	for _, r := range pending {
		if checkErr := s.EnqueueRestore(ctx, r.OrgID, r.ID); checkErr != nil {
			return checkErr
		}
	}
	expired, err := s.Restores.ExpiredRestores(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, r := range expired {
		// Scheduled cleanup retains the creator's permissions. A revoked creator
		// produces a visible failure that another authorized administrator can retry.
		p := security.Principal{OrgID: r.OrgID, UserID: r.CreatedBy}
		if checkErr := s.cleanupRestore(ctx, p, &r); checkErr != nil && !errors.Is(checkErr, ErrConflict) {
			failures = append(failures, checkErr)
		}
	}
	return errors.Join(failures...)
}
