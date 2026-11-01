package planning

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type MemberPrerequisite = domain.MemberPrerequisite
type TLSRenewalAuthority = domain.TLSRenewalAuthority
type DrainPolicy = domain.DrainPolicy

type MemberPrerequisiteInput struct {
	ID                                 string      `json:"id,omitempty"`
	AttachmentID                       string      `json:"attachment_id"`
	ExpectedRevision                   int64       `json:"expected_revision"`
	TLSSecretName                      string      `json:"tls_secret_name"`
	ExpectedTLSSecretUID               string      `json:"expected_tls_secret_uid"`
	RenewalAuthorityID                 string      `json:"renewal_authority_id"`
	ExpectedRenewalAuthorityGeneration int64       `json:"expected_renewal_authority_generation"`
	DrainPolicy                        DrainPolicy `json:"drain_policy"`
}
type TLSRenewalAuthorityInput struct {
	ID                      string `json:"id,omitempty"`
	ProjectID               string `json:"project_id"`
	AttachmentID            string `json:"attachment_id"`
	ExpectedGeneration      int64  `json:"expected_generation"`
	OwnerRef                string `json:"owner_ref"`
	Method                  string `json:"method"`
	VerificationEvidenceRef string `json:"verification_evidence_ref"`
	Active                  bool   `json:"active"`
}
type TimeoutVerificationInput struct {
	AttachmentID               string      `json:"attachment_id"`
	ExpectedAttachmentRevision int64       `json:"expected_attachment_revision"`
	ConfigRevision             string      `json:"config_revision"`
	DrainPolicy                DrainPolicy `json:"drain_policy"`
	EvidenceRef                string      `json:"evidence_ref"`
}
type GroupAttachmentScope struct {
	Attachment     *cloudinfra.RuntimeAttachment
	TargetID       string
	TargetRevision int64
	GrantRevision  int64
}

// MemberPrerequisiteService owns manager declarations and trusted live reads.
// Neither a declaration nor a saved review authorizes workload effects.
type MemberPrerequisiteService struct {
	Variables GroupVariableSelection
	Store     *PgStore
	Authz     Authorizer
	Projects  Projects
	Principal CapacityPrincipalVerifier
	Observer  MemberPrerequisiteObserver
	Audit     projects.Auditor
	Now       func() time.Time
}

func (s *MemberPrerequisiteService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s *MemberPrerequisiteService) authorize(ctx context.Context, p security.Principal, project string) error {
	if s.Store == nil || s.Authz == nil || s.Projects == nil || p.UserID == "" || p.OrgID == "" || p.ReadOnly || project == "" {
		return security.ErrForbidden
	}
	if s.Principal != nil {
		if err := s.Principal.ValidatePrincipal(ctx, p); err != nil {
			return err
		}
	} else if p.KeyID != "" {
		return security.ErrForbidden
	}
	if err := s.Authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID); err != nil {
		return err
	}
	if err := s.Authz.Require(ctx, p, security.ProjectRead, project); err != nil {
		return err
	}
	_, err := s.Projects.GetProject(ctx, p.OrgID, project)
	return err
}
func (s *MemberPrerequisiteService) record(ctx context.Context, p security.Principal, project, action, id string, revision int64) {
	if s.Audit != nil {
		s.Audit.Record(ctx, projects.AuditEvent{Action: action, Severity: "info", ProjectID: project, Principal: p, Attributes: map[string]any{"id": id, "revision": revision}})
	}
}
func (s *MemberPrerequisiteService) SaveRenewalAuthority(ctx context.Context, p security.Principal, in TLSRenewalAuthorityInput) (*TLSRenewalAuthority, error) {
	if err := s.authorize(ctx, p, in.ProjectID); err != nil {
		return nil, err
	}
	if in.ExpectedGeneration < 0 || in.OwnerRef == "" || len(in.OwnerRef) > 256 || in.VerificationEvidenceRef == "" || len(in.VerificationEvidenceRef) > 512 || in.Method != "external-dns01" && in.Method != "external-equivalent" || in.ID == "" && in.ExpectedGeneration != 0 || in.ID != "" && in.ExpectedGeneration < 1 {
		return nil, domain.ErrGroupProfile
	}
	scope, err := s.Store.GroupAttachmentScope(ctx, p.OrgID, in.ProjectID, in.AttachmentID, "")
	if err != nil {
		return nil, err
	}
	now := s.now()
	id := in.ID
	if id == "" {
		id = typeid.MustGenerate("tlra").String()
	}
	saved := &TLSRenewalAuthority{ID: id, OrgID: p.OrgID, SchemaVersion: 1, Generation: in.ExpectedGeneration + 1, OwnerRef: in.OwnerRef, Method: in.Method, VerificationEvidenceRef: in.VerificationEvidenceRef, VerifiedBy: p.UserID, VerifiedAt: now, Active: in.Active, CreatedAt: now, UpdatedAt: now}
	if saveErr := s.Store.saveGroupAuthority(ctx, in.ProjectID, scope, saved, in.ExpectedGeneration); saveErr != nil {
		return nil, saveErr
	}
	s.record(ctx, p, in.ProjectID, "placement_group.renewal_authority.save", id, saved.Generation)
	return saved, nil
}
func (s *MemberPrerequisiteService) VerifyTimeout(ctx context.Context, p security.Principal, project string, in TimeoutVerificationInput) (string, error) {
	if err := s.authorize(ctx, p, project); err != nil {
		return "", err
	}
	if in.ConfigRevision == "" || len(in.ConfigRevision) > 128 || in.EvidenceRef == "" || len(in.EvidenceRef) > 512 || in.DrainPolicy.Mode != "bounded" || in.DrainPolicy.Validate() != nil {
		return "", domain.ErrGroupProfile
	}
	scope, err := s.Store.GroupAttachmentScope(ctx, p.OrgID, project, in.AttachmentID, "")
	if err != nil {
		return "", err
	}
	if scope.Attachment.Revision != in.ExpectedAttachmentRevision {
		return "", ErrConflict
	}
	id := typeid.MustGenerate("gtimeout").String()
	if saveErr := s.Store.saveGroupTimeout(ctx, p, project, scope, id, in); saveErr != nil {
		return "", saveErr
	}
	s.record(ctx, p, project, "placement_group.timeout.verify", id, 1)
	return id, nil
}
func (s *MemberPrerequisiteService) Save(ctx context.Context, p security.Principal, project string, in MemberPrerequisiteInput) (*MemberPrerequisite, error) {
	if err := s.authorize(ctx, p, project); err != nil {
		return nil, err
	}
	if in.DrainPolicy.Validate() != nil || in.ExpectedRevision < 0 || in.TLSSecretName == "" || in.ExpectedTLSSecretUID == "" || in.ExpectedRenewalAuthorityGeneration < 1 || in.ID == "" && in.ExpectedRevision != 0 || in.ID != "" && in.ExpectedRevision < 1 {
		return nil, domain.ErrGroupProfile
	}
	scope, err := s.Store.GroupAttachmentScope(ctx, p.OrgID, project, in.AttachmentID, "")
	if err != nil {
		return nil, err
	}
	authority, err := s.Store.groupAuthority(ctx, p.OrgID, project, in.AttachmentID, in.RenewalAuthorityID)
	if err != nil {
		return nil, err
	}
	if !authority.Active || authority.Generation != in.ExpectedRenewalAuthorityGeneration {
		return nil, ErrConflict
	}
	if s.Observer == nil {
		return nil, ErrPrerequisiteObservation
	}
	observation, err := s.Observer.Observe(ctx, scope.Attachment, in.TLSSecretName)
	if err != nil {
		return nil, err
	}
	if observation.SecretUID != in.ExpectedTLSSecretUID || observation.NamespaceUID != scope.Attachment.NamespaceUID || observation.Namespace != scope.Attachment.Namespace || observation.SecretName != in.TLSSecretName {
		return nil, ErrConflict
	}
	now := s.now()
	if observation.CheckedAt.IsZero() || observation.CheckedAt.After(now) || !observation.CheckedAt.Add(time.Minute).After(now) || observation.NotBefore.After(now) || !observation.NotAfter.After(now) || len(observation.Hostnames) == 0 {
		return nil, ErrPrerequisiteObservation
	}
	if authorizationErr := s.authorize(ctx, p, project); authorizationErr != nil {
		return nil, authorizationErr
	}
	id := in.ID
	if id == "" {
		id = typeid.MustGenerate("mpr").String()
	}
	saved := &MemberPrerequisite{ID: id, OrgID: p.OrgID, ProjectID: project, AttachmentID: in.AttachmentID, SchemaVersion: 1, Revision: in.ExpectedRevision + 1, Namespace: observation.Namespace, NamespaceUID: observation.NamespaceUID, TLSSecretName: observation.SecretName, TLSSecretUID: observation.SecretUID, Hostnames: observation.Hostnames, RenewalAuthorityID: authority.ID, RenewalAuthorityGeneration: authority.Generation, DrainPolicy: in.DrainPolicy, CreatedAt: now, UpdatedAt: now}
	if in.DrainPolicy.Mode == "bounded" {
		saved.TimeoutVerificationID, _, err = s.Store.groupTimeout(ctx, p.OrgID, project, in.AttachmentID, observation.NamespaceUID, in.DrainPolicy, "")
		if err != nil {
			return nil, err
		}
	}
	if saveErr := s.Store.saveGroupPrerequisite(ctx, scope, saved, observation, in.ExpectedRevision); saveErr != nil {
		return nil, saveErr
	}
	s.record(ctx, p, project, "placement_group.prerequisite.save", id, saved.Revision)
	return saved, nil
}

// SaveDestinationMapping pins the granted target behind a federation alias.
func (s *MemberPrerequisiteService) SaveDestinationMapping(ctx context.Context, p security.Principal, scope domain.Scope, destination, target, attachment string, expected int64) error {
	if p.OrgID != scope.OrgID {
		return security.ErrForbidden
	}
	if err := s.authorize(ctx, p, scope.ProjectID); err != nil {
		return err
	}
	if expected < 0 {
		return domain.ErrGroupProfile
	}
	facts, err := s.Store.GroupAttachmentScope(ctx, p.OrgID, scope.ProjectID, attachment, target)
	if err != nil {
		return err
	}
	d, err := s.Store.GetFederationDestination(ctx, p.OrgID, destination, s.now())
	if err != nil {
		return err
	}
	if d.Scope != scope || d.Quarantined || d.ConnectionID != facts.Attachment.RuntimeConnectionID || d.Identity.NamespaceUID != facts.Attachment.NamespaceUID || d.Identity.Account != facts.Attachment.TrustFingerprint {
		return domain.ErrGroupProfile
	}
	if saveErr := s.Store.saveGroupMapping(ctx, scope, destination, facts, expected); saveErr != nil {
		return saveErr
	}
	s.record(ctx, p, scope.ProjectID, "placement_group.destination.save", destination, expected+1)
	return nil
}

func groupStoreError(err error) error {
	if errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	return err
}
