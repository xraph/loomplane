package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type clusterMemory struct {
	ClusterStore
	mu           sync.Mutex
	op           *ClusterOperation
	resource     *ManagedResource
	crashReceipt bool
}

func cloneCluster(o *ClusterOperation) *ClusterOperation {
	raw, _ := json.Marshal(o)
	var out ClusterOperation
	_ = json.Unmarshal(raw, &out)
	out.Principal = o.Principal
	return &out
}
func (m *clusterMemory) CreateClusterPreview(_ context.Context, o *ClusterOperation) error {
	m.op = cloneCluster(o)
	return nil
}
func (m *clusterMemory) GetClusterOperation(context.Context, string, string, string) (*ClusterOperation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneCluster(m.op), nil
}
func (m *clusterMemory) GetManagedResource(context.Context, string, string) (*ManagedResource, error) {
	if m.resource == nil {
		return nil, ErrNotFound
	}
	copied := *m.resource
	return &copied, nil
}
func (m *clusterMemory) AcceptClusterOperation(_ context.Context, o *ClusterOperation, _ int) error {
	if m.op.Status != "preview" {
		return ErrConflict
	}
	o.Revision++
	o.Status = "accepted"
	o.Attempt = 1
	o.ReservationIDs = []string{o.ID}
	m.op = cloneCluster(o)
	return nil
}
func (m *clusterMemory) LeaseClusterOperation(_ context.Context, _, _, owner string, d time.Duration) (*ClusterOperation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.op.LeaseExpiresAt != nil && m.op.LeaseExpiresAt.After(time.Now()) {
		return nil, ErrBusy
	}
	m.op.FencingEpoch++
	m.op.Revision++
	m.op.LeaseOwner = owner
	until := time.Now().Add(d)
	m.op.LeaseExpiresAt = &until
	m.op.Status = "running"
	return cloneCluster(m.op), nil
}
func (m *clusterMemory) SaveClusterCheckpoint(_ context.Context, o *ClusterOperation, expected int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.crashReceipt && len(o.Receipts) > 0 {
		m.crashReceipt = false
		return errors.New("lost database after provider receipt")
	}
	if m.op.Revision != expected || m.op.FencingEpoch != o.FencingEpoch || m.op.LeaseExpiresAt == nil || !m.op.LeaseExpiresAt.After(time.Now()) {
		return ErrConflict
	}
	m.op = cloneCluster(o)
	return nil
}
func (m *clusterMemory) AttemptClusterCreate(ctx context.Context, o *ClusterOperation) error {
	old := o.Revision
	o.Revision++
	now := time.Now()
	o.Steps[0].State = "attempted"
	o.Steps[0].AttemptedAt = &now
	return m.SaveClusterCheckpoint(ctx, o, old)
}
func (m *clusterMemory) ControlClusterOperation(_ context.Context, _, _, _ string, cancel bool) (*ClusterOperation, error) {
	m.op.CancelRequested = cancel
	m.op.Revision++
	m.op.FencingEpoch++
	m.op.LeaseExpiresAt = nil
	m.op.Status = "waiting_provider"
	return cloneCluster(m.op), nil
}
func (m *clusterMemory) CheckpointClusterResource(ctx context.Context, o *ClusterOperation, r *ManagedResource, rev int64) error {
	if err := m.SaveClusterCheckpoint(ctx, o, rev); err != nil {
		return err
	}
	m.resource = r
	return nil
}
func (m *clusterMemory) restart() {
	past := time.Now().Add(-time.Second)
	m.op.LeaseExpiresAt = &past
	m.op.NextAttemptAt = time.Time{}
}

type clusterAuth struct{ revoked bool }

func (a *clusterAuth) Require(context.Context, security.Principal, security.Permission, string) error {
	if a.revoked {
		return security.ErrForbidden
	}
	return nil
}
func (a *clusterAuth) RequireCurrentMembership(context.Context, security.Principal) error {
	if a.revoked {
		return security.ErrNotMember
	}
	return nil
}

type clusterClouds struct{}

func (clusterClouds) GetProfile(_ context.Context, org, _, id string) (*cloudprofiles.Profile, error) {
	return &cloudprofiles.Profile{ID: id, OrgID: org, Revision: 1}, nil
}
func (clusterClouds) GetProjectGrant(_ context.Context, org, cloud, project string) (*cloudprofiles.ProjectGrant, error) {
	return &cloudprofiles.ProjectGrant{OrgID: org, CloudID: cloud, ProjectID: project, Revision: 1}, nil
}
func (clusterClouds) SetProjectGrant(context.Context, string, string, string, []string, bool) (int64, error) {
	return 0, ErrInvalid
}

type clusterProvider struct {
	observeErr       error
	creates          int
	ambiguous, empty bool
	afterCreate      func()
	digest           string
}

func (*clusterProvider) Options(context.Context, provider.AccountAccess, string) (provider.ClusterOptions, error) {
	return provider.ClusterOptions{Digest: "options", CheckedAt: time.Now(), Versions: []string{"v1"}, Regions: []string{"nyc1"}, Networks: []string{"network"}, NodeSizeFacts: []provider.ClusterNodeSize{{ID: "s-2", CPUs: 2, MemoryMiB: 4096, DiskGB: 80}}}, nil
}
func (*clusterProvider) ListClusters(context.Context, provider.AccountAccess, string) (provider.ClusterPage, error) {
	return provider.ClusterPage{}, nil
}
func (*clusterProvider) GetCluster(context.Context, provider.AccountAccess, provider.ClusterRef) (provider.ClusterObservation, error) {
	return clusterObservation(), nil
}
func (a *clusterProvider) CreateCluster(_ context.Context, _ provider.AccountAccess, spec provider.ClusterSpec) (provider.ClusterMutation, error) {
	a.creates++
	a.digest = provider.ClusterRequestDigest(spec)
	if a.afterCreate != nil {
		a.afterCreate()
	}
	if a.ambiguous {
		return provider.ClusterMutation{}, provider.ErrInfrastructureAmbiguous
	}
	return provider.ClusterMutation{OperationID: spec.OperationID, Kind: "create", Ref: clusterObservation().Ref, RequestDigest: a.digest}, nil
}
func (a *clusterProvider) ObserveClusterMutation(_ context.Context, _ provider.AccountAccess, m provider.ClusterMutation) (provider.ClusterObservation, error) {
	if a.observeErr != nil {
		return provider.ClusterObservation{}, a.observeErr
	}
	if m.RequestDigest != a.digest {
		return provider.ClusterObservation{}, provider.ErrInfrastructureIdentity
	}
	if a.empty {
		return provider.ClusterObservation{}, provider.ErrInfrastructureAmbiguous
	}
	return clusterObservation(), nil
}
func clusterObservation() provider.ClusterObservation {
	return provider.ClusterObservation{Ref: provider.ClusterRef{ID: "remote", AccountID: "account", Region: "nyc1", Incarnation: "physical"}, State: "running", ObservedAt: time.Now()}
}
func clusterFixture(t *testing.T) (*Service, *clusterMemory, *clusterProvider, *clusterAuth, security.Principal, ClusterInput) {
	t.Helper()
	store := &clusterMemory{}
	adapter := &clusterProvider{}
	auth := &clusterAuth{}
	adapters := NewAdapters()
	if err := adapters.RegisterCluster("digitalocean", adapter, provider.ClusterCreation, provider.ClusterMutationObservation); err != nil {
		t.Fatal(err)
	}
	s := NewService(Deps{Clusters: store, Authz: auth, Clouds: clusterClouds{}, Connections: &doksCredentials{org: "org", status: connections.StatusReady, generation: 1}, Adapters: adapters, ManagedClusters: config.ManagedClustersConfig{Enabled: true, QualificationOrgID: "org", QualificationOperatorID: "manager", QualificationAccountID: "account", QualificationRegion: "nyc1", MaxNodes: 3, MaxNodeHourlyMicroUSD: 1_000_000}})
	in := ClusterInput{Kind: "create", Provider: "digitalocean", ConnectionID: "conn", AccountID: "account", ConnectionGeneration: 1, CloudRevision: 1, OptionsDigest: "options", PolicyRevision: 1, ManagementReachable: true, AcknowledgeNodeCosts: true, AcknowledgeUnknownCosts: true, WorkerImage: "worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Spec: provider.ClusterSpec{Name: "cluster", Region: "nyc1", Version: "v1", NetworkID: "network", NodePools: []provider.NodePoolSpec{{Name: "workers", Size: "s-2", Count: 2}}}, Attachment: &AttachmentInput{ProjectID: "proj", Namespace: "ns", ServiceAccountName: "runtime", TargetSlug: "worker", CloudRevision: 1, GrantRevision: 1}}
	return s, store, adapter, auth, security.Principal{OrgID: "org", UserID: "manager"}, in
}
func acceptCluster(t *testing.T, s *Service, p security.Principal, in ClusterInput) *ClusterOperation {
	t.Helper()
	o, err := s.PreviewCluster(t.Context(), p, "cloud", in)
	if err != nil {
		t.Fatal(err)
	}
	o, err = s.ApplyCluster(t.Context(), p, "cloud", o.ID, ApplyInput{Review: o.Digest})
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func TestClusterCreateAmbiguousResponse(t *testing.T) {
	s, store, adapter, _, p, in := clusterFixture(t)
	o := acceptCluster(t, s, p, in)
	adapter.ambiguous = true
	if err := s.ReconcileClusterOperation(t.Context(), "org", o.ID, nil); err != nil {
		t.Fatal(err)
	}
	store.restart()
	if err := s.ReconcileClusterOperation(t.Context(), "org", o.ID, nil); err != nil {
		t.Fatal(err)
	}
	if adapter.creates != 1 || store.op.ResourceID == "" || store.op.Status != "needs_attention" || len(store.op.ReservationIDs) != 1 || store.op.Steps[1].State != "pending" {
		t.Fatalf("unsafe recovery: creates=%d op=%+v", adapter.creates, store.op)
	}
}
func TestClusterCreateCrashAfterReceipt(t *testing.T) {
	s, store, adapter, _, p, in := clusterFixture(t)
	o := acceptCluster(t, s, p, in)
	store.crashReceipt = true
	if err := s.ReconcileClusterOperation(t.Context(), "org", o.ID, nil); err == nil {
		t.Fatal("expected lost checkpoint")
	}
	store.restart()
	if err := s.ReconcileClusterOperation(t.Context(), "org", o.ID, nil); err != nil {
		t.Fatal(err)
	}
	if adapter.creates != 1 || store.resource == nil {
		t.Fatal("receipt recovery recreated or lost cluster")
	}
}
func TestClusterCreateEmptyListIsNotRetryPermission(t *testing.T) {
	s, store, adapter, _, p, in := clusterFixture(t)
	o := acceptCluster(t, s, p, in)
	adapter.ambiguous = true
	adapter.empty = true
	for range 3 {
		store.restart()
		if err := s.ReconcileClusterOperation(t.Context(), "org", o.ID, nil); err != nil {
			t.Fatal(err)
		}
	}
	if adapter.creates != 1 || store.op.Status != "waiting_provider" || len(store.op.ReservationIDs) != 1 {
		t.Fatal("ambiguous effect was retried or released")
	}
}
func TestClusterStaleReview(t *testing.T) {
	s, store, _, _, p, in := clusterFixture(t)
	o, err := s.PreviewCluster(t.Context(), p, "cloud", in)
	if err != nil {
		t.Fatal(err)
	}
	store.op.ExpiresAt = time.Now().Add(-time.Minute)
	if _, err = s.ApplyCluster(t.Context(), p, "cloud", o.ID, ApplyInput{Review: o.Digest}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
func TestClusterRevokedApproval(t *testing.T) {
	s, store, adapter, auth, p, in := clusterFixture(t)
	o := acceptCluster(t, s, p, in)
	auth.revoked = true
	if err := s.ReconcileClusterOperation(t.Context(), "org", o.ID, nil); err != nil {
		t.Fatal(err)
	}
	if adapter.creates != 0 || store.op.Status != "needs_attention" {
		t.Fatal("revoked authority purchased")
	}
}
func TestClusterLeaseFencing(t *testing.T) {
	s, store, _, _, p, in := clusterFixture(t)
	o := acceptCluster(t, s, p, in)
	old, err := store.LeaseClusterOperation(t.Context(), "org", o.ID, "old", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	store.restart()
	if _, err = store.LeaseClusterOperation(t.Context(), "org", o.ID, "new", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err = store.AttemptClusterCreate(t.Context(), old); !errors.Is(err, ErrConflict) {
		t.Fatal("stale attempt", err)
	}
}
func TestClusterCancelAfterAcceptance(t *testing.T) {
	s, store, adapter, _, p, in := clusterFixture(t)
	o := acceptCluster(t, s, p, in)
	adapter.afterCreate = func() {
		if _, err := s.CancelCluster(t.Context(), p, "cloud", o.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ReconcileClusterOperation(t.Context(), "org", o.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancel must fence in-flight checkpoint: %v", err)
	}
	store.restart()
	if err := s.ReconcileClusterOperation(t.Context(), "org", o.ID, nil); err != nil {
		t.Fatal(err)
	}
	if adapter.creates != 1 || store.resource == nil || store.op.Status != "cancelled" || len(store.op.ReservationIDs) != 1 {
		t.Fatal("cancellation lost resource or reservation")
	}
}

func (*clusterMemory) SaveClusterAccount(context.Context, string, string, CapacityAccountSnapshot) error {
	return nil
}
func (*clusterProvider) CapacityInventory(context.Context, provider.InfrastructureCredential) (provider.CapacityInventory, error) {
	return provider.CapacityInventory{Provider: "digitalocean", AccountID: "account", CheckedAt: time.Now()}, nil
}

func TestClusterConflictingReceiptNeedsOperatorRecovery(t *testing.T) {
	s, store, adapter, _, p, in := clusterFixture(t)
	o := acceptCluster(t, s, p, in)
	adapter.observeErr = provider.ErrInfrastructureIdentity
	if err := s.ReconcileClusterOperation(t.Context(), p.OrgID, o.ID, nil); err != nil {
		t.Fatal(err)
	}
	if store.op.Status != "needs_attention" || adapter.creates != 1 {
		t.Fatalf("conflicting receipt: %s", store.op.Status)
	}
}
