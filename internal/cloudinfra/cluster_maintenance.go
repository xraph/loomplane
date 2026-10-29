package cloudinfra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"sort"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type ClusterMaintenanceInput struct {
	ObservationDigest string                    `json:"observation_digest"`
	PoolID            string                    `json:"pool_id,omitempty"`
	ObservedRevision  int64                     `json:"observed_revision"`
	Pool              *provider.NodePoolSpec    `json:"pool,omitempty"`
	Update            *provider.ClusterUpdate   `json:"update,omitempty"`
	Version           string                    `json:"version,omitempty"`
	DrainDigest       string                    `json:"drain_digest,omitempty"`
	Window            *ClusterMaintenanceWindow `json:"window,omitempty"`
}

// ClusterMaintenanceWindow pins one unambiguous local interval and its UTC bounds.
type ClusterMaintenanceWindow struct {
	Timezone        string    `json:"timezone"`
	LocalStart      string    `json:"local_start"`
	DurationSeconds int64     `json:"duration_seconds"`
	StartUTC        time.Time `json:"start_utc"`
	EndUTC          time.Time `json:"end_utc"`
}

func resolveMaintenanceWindow(w *ClusterMaintenanceWindow) error {
	if w == nil || w.DurationSeconds < 60 || w.DurationSeconds > 4*60*60 {
		return ErrInvalid
	}
	loc, err := time.LoadLocation(w.Timezone)
	if err != nil || w.Timezone == "Local" || w.Timezone == "" {
		return ErrInvalid
	}
	start, err := time.ParseInLocation("2006-01-02T15:04", w.LocalStart, loc)
	if err != nil || start.Format("2006-01-02T15:04") != w.LocalStart {
		return ErrInvalid
	}
	// Reject both folds and gaps. You must review a unique civil-time interval.
	for delta := -180; delta <= 180; delta++ {
		if delta != 0 && start.Add(time.Duration(delta)*time.Minute).In(loc).Format("2006-01-02T15:04") == w.LocalStart {
			return ErrInvalid
		}
	}
	end := start.Add(time.Duration(w.DurationSeconds) * time.Second)
	if !w.StartUTC.IsZero() && !w.StartUTC.Equal(start) || !w.EndUTC.IsZero() && !w.EndUTC.Equal(end) {
		return ErrConflict
	}
	w.StartUTC = start.UTC()
	w.EndUTC = end.UTC()
	return nil
}
func maintenanceKind(kind string) bool {
	switch kind {
	case "node-pool-create", "node-pool-update", "node-pool-delete", "cluster-upgrade", "cluster-configure":
		return true
	}
	return false
}
func maintenanceCapability(kind string) provider.InfrastructureCapability {
	switch kind {
	case "node-pool-create":
		return provider.NodePoolCreation
	case "node-pool-update":
		return provider.NodePoolUpdate
	case "node-pool-delete":
		return provider.NodePoolDeletion
	case "cluster-upgrade":
		return provider.ClusterUpgrade
	default:
		return provider.ClusterConfigurationUpdate
	}
}
func maintenanceDestructive(in ClusterInput, observation provider.ClusterObservation) bool {
	if in.Kind == "cluster-upgrade" || in.Kind == "node-pool-delete" {
		return true
	}
	if in.Kind == "node-pool-update" && in.Maintenance != nil && in.Maintenance.Pool != nil {
		for _, pool := range observation.Pools {
			if pool.ID == in.Maintenance.PoolID {
				return in.Maintenance.Pool.Count < pool.Count
			}
		}
	}
	return false
}
func maintenanceHash(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func (s *Service) maintenanceResource(ctx context.Context, p security.Principal, cloud string, in ClusterInput) (*ManagedResource, error) {
	if checkErr := s.Authz.Require(ctx, p, security.ClusterManage, ""); checkErr != nil {
		return nil, checkErr
	}
	r, err := s.Clusters.GetManagedResource(ctx, p.OrgID, in.ResourceID)
	if err != nil {
		return nil, err
	}
	if r.DeletedAt != nil || r.Ownership != "managed" || r.CreationReceipt == nil || r.CloudID != cloud || r.Provider != in.Provider || r.AccountID != in.AccountID || r.ConnectionID != in.ConnectionID || r.ExternalID != in.Ref.ID || r.Incarnation != in.Ref.Incarnation || r.Observation == nil || r.Observation.Ref != in.Ref {
		return nil, ErrConflict
	}
	receipt := r.CreationReceipt
	if receipt.Kind != "create" || receipt.Ref != in.Ref || receipt.OperationID == "" || receipt.RequestDigest == "" {
		return nil, ErrConflict
	}
	validator, ok := s.Clusters.(interface {
		ValidateClusterCreationReceipt(context.Context, *ManagedResource) error
	})
	if !ok {
		return nil, ErrInvalid
	}
	if checkErr := validator.ValidateClusterCreationReceipt(ctx, r); checkErr != nil {
		return nil, checkErr
	}
	if r.Revision != in.ResourceRevision || in.Maintenance == nil || r.ObservedRevision != in.Maintenance.ObservedRevision {
		return nil, ErrConflict
	}
	return r, nil
}
func (s *Service) prepareMaintenance(ctx context.Context, p security.Principal, cloud string, in *ClusterInput, reader provider.ClusterReader, access provider.AccountAccess) (capacity ResourceVector, nodeCost int64, unknownCosts []string, failure error) {
	var zero ResourceVector
	if checkErr := s.Authz.Require(ctx, p, security.ClusterManage, ""); checkErr != nil {
		return zero, 0, nil, checkErr
	}
	if err := s.Adapters.Require(in.Provider, maintenanceCapability(in.Kind)); err != nil {
		if preflight, ok := reader.(provider.ClusterLifecyclePreflight); ok && in.Maintenance != nil && (in.Kind == "cluster-upgrade" || in.Kind == "node-pool-delete") {
			if checkErr := preflight.CheckClusterLifecycle(ctx, access, in.Ref, in.Kind, in.Maintenance.Version); checkErr != nil {
				return zero, 0, nil, checkErr
			}
		}
		return zero, 0, nil, err
	}
	r, err := s.maintenanceResource(ctx, p, cloud, *in)
	if err != nil {
		return zero, 0, nil, err
	}
	if in.Attachment != nil || !reflect.DeepEqual(in.Spec, provider.ClusterSpec{}) {
		return zero, 0, nil, ErrInvalid
	}
	m := in.Maintenance
	observation, err := reader.GetCluster(ctx, access, in.Ref)
	if err != nil {
		return zero, 0, nil, err
	}
	if observation.Ref != in.Ref || observation.Endpoint != r.Observation.Endpoint || observation.CAHash != "" && observation.CAHash != r.Observation.CAHash || observation.State != "running" {
		return zero, 0, nil, ErrConflict
	}
	material := observation
	material.ObservedAt = time.Time{}
	material.CAHash = r.Observation.CAHash
	material.Pools = append([]provider.NodePoolSpec(nil), observation.Pools...)
	sort.Slice(material.Pools, func(i, j int) bool { return material.Pools[i].ID < material.Pools[j].ID })
	observedDigest := maintenanceHash(material)
	if m.ObservationDigest == "" {
		m.ObservationDigest = observedDigest
	} else if m.ObservationDigest != observedDigest {
		return zero, 0, nil, ErrConflict
	}
	if m.Window != nil {
		if checkErr := resolveMaintenanceWindow(m.Window); checkErr != nil {
			return zero, 0, nil, checkErr
		}
		if !s.now().Before(m.Window.EndUTC) {
			return zero, 0, nil, ErrConflict
		}
	}
	var current *provider.NodePoolSpec
	for i := range observation.Pools {
		if observation.Pools[i].ID == m.PoolID {
			current = &observation.Pools[i]
		}
	}
	if m.Pool != nil && (m.Pool.Name == "" || m.Pool.Size == "" || m.Pool.Count < 1 || m.Pool.Autoscale || m.Pool.Min != 0 || m.Pool.Max != 0 || m.Pool.ProviderOptions != nil) {
		return zero, 0, nil, ErrInvalid
	}
	var delta *provider.NodePoolSpec
	switch in.Kind {
	case "node-pool-create":
		if m.Pool == nil || m.PoolID != "" || m.Pool.ID != "" || m.Update != nil || m.Version != "" {
			return zero, 0, nil, ErrInvalid
		}
		for _, pool := range observation.Pools {
			if pool.Name == m.Pool.Name {
				return zero, 0, nil, ErrConflict
			}
		}
		d := *m.Pool
		delta = &d
	case "node-pool-update":
		if current == nil || m.Pool == nil || m.Pool.ID != "" || m.Update != nil || m.Version != "" {
			return zero, 0, nil, ErrInvalid
		}
		if current.Autoscale {
			return zero, 0, nil, &provider.ClusterLifecycleBlockedError{Reason: "native_count_owner"}
		}
		if current.Name != m.Pool.Name || current.Size != m.Pool.Size || !maps.Equal(current.Labels, m.Pool.Labels) || !slices.Equal(current.Taints, m.Pool.Taints) {
			return zero, 0, nil, provider.ErrInfrastructureUnsupported
		}
		if m.Pool.Count == current.Count {
			return zero, 0, nil, ErrConflict
		}
		if m.Pool.Count > current.Count {
			d := *m.Pool
			d.Count -= current.Count
			delta = &d
		}
	case "node-pool-delete":
		if current == nil || m.Pool != nil || m.Update != nil || m.Version != "" {
			return zero, 0, nil, ErrInvalid
		}
		if current.Autoscale || len(observation.Pools) < 2 {
			return zero, 0, nil, &provider.ClusterLifecycleBlockedError{Reason: "pool_minimum_or_native_owner"}
		}
	case "cluster-upgrade":
		if m.Version == "" || m.Version == observation.Version || m.Pool != nil || m.Update != nil || m.PoolID != "" || m.Window == nil {
			return zero, 0, nil, &provider.ClusterLifecycleBlockedError{Reason: "unsupported_upgrade_path"}
		}
	case "cluster-configure":
		if m.Update == nil || m.Pool != nil || m.Version != "" || m.PoolID != "" || len(m.Update.FieldMask) != 1 || m.Update.FieldMask[0] != "maintenance" || m.Update.HA || m.Update.ProviderOptions != nil {
			return zero, 0, nil, provider.ErrInfrastructureUnsupported
		}
	}
	if in.Kind == "cluster-configure" {
		if !slices.Contains([]string{"any", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}, m.Update.Maintenance.Day) {
			return zero, 0, nil, ErrInvalid
		}
		if _, checkErr := time.Parse("15:04", m.Update.Maintenance.StartTime); checkErr != nil {
			return zero, 0, nil, ErrInvalid
		}
	}
	destructive := maintenanceDestructive(*in, observation)
	if destructive {
		preflight, ok := reader.(provider.ClusterLifecyclePreflight)
		if !ok {
			return zero, 0, nil, provider.ErrInfrastructureUnsupported
		}
		kind := in.Kind
		if kind == "node-pool-update" {
			kind = "node-pool-decrease"
		}
		if checkErr := preflight.CheckClusterLifecycle(ctx, access, in.Ref, kind, m.Version); checkErr != nil {
			return zero, 0, nil, checkErr
		}
		review, err := s.PreviewClusterDrain(ctx, p, cloud, r.ID, m.PoolID)
		if err != nil {
			return zero, 0, nil, err
		}
		if len(review.Blockers) > 0 {
			return zero, 0, nil, &provider.ClusterLifecycleBlockedError{Reason: review.Blockers[0]}
		}
		if m.DrainDigest == "" || m.DrainDigest != review.Digest {
			return zero, 0, nil, ErrConflict
		}
		if in.Kind == "cluster-upgrade" {
			return s.maintenanceCapacity(ctx, p, in, reader, access, observation.Pools)
		}
	}
	if delta != nil {
		total := delta.Count
		for _, pool := range observation.Pools {
			if pool.Count < 1 || pool.Count > s.ManagedClusters.MaxNodes-total {
				return zero, 0, nil, ErrInvalid
			}
			total += pool.Count
		}
		return s.maintenanceCapacity(ctx, p, in, reader, access, []provider.NodePoolSpec{*delta})
	}
	return zero, 0, nil, nil
}
func (s *Service) maintenanceCapacity(ctx context.Context, p security.Principal, in *ClusterInput, reader provider.ClusterReader, access provider.AccountAccess, pools []provider.NodePoolSpec) (capacity ResourceVector, nodeCost int64, unknownCosts []string, failure error) {
	var zero ResourceVector
	if p.OrgID != s.ManagedClusters.QualificationOrgID || p.UserID != s.ManagedClusters.QualificationOperatorID || in.AccountID != s.ManagedClusters.QualificationAccountID || in.Ref.Region != s.ManagedClusters.QualificationRegion || !in.AcknowledgeNodeCosts || !in.AcknowledgeUnknownCosts {
		return zero, 0, nil, security.ErrForbidden
	}
	count := 0
	for _, pool := range pools {
		if pool.Count < 1 || pool.Autoscale || pool.Min != 0 || pool.Max != 0 || pool.ProviderOptions != nil || pool.Count > s.ManagedClusters.MaxNodes-count {
			return zero, 0, nil, ErrInvalid
		}
		count += pool.Count
	}
	if count == 0 {
		return zero, 0, nil, ErrInvalid
	}
	options, err := reader.Options(ctx, access, in.Ref.Region)
	if err != nil {
		return zero, 0, nil, err
	}
	if options.Digest != in.OptionsDigest || options.CheckedAt.IsZero() || options.CheckedAt.After(s.now()) || s.now().Sub(options.CheckedAt) >= 5*time.Minute {
		return zero, 0, nil, ErrConflict
	}
	// The size catalog is independent of the cluster's old network/version options.
	synthetic := *in
	synthetic.Spec = provider.ClusterSpec{Region: in.Ref.Region, NodePools: pools}
	options.Versions = []string{""}
	options.Networks = []string{""}
	v, err := clusterVector(synthetic, options)
	if err != nil {
		return zero, 0, nil, err
	}
	cost, unknown, err := clusterCosts(synthetic, options, s.ManagedClusters.MaxNodeHourlyMicroUSD)
	if err != nil {
		return zero, 0, nil, err
	}
	estimate, err := s.clusterMoney(ctx, synthetic, options, reader, access)
	if err != nil {
		return zero, 0, nil, err
	}
	if in.PriceDigest != "" && in.PriceDigest != estimate.PriceDigest {
		return zero, 0, nil, ErrConflict
	}
	in.costEvidence = &estimate
	return v, cost, unknown, nil
}
