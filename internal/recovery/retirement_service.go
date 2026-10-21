package recovery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// RetirementInput explicitly selects one candidate under a full-backup policy.
// It does not enable a recurring deletion policy.
type RetirementInput struct {
	RequestID string         `json:"request_id"`
	Policy    RetentionInput `json:"policy"`
}

type RetirementRetryInput struct {
	RequestID string `json:"request_id"`
}

type RetirementStore interface {
	ListRetirements(context.Context, string, string, RetirementListInput) (*RetirementPage, error)
	RetryRetirement(context.Context, string, string, string, string, RetirementGuard) (*Retirement, error)
	RetirementHistory(context.Context, string, string, RetirementHistoryInput) (*RetirementHistory, error)
	BeginRetirement(context.Context, *Retirement, RetirementGuard) (*Retirement, error)
	GetRetirement(context.Context, string, string) (*Retirement, error)
	PendingRetirements(context.Context) ([]Retirement, error)
	ClaimRetirement(context.Context, string, string, string) (*Retirement, error)
	FinishRetirement(context.Context, *Retirement, error) error
}

func (s *Service) Retire(ctx context.Context, p security.Principal, id string, in RetirementInput) (*Retirement, error) {
	b, err := s.Store.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if b.OrgID != p.OrgID || p.OrgID == "" || p.UserID == "" {
		return nil, security.ErrForbidden
	}
	guard := s.retirementGuard(p, b.ProjectID, b.Object)
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	if !requestKey.MatchString(in.RequestID) {
		return nil, ErrInvalid
	}
	if checkErr := in.Policy.Validate(); checkErr != nil {
		return nil, checkErr
	}
	key, err := typeid.Generate("retirement")
	if err != nil {
		return nil, err
	}
	r, err := s.Retirements.BeginRetirement(ctx, &Retirement{ID: key.String(), OrgID: p.OrgID, ProjectID: b.ProjectID, BackupID: id, RequestID: in.RequestID, Policy: in.Policy, CreatedBy: p.UserID}, guard)
	if err != nil {
		return nil, err
	}
	return s.publishRetirement(ctx, p, r, "request")
}

func (s *Service) RetryRetirement(ctx context.Context, p security.Principal, id string, in RetirementRetryInput) (*Retirement, error) {
	r, err := s.Retirements.GetRetirement(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if r.OrgID != p.OrgID || p.OrgID == "" {
		return nil, security.ErrForbidden
	}
	guard := s.retirementGuard(p, r.ProjectID, r.Object)
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	if !requestKey.MatchString(in.RequestID) {
		return nil, ErrInvalid
	}
	r, err = s.Retirements.RetryRetirement(ctx, p.OrgID, id, p.UserID, in.RequestID, guard)
	if err != nil {
		return nil, err
	}
	return s.publishRetirement(ctx, p, r, "retry")
}

func (s *Service) retirementGuard(p security.Principal, project string, ref backupfiles.Object) RetirementGuard {
	return func(ctx context.Context) error {
		if p.OrgID == "" || p.UserID == "" {
			return security.ErrForbidden
		}
		if err := s.Authz.Require(ctx, p, security.BackupRetire, project); err != nil {
			return err
		}
		if err := s.Files.CanDelete(ref); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		return nil
	}
}

func (s *Service) publishRetirement(ctx context.Context, p security.Principal, r *Retirement, action string) (*Retirement, error) {
	s.auditRetirement(ctx, p, r, action, false)
	if r.Status == "queued" {
		return r, s.EnqueueRetirement(ctx, p.OrgID, r.ID)
	}
	return r, nil
}

func (s *Service) RetirementHistory(ctx context.Context, p security.Principal, id string, in RetirementHistoryInput) (*RetirementHistory, error) {
	if _, err := s.GetRetirement(ctx, p, id); err != nil {
		return nil, err
	}
	return s.Retirements.RetirementHistory(ctx, p.OrgID, id, in)
}

func (s *Service) GetRetirement(ctx context.Context, p security.Principal, id string) (*Retirement, error) {
	r, err := s.Retirements.GetRetirement(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if r.OrgID != p.OrgID || p.OrgID == "" {
		return nil, security.ErrForbidden
	}
	if checkErr := s.Authz.Require(ctx, p, security.BackupRead, r.ProjectID); checkErr != nil {
		return nil, checkErr
	}
	return r, nil
}

func (s *Service) RunRetirement(ctx context.Context, org, id string) error {
	key, err := typeid.Generate("retireattempt")
	if err != nil {
		return err
	}
	r, err := s.Retirements.ClaimRetirement(ctx, org, id, key.String())
	if errors.Is(err, ErrConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Minute)
	defer cancel()
	p := security.Principal{OrgID: org, UserID: r.ExecutionBy}
	err = s.Authz.Require(ctx, p, security.BackupRetire, r.ProjectID)
	if err == nil {
		err = s.verifyRetirement(ctx, org, id, r)
	}
	if err == nil {
		err = s.Authz.Require(ctx, p, security.BackupRetire, r.ProjectID)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = s.Files.Delete(ctx, r.Object)
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer stop()
	err = errors.Join(err, s.Retirements.FinishRetirement(finish, r, err))
	s.auditRetirement(finish, p, r, "finish", err != nil)
	return err
}

func (s *Service) verifyRetirement(ctx context.Context, org, id string, r *Retirement) error {
	if org == "" || r.OrgID != org || r.ID != id || r.CreatedBy == "" || r.ExecutionBy == "" || r.BackupID == r.WitnessID {
		return ErrConflict
	}
	candidate, err := s.Store.Get(ctx, org, r.BackupID)
	if err != nil {
		return err
	}
	witness, err := s.Store.Get(ctx, org, r.WitnessID)
	if err != nil {
		return err
	}
	if candidate.Status != "retiring" || candidate.ProjectID != r.ProjectID || witness.ProjectID != r.ProjectID || candidate.Object != r.Object || witness.Object != r.WitnessObject || candidate.Object.Key != candidate.objectKey() {
		return ErrConflict
	}
	for _, b := range []*Backup{candidate, witness} {
		source, scopeErr := retirementSourceKey(b)
		if scopeErr != nil || source != r.SourceKey {
			return ErrConflict
		}
	}
	if verifiedRetentionPoint(time.Now().UTC(), []RetentionEntry{{Backup: *witness}}) == nil || r.WitnessObject.DestinationID == "" {
		return ErrConflict
	}
	if checkErr := s.Files.CanDelete(r.Object); checkErr != nil {
		return checkErr
	}
	// The intent holds this source group while we verify the retained object's
	// current bytes. Historical SQL verification alone cannot prove availability.
	return s.Files.CopyVerified(ctx, r.WitnessObject, io.Discard)
}

func (s *Service) reconcileRetirements(ctx context.Context) error {
	pending, err := s.Retirements.PendingRetirements(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, r := range pending {
		if err = s.EnqueueRetirement(ctx, r.OrgID, r.ID); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *Service) auditRetirement(ctx context.Context, p security.Principal, r *Retirement, action string, failed bool) {
	if s.Audit == nil {
		return
	}
	severity, outcome := "info", "success"
	if failed {
		severity, outcome = "warning", "failure"
	}
	s.Audit.Record(ctx, projects.AuditEvent{Action: "service.backup.retirement." + action, Severity: severity, Outcome: outcome, ProjectID: r.ProjectID, Principal: p, Attributes: map[string]any{"retirement_id": r.ID, "backup_id": r.BackupID, "witness_id": r.WitnessID, "attempt": r.Attempt, "requested_by": r.CreatedBy, "failed": failed}})
}
