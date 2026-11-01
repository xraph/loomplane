package planning

import (
	"context"
	"errors"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/connections"
)

var ErrCleanupWritesUnresolved = errors.New("managed cleanup has unresolved application or cleanup requests")

var ErrCleanupReviewStale = errors.New("the managed namespace changed after review, review cleanup again")

type cleanupSession struct {
	store                  *ManagedCleanupStore
	grant                  *ManagedCleanupGrant
	credentials            *connections.Service
	client                 *kube.Client
	capability, credential int64
	hpa                    bool
	peers                  []*cleanupSession
}

// ExecuteCleanupEffects sends reviewed deletes. It returns before capacity settlement.
func (s *ManagedCleanupStore) ExecuteCleanupEffects(ctx context.Context, selection ManagedCleanupSelection, credentials *connections.Service) error {
	sessions, err := s.cleanupSessions(ctx, selection, credentials)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if err := session.preflight(ctx); err != nil {
			return err
		}
	}
	for _, session := range sessions {
		if err := session.remove(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *ManagedCleanupStore) cleanupSessions(ctx context.Context, selection ManagedCleanupSelection, credentials *connections.Service) ([]*cleanupSession, error) {
	if credentials == nil || selection.TargetIndex != 0 {
		return nil, kube.ErrManagedAuthority
	}
	selections, err := s.effectSelections(ctx, selection)
	if err != nil {
		return nil, err
	}
	sessions := make([]*cleanupSession, 0, len(selections))
	for _, sel := range selections {
		grant, err := s.Open(ctx, sel)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, &cleanupSession{store: s, grant: grant, credentials: credentials})
	}
	for _, session := range sessions {
		session.peers = sessions
		if err := session.open(ctx); err != nil {
			return nil, err
		}
	}
	return sessions, nil
}

// Every grant comes from the same accepted action or versioned failed review.
func (s *ManagedCleanupStore) effectSelections(ctx context.Context, sel ManagedCleanupSelection) ([]ManagedCleanupSelection, error) {
	var count int
	var review *ManagedFailedCleanup
	var err error
	if sel.FailedReviewID != "" {
		review, err = s.ValidateFailedCleanupRun(ctx, sel.Org, sel.FailedReviewID, sel.ExecutionAttempt)
		if err != nil {
			return nil, err
		}
		if review.Deployment != sel.Deployment {
			return nil, kube.ErrManagedAuthority
		}
		count = len(review.Targets)
	} else {
		err = s.db.QueryRow(ctx, `SELECT jsonb_array_length(body) FROM loomplane_managed_cleanup_intents WHERE org_id=$1 AND plan_id=$2 AND action_id=$3`, sel.Org, sel.Plan, sel.Action).Scan(&count)
		if err != nil {
			return nil, err
		}
	}
	if count == 0 {
		return nil, kube.ErrManagedAuthority
	}
	out := make([]ManagedCleanupSelection, count)
	for i := range count {
		sel.TargetIndex = i
		if review != nil {
			sel.Plan = review.Targets[i].Scope.Plan
			sel.Action = review.Targets[i].Scope.Action
		}
		out[i] = sel
	}
	return out, nil
}

func (s *cleanupSession) material(ctx context.Context) (*kube.Config, connections.ManagedCredential, error) {
	t := s.grant.Target
	scope := t.Scope
	if _, err := s.store.inspectTarget(ctx, scope.Org, s.grant.ID, t.Kind); err != nil {
		return nil, connections.ManagedCredential{}, err
	}
	c, cred, err := s.credentials.CleanupCredential(ctx, scope.Org, scope.Connection, scope.Project, t.AttachmentID, t.ConnectionGeneration)
	if err != nil {
		return nil, cred, err
	}
	if cred.AttachmentRevision != t.AttachmentRevision {
		return nil, cred, ErrCleanupReviewStale
	}
	if cred.AccessRevision <= 0 {
		return nil, cred, kube.ErrCleanupPermissionsUnavailable
	}
	if c == nil || c.Method != connections.MethodManagedKubernetes || c.Details["namespace"] != scope.Namespace || c.Details["namespace_uid"] != scope.NamespaceUID || c.Details["cluster_ca"] != scope.ClusterCA {
		return nil, cred, kube.ErrManagedAuthority
	}
	cfg, err := kube.ParseConfig(cred.Kubeconfig, c.Details)
	if err != nil {
		return nil, cred, err
	}
	if cfg.Namespace != scope.Namespace || cfg.Details["cluster_ca"] != scope.ClusterCA || cfg.REST.Host != c.Details["endpoint"] {
		return nil, cred, kube.ErrManagedAuthority
	}
	return cfg, cred, nil
}

func (s *cleanupSession) open(ctx context.Context) error {
	cfg, cred, err := s.material(ctx)
	if err != nil {
		return err
	}
	s.capability, s.credential, s.hpa = cred.AccessRevision, cred.CredentialVersion, cred.HPA
	cfg.ManagedWorkload, cfg.ManagedProfile = true, kube.ManagedProfileJournal
	cfg.ManagedScope = s.grant.Target.Scope
	cfg.ManagedJournal = &managedCleanupJournal{store: s.store, org: cfg.ManagedScope.Org, id: s.grant.ID, session: s}
	s.client, err = kube.New(cfg)
	return err
}

func (s *cleanupSession) remove(ctx context.Context) error {
	t := s.grant.Target
	if t.Kind == ManagedCleanupRetireDependents {
		return s.retire(ctx, true)
	}
	seen := map[kube.ManagedRequest]bool{}
	for _, a := range t.Accepted {
		r := a.Request
		if a.Outcome.State != "accepted" || (r.Method != http.MethodPost && r.Method != http.MethodPut) {
			continue
		}
		r.UID, r.ResourceVersion, r.Method = a.Outcome.UID, "", http.MethodDelete
		if seen[r] {
			continue
		}
		seen[r] = true
		if r.Resource == "horizontalpodautoscalers" && !s.hpa {
			return kube.ErrCleanupPermissionsUnavailable
		}
		resource := s.client.Dynamic.Resource(schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Resource}).Namespace(t.Scope.Namespace)
		obj, err := resource.Get(ctx, r.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if string(obj.GetUID()) != r.UID || !cleanupObjectOwned(obj, t.Scope) {
			return kube.ErrManagedAuthority
		}
		if checkErr := s.delete(ctx, r, obj); checkErr != nil {
			return checkErr
		}
	}
	return s.drained(ctx)
}

func cleanupObjectOwned(obj metav1.Object, scope kube.ManagedScope) bool {
	labels := obj.GetLabels()
	return obj.GetNamespace() == scope.Namespace && obj.GetUID() != "" && obj.GetResourceVersion() != "" && labels[owned.LabelOrg] == scope.Org && labels[owned.LabelProject] == scope.Project && labels[owned.LabelEnvironment] == scope.Environment && labels[owned.LabelInstance] == scope.Instance
}

func (s *cleanupSession) delete(ctx context.Context, r kube.ManagedRequest, obj metav1.Object) error {
	if obj.GetDeletionTimestamp() != nil {
		return ErrCleanupConverging
	}
	uid, rv := obj.GetUID(), obj.GetResourceVersion()
	if uid == "" || rv == "" {
		return kube.ErrManagedAuthority
	}
	resource := s.client.Dynamic.Resource(schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Resource}).Namespace(s.grant.Target.Scope.Namespace)
	options := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}}
	policy := metav1.DeletePropagationOrphan
	options.PropagationPolicy = &policy
	return resource.Delete(ctx, obj.GetName(), options)
}

func (s *cleanupSession) drained(ctx context.Context) error {
	rows, err := s.store.journal.ManagedIssuances(ctx, s.grant.Target.Scope.Org, s.grant.Target.Scope.Release)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Outcome == nil {
			return errors.Join(kube.ErrManagedAuthority, ErrCleanupWritesUnresolved)
		}
	}
	return nil
}

func (s *cleanupSession) check(ctx context.Context, r kube.ManagedRequest) error {
	_, cred, err := s.material(ctx)
	if err != nil {
		return err
	}
	if cred.CredentialVersion != s.credential || cred.AccessRevision != s.capability || r.ResourceVersion == "" {
		return ErrCleanupReviewStale
	}
	if r.Resource == "horizontalpodautoscalers" && !cred.HPA {
		return kube.ErrCleanupPermissionsUnavailable
	}
	return nil
}

func (s *cleanupSession) checkWhole(ctx context.Context, r kube.ManagedRequest) error {
	scope := s.grant.Target.Scope
	res := s.client.Dynamic.Resource(schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Resource}).Namespace(scope.Namespace)
	obj, err := res.Get(ctx, r.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(obj.GetUID()) != r.UID || obj.GetResourceVersion() != r.ResourceVersion || !cleanupObjectOwned(obj, scope) {
		return kube.ErrManagedAuthority
	}
	if r.Resource != "deployments" {
		return nil
	}
	marker, _, err := unstructured.NestedString(obj.Object, "spec", "template", "metadata", "annotations", kube.WorkloadReleaseAnnotation)
	if err != nil {
		return err
	}
	for _, peer := range s.peers {
		target := peer.grant.Target
		if marker != target.Scope.Release || !sameCleanupPhysicalScope(scope, target.Scope) {
			continue
		}
		for _, a := range target.Accepted {
			if a.Request.Resource == r.Resource && a.Request.Name == r.Name && a.Outcome.State == "accepted" && a.Outcome.UID == r.UID && a.Outcome.Generation == obj.GetGeneration() && (a.Request.Method == http.MethodPut || a.Request.Method == http.MethodPost) {
				return nil
			}
		}
	}
	return kube.ErrManagedAuthority
}
