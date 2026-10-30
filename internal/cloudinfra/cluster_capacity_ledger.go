package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/pkg/provider"
)

// ClusterEnvelopeReview is populated by the server before the review is saved.
// A logical key is tied to the operation until an exact receipt binds a pool ID.
type ClusterEnvelopeReview struct {
	Automatic bool                        `json:"automatic,omitempty"`
	Temporary *ClusterTemporaryHoldReview `json:"temporary,omitempty"`
	Pools     []ClusterPoolEnvelopeReview `json:"pools"`
}
type ClusterTemporaryHoldReview struct {
	Nodes               []string                `json:"nodes"`
	Pools               []provider.NodePoolSpec `json:"pools"`
	ResourceFingerprint string                  `json:"resource_fingerprint"`
}
type ClusterPoolEnvelopeReview struct {
	EnvelopeID       string         `json:"envelope_id,omitempty"`
	Baseline         ResourceVector `json:"baseline"`
	Key              string         `json:"key"`
	PoolID           string         `json:"pool_id,omitempty"`
	RequestDigest    string         `json:"request_digest"`
	ExpectedRevision int64          `json:"expected_revision"`
	Maximum          ResourceVector `json:"maximum"`
	Initial          ResourceVector `json:"initial"`
	Unit             ResourceVector `json:"unit"`
	Money            MoneyEstimate  `json:"money"`
}
type ClusterCapacityEnvelope struct {
	PriorMaximum         *ResourceVector           `json:"prior_maximum,omitempty"`
	PriorMoney           *MoneyEstimate            `json:"prior_money,omitempty"`
	NodeIDs              []string                  `json:"node_ids,omitempty"`
	BindingOperationID   string                    `json:"binding_operation_id,omitempty"`
	BindingRequestDigest string                    `json:"binding_request_digest,omitempty"`
	ID                   string                    `json:"id"`
	OrgID                string                    `json:"org_id"`
	CloudID              string                    `json:"cloud_id"`
	OperationID          string                    `json:"operation_id"`
	ResourceID           string                    `json:"resource_id"`
	Provider             string                    `json:"provider"`
	AccountID            string                    `json:"account_id"`
	PoolID               string                    `json:"pool_id"`
	Review               ClusterPoolEnvelopeReview `json:"review"`
	Maximum              ResourceVector            `json:"maximum"`
	DesiredMaximum       ResourceVector            `json:"desired_maximum"`
	Consumed             ResourceVector            `json:"consumed"`
	Pending              ResourceVector            `json:"pending"`
	Money                MoneyEstimate             `json:"money"`
	PolicyRevision       int64                     `json:"policy_revision"`
	Revision             int64                     `json:"revision"`
	State                string                    `json:"state"`
	RequestedAt          time.Time                 `json:"requested_at"`
	CheckedAt            time.Time                 `json:"checked_at"`
	Ref                  provider.ClusterRef       `json:"ref"`
}
type ClusterEnvelopeInput struct {
	OrgID, CloudID, OperationID, EnvelopeID, ResourceID, Provider, AccountID string
	ExpectedPolicyRevision, ExpectedEnvelopeRevision                         int64
	Review                                                                   ClusterPoolEnvelopeReview
}

func clusterEnvelopesTx(ctx context.Context, tx driver.Tx, org string) ([]ClusterCapacityEnvelope, error) {
	rows, err := tx.Query(ctx, `SELECT body FROM loomplane_cluster_capacity_envelopes WHERE org_id=$1 ORDER BY id`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClusterCapacityEnvelope
	for rows.Next() {
		var raw []byte
		var e ClusterCapacityEnvelope
		if checkErr := rows.Scan(&raw); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := json.Unmarshal(raw, &e); checkErr != nil {
			return nil, checkErr
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func envelopeRemainder(e ClusterCapacityEnvelope) ResourceVector {
	return positiveCapacityDelta(positiveCapacityDelta(e.Maximum, e.Consumed), e.Pending)
}
func envelopeRemainderMoney(e ClusterCapacityEnvelope, count int64) (MoneyEstimate, error) {
	components := append([]MoneyComponent(nil), e.Money.Components...)
	for i := range components {
		if components[i].Name == "nodes" {
			components[i].Quantity = count
		}
	}
	return moneyEstimate(components, e.Money.HorizonSeconds, e.Money.CheckedAt)
}
func envelopeUsage(ctx context.Context, tx driver.Tx, org, cloud, providerName, account, exclude string) (local, shared ResourceVector, err error) {
	list, err := clusterEnvelopesTx(ctx, tx, org)
	if err != nil {
		return ResourceVector{}, ResourceVector{}, err
	}
	for _, e := range list {
		if e.OperationID == exclude {
			if e.PriorMaximum == nil {
				continue
			}
			e.Maximum = *e.PriorMaximum
			e.Pending = ResourceVector{}
		}
		v := envelopeRemainder(e)
		if e.CloudID == cloud {
			local, err = addCapacity(local, v)
			if err != nil {
				return local, shared, err
			}
		}
		if e.Provider == providerName && e.AccountID == account {
			shared, err = addCapacity(shared, v)
			if err != nil {
				return local, shared, err
			}
		}
	}
	return local, shared, nil
}

func (s *PgStore) ReserveClusterEnvelope(ctx context.Context, tx driver.Tx, in ClusterEnvelopeInput) (*ClusterCapacityEnvelope, error) {
	return s.writeClusterEnvelope(ctx, tx, in, false)
}
func (s *PgStore) AdjustClusterEnvelope(ctx context.Context, tx driver.Tx, in ClusterEnvelopeInput) (*ClusterCapacityEnvelope, error) {
	return s.writeClusterEnvelope(ctx, tx, in, true)
}
func (s *PgStore) writeClusterEnvelope(ctx context.Context, tx driver.Tx, in ClusterEnvelopeInput, adjust bool) (*ClusterCapacityEnvelope, error) {
	if checkErr := lockCapacityCloud(ctx, tx, in.OrgID, in.CloudID); checkErr != nil {
		return nil, checkErr
	}
	o, err := clusterOperationTx(ctx, tx, in.OrgID, in.CloudID, in.OperationID)
	if err != nil {
		return nil, err
	}
	if o.Input.EnvelopeReview == nil || o.Input.Provider != in.Provider || o.Input.AccountID != in.AccountID || o.Input.PolicyRevision != in.ExpectedPolicyRevision || o.Input.ResourceID != in.ResourceID {
		return nil, ErrConflict
	}
	matched := false
	for _, r := range o.Input.EnvelopeReview.Pools {
		if maintenanceHash(r) == maintenanceHash(in.Review) {
			matched = true
		}
	}
	if !matched || in.ExpectedEnvelopeRevision != in.Review.ExpectedRevision {
		return nil, ErrConflict
	}
	p, err := capacityPolicyTx(ctx, tx, in.OrgID, in.CloudID)
	if err != nil {
		return nil, err
	}
	if p.Revision != in.ExpectedPolicyRevision {
		return nil, ErrConflict
	}
	if in.EnvelopeID == "" {
		in.EnvelopeID = in.Review.EnvelopeID
		if in.EnvelopeID == "" {
			in.EnvelopeID = o.ID + ":" + in.Review.Key
		}
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT body FROM loomplane_cluster_capacity_envelopes WHERE org_id=$1 AND id=$2 FOR UPDATE`, in.OrgID, in.EnvelopeID).Scan(&raw)
	if err == nil {
		var existing ClusterCapacityEnvelope
		if checkErr := json.Unmarshal(raw, &existing); checkErr != nil {
			return nil, checkErr
		}
		if existing.OperationID == o.ID && maintenanceHash(existing.Review) == maintenanceHash(in.Review) {
			return &existing, nil
		}
		if existing.State == "released" || !adjust || existing.Revision != in.ExpectedEnvelopeRevision || existing.ResourceID != in.ResourceID || existing.PoolID != in.Review.PoolID {
			return nil, ErrConflict
		}
		if existing.Provider != in.Provider || existing.AccountID != in.AccountID || existing.Pending != (ResourceVector{}) || in.Review.Baseline != existing.Consumed {
			return nil, ErrConflict
		}
		priorMaximum, priorMoney := existing.Maximum, existing.Money
		existing.PriorMaximum = &priorMaximum
		existing.PriorMoney = &priorMoney
		if !capacityFits(in.Review.Maximum, existing.Maximum) {
			var held bool
			if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cloud_capacity_reservations WHERE org_id=$1 AND id=$2 AND status='reserved')`, in.OrgID, o.ID).Scan(&held); checkErr != nil {
				return nil, checkErr
			}
			if !held {
				return nil, ErrConflict
			}
			existing.Maximum = in.Review.Maximum
			existing.Money = in.Review.Money
		}
		existing.Pending = in.Review.Initial

		existing.DesiredMaximum = in.Review.Maximum
		existing.Review = in.Review
		existing.OperationID = o.ID
		existing.RequestedAt = time.Now().UTC()
		existing.PolicyRevision = p.Revision
		existing.State = "decreasing"
		if existing.Maximum == in.Review.Maximum {
			existing.State = "pending"
		}
		existing.Revision++
		return &existing, saveClusterEnvelope(ctx, tx, &existing)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if adjust || in.ExpectedEnvelopeRevision != 0 {
		return nil, ErrConflict
	}
	if in.Review.Key == "" || in.Review.Unit.Servers != 1 || !nonnegativeCapacity(in.Review.Maximum) || !capacityFits(in.Review.Initial, in.Review.Maximum) {
		return nil, ErrInvalid
	}
	e := &ClusterCapacityEnvelope{ID: in.EnvelopeID, OrgID: in.OrgID, CloudID: in.CloudID, OperationID: o.ID, ResourceID: in.ResourceID, Provider: in.Provider, AccountID: in.AccountID, PoolID: in.Review.PoolID, Review: in.Review, Maximum: in.Review.Maximum, DesiredMaximum: in.Review.Maximum, Consumed: in.Review.Baseline, Pending: in.Review.Initial, Money: in.Review.Money, PolicyRevision: p.Revision, Revision: 1, State: "pending", RequestedAt: time.Now().UTC()}
	// Only reserveClusterCapacity may create standing authority. Its pending hold
	// proves the shared ceiling admission happened inside this same transaction.
	var admitted bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cloud_capacity_reservations WHERE org_id=$1 AND id=$2 AND status='reserved')`, in.OrgID, o.ID).Scan(&admitted); checkErr != nil {
		return nil, checkErr
	}
	if !admitted {
		return nil, ErrConflict
	}
	return e, saveClusterEnvelope(ctx, tx, e)
}
func saveClusterEnvelope(ctx context.Context, tx driver.Tx, e *ClusterCapacityEnvelope) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cluster_capacity_envelopes(org_id,cloud_id,id,resource_id,provider,account_id,pool_id,revision,body) VALUES($1,$2,$3,nullif($4,''),$5,$6,nullif($7,''),$8,$9) ON CONFLICT(org_id,id) DO UPDATE SET resource_id=excluded.resource_id,pool_id=excluded.pool_id,revision=excluded.revision,body=excluded.body`, e.OrgID, e.CloudID, e.ID, e.ResourceID, e.Provider, e.AccountID, e.PoolID, e.Revision, raw)
	return err
}

// Membership is private persistence evidence and is never serialized by CapacityUsage.
type accountMembership struct {
	Complete  bool                      `json:"complete"`
	Provider  string                    `json:"provider"`
	AccountID string                    `json:"account_id"`
	CheckedAt time.Time                 `json:"checked_at"`
	Servers   []provider.CapacityServer `json:"servers"`
}

func saveAccountMembership(ctx context.Context, tx driver.Tx, org string, a CapacityAccountSnapshot) error {
	raw, err := json.Marshal(a.membership)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE loomplane_cloud_capacity_accounts SET membership=$4 WHERE org_id=$1 AND provider=$2 AND account_id=$3 AND checked_at=$5`, org, a.Provider, a.AccountID, raw, a.CheckedAt)
	return err
}

func prepareCreateEnvelopes(in ClusterInput, options provider.ClusterOptions) (*ClusterEnvelopeReview, error) {
	review := &ClusterEnvelopeReview{}
	for _, pool := range in.Spec.NodePools {
		single := in
		single.Spec.NodePools = []provider.NodePoolSpec{pool}
		single.Spec.NodePools[0].Count = 1
		unit, err := clusterVector(single, options)
		if err != nil {
			return nil, err
		}
		maximum, err := multiplyCapacity(unit, int64(pool.Count))
		if err != nil {
			return nil, err
		}
		var price *int64
		for _, size := range options.NodeSizeFacts {
			if size.ID == pool.Size && size.HourlyUSD != nil {
				n, priceErr := hourlyMicroUSD(*size.HourlyUSD)
				if priceErr != nil {
					return nil, priceErr
				}
				price = &n
			}
		}
		money, err := moneyEstimate([]MoneyComponent{{Name: "nodes", Source: "provider_options", Identity: in.Provider + "/" + in.AccountID + "/" + options.Digest + "/" + pool.Size, Quantity: int64(pool.Count), HourlyMicroUSD: price}}, 0, options.CheckedAt)
		if err != nil {
			return nil, err
		}
		review.Pools = append(review.Pools, ClusterPoolEnvelopeReview{Key: pool.Name, RequestDigest: provider.ClusterPoolRequestDigest(in.Spec, pool), Maximum: maximum, Initial: maximum, Unit: unit, Money: money})
	}
	return review, nil
}

func (s *PgStore) reserveReviewedEnvelopes(ctx context.Context, tx driver.Tx, o *ClusterOperation) error {
	if o.Input.EnvelopeReview == nil {
		return nil
	}
	for _, review := range o.Input.EnvelopeReview.Pools {
		method := s.ReserveClusterEnvelope
		if review.ExpectedRevision > 0 {
			method = s.AdjustClusterEnvelope
		}
		_, err := method(ctx, tx, ClusterEnvelopeInput{OrgID: o.OrgID, CloudID: o.CloudID, OperationID: o.ID, ResourceID: o.Input.ResourceID, Provider: o.Input.Provider, AccountID: o.Input.AccountID, ExpectedPolicyRevision: o.Input.PolicyRevision, ExpectedEnvelopeRevision: review.ExpectedRevision, Review: review})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *PgStore) SettleClusterEnvelope(ctx context.Context, tx driver.Tx, envelopeID string, observation ClusterCapacitySnapshot) error {
	// The resource lookup establishes scope before taking the organization gate.
	var org, cloud string
	if checkErr := tx.QueryRow(ctx, `SELECT org_id,cloud_id FROM loomplane_cluster_capacity_envelopes WHERE id=$1 AND resource_id=$2`, envelopeID, observation.ResourceID).Scan(&org, &cloud); checkErr != nil {
		return storeError(checkErr)
	}
	if checkErr := lockCapacityCloud(ctx, tx, org, cloud); checkErr != nil {
		return checkErr
	}
	r, err := scanManagedResource(tx.QueryRow(ctx, `SELECT `+managedResourceColumns+` FROM loomplane_managed_resources WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, observation.ResourceID))
	if err != nil {
		return err
	}
	var raw []byte
	if checkErr := tx.QueryRow(ctx, `SELECT body FROM loomplane_cluster_capacity_inventory WHERE org_id=$1 AND resource_id=$2`, org, r.ID).Scan(&raw); checkErr != nil {
		return checkErr
	}
	var saved ClusterCapacitySnapshot
	if checkErr := json.Unmarshal(raw, &saved); checkErr != nil {
		return checkErr
	}
	if maintenanceHash(saved) != maintenanceHash(observation) {
		return ErrConflict
	}
	return settleClusterEnvelopes(ctx, tx, r, saved, envelopeID)
}

func settleClusterEnvelopes(ctx context.Context, tx driver.Tx, r *ManagedResource, snapshot ClusterCapacitySnapshot, only string) error {
	now := time.Now()
	if clusterCapacityIdentityInvalid(r) || r.Observation == nil || !snapshot.Fresh(now) || snapshot.ResourceFingerprint != retirementResourceFingerprint(r) || snapshot.Ref != r.Observation.Ref || !freshClusterEnvelopeEvidence(r.Observation.ObservedAt, now) {
		return nil
	}
	var raw []byte
	if checkErr := tx.QueryRow(ctx, `SELECT membership FROM loomplane_cloud_capacity_accounts WHERE org_id=$1 AND provider=$2 AND account_id=$3`, r.OrgID, r.Provider, r.AccountID).Scan(&raw); errors.Is(checkErr, pgx.ErrNoRows) {
		return nil
	} else if checkErr != nil {
		return checkErr
	}
	if len(raw) == 0 {
		return nil
	}
	var membership *accountMembership
	if checkErr := json.Unmarshal(raw, &membership); checkErr != nil {
		return checkErr
	}
	if membership == nil || !membership.Complete || membership.Provider != r.Provider || membership.AccountID != r.AccountID || !freshClusterEnvelopeEvidence(membership.CheckedAt, now) {
		return nil
	}
	members := map[string]provider.CapacityServer{}
	for _, member := range membership.Servers {
		if member.ID == "" {
			return nil
		}
		if _, exists := members[member.ID]; exists {
			return nil
		}
		members[member.ID] = member
	}
	for _, node := range snapshot.Nodes {
		member, ok := members[node.ExternalVMID]
		if !ok || !reflect.DeepEqual(node.Provisioned, CapacityResources{Servers: number(1), CPUMillis: member.CPUMillis, MemoryBytes: member.MemoryBytes, StorageBytes: member.StorageBytes}) {
			return nil
		}
	}
	envelopes, err := clusterEnvelopesTx(ctx, tx, r.OrgID)
	if err != nil {
		return err
	}
	for i := range envelopes {
		e := &envelopes[i]
		if only != "" && e.ID != only || e.CloudID != r.CloudID || e.Provider != r.Provider || e.AccountID != r.AccountID || e.ResourceID != "" && e.ResourceID != r.ID {
			continue
		}
		o, err := clusterOperationTx(ctx, tx, r.OrgID, r.CloudID, e.OperationID)
		if err != nil {
			return err
		}
		if o.ResourceID != r.ID || len(o.Steps) == 0 || o.Steps[0].State != "completed" || o.Steps[0].CompletedAt == nil || snapshot.CheckedAt.Before(*o.Steps[0].CompletedAt) || r.Observation.ObservedAt.Before(*o.Steps[0].CompletedAt) || membership.CheckedAt.Before(*o.Steps[0].CompletedAt) {
			continue
		}
		if len(o.Receipts) != 1 || o.Receipts[0].OperationID != o.ID || o.Receipts[0].Ref != snapshot.Ref {
			continue
		}
		if o.Kind == "create" && (r.CreationReceipt == nil || *r.CreationReceipt != o.Receipts[0] || o.Receipts[0].RequestDigest != provider.ClusterRequestDigest(o.Input.Spec)) {
			continue
		}
		var pool *provider.NodePoolSpec
		for j := range r.Observation.Pools {
			candidate := &r.Observation.Pools[j]
			if e.PoolID != "" {
				if candidate.ID == e.PoolID {
					if pool != nil {
						return nil
					}
					pool = candidate
				}
				continue
			}
			if candidate.OperationID == o.ID && candidate.RequestDigest == e.Review.RequestDigest {
				if pool != nil {
					return nil
				}
				pool = candidate
			}
		}
		if pool == nil || pool.ID == "" {
			continue
		}
		if o.Receipts[0].PoolID != "" && o.Receipts[0].PoolID != pool.ID {
			continue
		}
		if e.PoolID == "" {
			if pool.OperationID != o.ID || pool.RequestDigest != e.Review.RequestDigest {
				continue
			}
		} else if pool.OperationID != e.BindingOperationID || pool.RequestDigest != e.BindingRequestDigest {
			continue
		}
		// Once bound, an envelope never follows markers to a replacement physical pool.
		if e.Ref.ID != "" && e.Ref != snapshot.Ref {
			continue
		}
		count := int64(0)
		nodeIDs := []string{}
		for _, node := range snapshot.Nodes {
			if node.PoolID == pool.ID {
				if !capacityFactsCover(node.Provisioned, e.Review.Unit) {
					return nil
				}
				nodeIDs = append(nodeIDs, node.ExternalVMID)
				count++
			}
		}
		if count != int64(pool.Count) {
			continue
		}
		consumed, err := multiplyCapacity(e.Review.Unit, count)
		if err != nil {
			return err
		}
		bounds := pool.Count
		if pool.Autoscale {
			bounds = pool.Max
		}
		if bounds < pool.Count || bounds < 0 {
			return nil
		}
		maximum, err := multiplyCapacity(e.Review.Unit, int64(bounds))
		if err != nil {
			return err
		}
		if consumed.Servers < e.Consumed.Servers {
			remaining := map[string]bool{}
			for _, id := range nodeIDs {
				remaining[id] = true
			}
			ambiguous := false
			for _, id := range e.NodeIDs {
				if _, exists := members[id]; exists && !remaining[id] {
					ambiguous = true
				}
			}
			if ambiguous {
				continue
			}
		}
		if e.State == "decreasing" {
			if r.Observation.ObservedAt.Before(e.RequestedAt) || snapshot.CheckedAt.Before(e.RequestedAt) || membership.CheckedAt.Before(e.RequestedAt) || maximum != e.DesiredMaximum || !capacityFits(consumed, e.DesiredMaximum) {
				continue
			}
			e.Maximum = e.DesiredMaximum
			e.Money = e.Review.Money
		} else if maximum != e.Maximum {
			continue
		}
		if !capacityFits(e.Pending, consumed) {
			continue
		}
		if e.PoolID == "" {
			e.BindingOperationID = pool.OperationID
			e.BindingRequestDigest = pool.RequestDigest
		}
		e.PoolID = pool.ID
		e.ResourceID = r.ID
		e.Ref = snapshot.Ref
		e.Consumed = consumed
		e.NodeIDs = nodeIDs
		e.Pending = ResourceVector{}
		e.CheckedAt = snapshot.CheckedAt
		e.State = "active"
		e.Revision++
		if checkErr := saveClusterEnvelope(ctx, tx, e); checkErr != nil {
			return checkErr
		}
	}
	// Release an operation's temporary hold only when every reviewed pool is bound.
	seen := map[string]bool{}
	for _, e := range envelopes {
		if e.ResourceID != r.ID || seen[e.OperationID] {
			continue
		}
		seen[e.OperationID] = true
		o, err := clusterOperationTx(ctx, tx, r.OrgID, r.CloudID, e.OperationID)
		if err != nil {
			return err
		}
		if o.Input.EnvelopeReview == nil {
			continue
		}
		covered := ResourceVector{}
		for _, review := range o.Input.EnvelopeReview.Pools {
			for _, candidate := range envelopes {
				if candidate.OperationID == o.ID && candidate.Review.Key == review.Key && candidate.State == "active" && candidate.Pending == (ResourceVector{}) && candidate.ResourceID == r.ID {
					credit := review.Initial
					if o.Kind == "node-pool-scaling" {
						credit = o.CapacityTarget
					}
					covered, err = addCapacity(covered, credit)
					if err != nil {
						return err
					}
				}
			}
		}
		var reservationRaw []byte
		err = tx.QueryRow(ctx, `SELECT body FROM loomplane_cloud_capacity_reservations WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND status='reserved'`, r.OrgID, r.CloudID, o.ID).Scan(&reservationRaw)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		var reservation infrastructureReservation
		if checkErr := json.Unmarshal(reservationRaw, &reservation); checkErr != nil {
			return checkErr
		}
		remaining := positiveCapacityDelta(reservation.Target, covered)
		reservation.Delta = remaining
		reservation.ManagedResourceID = r.ID
		reservationRaw, err = json.Marshal(reservation)
		if err != nil {
			return err
		}
		state := "reserved"
		if remaining == (ResourceVector{}) {
			state = "materialized"
		}
		if _, checkErr := tx.Exec(ctx, `UPDATE loomplane_cloud_capacity_reservations SET status=$4,servers=$5,cpu_millis=$6,memory_bytes=$7,storage_bytes=$8,body=$9 WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND status='reserved'`, r.OrgID, r.CloudID, o.ID, state, remaining.Servers, remaining.CPUMillis, remaining.MemoryBytes, remaining.StorageBytes, reservationRaw); checkErr != nil {
			return checkErr
		}
	}
	return settleClusterTemporaryHolds(ctx, tx, r, snapshot, membership)
}
func capacityFactsCover(facts CapacityResources, target ResourceVector) bool {
	for i, v := range factValues(facts) {
		if v == nil || *v < vectorValues(target)[i] {
			return false
		}
	}
	return target.GPUs == 0
}

func prepareMaintenanceEnvelope(in *ClusterInput, capacity ResourceVector) error {
	if in.Kind != "node-pool-create" || in.Maintenance == nil || in.Maintenance.Pool == nil || in.costEvidence == nil {
		return nil
	}
	pool := in.Maintenance.Pool
	count := int64(pool.Count)
	if count <= 0 {
		return ErrInvalid
	}
	unit := ResourceVector{Servers: 1, CPUMillis: capacity.CPUMillis / count, MemoryBytes: capacity.MemoryBytes / count, StorageBytes: capacity.StorageBytes / count}
	verified, err := multiplyCapacity(unit, count)
	if err != nil || verified != capacity {
		return ErrInvalid
	}
	money, err := clusterPoolEnvelopeMoney(in.costEvidence, pool.Name)
	if err != nil {
		return err
	}
	in.EnvelopeReview = &ClusterEnvelopeReview{Pools: []ClusterPoolEnvelopeReview{{Key: pool.Name, RequestDigest: pool.RequestDigest, Maximum: capacity, Initial: capacity, Unit: unit, Money: money}}}
	return nil
}

// clusterPoolEnvelopeMoney keeps the standing node maximum separate from non-node review costs.
func clusterPoolEnvelopeMoney(estimate *MoneyEstimate, poolName string) (MoneyEstimate, error) {
	if estimate == nil {
		return MoneyEstimate{}, ErrInvalid
	}
	var component *MoneyComponent
	for _, candidate := range estimate.Components {
		if candidate.Name != "nodes:"+poolName {
			continue
		}
		if component != nil {
			return MoneyEstimate{}, ErrInvalid
		}
		copyComponent := candidate
		copyComponent.Name = "nodes"
		component = &copyComponent
	}
	if component == nil {
		return MoneyEstimate{}, ErrInvalid
	}
	return moneyEstimate([]MoneyComponent{*component}, estimate.HorizonSeconds, estimate.CheckedAt)
}

func prepareTemporaryHoldTx(ctx context.Context, tx driver.Tx, o *ClusterOperation) error {
	if o.Kind != "cluster-upgrade" || o.CapacityTarget.Servers == 0 {
		return nil
	}
	r, err := scanManagedResource(tx.QueryRow(ctx, `SELECT `+managedResourceColumns+` FROM loomplane_managed_resources WHERE org_id=$1 AND id=$2`, o.OrgID, o.ResourceID))
	if err != nil {
		return err
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT body FROM loomplane_cluster_capacity_inventory WHERE org_id=$1 AND resource_id=$2`, o.OrgID, o.ResourceID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var snapshot ClusterCapacitySnapshot
	if checkErr := json.Unmarshal(raw, &snapshot); checkErr != nil {
		return checkErr
	}
	if !snapshot.Fresh(time.Now()) || snapshot.Ref != o.Input.Ref || snapshot.ResourceFingerprint != retirementResourceFingerprint(r) || r.Observation == nil {
		return nil
	}
	review := &ClusterTemporaryHoldReview{ResourceFingerprint: snapshot.ResourceFingerprint, Pools: append([]provider.NodePoolSpec(nil), r.Observation.Pools...)}
	for _, node := range snapshot.Nodes {
		review.Nodes = append(review.Nodes, node.ExternalVMID)
	}
	o.Input.EnvelopeReview = &ClusterEnvelopeReview{Temporary: review}
	return nil
}
func settleClusterTemporaryHolds(ctx context.Context, tx driver.Tx, r *ManagedResource, snapshot ClusterCapacitySnapshot, membership *accountMembership) error {
	reservations, err := capacityReservationsTx(ctx, tx, r.OrgID)
	if err != nil {
		return err
	}
	current := map[string]bool{}
	counts := map[string]int{}
	for _, node := range snapshot.Nodes {
		current[node.ExternalVMID] = true
		counts[node.PoolID]++
	}
	account := map[string]bool{}
	for _, node := range membership.Servers {
		account[node.ID] = true
	}
	for _, scoped := range reservations {
		hold := scoped.reservation
		if scoped.cloud != r.CloudID || hold.ClusterOperationID == "" {
			continue
		}
		o, err := clusterOperationTx(ctx, tx, r.OrgID, r.CloudID, hold.OperationID)
		if err != nil {
			return err
		}
		if o.ResourceID != r.ID || o.Kind != "cluster-upgrade" || o.Input.EnvelopeReview == nil || o.Input.EnvelopeReview.Temporary == nil || o.Input.Maintenance == nil || o.Input.Maintenance.Version != r.Observation.Version || len(o.Steps) == 0 || o.Steps[0].State != "completed" || o.Steps[0].CompletedAt == nil || len(o.Receipts) != 1 || o.Receipts[0].Ref != snapshot.Ref || o.Receipts[0].OperationID != o.ID {
			continue
		}
		done := *o.Steps[0].CompletedAt
		if snapshot.CheckedAt.Before(done) || r.Observation.ObservedAt.Before(done) || membership.CheckedAt.Before(done) {
			continue
		}
		reviewed := o.Input.EnvelopeReview.Temporary
		if reviewed.ResourceFingerprint != retirementResourceFingerprint(r) || len(reviewed.Pools) != len(r.Observation.Pools) {
			continue
		}
		converged := true
		for _, id := range reviewed.Nodes {
			if account[id] && !current[id] {
				converged = false
			}
		}
		for _, expected := range reviewed.Pools {
			found := false
			for _, pool := range r.Observation.Pools {
				if pool.ID == expected.ID && pool.Size == expected.Size && pool.Count == expected.Count && pool.Min == expected.Min && pool.Max == expected.Max && pool.Autoscale == expected.Autoscale && counts[pool.ID] == pool.Count {
					found = true
				}
			}
			if !found {
				converged = false
			}
		}
		if !converged {
			continue
		}
		if _, checkErr := tx.Exec(ctx, `UPDATE loomplane_cloud_capacity_reservations SET status='materialized' WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND status='reserved'`, r.OrgID, r.CloudID, o.ID); checkErr != nil {
			return checkErr
		}
	}
	return nil
}

func clusterEnvelopeAdmissionDelta(ctx context.Context, tx driver.Tx, o *ClusterOperation, requested ResourceVector) (ResourceVector, error) {
	if o.Input.EnvelopeReview == nil {
		return requested, nil
	}
	list, err := clusterEnvelopesTx(ctx, tx, o.OrgID)
	if err != nil {
		return ResourceVector{}, err
	}
	covered := ResourceVector{}
	growth := ResourceVector{}
	seen := map[string]bool{}
	for _, review := range o.Input.EnvelopeReview.Pools {
		if seen[review.Key] || review.Key == "" || review.Unit.Servers != 1 {
			return ResourceVector{}, ErrInvalid
		}
		seen[review.Key] = true
		for _, v := range []ResourceVector{review.Maximum, review.Initial, review.Baseline} {
			expected, multiplyErr := multiplyCapacity(review.Unit, v.Servers)
			if multiplyErr != nil || expected != v {
				return ResourceVector{}, ErrInvalid
			}
		}
		old := review.Baseline
		if review.ExpectedRevision == 0 {
			if review.EnvelopeID != "" || review.Baseline != (ResourceVector{}) {
				return ResourceVector{}, ErrConflict
			}
		} else {
			found := false
			for _, e := range list {
				if e.ID != review.EnvelopeID {
					continue
				}
				if e.OperationID == o.ID && maintenanceHash(e.Review) == maintenanceHash(review) && e.PriorMaximum != nil {
					old = *e.PriorMaximum
				} else {
					if e.Revision != review.ExpectedRevision || e.ResourceID != o.Input.ResourceID || e.Provider != o.Input.Provider || e.AccountID != o.Input.AccountID || e.PoolID != review.PoolID || e.Consumed != review.Baseline || e.Pending != (ResourceVector{}) {
						return ResourceVector{}, ErrConflict
					}
					old = e.Maximum
				}
				found = true
			}
			if !found {
				return ResourceVector{}, ErrConflict
			}
		}
		if review.Initial != (ResourceVector{}) && !capacityFits(review.Initial, positiveCapacityDelta(review.Maximum, review.Baseline)) {
			return ResourceVector{}, ErrInvalid
		}
		covered, err = addCapacity(covered, review.Initial)
		if err != nil {
			return ResourceVector{}, err
		}
		growth, err = addCapacity(growth, positiveCapacityDelta(review.Maximum, old))
		if err != nil {
			return ResourceVector{}, err
		}
	}
	if !capacityFits(covered, requested) {
		return ResourceVector{}, ErrInvalid
	}
	if o.Kind == "node-pool-scaling" {
		return growth, nil
	}
	return addCapacity(positiveCapacityDelta(requested, covered), growth)
}

func freshClusterEnvelopeEvidence(at, now time.Time) bool {
	return !at.IsZero() && !at.After(now) && now.Sub(at) <= ClusterCapacityFreshness
}

func checkAutomaticEnvelopePrices(o *ClusterOperation) error {
	if o.Input.EnvelopeReview == nil || !o.Input.EnvelopeReview.Automatic {
		return nil
	}
	if o.MoneyEstimate == nil || !o.MoneyEstimate.Known || !freshClusterEnvelopeEvidence(o.MoneyEstimate.CheckedAt, time.Now()) {
		return ErrConflict
	}
	for _, review := range o.Input.EnvelopeReview.Pools {
		if !review.Money.Known || !freshClusterEnvelopeEvidence(review.Money.CheckedAt, time.Now()) {
			return ErrConflict
		}
	}
	return nil
}

// Account refresh can finish a deleted cluster's hold after the cluster observer
// has stopped. The retirement receipt and absent bound VM IDs are both required.
func settleDeletedClusterEnvelopes(ctx context.Context, tx driver.Tx, org string, a CapacityAccountSnapshot) error {
	m := a.membership
	if m == nil || !m.Complete || m.Provider != a.Provider || m.AccountID != a.AccountID || !freshClusterEnvelopeEvidence(m.CheckedAt, time.Now()) {
		return nil
	}
	var persistedRaw []byte
	if checkErr := tx.QueryRow(ctx, `SELECT membership FROM loomplane_cloud_capacity_accounts WHERE org_id=$1 AND provider=$2 AND account_id=$3`, org, a.Provider, a.AccountID).Scan(&persistedRaw); checkErr != nil {
		return checkErr
	}
	var persisted *accountMembership
	if len(persistedRaw) == 0 {
		return nil
	}
	if checkErr := json.Unmarshal(persistedRaw, &persisted); checkErr != nil {
		return checkErr
	}
	if maintenanceHash(persisted) != maintenanceHash(m) {
		return nil
	}
	present := map[string]bool{}
	for _, node := range m.Servers {
		present[node.ID] = true
	}
	list, err := clusterEnvelopesTx(ctx, tx, org)
	if err != nil {
		return err
	}
	for _, e := range list {
		if e.Provider != a.Provider || e.AccountID != a.AccountID || e.State == "released" || e.ResourceID == "" || e.Ref.ID == "" || len(e.NodeIDs) == 0 {
			continue
		}
		gone := true
		for _, id := range e.NodeIDs {
			if present[id] {
				gone = false
			}
		}
		if !gone {
			continue
		}
		r, err := scanManagedResource(tx.QueryRow(ctx, `SELECT `+managedResourceColumns+` FROM loomplane_managed_resources WHERE org_id=$1 AND id=$2`, org, e.ResourceID))
		if err != nil {
			return err
		}
		if r.DeletedAt == nil || r.Observation == nil || r.Observation.Ref != e.Ref || m.CheckedAt.Before(*r.DeletedAt) {
			continue
		}
		rows, err := tx.Query(ctx, `SELECT body FROM loomplane_cluster_operations WHERE org_id=$1 AND resource_id=$2 AND status='succeeded' AND body->>'kind'='retire-cluster'`, org, r.ID)
		if err != nil {
			return err
		}
		verified := false
		for rows.Next() {
			var raw []byte
			var o ClusterOperation
			if checkErr := rows.Scan(&raw); checkErr != nil {
				rows.Close()
				return checkErr
			}
			if checkErr := json.Unmarshal(raw, &o); checkErr != nil {
				rows.Close()
				return checkErr
			}
			if retirementEnvelopeReleaseConfirmed(&o, e.Ref, m.CheckedAt) {
				verified = true
			}
		}
		rows.Close()
		if checkErr := rows.Err(); checkErr != nil {
			return checkErr
		}
		if !verified {
			continue
		}
		e.Maximum = ResourceVector{}
		e.DesiredMaximum = ResourceVector{}
		e.Consumed = ResourceVector{}
		e.Pending = ResourceVector{}
		e.State = "released"
		e.CheckedAt = m.CheckedAt
		e.Revision++
		if checkErr := saveClusterEnvelope(ctx, tx, &e); checkErr != nil {
			return checkErr
		}
	}
	return nil
}

func retirementEnvelopeReleaseConfirmed(o *ClusterOperation, ref provider.ClusterRef, checked time.Time) bool {
	return o.Status == "succeeded" && o.Kind == "retire-cluster" && len(o.Receipts) == 1 && o.Receipts[0].Kind == "retire-cluster" && o.Receipts[0].OperationID == o.ID && o.Receipts[0].Ref == ref && o.Receipts[0].RequestDigest == o.Digest && len(o.Steps) > 0 && o.Steps[0].State == "completed" && o.Steps[0].CompletedAt != nil && !checked.Before(*o.Steps[0].CompletedAt)
}

type retainedEnvelopeAccount struct{ ConnectionID, Provider, AccountID string }
type retainedEnvelopeAccountStore interface {
	retainedEnvelopeAccounts(context.Context, string, string) ([]retainedEnvelopeAccount, error)
}

func (s *PgStore) retainedEnvelopeAccounts(ctx context.Context, org, cloud string) ([]retainedEnvelopeAccount, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT r.connection_id,r.provider,r.account_id FROM loomplane_managed_resources r JOIN loomplane_cluster_capacity_envelopes e ON e.org_id=r.org_id AND e.resource_id=r.id WHERE r.org_id=$1 AND r.cloud_id=$2 AND r.deleted_at IS NOT NULL AND r.connection_id IS NOT NULL AND e.body->>'state'<>'released' ORDER BY r.connection_id,r.provider,r.account_id`, org, cloud)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []retainedEnvelopeAccount{}
	for rows.Next() {
		var item retainedEnvelopeAccount
		if checkErr := rows.Scan(&item.ConnectionID, &item.Provider, &item.AccountID); checkErr != nil {
			return nil, checkErr
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func legacyClusterPolicyGate(ctx context.Context, tx driver.Tx, org, cloud, operation string) (*CapacityBlock, error) {
	if checkErr := lockCapacityCloud(ctx, tx, org, cloud); checkErr != nil {
		return nil, checkErr
	}
	saved, err := scanOperation(tx.QueryRow(ctx, `SELECT body,principal FROM loomplane_infrastructure_operations WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, org, cloud, operation))
	if err != nil {
		return nil, err
	}
	if saved.Attempted || saved.Provider == "server" || saved.Input.Kind != "create" && saved.Input.Kind != "resize" {
		return nil, nil
	}
	var participating bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_managed_resources WHERE org_id=$1 AND provider=$2 AND account_id=$3 AND kind='cluster' AND deleted_at IS NULL) OR EXISTS(SELECT 1 FROM loomplane_cluster_capacity_envelopes WHERE org_id=$1 AND provider=$2 AND account_id=$3 AND body->>'state'<>'released') OR EXISTS(SELECT 1 FROM loomplane_cloud_capacity_reservations WHERE org_id=$1 AND status IN ('reserved','committed') AND body->>'provider'=$2 AND body->>'account_id'=$3 AND coalesce(body->>'cluster_operation_id','')<>'')`, org, saved.Provider, saved.AccountID).Scan(&participating)
	if err != nil || !participating {
		return nil, err
	}
	policy := &CapacityPolicy{}
	demand := CapacityDemand{OrgID: org, CloudID: cloud}
	if saved.Input.Kind == "resize" && saved.CapacityTarget != nil {
		delta, checkErr := operationCapacityDelta(ctx, tx, saved, policy, demand)
		if checkErr == nil && delta == (ResourceVector{}) {
			return nil, nil
		}
	}
	return capacityBlock(policy, demand, "review_required", "policy"), nil
}
