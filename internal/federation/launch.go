package federation

import (
	"context"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/agentinstall"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type ReadyEnrollmentStore interface {
	Ready(context.Context, string, string) (*workloadidentity.EnrollmentRecord, error)
}
type LaunchSigningKey func(context.Context, domain.Scope) (access.SnapshotKey, error)

// LaunchDeliveryInput names existing server-owned resources. It cannot select
// credentials, identity selectors, service targets or signing trust.
type LaunchDeliveryInput struct {
	DestinationID string `json:"destination_id"`
	EnrollmentID  string `json:"enrollment_id"`
	AttemptID     string `json:"attempt_id"`
}
type LaunchDelivery struct {
	DestinationID string `json:"destination_id"`
	EnrollmentID  string `json:"enrollment_id"`
	PodUID        string `json:"pod_uid"`
	Namespace     string `json:"namespace"`
	ConfigMapName string `json:"config_map_name"`
	ConfigMapUID  string `json:"config_map_uid"`
}

type LaunchService struct {
	installations *InstallationService
	enrollments   ReadyEnrollmentStore
	credentials   InstallationCredentials
	keys          LaunchSigningKey
	cfg           config.FederationLaunchConfig
	org           string
	deliver       func(context.Context, *agentinstall.KubernetesPlan, domain.Destination, workloadidentity.EnrollmentRecord, agentinstall.LaunchSettings, func(context.Context) error) (*corev1.ConfigMap, error)
}

func NewLaunchService(installations *InstallationService, enrollments ReadyEnrollmentStore, credentials InstallationCredentials, keys LaunchSigningKey, cfg config.FederationLaunchConfig, org string) *LaunchService {
	s := &LaunchService{installations: installations, enrollments: enrollments, credentials: credentials, keys: keys, cfg: cfg, org: org}
	s.deliver = s.deliverKubernetes
	return s
}

func (s *LaunchService) load(ctx context.Context, p security.Principal, in LaunchDeliveryInput) (*domain.Destination, *KubernetesInstallation, *workloadidentity.EnrollmentRecord, error) {
	if s == nil || !s.cfg.Enabled || s.cfg.Validate() != nil || s.installations == nil || s.installations.destinations == nil || s.enrollments == nil || s.keys == nil || s.deliver == nil {
		return nil, nil, nil, ErrDisabled
	}
	if p.OrgID != s.org {
		return nil, nil, nil, security.ErrForbidden
	}
	if err := s.installations.authorize(ctx, p, in.DestinationID, true); err != nil {
		return nil, nil, nil, err
	}
	destinations := s.installations.destinations
	d, err := destinations.verifiedDestination(ctx, p.OrgID, in.DestinationID)
	if err != nil {
		return nil, nil, nil, err
	}
	if !d.Capabilities.Supports(domain.WorkloadAttestation, d.Identity.Revision(), destinations.now()) {
		return nil, nil, nil, ErrUnverified
	}
	installation, err := s.installations.store.GetInstallation(ctx, p.OrgID, d.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	if installation.Generation != d.Generation || !profileMatchesDestination(installation.Profile, *d) {
		return nil, nil, nil, ErrIdentityChanged
	}
	r, err := s.enrollments.Ready(ctx, p.OrgID, in.EnrollmentID)
	if err != nil {
		return nil, nil, nil, err
	}
	// Verified service-target assignment is separate. Until it exists this wired
	// path delivers only consumer installations, matching InstallationService.Resolve.
	if r == nil || r.ID != in.EnrollmentID || r.Binding.Agent.Scope != d.Scope || r.Binding.Agent.DestinationID != d.ID || r.Registration.Agent != r.Binding.Agent || r.Registration.DestinationGeneration != d.Generation || len(r.Registration.Exports) != 0 || runtimecatalog.ValidateRegistration(r.Registration) != nil || !installationBindingMatches(r.Binding, *d, installation.Profile) || !r.ExpiresAt.After(time.Now()) {
		return nil, nil, nil, ErrIdentityChanged
	}
	return d, installation, r, nil
}

// Deliver publishes once to an existing verified pod. It neither reissues an
// enrollment nor retries uncertain Kubernetes writes. Recovery must inspect and
// retire that attempt before preparing a replacement.
func (s *LaunchService) Deliver(ctx context.Context, p security.Principal, in LaunchDeliveryInput) (*LaunchDelivery, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	d, installation, r, err := s.load(ctx, p, in)
	if err != nil {
		return nil, err
	}
	if s.installations.verify == nil {
		return nil, ErrUnverified
	}
	fresh, err := s.installations.verify(ctx, *d, installation.Profile)
	if err != nil {
		return nil, err
	}
	if !installationBindingMatches(fresh, *d, installation.Profile) || fresh.ParentURI != r.Binding.ParentURI || fresh.Namespace != r.Binding.Namespace || fresh.NamespaceUID != r.Binding.NamespaceUID {
		return nil, ErrIdentityChanged
	}
	plan, err := agentinstall.NewKubernetesPlan(fresh.Namespace, in.AttemptID, s.cfg.AgentImage, false)
	if err != nil {
		return nil, err
	}
	pod := plan.Pod()
	if pod.Name != installation.Profile.PodName || pod.Spec.ServiceAccountName != installation.Profile.ServiceAccount || installation.Profile.Container != agentinstall.ContainerName {
		return nil, ErrIdentityChanged
	}
	key, err := s.keys(ctx, d.Scope)
	if err != nil {
		return nil, err
	}
	guard := func(ctx context.Context) error {
		current, currentInstallation, currentEnrollment, checkErr := s.load(ctx, p, in)
		if checkErr != nil {
			return checkErr
		}
		if current.Generation != d.Generation || current.Identity != d.Identity || current.ConnectionID != d.ConnectionID || current.Scope != d.Scope || !reflect.DeepEqual(currentInstallation, installation) || !reflect.DeepEqual(currentEnrollment, r) {
			return ErrIdentityChanged
		}
		currentKey, keyErr := s.keys(ctx, d.Scope)
		if keyErr != nil {
			return keyErr
		}
		if currentKey.ID() != key.ID() || !currentKey.NotBefore.Equal(key.NotBefore) || !currentKey.NotAfter.Equal(key.NotAfter) {
			return ErrIdentityChanged
		}
		return nil
	}
	if guardErr := guard(ctx); guardErr != nil {
		return nil, guardErr
	}
	generation := s.cfg.TrustGeneration
	if generation < 1 {
		return nil, ErrInvalid
	}
	settings := agentinstall.LaunchSettings{ControlEndpoint: s.cfg.ControlEndpoint, TrustGeneration: uint64(generation), SigningKeys: []access.SnapshotKey{key}}
	cm, err := s.deliver(ctx, plan, *d, *r, settings, guard)
	if err != nil {
		return nil, err
	}
	if cm == nil || cm.UID == "" || cm.Name != pod.Name || cm.Namespace != pod.Namespace {
		return nil, ErrIdentityChanged
	}
	result := &LaunchDelivery{DestinationID: d.ID, EnrollmentID: r.ID, PodUID: installation.Profile.PodUID, Namespace: cm.Namespace, ConfigMapName: cm.Name, ConfigMapUID: string(cm.UID)}
	if audit := s.installations.destinations.audit; audit != nil {
		audit.Record(ctx, projects.AuditEvent{Action: "federation.launch.deliver", Severity: "info", ProjectID: d.ProjectID, Principal: p, Attributes: map[string]any{"destination_id": d.ID, "enrollment_id": r.ID, "pod_uid": result.PodUID, "config_map_uid": result.ConfigMapUID}})
	}
	return result, nil
}
func (s *LaunchService) deliverKubernetes(ctx context.Context, plan *agentinstall.KubernetesPlan, d domain.Destination, r workloadidentity.EnrollmentRecord, settings agentinstall.LaunchSettings, guard func(context.Context) error) (*corev1.ConfigMap, error) {
	if err := guard(ctx); err != nil {
		return nil, err
	}
	client, err := openInstallationKubernetes(ctx, s.credentials, s.org, d)
	if err != nil {
		return nil, err
	}
	return plan.Deliver(ctx, client.Kube, r, settings, guard)
}
