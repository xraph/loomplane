package federation

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/agentinstall"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// Recover only observes a saved write intent. It never creates configuration,
// reissues enrollment or grants runtime authority, including after consumption.
func (s *LaunchService) Recover(ctx context.Context, p security.Principal, destinationID, id string) (*LaunchDelivery, error) {
	if s == nil || s.attempts == nil || s.installations == nil || s.inspect == nil || p.OrgID != s.org {
		return nil, ErrDisabled
	}
	if err := s.installations.authorize(ctx, p, destinationID, true); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	attempt, err := s.attempts.GetLaunch(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if attempt.DestinationID != destinationID {
		return nil, security.ErrForbidden
	}
	d, err := s.authorizeAttempt(ctx, p, attempt)
	if err != nil {
		return nil, err
	}
	if attempt.State == LaunchDelivered {
		return launchResult(attempt), nil
	}
	cm, err := s.inspect(ctx, *d, attempt)
	if err != nil {
		return nil, err
	}
	return s.confirmDelivery(ctx, p, attempt, cm)
}
func (s *LaunchService) authorizeAttempt(ctx context.Context, p security.Principal, a *LaunchAttempt) (*domain.Destination, error) {
	if a == nil || a.OrgID != p.OrgID || p.OrgID != s.org {
		return nil, security.ErrForbidden
	}
	if err := s.installations.authorize(ctx, p, a.DestinationID, true); err != nil {
		return nil, err
	}
	d, err := s.installations.destinations.verifiedDestination(ctx, p.OrgID, a.DestinationID)
	if err != nil {
		return nil, err
	}
	installation, err := s.installations.store.GetInstallation(ctx, p.OrgID, d.ID)
	if err != nil {
		return nil, err
	}
	if a.Scope != d.Scope || a.DestinationGeneration != d.Generation || a.IdentityRevision != d.Identity.Revision() || installation.Generation != d.Generation || !profileMatchesDestination(installation.Profile, *d) || installation.Profile.PodUID != a.PodUID || installation.Profile.PodName != a.ConfigMapName {
		return nil, ErrIdentityChanged
	}
	return d, nil
}
func (s *LaunchService) confirmDelivery(ctx context.Context, p security.Principal, a *LaunchAttempt, cm *corev1.ConfigMap) (*LaunchDelivery, error) {
	digest, err := agentinstall.ConfigMapDigest(cm)
	if err != nil || cm.UID == "" || digest != a.Digest || cm.Name != a.ConfigMapName || cm.Namespace != a.Namespace {
		return nil, ErrIdentityChanged
	}
	guard := func(ctx context.Context) error { _, checkErr := s.authorizeAttempt(ctx, p, a); return checkErr }
	confirmed, err := s.attempts.ConfirmLaunch(ctx, p.OrgID, a.ID, a.Generation, string(cm.UID), p.UserID, guard)
	if err != nil {
		return nil, err
	}
	if audit := s.installations.destinations.audit; audit != nil {
		audit.Record(ctx, projects.AuditEvent{Action: "federation.launch.deliver", Severity: "info", ProjectID: a.ProjectID, Principal: p, Attributes: map[string]any{"destination_id": a.DestinationID, "enrollment_id": a.EnrollmentID, "attempt_id": a.ID, "pod_uid": a.PodUID, "config_map_uid": confirmed.ConfigMapUID}})
	}
	return launchResult(confirmed), nil
}
func launchResult(a *LaunchAttempt) *LaunchDelivery {
	return &LaunchDelivery{DestinationID: a.DestinationID, EnrollmentID: a.EnrollmentID, PodUID: a.PodUID, Namespace: a.Namespace, ConfigMapName: a.ConfigMapName, ConfigMapUID: a.ConfigMapUID}
}
func (s *LaunchService) inspectKubernetes(ctx context.Context, d domain.Destination, a *LaunchAttempt) (*corev1.ConfigMap, error) {
	client, err := openInstallationKubernetes(ctx, s.credentials, s.org, d)
	if err != nil {
		return nil, err
	}
	if client.Config.Namespace != a.Namespace {
		return nil, ErrIdentityChanged
	}
	ns, err := client.Kube.CoreV1().Namespaces().Get(ctx, a.Namespace, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if ns.DeletionTimestamp != nil || string(ns.UID) != d.Identity.NamespaceUID {
		return nil, ErrIdentityChanged
	}
	pod, err := client.Kube.CoreV1().Pods(a.Namespace).Get(ctx, a.ConfigMapName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if pod.DeletionTimestamp != nil || string(pod.UID) != a.PodUID {
		return nil, ErrIdentityChanged
	}
	return client.Kube.CoreV1().ConfigMaps(a.Namespace).Get(ctx, a.ConfigMapName, metav1.GetOptions{})
}
