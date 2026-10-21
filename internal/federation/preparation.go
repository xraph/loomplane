package federation

import (
	"context"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/xraph/loomplane/internal/agentinstall"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type PreparationService struct {
	launch *LaunchService
	store  PreparationStore
	open   func(context.Context, domain.Destination) (kubernetes.Interface, string, error)
}

func NewPreparationService(launch *LaunchService, store PreparationStore) *PreparationService {
	s := &PreparationService{launch: launch, store: store}
	s.open = func(ctx context.Context, d domain.Destination) (kubernetes.Interface, string, error) {
		c, err := openInstallationKubernetes(ctx, launch.credentials, launch.org, d)
		if err != nil {
			return nil, "", err
		}
		return c.Kube, c.Config.Namespace, nil
	}
	return s
}
func (s *PreparationService) destination(ctx context.Context, p security.Principal, id string) (*domain.Destination, error) {
	if s == nil || s.launch == nil || s.launch.installations == nil || s.store == nil || s.open == nil || !s.launch.cfg.Enabled || s.launch.cfg.Validate() != nil {
		return nil, ErrDisabled
	}
	if p.OrgID != s.launch.org {
		return nil, security.ErrForbidden
	}
	if err := s.launch.installations.authorize(ctx, p, id, true); err != nil {
		return nil, err
	}
	d, err := s.launch.installations.destinations.verifiedDestination(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if !d.Capabilities.Supports(domain.WorkloadAttestation, d.Identity.Revision(), time.Now()) {
		return nil, ErrUnverified
	}
	return d, nil
}

// Start saves server-owned names and configuration before provisioning. Errors
// after reservation return the attempt so callers can resume by its saved ID.
func (s *PreparationService) Start(ctx context.Context, p security.Principal, destinationID string) (*Preparation, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	d, err := s.destination(ctx, p, destinationID)
	if err != nil {
		return nil, err
	}
	client, namespace, err := s.open(ctx, *d)
	if err != nil {
		return nil, err
	}
	in := PreparationIntent{ID: uuid.NewString(), Namespace: namespace, NamespaceUID: d.Identity.NamespaceUID, Image: s.launch.cfg.AgentImage}
	if !validPreparationIntent(in) {
		return nil, ErrInvalid
	}
	if checkErr := verifyPreparationNamespace(ctx, client, in); checkErr != nil {
		return nil, checkErr
	}
	guard := func(ctx context.Context) error {
		current, checkErr := s.destination(ctx, p, d.ID)
		if checkErr != nil {
			return checkErr
		}
		if current.Scope != d.Scope || current.Generation != d.Generation || current.Identity != d.Identity || current.ConnectionID != d.ConnectionID {
			return ErrIdentityChanged
		}
		return nil
	}
	r, err := s.store.ReservePreparation(ctx, *d, in, p.UserID, guard)
	if err != nil {
		return nil, err
	}
	s.record(ctx, p, r)
	return s.resume(ctx, p, r)
}
func (s *PreparationService) Current(ctx context.Context, p security.Principal, destinationID string) (*Preparation, error) {
	if _, err := s.destination(ctx, p, destinationID); err != nil {
		return nil, err
	}
	return s.store.CurrentPreparation(ctx, p.OrgID, destinationID)
}
func (s *PreparationService) Resume(ctx context.Context, p security.Principal, destinationID, id string) (*Preparation, error) {
	if _, err := s.destination(ctx, p, destinationID); err != nil {
		return nil, err
	}
	r, err := s.store.GetPreparation(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if r.DestinationID != destinationID {
		return nil, security.ErrForbidden
	}
	return s.resume(ctx, p, r)
}
func (s *PreparationService) resume(ctx context.Context, p security.Principal, r *Preparation) (*Preparation, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	authorize := func(ctx context.Context) (*domain.Destination, error) {
		d, err := s.destination(ctx, p, r.DestinationID)
		if err != nil {
			return nil, err
		}
		if d.Scope != r.Scope || d.Generation != r.DestinationGeneration || d.Identity.Revision() != r.IdentityRevision || d.Identity.NamespaceUID != r.NamespaceUID || r.Image != s.launch.cfg.AgentImage {
			return nil, ErrIdentityChanged
		}
		return d, nil
	}
	d, err := authorize(ctx)
	if err != nil {
		return r, err
	}
	if r.State == PreparationPrepared {
		return r, nil
	}
	client, namespace, err := s.open(ctx, *d)
	if err != nil {
		return r, err
	}
	if namespace != r.Namespace {
		return r, ErrIdentityChanged
	}
	if checkErr := verifyPreparationNamespace(ctx, client, r.PreparationIntent); checkErr != nil {
		return r, checkErr
	}
	plan, err := agentinstall.NewKubernetesPlan(r.Namespace, r.ID, r.Image, false)
	if err != nil {
		return r, err
	}
	guard := func(ctx context.Context) error {
		if _, checkErr := authorize(ctx); checkErr != nil {
			return checkErr
		}
		current, checkErr := s.store.GetPreparation(ctx, p.OrgID, r.ID)
		if checkErr != nil {
			return checkErr
		}
		if current.Generation != r.Generation || current.State != r.State || current.PreparationIntent != r.PreparationIntent {
			return ErrConflict
		}
		return nil
	}
	advance := func(target, uid string) error {
		next, stepErr := s.store.AdvancePreparation(ctx, p.OrgID, r.ID, r.Generation, target, uid, p.UserID, guard)
		if stepErr != nil {
			return stepErr
		}
		r = next
		s.record(ctx, p, r)
		return nil
	}
	if r.State == PreparationReserved || r.State == PreparationAccountPending {
		create := r.State == PreparationReserved
		if create {
			if checkErr := advance(PreparationAccountPending, ""); checkErr != nil {
				return r, checkErr
			}
		}
		if checkErr := guard(ctx); checkErr != nil {
			return r, checkErr
		}
		var account *corev1.ServiceAccount
		if create {
			desired, _, buildErr := plan.PreparationResources(r.NamespaceUID)
			if buildErr != nil {
				return r, buildErr
			}
			account, err = client.CoreV1().ServiceAccounts(r.Namespace).Create(ctx, desired, metav1.CreateOptions{})
		} else {
			account, err = client.CoreV1().ServiceAccounts(r.Namespace).Get(ctx, "loomplane-agent-"+r.ID, metav1.GetOptions{})
		}
		if apierrors.IsNotFound(err) {
			return r, ErrInUse
		}
		if err != nil {
			return r, err
		}
		if !plan.MatchesPreparedAccount(account, r.NamespaceUID) {
			return r, ErrIdentityChanged
		}
		if checkErr := verifyPreparationNamespace(ctx, client, r.PreparationIntent); checkErr != nil {
			return r, checkErr
		}
		if checkErr := advance(PreparationAccountCreated, string(account.UID)); checkErr != nil {
			return r, checkErr
		}
	}
	if r.State != PreparationAccountCreated && r.State != PreparationPodPending {
		return r, ErrConflict
	}
	if checkErr := verifyPreparedAccount(ctx, client, plan, r); checkErr != nil {
		return r, checkErr
	}
	create := r.State == PreparationAccountCreated
	if create {
		if checkErr := advance(PreparationPodPending, ""); checkErr != nil {
			return r, checkErr
		}
	}
	if checkErr := guard(ctx); checkErr != nil {
		return r, checkErr
	}
	var pod *corev1.Pod
	if create {
		_, desired, buildErr := plan.PreparationResources(r.NamespaceUID)
		if buildErr != nil {
			return r, buildErr
		}
		pod, err = client.CoreV1().Pods(r.Namespace).Create(ctx, desired, metav1.CreateOptions{})
	} else {
		pod, err = client.CoreV1().Pods(r.Namespace).Get(ctx, "loomplane-agent-"+r.ID, metav1.GetOptions{})
	}
	if apierrors.IsNotFound(err) {
		return r, ErrInUse
	}
	if err != nil {
		return r, err
	}
	if !plan.MatchesPreparedPod(pod, r.NamespaceUID) {
		return r, ErrIdentityChanged
	}
	if checkErr := verifyPreparationNamespace(ctx, client, r.PreparationIntent); checkErr != nil {
		return r, checkErr
	}
	if checkErr := verifyPreparedAccount(ctx, client, plan, r); checkErr != nil {
		return r, checkErr
	}
	if checkErr := advance(PreparationPrepared, string(pod.UID)); checkErr != nil {
		return r, checkErr
	}
	return r, nil
}
func verifyPreparationNamespace(ctx context.Context, client kubernetes.Interface, in PreparationIntent) error {
	if client == nil {
		return ErrUnverified
	}
	ns, err := client.CoreV1().Namespaces().Get(ctx, in.Namespace, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(ns.UID) != in.NamespaceUID || ns.DeletionTimestamp != nil {
		return ErrIdentityChanged
	}
	return nil
}
func (s *PreparationService) record(ctx context.Context, p security.Principal, r *Preparation) {
	if audit := s.launch.installations.destinations.audit; audit != nil {
		audit.Record(ctx, projects.AuditEvent{Action: "federation.preparation." + r.State, Severity: "info", ProjectID: r.ProjectID, Principal: p, Attributes: map[string]any{"attempt_id": r.ID, "destination_id": r.DestinationID, "account_uid": r.AccountUID, "pod_uid": r.PodUID}})
	}
}

func verifyPreparedAccount(ctx context.Context, client kubernetes.Interface, plan *agentinstall.KubernetesPlan, r *Preparation) error {
	account, err := client.CoreV1().ServiceAccounts(r.Namespace).Get(ctx, "loomplane-agent-"+r.ID, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !plan.MatchesPreparedAccount(account, r.NamespaceUID) || string(account.UID) != r.AccountUID {
		return ErrIdentityChanged
	}
	return nil
}
