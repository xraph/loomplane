package federation

import (
	"context"
	"errors"
	"math"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/agentinstall"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// PreparedLaunchInput supplies only the trusted SPIRE installation profile.
// Workload identity, resource names and launch trust come from saved records.
type PreparedLaunchInput struct {
	DestinationID       string `json:"destination_id"`
	PreparationID       string `json:"preparation_id"`
	SPIRECluster        string `json:"spire_cluster"`
	SPIRENamespace      string `json:"spire_namespace"`
	SPIREServiceAccount string `json:"spire_service_account"`
}
type PreparationLaunchService struct {
	preparation *PreparationService
	enrollment  *EnrollmentService
}

func NewPreparationLaunchService(preparation *PreparationService, enrollment *EnrollmentService) *PreparationLaunchService {
	return &PreparationLaunchService{preparation: preparation, enrollment: enrollment}
}

// Launch composes the durable preparation, installation, enrollment and launch
// records. Success confirms configuration delivery, not agent or service health.
func (s *PreparationLaunchService) Launch(ctx context.Context, p security.Principal, in PreparedLaunchInput) (*LaunchDelivery, error) {
	if s == nil || s.preparation == nil || s.preparation.launch == nil || s.preparation.launch.installations == nil || s.preparation.launch.attempts == nil || s.enrollment == nil || s.enrollment.destinations != s.preparation.launch.installations.destinations {
		return nil, ErrDisabled
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	d, err := s.preparation.destination(ctx, p, in.DestinationID)
	if err != nil {
		return nil, err
	}
	r, err := s.preparation.store.GetPreparation(ctx, p.OrgID, in.PreparationID)
	if err != nil {
		return nil, err
	}
	if r.DestinationID != in.DestinationID {
		return nil, security.ErrForbidden
	}
	if r.State == PreparationRetiring || r.State == PreparationRetired {
		return nil, ErrConflict
	}
	if r.State != PreparationPrepared {
		r, err = s.preparation.Resume(ctx, p, in.DestinationID, in.PreparationID)
		if err != nil {
			return nil, err
		}
		d, err = s.preparation.destination(ctx, p, in.DestinationID)
		if err != nil {
			return nil, err
		}
	}
	launch := s.preparation.launch
	if r.Scope != d.Scope || r.IdentityRevision != d.Identity.Revision() || r.NamespaceUID != d.Identity.NamespaceUID || r.Image != launch.cfg.AgentImage || r.DestinationGeneration == math.MaxInt64 {
		return nil, ErrIdentityChanged
	}
	name := "loomplane-agent-" + r.ID
	profile := KubernetesAttestationProfile{Scope: d.Scope, DestinationID: d.ID, ConnectionID: d.ConnectionID, IdentityRevision: d.Identity.Revision(), PodName: name, PodUID: r.PodUID, ServiceAccount: name, Container: agentinstall.ContainerName, SPIRECluster: in.SPIRECluster, SPIRENamespace: in.SPIRENamespace, SPIREServiceAccount: in.SPIREServiceAccount}
	if !profileMatchesDestination(profile, *d) {
		return nil, ErrInvalid
	}
	installation, readErr := launch.installations.store.GetInstallation(ctx, p.OrgID, d.ID)
	if readErr != nil && !errors.Is(readErr, ErrNotFound) {
		return nil, readErr
	}
	if installation != nil {
		if installation.Profile != profile || installation.Generation != d.Generation || d.Generation != r.DestinationGeneration+1 {
			return nil, ErrIdentityChanged
		}
	} else if d.Generation != r.DestinationGeneration {
		return nil, ErrIdentityChanged
	}
	attempt, readErr := launch.attempts.GetLaunch(ctx, p.OrgID, r.ID)
	if readErr != nil && !errors.Is(readErr, ErrNotFound) {
		return nil, readErr
	}
	if attempt != nil {
		// Delivery may have succeeded and the agent may already have consumed its
		// handle. Recover the exact write before attempting another enrollment.
		current, checkErr := s.preparation.store.GetPreparation(ctx, p.OrgID, r.ID)
		if checkErr != nil {
			return nil, checkErr
		}
		if installation == nil || current.State != PreparationPrepared || current.EnrollmentID == "" || current.EnrollmentID != attempt.EnrollmentID || attempt.DestinationID != d.ID || attempt.PodUID != r.PodUID {
			return nil, ErrIdentityChanged
		}
		return launch.Recover(ctx, p, d.ID, r.ID)
	}
	if checkErr := s.waiting(ctx, *d, r); checkErr != nil {
		return nil, checkErr
	}
	if installation == nil {
		_, err = launch.installations.Record(ctx, p, InstallationInput{DestinationID: d.ID, DestinationGeneration: d.Generation, ExpectedGeneration: 0, PodName: profile.PodName, PodUID: profile.PodUID, ServiceAccount: profile.ServiceAccount, Container: profile.Container, SPIRECluster: profile.SPIRECluster, SPIRENamespace: profile.SPIRENamespace, SPIREServiceAccount: profile.SPIREServiceAccount})
		if err != nil {
			return nil, err
		}
	}
	enrollment, err := s.enrollment.EnrollPreparation(ctx, p, d.ID, r.ID)
	if err != nil {
		return nil, err
	}
	return launch.Deliver(ctx, p, LaunchDeliveryInput{DestinationID: d.ID, EnrollmentID: enrollment.ID, AttemptID: r.ID})
}

// waiting checks the shared launch requirements before reserving fresh trust.
// An existing or running configuration requires recovery or retirement instead.
func (s *PreparationLaunchService) waiting(ctx context.Context, d domain.Destination, r *Preparation) error {
	client, namespace, err := s.preparation.open(ctx, d)
	if err != nil {
		return err
	}
	if namespace != r.Namespace {
		return ErrIdentityChanged
	}
	if checkErr := verifyPreparationNamespace(ctx, client, r.PreparationIntent); checkErr != nil {
		return checkErr
	}
	plan, err := agentinstall.NewKubernetesPlan(r.Namespace, r.ID, r.Image, false)
	if err != nil {
		return err
	}
	if checkErr := verifyPreparedAccount(ctx, client, plan, r); checkErr != nil {
		return checkErr
	}
	pod, err := client.CoreV1().Pods(r.Namespace).Get(ctx, "loomplane-agent-"+r.ID, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !plan.MatchesPreparedPod(pod, r.NamespaceUID) || !plan.MatchesWaitingPod(pod, r.PodUID, r.NamespaceUID) {
		return ErrUnverified
	}
	if _, err = client.CoreV1().ConfigMaps(r.Namespace).Get(ctx, pod.Name, metav1.GetOptions{}); err == nil {
		return ErrConflict
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
