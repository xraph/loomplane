package cloudinfra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestClusterMaintenanceRejectsUnqualifiedNativeDrain(t *testing.T) {
	s, _, _, _, p, in := clusterFixture(t)
	in.Kind = "cluster-upgrade"
	in.Attachment = nil
	in.ResourceID = "cluster"
	in.Ref = clusterObservation().Ref
	_, err := s.PreviewCluster(t.Context(), p, "cloud", in)
	if errors.Is(err, ErrInvalid) {
		t.Fatalf("maintenance still falls through creation/adoption switch: %v", err)
	}
	if err == nil {
		t.Fatal("unqualified upgrade accepted")
	}
}

func TestClusterMaintenanceWindow(t *testing.T) {
	for _, tc := range []struct {
		local string
		valid bool
	}{{"2026-03-08T02:30", false}, {"2026-11-01T01:30", false}, {"2026-11-01T03:30", true}} {
		w := &ClusterMaintenanceWindow{Timezone: "America/Chicago", LocalStart: tc.local, DurationSeconds: 3600}
		err := resolveMaintenanceWindow(w)
		if (err == nil) != tc.valid {
			t.Fatalf("%s: %v", tc.local, err)
		}
		if tc.valid {
			if w.StartUTC.Format(time.RFC3339) != "2026-11-01T09:30:00Z" {
				t.Fatal(w)
			}
			w.DurationSeconds = 7200
			if !errors.Is(resolveMaintenanceWindow(w), ErrConflict) {
				t.Fatal("changed reviewed interval accepted")
			}
		}
	}
}
func drainFixture() (*ManagedResource, ClusterManagementFacts) {
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node-uid", Labels: map[string]string{"doks.digitalocean.com/node-pool-id": "pool"}}, Spec: corev1.NodeSpec{ProviderID: "digitalocean://1"}}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ns", UID: "pod-uid", Labels: map[string]string{"app": "one"}}, Spec: corev1.PodSpec{NodeName: "node", Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi")}}}}}}
	return &ManagedResource{ID: "cluster", ObservedRevision: 1, Observation: &provider.ClusterObservation{Ref: provider.ClusterRef{ID: "physical"}}}, ClusterManagementFacts{Nodes: []corev1.Node{node}, Pods: []corev1.Pod{pod}, Complete: true, ObservedAt: time.Now()}
}
func TestClusterDrainPDBBlocked(t *testing.T) {
	r, f := drainFixture()
	f.PDBs = []policyv1.PodDisruptionBudget{{ObjectMeta: metav1.ObjectMeta{Name: "pdb", Namespace: "ns", UID: "pdb-uid", Generation: 1}, Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "one"}}}}}
	review := drainReview(r, "pool", f, time.Now())
	if !slices.Contains(review.Blockers, "pdb_blocked") || len(review.PDBs) != 1 {
		t.Fatal(review)
	}
}
func TestClusterDrainLocalDataBlocked(t *testing.T) {
	r, f := drainFixture()
	f.Pods[0].Spec.Volumes = []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	if review := drainReview(r, "pool", f, time.Now()); !slices.Contains(review.Blockers, "local_data") {
		t.Fatal(review)
	}
}
func TestClusterDrainMissingSpareCapacity(t *testing.T) {
	r, f := drainFixture()
	review := drainReview(r, "pool", f, time.Now())
	if !slices.Contains(review.Blockers, "missing_spare_capacity") || review.RequiredSpareCapacity.CPUMillis != 1000 {
		t.Fatal(review)
	}
}
func TestClusterDrainReviewBindsFreshFacts(t *testing.T) {
	r, f := drainFixture()
	first := drainReview(r, "pool", f, time.Now())
	f.ObservedAt = f.ObservedAt.Add(time.Second)
	if drainReview(r, "pool", f, time.Now().Add(time.Second)).Digest != first.Digest {
		t.Fatal("routine timestamp invalidated review")
	}
	f.Pods[0].UID = "replacement"
	if drainReview(r, "pool", f, time.Now().Add(time.Second)).Digest == first.Digest {
		t.Fatal("pod replacement reused review")
	}
	f.ObservedAt = time.Now().Add(-3 * time.Minute)
	if !slices.Contains(drainReview(r, "pool", f, time.Now()).Blockers, "management_observation_incomplete") {
		t.Fatal("stale drain accepted")
	}
}
func TestClusterUpgradeUnsupportedPath(t *testing.T) {
	s, _, _, _, p, in := clusterFixture(t)
	in.Kind = "cluster-upgrade"
	in.Attachment = nil
	in.Spec = provider.ClusterSpec{}
	if _, err := s.PreviewCluster(t.Context(), p, "cloud", in); !errors.Is(err, provider.ErrInfrastructureUnsupported) {
		t.Fatalf("unsupported adapter: %v", err)
	}
}

type maintenanceProvider struct {
	bootstrapCalls  int
	beforeBootstrap func()
	bootstrap       provider.BootstrapAccess
	*clusterProvider
	observation provider.ClusterObservation
	effects     int
	lost        bool
	before      func()
}

func (p *maintenanceProvider) GetCluster(context.Context, provider.AccountAccess, provider.ClusterRef) (provider.ClusterObservation, error) {
	return p.observation, nil
}
func (p *maintenanceProvider) UpdateCluster(ctx context.Context, a provider.AccountAccess, ref provider.ClusterRef, change provider.ClusterUpdate) (provider.ClusterMutation, error) {
	if p.before != nil {
		p.before()
	}
	if err := a.BeforeEffect(ctx); err != nil {
		return provider.ClusterMutation{}, err
	}
	p.effects++
	p.observation.Maintenance = change.Maintenance
	if p.lost {
		return provider.ClusterMutation{}, provider.ErrInfrastructureAmbiguous
	}
	return provider.ClusterMutation{Kind: "cluster-configure", Ref: ref}, nil
}
func (p *maintenanceProvider) UpgradeCluster(ctx context.Context, a provider.AccountAccess, ref provider.ClusterRef, version string) (provider.ClusterMutation, error) {
	if err := a.BeforeEffect(ctx); err != nil {
		return provider.ClusterMutation{}, err
	}
	p.effects++
	p.observation.Version = version
	return provider.ClusterMutation{Kind: "cluster-upgrade", Ref: ref}, nil
}
func (m *clusterMemory) ValidateClusterMaintenanceEffect(_ context.Context, o *ClusterOperation) error {
	if m.op.FencingEpoch != o.FencingEpoch || m.op.CancelRequested {
		return ErrConflict
	}
	return nil
}
func maintenanceFixture(t *testing.T) (*Service, *clusterMemory, *maintenanceProvider, *clusterAuth, security.Principal, ClusterInput) {
	s, store, base, auth, p, in := clusterFixture(t)
	adapter := &maintenanceProvider{clusterProvider: base, observation: clusterObservation()}
	s.Adapters = NewAdapters()
	if err := s.Adapters.RegisterCluster("digitalocean", adapter, provider.ClusterConfigurationUpdate, provider.ClusterMutationObservation); err != nil {
		t.Fatal(err)
	}
	in.Kind = "cluster-configure"
	in.Spec = provider.ClusterSpec{}
	in.Attachment = nil
	in.ResourceID = "cluster"
	in.ResourceRevision = 1
	in.Ref = adapter.observation.Ref
	in.Maintenance = &ClusterMaintenanceInput{ObservedRevision: 1, Update: &provider.ClusterUpdate{FieldMask: []string{"maintenance"}, Maintenance: provider.ClusterMaintenance{Day: "monday", StartTime: "04:00"}}}
	store.resource = &ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", Provider: "digitalocean", ConnectionID: "conn", AccountID: "account", ExternalID: in.Ref.ID, Incarnation: in.Ref.Incarnation, Revision: 1, ObservedRevision: 1, Ownership: "managed", CreationReceipt: &provider.ClusterMutation{Kind: "create", Ref: in.Ref, OperationID: "original-create", RequestDigest: "original-request"}, Observation: &adapter.observation}
	return s, store, adapter, auth, p, in
}
func TestClusterMaintenanceLostResponseDoesNotRepeat(t *testing.T) {
	s, store, adapter, _, p, in := maintenanceFixture(t)
	adapter.lost = true
	o := acceptCluster(t, s, p, in)
	if err := s.ReconcileClusterOperation(t.Context(), p.OrgID, o.ID, nil); err != nil {
		t.Fatal(err)
	}
	store.restart()
	if err := s.ReconcileClusterOperation(t.Context(), p.OrgID, o.ID, nil); err != nil {
		t.Fatal(err)
	}
	if adapter.effects != 1 || store.op.Status != "succeeded" || len(store.op.Receipts) != 0 {
		t.Fatalf("effects=%d status=%s receipts=%d", adapter.effects, store.op.Status, len(store.op.Receipts))
	}
}
func TestClusterMaintenanceRevokedAfterSlowRead(t *testing.T) {
	s, store, adapter, auth, p, in := maintenanceFixture(t)
	o := acceptCluster(t, s, p, in)
	adapter.before = func() { auth.revoked = true }
	if err := s.ReconcileClusterOperation(t.Context(), p.OrgID, o.ID, nil); err != nil {
		t.Fatal(err)
	}
	if adapter.effects != 0 || store.op.Steps[0].State != "attempted" {
		t.Fatal("late revocation permitted effect")
	}
}
func TestClusterMaintenanceChangedPolicyAfterReview(t *testing.T) {
	s, _, adapter, _, p, in := maintenanceFixture(t)
	o, err := s.PreviewCluster(t.Context(), p, "cloud", in)
	if err != nil {
		t.Fatal(err)
	}
	adapter.observation.Maintenance = provider.ClusterMaintenance{Day: "friday", StartTime: "21:00"}
	if _, err = s.ApplyCluster(t.Context(), p, "cloud", o.ID, ApplyInput{Review: o.Digest}); err == nil || adapter.effects != 0 {
		t.Fatal("changed provider policy/state accepted")
	}
}
func TestClusterMaintenancePositiveEffect(t *testing.T) {
	s, store, adapter, _, p, in := maintenanceFixture(t)
	o := acceptCluster(t, s, p, in)
	if err := s.ReconcileClusterOperation(t.Context(), p.OrgID, o.ID, nil); err != nil {
		t.Fatal(err)
	}
	if adapter.effects != 1 || store.op.Status != "succeeded" || len(store.op.Receipts) != 1 || store.op.Receipts[0].RequestDigest != o.Digest || !slices.Equal(store.resource.LifecycleAuthority, []string{"cluster:update"}) {
		t.Fatal(store.op)
	}
}
func TestClusterMaintenancePartialPoolCreation(t *testing.T) {
	spec := &provider.NodePoolSpec{Name: "pool", Size: "size", Count: 3, OperationID: "op", RequestDigest: "request"}
	op := &ClusterOperation{ID: "op", Kind: "node-pool-create", Input: ClusterInput{Maintenance: &ClusterMaintenanceInput{Pool: spec}}}
	observed := provider.ClusterObservation{State: "running", Pools: []provider.NodePoolSpec{{ID: "pool-id", Name: "pool", Size: "size", Count: 1, OperationID: "op", RequestDigest: "request"}}}
	if maintenanceConverged(op, observed) {
		t.Fatal("partial creation completed")
	}
	observed.Pools[0].Count = 3
	if !maintenanceConverged(op, observed) {
		t.Fatal("exact pool marker did not converge")
	}
	observed.Pools[0].OperationID = "other"
	if maintenanceConverged(op, observed) {
		t.Fatal("name matched unrelated pool")
	}
}

func TestClusterUpgradeLostResponsePinsExactVersion(t *testing.T) {
	s, store, adapter, _, p, in := maintenanceFixture(t)
	in.Kind = "cluster-upgrade"
	in.Maintenance = &ClusterMaintenanceInput{Version: "1.36.1-do.0"}
	now := time.Now()
	past := now.Add(-time.Second)
	o := &ClusterOperation{ID: "upgrade", OrgID: p.OrgID, CloudID: "cloud", Kind: in.Kind, Input: in, Digest: "pinned", SchemaVersion: 1, Revision: 1, Status: "waiting_provider", Principal: p, Steps: []ClusterStep{{Name: "provider", State: "attempted", RequestDigest: "pinned", AttemptedAt: &past}, {Name: "enrollment", State: "pending", RequestDigest: "pinned"}}}
	store.op = cloneCluster(o)
	adapter.observation.Version = "1.36.0-do.0"
	if err := s.ReconcileClusterOperation(t.Context(), p.OrgID, o.ID, nil); err != nil {
		t.Fatal(err)
	}
	if store.op.Status != "waiting_provider" || adapter.effects != 0 {
		t.Fatal("unknown upgrade repeated or wrong version completed")
	}
	store.restart()
	adapter.observation.Version = in.Maintenance.Version
	if err := s.ReconcileClusterOperation(t.Context(), p.OrgID, o.ID, nil); err != nil {
		t.Fatal(err)
	}
	if store.op.Status != "succeeded" || adapter.effects != 0 {
		t.Fatal("exact version did not converge safely")
	}
}
func TestClusterUpgradeDeclaredNoForceAdapterPositiveControl(t *testing.T) {
	_, _, adapter, _, _, in := maintenanceFixture(t)
	in.Kind = "cluster-upgrade"
	in.Maintenance = &ClusterMaintenanceInput{Version: "1.36.1-do.0"}
	guarded := false
	receipt, err := executeClusterMaintenance(t.Context(), adapter, provider.AccountAccess{BeforeEffect: func(context.Context) error { guarded = true; return nil }}, in)
	if err != nil || !guarded || adapter.effects != 1 || adapter.observation.Version != in.Maintenance.Version || receipt.Kind != "cluster-upgrade" {
		t.Fatalf("%+v %v", receipt, err)
	}
}

func TestClusterDrainRetainedLocalVolumeBlocked(t *testing.T) {
	r, f := drainFixture()
	f.Pods = nil
	f.Volumes = []corev1.PersistentVolume{{ObjectMeta: metav1.ObjectMeta{Name: "local-pv", UID: "pv-uid"}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{Local: &corev1.LocalVolumeSource{Path: "/data"}}}}}
	if review := drainReview(r, "pool", f, time.Now()); !slices.Contains(review.Blockers, "retained_local_volume") {
		t.Fatal(review)
	}
}

type maintenanceMemory struct {
	*clusterMemory
	management map[string]*ClusterOperation
}

func (m *maintenanceMemory) CreateClusterPreview(ctx context.Context, o *ClusterOperation) error {
	if o.Kind == "management-observation" {
		m.management[o.ID] = cloneCluster(o)
		return nil
	}
	return m.clusterMemory.CreateClusterPreview(ctx, o)
}
func (m *maintenanceMemory) SaveClusterCheckpoint(ctx context.Context, o *ClusterOperation, revision int64) error {
	if o.Kind == "management-observation" {
		if m.management[o.ID].Revision != revision {
			return ErrConflict
		}
		m.management[o.ID] = cloneCluster(o)
		return nil
	}
	return m.clusterMemory.SaveClusterCheckpoint(ctx, o, revision)
}
func (p *maintenanceProvider) BootstrapCluster(ctx context.Context, a provider.AccountAccess, _ provider.ClusterRef, _ time.Duration) (provider.BootstrapAccess, error) {
	if p.beforeBootstrap != nil {
		p.beforeBootstrap()
	}
	if err := a.BeforeEffect(ctx); err != nil {
		return provider.BootstrapAccess{}, err
	}
	p.bootstrapCalls++
	return p.bootstrap, nil
}
func (p *maintenanceProvider) CheckClusterLifecycle(_ context.Context, _ provider.AccountAccess, _ provider.ClusterRef, kind, version string) error {
	if kind == "cluster-upgrade" && version != "1.36.1-do.0" {
		return &provider.ClusterLifecycleBlockedError{Reason: "unsupported_upgrade_path"}
	}
	return nil
}
func emptyMaintenanceFixture(t *testing.T) (*Service, *maintenanceMemory, *maintenanceProvider, security.Principal, *ClusterOperation) {
	t.Helper()
	s, base, adapter, _, principal, in := maintenanceFixture(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/nodes":
			_, _ = fmt.Fprint(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"worker","uid":"uid","labels":{"doks.digitalocean.com/node-pool-id":"pool"}},"spec":{"providerID":"digitalocean://1"}}]}`)
		case "/api/v1/pods":
			_, _ = fmt.Fprint(w, `{"apiVersion":"v1","kind":"PodList","items":[]}`)
		case "/apis/policy/v1/poddisruptionbudgets":
			_, _ = fmt.Fprint(w, `{"apiVersion":"policy/v1","kind":"PodDisruptionBudgetList","items":[]}`)
		case "/api/v1/persistentvolumes":
			_, _ = fmt.Fprint(w, `{"apiVersion":"v1","kind":"PersistentVolumeList","items":[]}`)
		default:
			t.Errorf("unexpected management request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	hash := sha256.Sum256(ca)
	adapter.observation.Endpoint = server.URL
	adapter.observation.CAHash = hex.EncodeToString(hash[:])
	adapter.observation.Version = "v1"
	adapter.observation.Pools = []provider.NodePoolSpec{{ID: "pool", Name: "workers", Size: "s-2", Count: 1}}
	adapter.bootstrap = provider.BootstrapAccess{Endpoint: server.URL, CAData: ca, ExpiresAt: time.Now().Add(time.Hour)}
	store := &maintenanceMemory{clusterMemory: base, management: map[string]*ClusterOperation{}}
	s.Clusters = store
	s.Adapters = NewAdapters()
	if err := s.Adapters.RegisterCluster("digitalocean", adapter, provider.ClusterUpgrade, provider.ClusterBootstrapAccess, provider.ClusterMutationObservation); err != nil {
		t.Fatal(err)
	}
	review, err := s.PreviewClusterDrain(t.Context(), principal, "cloud", "cluster", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Blockers) != 0 {
		t.Fatal(review)
	}
	in.Kind = "cluster-upgrade"
	in.Maintenance = &ClusterMaintenanceInput{ObservedRevision: 1, Version: "1.36.1-do.0", DrainDigest: review.Digest, Window: &ClusterMaintenanceWindow{Timezone: "UTC", LocalStart: time.Now().UTC().Format("2006-01-02T15:04"), DurationSeconds: 3600}}
	op := acceptCluster(t, s, principal, in)
	return s, store, adapter, principal, op
}
func TestClusterUpgradeEmptyManagedClusterDurableEffect(t *testing.T) {
	s, store, adapter, principal, op := emptyMaintenanceFixture(t)
	if err := s.ReconcileClusterOperation(t.Context(), principal.OrgID, op.ID, nil); err != nil {
		t.Fatal(err)
	}
	if adapter.effects != 1 || store.op.Status != "succeeded" || len(store.op.Receipts) != 1 || len(store.op.ReservationIDs) != 1 {
		t.Fatalf("effects=%d operation=%+v", adapter.effects, store.op)
	}
	if len(store.management) < 3 {
		t.Fatal("missing fresh management preflight")
	}
}

func TestClusterMaintenanceLeaseLostAfterProviderReads(t *testing.T) {
	s, store, adapter, _, p, in := maintenanceFixture(t)
	op := acceptCluster(t, s, p, in)
	adapter.before = func() { store.op.FencingEpoch++ }
	if err := s.ReconcileClusterOperation(t.Context(), p.OrgID, op.ID, nil); err != nil && !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if adapter.effects != 0 {
		t.Fatal("stale maintenance lease mutated provider")
	}
}

func (m *clusterMemory) ValidateClusterCreationReceipt(_ context.Context, r *ManagedResource) error {
	if r.CreationReceipt == nil || r.CreationReceipt.OperationID != "original-create" || r.CreationReceipt.RequestDigest != "original-request" {
		return ErrConflict
	}
	return nil
}
func TestClusterMaintenanceRejectsUnboundCreationReceipt(t *testing.T) {
	for _, change := range []func(*provider.ClusterMutation){func(r *provider.ClusterMutation) { r.Kind = "adopt-managed" }, func(r *provider.ClusterMutation) { r.Ref.Incarnation = "replacement" }, func(r *provider.ClusterMutation) { r.OperationID = "" }, func(r *provider.ClusterMutation) { r.RequestDigest = "other" }} {
		s, store, adapter, _, p, in := maintenanceFixture(t)
		change(store.resource.CreationReceipt)
		if _, err := s.PreviewCluster(t.Context(), p, "cloud", in); err == nil || adapter.effects != 0 {
			t.Fatal("unbound creation receipt authorized maintenance")
		}
	}
}

func TestClusterMaintenanceRecoveryKeepsStableAttachmentIdentity(t *testing.T) {
	s, store, adapter, _, p, in := maintenanceFixture(t)
	adapter.lost = true
	op := acceptCluster(t, s, p, in)
	if err := s.ReconcileClusterOperation(t.Context(), p.OrgID, op.ID, nil); err != nil {
		t.Fatal(err)
	}
	store.resource.Revision++
	store.resource.ObservedRevision = store.resource.Revision
	store.restart()
	if err := s.ReconcileClusterOperation(t.Context(), p.OrgID, op.ID, nil); err != nil {
		t.Fatal(err)
	}
	if store.op.Status != "succeeded" || adapter.effects != 1 {
		t.Fatal("benign observation blocked recovery or repeated effect")
	}
	old := &ClusterOperation{ID: "attachment-op", Kind: "adopt-managed", OrgID: p.OrgID, CloudID: "cloud", Input: ClusterInput{Provider: in.Provider, AccountID: in.AccountID, ConnectionID: in.ConnectionID, Ref: in.Ref}}
	store.resource.Observation.CAHash = "stable-trust"
	store.resource.Observation.Endpoint = "https://stable.example"
	attachment := &RuntimeAttachment{OperationID: old.ID, ClusterID: store.resource.ID, TrustFingerprint: "stable-trust", EndpointFingerprint: endpointFingerprint("https://stable.example")}
	if !clusterAttachmentPins(old, store.resource, attachment) {
		t.Fatal("benign resource revision invalidated successful attachment")
	}
}

func TestClusterMaintenanceEmptyCollectionsConverge(t *testing.T) {
	desired := &provider.NodePoolSpec{Name: "workers", Size: "size", Count: 2, OperationID: "op", RequestDigest: "request"}
	op := &ClusterOperation{ID: "op", Kind: "node-pool-create", Input: ClusterInput{Maintenance: &ClusterMaintenanceInput{Pool: desired}}}
	observation := provider.ClusterObservation{State: "running", Pools: []provider.NodePoolSpec{{ID: "pool", Name: "workers", Size: "size", Count: 2, OperationID: "op", RequestDigest: "request", Labels: map[string]string{}, Taints: []provider.NodePoolTaint{}}}}
	if !maintenanceConverged(op, observation) {
		t.Fatal("empty provider collections strand successful pool")
	}
}
func TestClusterMaintenanceBootstrapRequiresInstalledCapability(t *testing.T) {
	s, _, adapter, p, _ := emptyMaintenanceFixture(t)
	before := adapter.bootstrapCalls
	s.Adapters = NewAdapters()
	if err := s.Adapters.RegisterCluster("digitalocean", adapter, provider.ClusterUpgrade); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PreviewClusterDrain(t.Context(), p, "cloud", "cluster", ""); !errors.Is(err, provider.ErrInfrastructureUnsupported) {
		t.Fatalf("uninstalled bootstrap: %v", err)
	}
	if adapter.bootstrapCalls != before {
		t.Fatal("uninstalled bootstrap minted credentials")
	}
}
func TestClusterMaintenanceNestedBootstrapFence(t *testing.T) {
	for _, action := range []string{"cancel", "lease", "capability", "retain"} {
		t.Run(action, func(t *testing.T) {
			s, store, adapter, p, op := emptyMaintenanceFixture(t)
			before := adapter.bootstrapCalls
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			adapter.beforeBootstrap = func() { once.Do(func() { close(entered); <-release }) }
			done := make(chan error, 1)
			go func() { done <- s.ReconcileClusterOperation(t.Context(), p.OrgID, op.ID, nil) }()
			<-entered
			switch action {
			case "cancel":
				store.op.CancelRequested = true
				store.op.FencingEpoch++
			case "lease":
				store.op.FencingEpoch++
			case "capability":
				s.Adapters = NewAdapters()
				if err := s.Adapters.RegisterCluster("digitalocean", adapter, provider.ClusterUpgrade); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			err := <-done
			if action == "retain" {
				if err != nil || adapter.bootstrapCalls == before || adapter.effects != 1 {
					t.Fatalf("retained authority failed: %v", err)
				}
			} else if adapter.bootstrapCalls != before || adapter.effects != 0 {
				t.Fatal("lost lifecycle authority minted management credentials")
			}
		})
	}
}
