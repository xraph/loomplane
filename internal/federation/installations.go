package federation

import (
	"context"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// InstallationInput identifies an existing pod for an authorized installation
// operation. Scope, connection and physical identity always come from the destination.
type InstallationInput struct {
	DestinationID         string `json:"destination_id"`
	DestinationGeneration int64  `json:"destination_generation"`
	ExpectedGeneration    int64  `json:"expected_generation"`
	PodName               string `json:"pod_name"`
	PodUID                string `json:"pod_uid"`
	ServiceAccount        string `json:"service_account"`
	Container             string `json:"container"`
	SPIRECluster          string `json:"spire_cluster"`
	SPIRENamespace        string `json:"spire_namespace"`
	SPIREServiceAccount   string `json:"spire_service_account"`
}

type InstallationVerifier func(context.Context, domain.Destination, KubernetesAttestationProfile) (workloadidentity.Binding, error)
type InstallationCredentials interface {
	Credential(context.Context, string, string) (*connections.Connection, []byte, error)
}

// VerifyKubernetesInstallation resolves credentials from the existing connection
// service. It checks their current physical identity before reading cluster evidence.
func VerifyKubernetesInstallation(credentials InstallationCredentials, nodes SPIRENodeReader, trustDomain, org string) InstallationVerifier {
	_, scopeErr := workloadidentity.ControlURIForOrganization(trustDomain, org)
	return func(ctx context.Context, d domain.Destination, p KubernetesAttestationProfile) (workloadidentity.Binding, error) {
		if scopeErr != nil || d.OrgID != org || credentials == nil || nodes == nil || d.Identity.Provider != "kubernetes" || !profileMatchesDestination(p, d) {
			return workloadidentity.Binding{}, ErrUnverified
		}
		client, err := openInstallationKubernetes(ctx, credentials, org, d)
		if err != nil {
			return workloadidentity.Binding{}, err
		}
		resolver := &KubernetesAttestationResolver{client: client, nodes: nodes, profile: p, domain: trustDomain, now: time.Now}
		return resolver.Resolve(ctx, d)
	}
}

type InstallationService struct {
	destinations *Service
	store        InstallationStore
	verify       InstallationVerifier
}

func NewInstallationService(destinations *Service, store InstallationStore, verify InstallationVerifier) *InstallationService {
	return &InstallationService{destinations: destinations, store: store, verify: verify}
}
func (s *InstallationService) authorize(ctx context.Context, p security.Principal, id string, write bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.UserID == "" || p.OrgID == "" {
		return security.ErrUnauthenticated
	}
	if write && p.ReadOnly {
		return security.ErrForbidden
	}
	if s == nil || s.destinations == nil || s.destinations.authz == nil || s.store == nil {
		return ErrDisabled
	}
	if write {
		return s.destinations.authorizeTrustChange(ctx, p, id)
	}
	return s.destinations.authorize(ctx, p, security.DestinationRead, id)
}
func (s *InstallationService) Get(ctx context.Context, p security.Principal, id string) (*KubernetesInstallation, error) {
	if err := s.authorize(ctx, p, id, false); err != nil {
		return nil, err
	}
	return s.store.GetInstallation(ctx, p.OrgID, id)
}

// Record verifies and retains an already installed pod. It performs no Kubernetes
// writes and grants no service access. Replacing the record fences older agents.
func (s *InstallationService) Record(ctx context.Context, p security.Principal, in InstallationInput) (*KubernetesInstallation, error) {
	if err := s.authorize(ctx, p, in.DestinationID, true); err != nil {
		return nil, err
	}
	if in.DestinationGeneration < 1 || in.ExpectedGeneration < 0 || s.verify == nil {
		return nil, ErrInvalid
	}
	d, err := s.destinations.verifiedDestination(ctx, p.OrgID, in.DestinationID)
	if err != nil {
		return nil, err
	}
	if d.Generation != in.DestinationGeneration {
		return nil, ErrConflict
	}
	profile := KubernetesAttestationProfile{Scope: d.Scope, DestinationID: d.ID, ConnectionID: d.ConnectionID, IdentityRevision: d.Identity.Revision(), PodName: in.PodName, PodUID: in.PodUID, ServiceAccount: in.ServiceAccount, Container: in.Container, SPIRECluster: in.SPIRECluster, SPIRENamespace: in.SPIRENamespace, SPIREServiceAccount: in.SPIREServiceAccount}
	if !profileMatchesDestination(profile, *d) {
		return nil, ErrInvalid
	}
	binding, err := s.verify(ctx, *d, profile)
	if err != nil {
		return nil, err
	}
	if !installationBindingMatches(binding, *d, profile) {
		return nil, ErrIdentityChanged
	}
	guard := func(ctx context.Context) error {
		if checkErr := s.authorize(ctx, p, in.DestinationID, true); checkErr != nil {
			return checkErr
		}
		current, checkErr := s.destinations.verifiedDestination(ctx, p.OrgID, in.DestinationID)
		if checkErr != nil {
			return checkErr
		}
		if current.Generation != d.Generation || current.Scope != d.Scope || current.Identity != d.Identity {
			return ErrConflict
		}
		return nil
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	record, err := s.store.PutInstallation(ctx, *d, profile, in.ExpectedGeneration, p.UserID, binding.CheckedAt, guard)
	if err != nil {
		return nil, err
	}
	s.record(ctx, p, "federation.installation.record", record.Profile, record.Generation)
	return record, nil
}
func (s *InstallationService) Retire(ctx context.Context, p security.Principal, id string, generation int64) error {
	guard := func(ctx context.Context) error { return s.authorize(ctx, p, id, true) }
	if err := guard(ctx); err != nil {
		return err
	}
	if generation < 1 {
		return ErrInvalid
	}
	d, err := s.destinations.store.Get(ctx, p.OrgID, id)
	if err != nil {
		return err
	}
	if err := s.store.RetireInstallation(ctx, p.OrgID, id, generation, p.UserID, guard); err != nil {
		return err
	}
	s.record(ctx, p, "federation.installation.retire", KubernetesAttestationProfile{Scope: d.Scope, DestinationID: d.ID}, generation)
	return nil
}

// Resolve is the internal enrollment path. Consumer installations carry no
// exports; publishing requires a separate verified service-target assignment.
func (s *InstallationService) Resolve(ctx context.Context, d domain.Destination) (workloadidentity.Binding, map[string]runtimecatalog.ExportBounds, error) {
	if s == nil || s.store == nil || s.verify == nil {
		return workloadidentity.Binding{}, nil, ErrUnverified
	}
	record, err := s.store.GetInstallation(ctx, d.OrgID, d.ID)
	if err != nil {
		return workloadidentity.Binding{}, nil, err
	}
	if record.Generation != d.Generation || !profileMatchesDestination(record.Profile, d) {
		return workloadidentity.Binding{}, nil, ErrIdentityChanged
	}
	binding, err := s.verify(ctx, d, record.Profile)
	if err != nil {
		return workloadidentity.Binding{}, nil, err
	}
	if !installationBindingMatches(binding, d, record.Profile) {
		return workloadidentity.Binding{}, nil, ErrIdentityChanged
	}
	return binding, nil, nil
}
func profileMatchesDestination(p KubernetesAttestationProfile, d domain.Destination) bool {
	return validAttestationProfile(p) && p.Scope == d.Scope && p.DestinationID == d.ID && p.ConnectionID == d.ConnectionID && p.IdentityRevision == d.Identity.Revision() && d.Identity.Provider == "kubernetes" && !d.Quarantined
}
func installationBindingMatches(b workloadidentity.Binding, d domain.Destination, p KubernetesAttestationProfile) bool {
	now := time.Now()
	return installationBindingIdentityMatches(b, d, p) && !b.CheckedAt.IsZero() && !b.CheckedAt.Before(now.Add(-5*time.Minute)) && !b.CheckedAt.After(now.Add(30*time.Second))
}

// Identity pins remain relevant after the bootstrap's freshness window closes.
// Issuance still requires installationBindingMatches and fresh node evidence.
func installationBindingIdentityMatches(b workloadidentity.Binding, d domain.Destination, p KubernetesAttestationProfile) bool {
	return b.Destination.Scope == d.Scope && b.Destination.ID == d.ID && b.Destination.ConnectionID == d.ConnectionID && b.Destination.Identity == d.Identity && b.Destination.Generation == d.Generation && !b.Destination.Quarantined && b.Method == "kubernetes" && b.PodUID == p.PodUID && b.ServiceAccount == p.ServiceAccount && b.Container == p.Container
}

func (s *InstallationService) record(ctx context.Context, p security.Principal, action string, profile KubernetesAttestationProfile, generation int64) {
	if s.destinations.audit != nil {
		s.destinations.audit.Record(ctx, projects.AuditEvent{Action: action, Severity: "info", ProjectID: profile.ProjectID, Principal: p, Attributes: map[string]any{"destination_id": profile.DestinationID, "installation_generation": generation, "pod_uid": profile.PodUID}})
	}
}
