package cloudinfra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type ClusterWorkflow interface {
	StartClusterOperation(context.Context, string, string, int) error
}

type ClusterLifecycleStore interface {
	ClusterStore
	SaveClusterAccount(context.Context, string, string, CapacityAccountSnapshot) error
	AttemptClusterCreate(context.Context, *ClusterOperation) error
	AcceptClusterOperation(context.Context, *ClusterOperation, int) error
	LeaseClusterOperation(context.Context, string, string, string, time.Duration) (*ClusterOperation, error)
	ControlClusterOperation(context.Context, string, string, string, bool) (*ClusterOperation, error)
	CheckpointClusterResource(context.Context, *ClusterOperation, *ManagedResource, int64) error
}

func (s *Service) clusterEnabled() error {
	if !s.ManagedClusters.Enabled || s.Clusters == nil {
		return security.ErrForbidden
	}
	return nil
}
func (s *Service) clusterAuthority(ctx context.Context, p security.Principal, cloud string, in ClusterInput) (provider.ClusterReader, provider.AccountAccess, error) {
	var access provider.AccountAccess
	if p.ReadOnly {
		return nil, access, security.ErrForbidden
	}
	membership, ok := s.Authz.(interface {
		RequireCurrentMembership(context.Context, security.Principal) error
	})
	if !ok {
		return nil, access, security.ErrForbidden
	}
	if err := membership.RequireCurrentMembership(ctx, p); err != nil {
		return nil, access, err
	}
	if p.KeyID != "" {
		if s.CapacityPrincipal == nil {
			return nil, access, security.ErrForbidden
		}
		if err := s.CapacityPrincipal.ValidatePrincipal(ctx, p); err != nil {
			return nil, access, err
		}
	}
	if maintenanceKind(in.Kind) || in.Kind == "adopt-external" || in.Kind == "adopt-managed" {
		if err := s.Authz.Require(ctx, p, security.ClusterManage, ""); err != nil {
			return nil, access, err
		}
	}
	if retirementKind(in.Kind) {
		if checkErr := s.Authz.Require(ctx, p, security.ClusterDelete, ""); checkErr != nil {
			return nil, access, checkErr
		}
	}
	profile, err := s.authorize(ctx, p, cloud)
	if err != nil {
		return nil, access, err
	}
	if profile.Revision != in.CloudRevision {
		return nil, access, ErrConflict
	}
	if checkErr := s.Authz.Require(ctx, p, security.CloudAccountManage, ""); checkErr != nil {
		return nil, access, checkErr
	}
	reader, access, err := s.clusterAccount(ctx, p, in.ConnectionID, provider.ClusterDiscovery)
	if err != nil {
		return nil, access, err
	}
	if access.Provider != in.Provider || access.AccountID != in.AccountID || access.Generation != in.ConnectionGeneration {
		return nil, access, ErrConflict
	}
	if in.Attachment != nil {
		a := in.Attachment
		if a.ProjectID == "" || a.Namespace == "" || a.ServiceAccountName == "" || a.TargetSlug == "" || a.CloudRevision != in.CloudRevision || a.ResourceRevision != in.ResourceRevision {
			return nil, access, ErrInvalid
		}
		if checkErr := supportedClusterPrerequisites(a.Prerequisites); checkErr != nil {
			return nil, access, checkErr
		}
		if len(validation.IsDNS1123Label(a.Namespace)) != 0 || len(validation.IsDNS1123Label(a.ServiceAccountName)) != 0 || len(validation.IsDNS1123Label(a.TargetSlug)) != 0 || a.AdoptNamespace && a.NamespaceUID == "" || !a.AdoptNamespace && a.NamespaceUID != "" {
			return nil, access, ErrInvalid
		}
		prereq := a.Prerequisites
		if in.Kind == "create" && (prereq.RequireIngress || prereq.RequireTLS || prereq.IngressAddress != "") {
			return nil, access, fmt.Errorf("%w: new managed clusters support private workers; existing ingress and TLS prerequisites require cluster adoption", ErrInvalid)
		}
		if (prereq.RequireBuilds && prereq.RegistryHostname == "") || ((prereq.RequireIngress || prereq.RequireTLS) && prereq.IngressAddress == "") || (prereq.RequirePersistentStorage && prereq.StorageClass == "") {
			return nil, access, ErrInvalid
		}
		grants, ok := s.Clouds.(cloudprofiles.GrantStore)
		if !ok {
			return nil, access, security.ErrForbidden
		}
		grant, grantErr := grants.GetProjectGrant(ctx, p.OrgID, cloud, a.ProjectID)
		if grantErr != nil {
			return nil, access, grantErr
		}
		if grant.Revoked || grant.Revision != a.GrantRevision || len(grant.TargetIDs) != 0 {
			return nil, access, security.ErrForbidden
		}
	}
	return reader, access, nil
}
func (s *Service) clusterPurchaseGate(p security.Principal, in ClusterInput) error {
	c := s.ManagedClusters
	if !c.Enabled || c.QualificationOrgID == "" || c.QualificationOperatorID == "" || c.QualificationAccountID == "" || c.QualificationRegion == "" || c.MaxNodes < 1 || c.MaxNodeHourlyMicroUSD < 1 || p.OrgID != c.QualificationOrgID || p.UserID != c.QualificationOperatorID || in.AccountID != c.QualificationAccountID || in.Spec.Region != c.QualificationRegion {
		return security.ErrForbidden
	}
	imageParts := strings.Split(in.WorkerImage, "@sha256:")
	validImage := len(imageParts) == 2 && imageParts[0] != "" && len(imageParts[1]) == 64
	if validImage {
		_, decodeErr := hex.DecodeString(imageParts[1])
		validImage = decodeErr == nil
	}
	if !validImage || !in.ManagementReachable || !in.AcknowledgeUnknownCosts || !in.AcknowledgeNodeCosts || !strings.Contains(in.WorkerImage, "@sha256:") {
		return ErrInvalid
	}
	var count int
	for _, pool := range in.Spec.NodePools {
		if pool.ID != "" || pool.OperationID != "" || pool.RequestDigest != "" || pool.Autoscale || pool.Min != 0 || pool.Max != 0 || pool.Count < 1 || pool.Count > c.MaxNodes-count {
			return ErrInvalid
		}
		count += pool.Count
	}
	if count == 0 {
		return ErrInvalid
	}
	return nil
}
func clusterVector(in ClusterInput, options provider.ClusterOptions) (ResourceVector, error) {
	var v ResourceVector
	if !slices.Contains(options.Versions, in.Spec.Version) || !slices.Contains(options.Regions, in.Spec.Region) || !slices.Contains(options.Networks, in.Spec.NetworkID) {
		return v, ErrInvalid
	}
	for _, pool := range in.Spec.NodePools {
		found := false
		for _, size := range options.NodeSizeFacts {
			if size.ID != pool.Size {
				continue
			}
			found = true
			if size.CPUs <= 0 || size.MemoryMiB <= 0 || size.DiskGB <= 0 || pool.Count <= 0 {
				return v, ErrInvalid
			}
			quantities := []int64{1, int64(size.CPUs), int64(size.MemoryMiB), int64(size.DiskGB)}
			units := []int64{1, 1000, 1 << 20, 1_000_000_000}
			values := []*int64{&v.Servers, &v.CPUMillis, &v.MemoryBytes, &v.StorageBytes}
			for i, q := range quantities {
				if q > math.MaxInt64/units[i]/int64(pool.Count) {
					return v, ErrInvalid
				}
				n := q * units[i] * int64(pool.Count)
				if *values[i] > math.MaxInt64-n {
					return v, ErrInvalid
				}
				*values[i] += n
			}
		}
		if !found {
			return v, ErrInvalid
		}
	}
	return v, nil
}
func clusterDigest(in ClusterInput) string {
	if in.Retirement != nil {
		copyInput := *in.Retirement
		copyInput.Inventory.ObservedAt = time.Time{}
		in.Retirement = &copyInput
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Service) PreviewCluster(ctx context.Context, p security.Principal, cloud string, in ClusterInput) (*ClusterOperation, error) {
	if err := s.clusterEnabled(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	reader, access, err := s.clusterAuthority(ctx, p, cloud, in)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	var capacity ResourceVector
	var nodeCost int64
	var unknownCosts []string
	switch in.Kind {
	case "retire-cluster", "detach-runtime":
		if in.Retirement == nil || in.Retirement.ReviewDigest == "" || in.Attachment != nil || in.Maintenance != nil || !reflect.DeepEqual(in.Spec, provider.ClusterSpec{}) {
			return nil, ErrInvalid
		}
		review, checkErr := s.prepareClusterRetirement(ctx, p, cloud, in, reader, access, "")
		if checkErr != nil {
			return nil, checkErr
		}
		if len(review.Blockers) > 0 {
			return nil, &provider.ClusterLifecycleBlockedError{Reason: review.Blockers[0]}
		}
		if review.Digest != in.Retirement.ReviewDigest {
			return nil, ErrConflict
		}
		if in.Kind == "retire-cluster" {
			if checkErr := s.Adapters.Require(in.Provider, provider.ClusterDeletion); checkErr != nil {
				return nil, checkErr
			}
		}
		in.Retirement.Inventory = review.Inventory
		in.Retirement.ResourceFingerprint = review.ResourceFingerprint
	case "node-pool-create", "node-pool-update", "node-pool-delete", "cluster-upgrade", "cluster-configure":
		if in.Maintenance != nil && (in.Maintenance.ObservationDigest != "" || in.Maintenance.Pool != nil && (in.Maintenance.Pool.OperationID != "" || in.Maintenance.Pool.RequestDigest != "")) {
			return nil, ErrInvalid
		}
		capacity, nodeCost, unknownCosts, err = s.prepareMaintenance(ctx, p, cloud, &in, reader, access)
		if err != nil {
			return nil, err
		}
		if in.Kind == "node-pool-create" {
			in.Maintenance.Pool.OperationID = id
			in.Maintenance.Pool.RequestDigest = maintenanceHash(in.Maintenance.Pool)
		}
	case "create":
		if in.ResourceID != "" || in.Ref != (provider.ClusterRef{}) || in.ResourceRevision != 0 || in.Spec.OperationID != "" {
			return nil, ErrInvalid
		}
		if checkErr := s.clusterPurchaseGate(p, in); checkErr != nil {
			return nil, checkErr
		}
		in.ManagementQualificationDigest, in.ManagementEvidenceID, err = s.probeClusterManagement(ctx, p.OrgID, in)
		if err != nil {
			return nil, err
		}
		if checkErr := s.Adapters.Require(in.Provider, provider.ClusterCreation); checkErr != nil {
			return nil, checkErr
		}
		options, e := reader.Options(ctx, access, in.Spec.Region)
		if e != nil {
			return nil, e
		}
		if options.Digest != in.OptionsDigest || options.CheckedAt.IsZero() || s.now().Sub(options.CheckedAt) < 0 || s.now().Sub(options.CheckedAt) >= 5*time.Minute {
			return nil, ErrConflict
		}
		capacity, err = clusterVector(in, options)
		if err != nil {
			return nil, err
		}
		nodeCost, unknownCosts, err = clusterCosts(in, options, s.ManagedClusters.MaxNodeHourlyMicroUSD)
		if err != nil {
			return nil, err
		}
		in.Spec.OperationID = id
		in.Spec.ReconcileOnly = false
		in.OptionsCheckedAt = options.CheckedAt
		if validator, ok := reader.(provider.ClusterSpecValidator); ok {
			if checkErr := validator.ValidateClusterSpec(in.Spec); checkErr != nil {
				return nil, checkErr
			}
		}
	case "adopt-external", "adopt-managed":
		if in.Ref.ID == "" || in.Ref.AccountID != in.AccountID || in.Ref.Incarnation == "" || in.Ref.Region == "" {
			return nil, ErrInvalid
		}
		if !reflect.DeepEqual(in.Spec, provider.ClusterSpec{}) {
			return nil, ErrInvalid
		}
		if _, err = reader.GetCluster(ctx, access, in.Ref); err != nil {
			return nil, err
		}
		if checkErr := s.validateClusterAdoption(ctx, p.OrgID, cloud, in); checkErr != nil {
			return nil, checkErr
		}
	default:
		return nil, ErrInvalid
	}
	now := s.now().UTC()
	digest := clusterDigest(in)
	if digest == "" {
		return nil, ErrInvalid
	}
	o := &ClusterOperation{ID: id, OrgID: p.OrgID, CloudID: cloud, Kind: in.Kind, SchemaVersion: 1, Input: in, Digest: digest, Revision: 1, Status: "preview", Principal: p, CapacityTarget: capacity, EstimatedNodeHourlyMicroUSD: nodeCost, UnknownCosts: unknownCosts, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(15 * time.Minute), Steps: []ClusterStep{{Name: "provider", State: "pending", RequestDigest: digest}, {Name: "enrollment", State: "pending", RequestDigest: digest}}}
	if maintenanceKind(in.Kind) {
		o.ResourceID = in.ResourceID
		o.Steps[1].Name = "verification"
	}
	if retirementKind(in.Kind) {
		o.ResourceID = in.ResourceID
		o.Steps[1].Name = "verification"
		if in.Kind == "detach-runtime" {
			o.Steps = []ClusterStep{{Name: "disable", State: "pending", RequestDigest: digest}}
			for _, obj := range in.Retirement.Attachment.RBACObjects {
				o.Steps = append(o.Steps, ClusterStep{Name: obj.Kind + "/" + obj.Namespace + "/" + obj.Name, State: "pending", RequestDigest: digest})
			}
			o.Steps = append(o.Steps, ClusterStep{Name: "vault", State: "pending", RequestDigest: digest})
		}
	}
	if checkErr := s.Clusters.CreateClusterPreview(ctx, o); checkErr != nil {
		return nil, checkErr
	}
	s.recordCluster(ctx, "cloud.cluster.preview", o)
	return o, nil
}
func (s *Service) validateClusterAdoption(ctx context.Context, org, cloud string, in ClusterInput) error {
	if in.ResourceID == "" {
		if in.Kind == "adopt-managed" || in.ResourceRevision != 0 {
			return ErrInvalid
		}
		return nil
	}
	r, err := s.Clusters.GetManagedResource(ctx, org, in.ResourceID)
	if err != nil {
		return err
	}
	if r.Revision != in.ResourceRevision || r.DeletedAt != nil || (r.CloudID != "" && r.CloudID != cloud) || r.Provider != in.Provider || r.AccountID != in.AccountID || r.ExternalID != in.Ref.ID || r.Incarnation != in.Ref.Incarnation {
		return ErrConflict
	}
	if in.Kind == "adopt-managed" && (r.Ownership != "managed" || r.CreationReceipt == nil) {
		return ErrInvalid
	}
	if in.Kind == "adopt-external" && r.Ownership == "managed" {
		return ErrInvalid
	}
	return nil
}
func (s *Service) ApplyCluster(ctx context.Context, p security.Principal, cloud, id string, in ApplyInput) (*ClusterOperation, error) {
	if err := s.clusterEnabled(); err != nil {
		return nil, err
	}
	o, err := s.Clusters.GetClusterOperation(ctx, p.OrgID, cloud, id)
	if err != nil {
		return nil, err
	}
	if o.Digest != in.Review || o.Principal != p {
		return nil, ErrConflict
	}

	if o.Status == "preview" && !s.now().Before(o.ExpiresAt) {
		return nil, ErrConflict
	}
	reader, access, err := s.clusterAuthority(ctx, p, cloud, o.Input)
	if err != nil {
		return nil, err
	}
	if o.Status != "preview" {
		return o, nil
	}
	if o.EstimatedNodeHourlyMicroUSD > s.ManagedClusters.MaxNodeHourlyMicroUSD {
		return nil, security.ErrForbidden
	}
	if o.Kind == "create" {
		if checkErr := s.clusterPurchaseGate(p, o.Input); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := s.recheckClusterManagement(ctx, o); checkErr != nil {
			return nil, checkErr
		}
		call, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		options, e := reader.Options(call, access, o.Input.Spec.Region)
		if e != nil {
			return nil, e
		}
		if checkErr := s.refreshClusterAccount(call, o, reader, access); checkErr != nil {
			return nil, checkErr
		}
		if options.Digest != o.Input.OptionsDigest || s.now().Sub(options.CheckedAt) >= 5*time.Minute || options.CheckedAt.After(s.now()) {
			return nil, ErrConflict
		}
	} else if retirementKind(o.Kind) {
		review, checkErr := s.prepareClusterRetirement(ctx, p, cloud, o.Input, reader, access, o.ID)
		if checkErr != nil {
			return nil, checkErr
		}
		if len(review.Blockers) > 0 {
			return nil, ErrBusy
		}
		if review.Digest != o.Input.Retirement.ReviewDigest {
			return nil, ErrConflict
		}
		if o.Kind == "retire-cluster" {
			if checkErr := s.Adapters.Require(o.Input.Provider, provider.ClusterDeletion); checkErr != nil {
				return nil, checkErr
			}
		}
	} else if maintenanceKind(o.Kind) {
		capacity, cost, _, checkErr := s.prepareMaintenance(ctx, p, cloud, &o.Input, reader, access)
		if checkErr != nil {
			return nil, checkErr
		}
		if capacity != o.CapacityTarget || cost != o.EstimatedNodeHourlyMicroUSD {
			return nil, ErrConflict
		}
		if capacity.Servers > 0 {
			if err := s.refreshClusterAccount(ctx, o, reader, access); err != nil {
				return nil, err
			}
		}
	} else if checkErr := s.validateClusterAdoption(ctx, p.OrgID, cloud, o.Input); checkErr != nil {
		return nil, checkErr
	}
	store, ok := s.Clusters.(ClusterLifecycleStore)
	if !ok {
		return nil, ErrInvalid
	}
	if checkErr := store.AcceptClusterOperation(ctx, o, s.clusterCreateLimit()); checkErr != nil {
		return nil, checkErr
	}
	s.recordCluster(ctx, "cloud.cluster.accept", o)
	if s.ClusterWorkflow != nil {
		if checkErr := s.ClusterWorkflow.StartClusterOperation(ctx, o.OrgID, o.ID, o.Attempt); checkErr != nil {
			return o, checkErr
		}
	}
	return o, nil
}
func (s *Service) clusterCreateLimit() int {
	n := s.ManagedClusters.MaxAccountCreates
	if n <= 0 || n > 2 {
		return 2
	}
	return n
}
func (s *Service) RetryCluster(ctx context.Context, p security.Principal, cloud, id string) (*ClusterOperation, error) {
	return s.controlCluster(ctx, p, cloud, id, false)
}
func (s *Service) CancelCluster(ctx context.Context, p security.Principal, cloud, id string) (*ClusterOperation, error) {
	return s.controlCluster(ctx, p, cloud, id, true)
}
func (s *Service) controlCluster(ctx context.Context, p security.Principal, cloud, id string, cancel bool) (*ClusterOperation, error) {
	if err := s.clusterEnabled(); err != nil {
		return nil, err
	}
	o, err := s.Clusters.GetClusterOperation(ctx, p.OrgID, cloud, id)
	if err != nil {
		return nil, err
	}
	if _, _, err = s.clusterAuthority(ctx, p, cloud, o.Input); err != nil {
		return nil, err
	}
	store, ok := s.Clusters.(ClusterLifecycleStore)
	if !ok {
		return nil, ErrInvalid
	}
	o, err = store.ControlClusterOperation(ctx, p.OrgID, cloud, id, cancel)
	if err != nil {
		return nil, err
	}
	s.recordCluster(ctx, "cloud.cluster.control", o)
	if !cancel && s.ClusterWorkflow != nil {
		err = s.ClusterWorkflow.StartClusterOperation(ctx, o.OrgID, o.ID, o.Attempt)
	}
	return o, err
}
func (s *Service) RecoverClusterOperations(ctx context.Context) error {
	if s.Clusters == nil {
		return nil
	}
	if s.ClusterWorkflow == nil {
		return nil
	}
	operations, err := s.Clusters.ListPendingClusterOperations(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, o := range operations {
		if (!s.ManagedClusters.Enabled && !clusterObservationOnly(o)) || o.NextAttemptAt.After(s.now()) {
			continue
		}
		if err := s.ClusterWorkflow.StartClusterOperation(ctx, o.OrgID, o.ID, o.Attempt); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// clusterCosts estimates nodes only. Unknown charges stay itemized in the review.
func clusterCosts(in ClusterInput, options provider.ClusterOptions, limit int64) (nodeMicroUSD int64, unknownCosts []string, err error) {
	var total int64
	unknown := []string{"control_plane", "network_egress", "load_balancers", "persistent_storage"}
	for _, pool := range in.Spec.NodePools {
		for _, size := range options.NodeSizeFacts {
			if size.ID != pool.Size {
				continue
			}
			if size.HourlyUSD == nil {
				unknown = append(unknown, "nodes:"+pool.Name)
				continue
			}
			amount := math.Ceil(*size.HourlyUSD * 1_000_000 * float64(pool.Count))
			if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 || amount >= float64(math.MaxInt64) {
				return 0, nil, ErrInvalid
			}
			if total > limit || int64(amount) > limit-total {
				return 0, nil, ErrInvalid
			}
			total += int64(amount)
		}
	}
	return total, unknown, nil
}
