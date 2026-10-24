package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

type expansionSchedule struct {
	mu    sync.Mutex
	after string
}

// CapacityDemandAuthority resolves immutable approved intent and rechecks its
// actor's current deployment permission. Implementations reject revoked grants,
// changed targets and unapproved plans. Caller input cannot register authority.
type CapacityDemandAuthority interface {
	ResolveApprovedCapacityDemand(context.Context, string, string, string) (*CapacityDemand, error)
}

// CapacityDemandWakeup signals a durable deployment wait. It carries a hint only;
// consumers revalidate authority and admission before resuming execution.
type CapacityDemandWakeup interface {
	WakeCapacityDemand(context.Context, CapacityDemand) error
}

func expansionDemandFits(host, reserve, used, demand ResourceVector) bool {
	if !nonnegativeCapacity(demand) || demand.Servers != 0 {
		return false
	}
	total, err := addCapacity(reserve, used)
	if err != nil {
		return false
	}
	total, err = addCapacity(total, demand)
	return err == nil && capacityFits(total, host)
}
func sameExpansionTemplate(a, b PoolExpansionPolicy) bool { return reflect.DeepEqual(a, b) }
func (s *Service) approvedCapacityDemand(ctx context.Context, d CapacityDemand) error {
	if s.CapacityDemandAuthority == nil {
		return security.ErrForbidden
	}
	saved, err := s.CapacityDemandAuthority.ResolveApprovedCapacityDemand(ctx, d.OrgID, d.PlanID, d.Service)
	if err != nil {
		return err
	}
	if saved == nil || *saved != d || d.OrgID == "" || d.PlanID == "" || d.Service == "" || d.IntentHash == "" || d.PoolID == "" || !nonnegativeCapacity(d.Resources) || d.Resources.Servers != 0 || d.Resources.CPUMillis <= 0 || d.Resources.MemoryBytes <= 0 {
		return security.ErrForbidden
	}
	return nil
}
func expansionDecision(e *Expansion) *CapacityDecision {
	block := e.Block
	if block == nil {
		reason := "provisioning"
		if e.Status == "awaiting_approval" {
			reason = "expansion_approval"
		}
		block = &CapacityBlock{Reason: reason, CloudID: e.CloudID, PoolID: e.PoolID, PolicyRevision: e.PolicyRevision, Retryable: true}
	}
	return &CapacityDecision{ExpansionID: e.ID, Block: block}
}

// EnsureCapacity returns a readiness candidate, never workload admission proof.
// Planning must reserve and CAS that exact placement before runtime execution.
// The demand retains its approved policy revision; new effects use current policy.
func (s *Service) EnsureCapacity(ctx context.Context, d CapacityDemand) (*CapacityDecision, error) {
	if authorityErr := s.approvedCapacityDemand(ctx, d); authorityErr != nil {
		return nil, authorityErr
	}
	st, ok := s.Store.(*PgStore)
	if !ok {
		return nil, ErrInvalid
	}
	if d.Resources.StorageBytes != 0 {
		return &CapacityDecision{Block: &CapacityBlock{Reason: "placement_constraint", Resource: "storage_bytes", CloudID: d.CloudID, PoolID: d.PoolID}}, nil
	}
	if placement, checkErr := st.capacityCandidate(ctx, d, ""); checkErr == nil {
		return &CapacityDecision{Placement: placement}, nil
	} else if !errors.Is(checkErr, ErrConflict) {
		return nil, checkErr
	}
	if !s.EnableAutomaticCapacity {
		return &CapacityDecision{Block: &CapacityBlock{Reason: "review_required", CloudID: d.CloudID, PoolID: d.PoolID, Resource: "rollout"}}, nil
	}
	var result *CapacityDecision
	err := st.withExpansionLease(ctx, d.OrgID, d.CloudID, d.PoolID, func(ctx context.Context, token string) error {
		if authorityErr := s.approvedCapacityDemand(ctx, d); authorityErr != nil {
			return authorityErr
		}
		e, err := st.demandExpansion(ctx, d)
		if err == nil {
			if updateErr := s.refreshPendingExpansion(ctx, st, e, token); updateErr != nil {
				return updateErr
			}
			if e.OperationID == "" && e.Status == "reserved" {
				if acceptErr := s.acceptPolicyExpansion(ctx, st, e, nil, token); acceptErr != nil {
					if saveErr := s.blockExpansion(ctx, st, e, acceptErr, token); saveErr != nil {
						return saveErr
					}
				}
			}
			if e.Status == "ready" {
				o, getErr := st.GetOperation(ctx, e.OrgID, e.CloudID, e.OperationID)
				if getErr != nil {
					return getErr
				}
				candidate, getErr := st.capacityCandidate(ctx, d, o.ServerID)
				if getErr == nil {
					result = &CapacityDecision{Placement: candidate, ExpansionID: e.ID}
					return nil
				}
			}
			result = expansionDecision(e)
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		policy, err := st.GetCapacityPolicy(ctx, d.OrgID, d.CloudID)
		if err != nil {
			return err
		}
		pool, found := expansionPool(policy, d.PoolID)
		if !policy.Enabled || !found || d.StickyServerID != "" || d.LocalData || d.Resources.StorageBytes != 0 {
			result = &CapacityDecision{Block: capacityBlock(policy, d, "placement_constraint", "pool")}
			return nil
		}
		expansions, err := st.poolExpansions(ctx, d.OrgID, d.CloudID, d.PoolID)
		if err != nil {
			return err
		}
		for _, candidate := range expansions {
			if candidate.PolicyRevision != policy.Revision || candidate.Architecture != d.Architecture && d.Architecture != "" {
				continue
			}
			if candidate.Status == "blocked" || candidate.Status == "ready" {
				continue
			}
			if attachErr := st.attachExpansionDemand(ctx, candidate, d, token); attachErr == nil {
				result = expansionDecision(candidate)
				return nil
			} else if !errors.Is(attachErr, ErrConflict) {
				return attachErr
			}
		}
		e, err = s.newExpansion(ctx, st, policy, pool, d, false, token)
		if err != nil {
			return err
		}
		if attachErr := st.attachExpansionDemand(ctx, e, d, token); attachErr != nil {
			return attachErr
		}
		if !policy.RequireExpansionApproval {
			if checkErr := s.acceptPolicyExpansion(ctx, st, e, nil, token); checkErr != nil {
				if saveErr := s.blockExpansion(ctx, st, e, checkErr, token); saveErr != nil {
					return saveErr
				}
			}
		}
		result = expansionDecision(e)
		return nil
	})
	return result, err
}
func expansionPool(p *CapacityPolicy, id string) (PoolExpansionPolicy, bool) {
	for _, pool := range p.Pools {
		if pool.PoolID == id {
			return pool, true
		}
	}
	return PoolExpansionPolicy{}, false
}
func (s *Service) newExpansion(ctx context.Context, st *PgStore, policy *CapacityPolicy, pool PoolExpansionPolicy, d CapacityDemand, minimum bool, token string) (*Expansion, error) {
	principal, err := st.expansionAuthority(ctx, policy.OrgID, policy.CloudID, policy.Revision)
	if err != nil {
		return nil, err
	}
	if _, authErr := s.authorizeCapacityPrincipal(ctx, principal, policy.CloudID); authErr != nil {
		return nil, authErr
	}
	options, err := s.ServerOptions(ctx, principal, policy.CloudID, pool.ConnectionID)
	if err != nil {
		return nil, err
	}
	size := pool.BootstrapSize
	if !minimum {
		size, err = SelectExpansionSize(options, pool, d)
		if err != nil {
			return nil, err
		}
	}
	if minimum {
		pinned := pool
		pinned.AllowedSizes = []string{size}
		if _, sizeErr := SelectExpansionSize(options, pinned, CapacityDemand{}); sizeErr != nil {
			return nil, sizeErr
		}
	}
	if !slices.Contains(pool.AllowedSizes, size) {
		return nil, ErrConflict
	}
	var resources ResourceVector
	for _, candidate := range options.Sizes {
		if candidate.ID == size {
			resources, err = sizeResources(candidate)
			if err != nil {
				return nil, err
			}
		}
	}
	if resources.Servers != 1 {
		return nil, ErrConflict
	}
	conn, raw, err := s.Connections.Credential(ctx, policy.OrgID, pool.ConnectionID)
	if err != nil {
		return nil, err
	}
	credential, err := Credential(conn, raw)
	if err != nil {
		return nil, err
	}
	id, err := randomID("expansion")
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	status := "reserved"
	if policy.RequireExpansionApproval {
		status = "awaiting_approval"
	}
	e := &Expansion{ID: id, OrgID: policy.OrgID, CloudID: policy.CloudID, PoolID: pool.PoolID, Status: status, IntentHash: id, Revision: 1, PolicyRevision: policy.Revision, Resources: resources, Provider: conn.Cloud, AccountID: credential.AccountID, ConnectionID: conn.ID, ConnectionGeneration: conn.Generation, Region: pool.Region, Size: size, Architecture: pool.Architecture, Image: pool.Image, Template: pool, Minimum: minimum, CreatedAt: now, UpdatedAt: now}
	if createErr := st.createExpansion(ctx, e, token); createErr != nil {
		return nil, createErr
	}
	return e, nil
}
func (s *Service) validateExpansionDemand(ctx context.Context, st *PgStore, e *Expansion) error {
	demands, err := st.expansionDemands(ctx, e)
	if err != nil {
		return err
	}
	if e.Minimum && len(demands) == 0 {
		return nil
	}
	if len(demands) == 0 {
		return security.ErrForbidden
	}
	for _, d := range demands {
		if authorityErr := s.approvedCapacityDemand(ctx, d); authorityErr != nil {
			return authorityErr
		}
	}
	return nil
}
func (s *Service) acceptPolicyExpansion(ctx context.Context, st *PgStore, e *Expansion, approver *security.Principal, token string) error {
	if !s.EnableAutomaticCapacity {
		return security.ErrForbidden
	}
	if demandErr := s.validateExpansionDemand(ctx, st, e); demandErr != nil {
		return demandErr
	}
	policy, err := st.GetCapacityPolicy(ctx, e.OrgID, e.CloudID)
	if err != nil {
		return err
	}
	pool, found := expansionPool(policy, e.PoolID)
	if !policy.Enabled || !found || !sameExpansionTemplate(pool, e.Template) || policy.Revision != e.PolicyRevision {
		return ErrConflict
	}
	p, err := st.expansionAuthority(ctx, e.OrgID, e.CloudID, e.PolicyRevision)
	if err != nil {
		return err
	}
	if policy.RequireExpansionApproval {
		if approver == nil {
			return security.ErrForbidden
		}
		p = *approver
		now := s.now().UTC()
		e.ApprovedBy = p.UserID
		e.ApprovedAt = &now
	}
	if p.ReadOnly || p.OrgID != e.OrgID {
		return security.ErrForbidden
	}
	profile, err := s.authorizeCapacityPrincipal(ctx, p, e.CloudID)
	if err != nil {
		return err
	}
	options, err := s.ServerOptions(ctx, p, e.CloudID, e.ConnectionID)
	if err != nil {
		return err
	}
	pinned := e.Template
	pinned.AllowedSizes = []string{e.Size}
	if _, sizeErr := SelectExpansionSize(options, pinned, CapacityDemand{}); sizeErr != nil {
		return sizeErr
	}
	var o *Operation
	if e.OperationID != "" {
		o, err = st.GetOperation(ctx, e.OrgID, e.CloudID, e.OperationID)
		if err != nil {
			return err
		}
		if o.Status != "preview" {
			return nil
		}
		if o.Principal != p {
			return ErrConflict
		}
	} else {
		o, err = s.PreviewServer(ctx, p, e.CloudID, ServerInput{Kind: "create", ConnectionID: e.ConnectionID, Name: "capacity-" + e.ID[len(e.ID)-16:], Region: e.Region, Size: e.Size, Image: e.Image, CloudRevision: profile.Revision})
		if err != nil {
			return err
		}
		e.OperationID = o.ID
		if saveErr := st.saveExpansion(ctx, e, token); saveErr != nil {
			return saveErr
		}
	}
	if !s.now().Before(o.ExpiresAt) {
		return ErrConflict
	}
	if _, _, err = s.validateConnection(ctx, o); err != nil {
		return err
	}
	if demandErr := s.validateExpansionDemand(ctx, st, e); demandErr != nil {
		return demandErr
	}
	o.Status = "pending"
	o.Attempt = 1
	o.UpdatedAt = s.now().UTC()
	if acceptErr := st.acceptExpansion(ctx, e, o, p, token); acceptErr != nil {
		return acceptErr
	}
	s.record(ctx, p, "cloud.capacity_expansion.accept", o)
	if checkErr := s.Workflow.StartInfrastructure(ctx, o.OrgID, o.ID, o.Attempt); checkErr != nil {
		s.record(ctx, p, "cloud.server.enqueue_pending", o)
	}
	return nil
}
func (s *Service) blockExpansion(ctx context.Context, st *PgStore, e *Expansion, cause error, token string) error {
	if stopped, retireErr := st.retireObsoleteMinimum(ctx, e, token); retireErr != nil || stopped {
		return retireErr
	}
	var admission *CapacityAdmissionError
	e.Block = &CapacityBlock{Reason: "review_required", CloudID: e.CloudID, PoolID: e.PoolID, PolicyRevision: e.PolicyRevision, Retryable: true}
	if errors.As(cause, &admission) {
		e.Block = &admission.Block
	}
	e.Status = "blocked"
	if e.Block.Reason == "expansion_approval" {
		e.Status = "awaiting_approval"
	}
	return st.saveExpansion(ctx, e, token)
}
func (s *Service) ApproveExpansion(ctx context.Context, p security.Principal, cloud, id string, expectedRevision int64) (*Expansion, error) {
	if _, authErr := s.authorizeCapacityPrincipal(ctx, p, cloud); authErr != nil {
		return nil, authErr
	}
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	st, ok := s.Store.(*PgStore)
	if !ok {
		return nil, ErrInvalid
	}
	e, err := st.GetExpansion(ctx, p.OrgID, cloud, id)
	if err != nil {
		return nil, err
	}
	err = st.withExpansionLease(ctx, e.OrgID, e.CloudID, e.PoolID, func(ctx context.Context, token string) error {
		var readErr error
		e, readErr = st.GetExpansion(ctx, p.OrgID, cloud, id)
		if readErr != nil {
			return readErr
		}
		if e.Revision != expectedRevision || e.Status != "awaiting_approval" && e.Status != "blocked" {
			return ErrConflict
		}
		if e.OperationID != "" {
			o, getErr := st.GetOperation(ctx, e.OrgID, e.CloudID, e.OperationID)
			if getErr != nil {
				return getErr
			}
			if o.Status != "preview" {
				if resumeErr := s.resumeAcceptedExpansion(ctx, st, e, &p, token); resumeErr != nil {
					if saveErr := s.blockExpansion(ctx, st, e, resumeErr, token); saveErr != nil {
						return saveErr
					}
					return resumeErr
				}
				return nil
			}
		}
		if readErr = s.acceptPolicyExpansion(ctx, st, e, &p, token); readErr != nil {
			if saveErr := s.blockExpansion(ctx, st, e, readErr, token); saveErr != nil {
				return saveErr
			}
			return readErr
		}
		return nil
	})
	if err == nil {
		s.wakeCapacityCloud(ctx, p.OrgID, cloud, fmt.Sprintf("approval:%s:%d", e.ID, e.Revision))
	}
	return e, err
}
func (s *Service) DetachCapacityDemand(ctx context.Context, org, plan, service string) error {
	if org == "" || plan == "" || service == "" {
		return ErrInvalid
	}
	st, ok := s.Store.(*PgStore)
	if !ok {
		return ErrInvalid
	}
	return st.detachCapacityDemand(ctx, org, plan, service)
}

// ReconcileExpansions processes bounded pool leases. Disabled rollout still
// observes accepted operations, but never accepts another provider effect.
func (s *Service) ReconcileExpansions(ctx context.Context) error {
	st, ok := s.Store.(*PgStore)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	scopes, err := st.CapacityScopes(ctx)
	if err != nil {
		return err
	}
	var failures []error
	type poolWork struct {
		key  string
		pool *Pool
	}
	var work []poolWork
	for _, scope := range scopes {
		pools, readErr := st.ListPools(ctx, scope.OrgID, scope.CloudID)
		if readErr != nil {
			failures = append(failures, readErr)
			continue
		}
		for _, pool := range pools {
			work = append(work, poolWork{key: pool.OrgID + "\x00" + pool.CloudID + "\x00" + pool.ID, pool: pool})
		}
	}
	sort.Slice(work, func(i, j int) bool { return work[i].key < work[j].key })
	s.expansionSchedule.mu.Lock()
	after := s.expansionSchedule.after
	s.expansionSchedule.mu.Unlock()
	start := sort.Search(len(work), func(i int) bool { return work[i].key > after })
	if start == len(work) {
		start = 0
	}
	for i := range work {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		job := work[(start+i)%len(work)]
		// Advance before any fallible or slow work. Cancellation and provider
		// failures cannot force the next pass back to this pool. Restart resets
		// only this scheduling position; persisted leases still fence execution.
		s.expansionSchedule.mu.Lock()
		s.expansionSchedule.after = job.key
		s.expansionSchedule.mu.Unlock()
		pool := job.pool
		policy, readErr := st.GetCapacityPolicy(ctx, pool.OrgID, pool.CloudID)
		if readErr != nil {
			failures = append(failures, readErr)
			continue
		}
		if checkErr := st.withExpansionLease(ctx, pool.OrgID, pool.CloudID, pool.ID, func(ctx context.Context, token string) error {
			return s.reconcileExpansionPool(ctx, st, policy, pool, token)
		}); checkErr != nil && !errors.Is(checkErr, ErrBusy) {
			failures = append(failures, checkErr)
		}
	}
	return errors.Join(failures...)
}
func (s *Service) reconcileExpansionPool(ctx context.Context, st *PgStore, policy *CapacityPolicy, pool *Pool, token string) error {
	expansions, err := st.poolExpansions(ctx, policy.OrgID, policy.CloudID, pool.ID)
	if err != nil {
		return err
	}
	outstanding := int64(len(pool.Members))
	for _, e := range expansions {
		if stopped, retireErr := st.retireObsoleteMinimum(ctx, e, token); retireErr != nil {
			return retireErr
		} else if stopped {
			continue
		}
		if !e.Minimum && e.Status != "ready" {
			demands, readErr := st.expansionDemands(ctx, e)
			if readErr != nil {
				return readErr
			}
			if len(demands) == 0 {
				cancelled, cancelErr := s.cancelUnacceptedExpansion(ctx, st, e, token)
				if cancelErr != nil {
					return cancelErr
				}
				if cancelled {
					continue
				}
			}
		}
		if refreshErr := s.refreshPendingExpansion(ctx, st, e, token); refreshErr != nil {
			return refreshErr
		}
		if e.Status == "ready" {
			if wakeErr := s.wakeExpansionDemands(ctx, st, e); wakeErr != nil {
				return wakeErr
			}
			continue
		}
		outstanding++
		if e.OperationID == "" {
			if !e.Minimum {
				ds, readErr := st.expansionDemands(ctx, e)
				if readErr != nil {
					return readErr
				}
				if len(ds) == 0 {
					e.Status = "cancelled"
					if saveErr := st.saveExpansion(ctx, e, token); saveErr != nil {
						return saveErr
					}
					outstanding--
					continue
				}
			}
			if e.Status == "reserved" && s.EnableAutomaticCapacity {
				if checkErr := s.acceptPolicyExpansion(ctx, st, e, nil, token); checkErr != nil {
					if blockErr := s.blockExpansion(ctx, st, e, checkErr, token); blockErr != nil {
						return blockErr
					}
				}
			}
			continue
		}
		o, readErr := st.GetOperation(ctx, e.OrgID, e.CloudID, e.OperationID)
		if readErr != nil {
			return readErr
		}
		if o.Status == "preview" {
			if e.Status == "reserved" && s.EnableAutomaticCapacity {
				if checkErr := s.acceptPolicyExpansion(ctx, st, e, nil, token); checkErr != nil {
					if blockErr := s.blockExpansion(ctx, st, e, checkErr, token); blockErr != nil {
						return blockErr
					}
				}
			}
			continue
		}
		if !o.Attempted && o.Status != "succeeded" {
			if resumeErr := s.resumeAcceptedExpansion(ctx, st, e, nil, token); resumeErr != nil {
				if blockErr := s.blockExpansion(ctx, st, e, resumeErr, token); blockErr != nil {
					return blockErr
				}
				continue
			}
		}
		if o.Status != "succeeded" {
			if o.Status == "failed" && o.Attempted {
				o.Status = "pending"
				o.Attempt++
				if saveErr := st.SaveOperation(ctx, o); saveErr != nil {
					return saveErr
				}
			}
			if checkErr := s.ReconcileInfrastructure(ctx, e.OrgID, o.ID, nil); checkErr != nil && !errors.Is(checkErr, ErrPending) {
				return checkErr
			}
			o, readErr = st.GetOperation(ctx, e.OrgID, e.CloudID, e.OperationID)
			if readErr != nil {
				return readErr
			}
		}
		switch {
		case o.Status == "succeeded":
			if enrollErr := s.enrollExpansionPool(ctx, st, e, o, token); enrollErr != nil {
				return enrollErr
			}
		case o.Status == "failed":
			e.Status = "blocked"
			reason := "review_required"
			if o.Attempted {
				reason = "provider_failure"
			}
			e.Block = &CapacityBlock{Reason: reason, Resource: "operation", CloudID: e.CloudID, PoolID: e.PoolID, PolicyRevision: e.PolicyRevision, Retryable: true}
			if saveErr := st.saveExpansion(ctx, e, token); saveErr != nil {
				return saveErr
			}
		case o.Attempted:
			e.Status = "enrolling"
			if saveErr := st.saveExpansion(ctx, e, token); saveErr != nil {
				return saveErr
			}
		}
	}
	template, found := expansionPool(policy, pool.ID)
	if !s.EnableAutomaticCapacity || !policy.Enabled || !found || outstanding >= template.MinServers {
		return nil
	}
	e, err := s.newExpansion(ctx, st, policy, template, CapacityDemand{}, true, token)
	if err != nil {
		return err
	}
	if !policy.RequireExpansionApproval {
		if checkErr := s.acceptPolicyExpansion(ctx, st, e, nil, token); checkErr != nil {
			return s.blockExpansion(ctx, st, e, checkErr, token)
		}
	}
	return nil
}
func (s *Service) enrollExpansionPool(ctx context.Context, st *PgStore, e *Expansion, o *Operation, token string) error {
	v, err := st.GetServer(ctx, e.OrgID, e.CloudID, o.ServerID)
	if err != nil {
		return err
	}
	if !o.Enrolled || v.Status != "ready" || v.Health.Architecture != e.Architecture {
		return ErrConflict
	}
	// Refresh exact receipts before adding membership, avoiding double attribution
	// of the accepted reservation and its materialized host.
	if _, refreshErr := s.refreshCapacity(ctx, e.OrgID, e.CloudID); refreshErr != nil {
		return refreshErr
	}
	pool, err := st.GetPool(ctx, e.OrgID, e.CloudID, e.PoolID)
	if err != nil {
		return err
	}
	found := false
	for _, member := range pool.Members {
		if member.ServerID == v.ID {
			found = true
		}
	}
	if !found {
		cpu := min(v.Health.EffectiveCPUMillis, e.Resources.CPUMillis) - e.Template.SystemReserve.CPUMillis
		memory := min(v.Health.EffectiveMemoryBytes, e.Resources.MemoryBytes) - e.Template.SystemReserve.MemoryBytes
		if cpu <= 0 || memory <= 0 {
			return ErrConflict
		}
		previous := pool.Revision
		pool.Revision++
		pool.Members = append(pool.Members, PoolMember{ServerID: v.ID, CPUMillis: cpu, MemoryBytes: memory})
		if saveErr := st.SavePool(ctx, pool, previous); saveErr != nil {
			return saveErr
		}
	}
	e.Status = "ready"
	e.Block = nil
	if saveErr := st.saveExpansion(ctx, e, token); saveErr != nil {
		return saveErr
	}
	return s.wakeExpansionDemands(ctx, st, e)
}

// expansionEffect permits accepted identity reconciliation after revocation.
// Unattempted effects still need rollout, current policy and current authorities.
func (s *Service) expansionEffect(ctx context.Context, o *Operation) (bool, error) {
	st, ok := s.Store.(*PgStore)
	if !ok {
		return false, nil
	}
	e, err := st.expansionForOperation(ctx, o)
	if err != nil || e == nil {
		return false, err
	}
	if o.Attempted {
		return true, nil
	}
	if !s.EnableAutomaticCapacity {
		return true, security.ErrForbidden
	}
	policy, err := st.GetCapacityPolicy(ctx, e.OrgID, e.CloudID)
	if err != nil {
		return true, err
	}
	pool, found := expansionPool(policy, e.PoolID)
	if !policy.Enabled || !found || policy.Revision != e.PolicyRevision || !sameExpansionTemplate(pool, e.Template) || policy.RequireExpansionApproval && e.ApprovedAt == nil {
		return true, ErrConflict
	}
	if demandErr := s.validateExpansionDemand(ctx, st, e); demandErr != nil {
		return true, demandErr
	}
	principal, principalErr := s.expansionExecutionAuthority(ctx, st, e, policy, nil)
	if principalErr != nil {
		return true, principalErr
	}
	if catalogErr := s.validateExpansionCatalog(ctx, e, principal); catalogErr != nil {
		return true, catalogErr
	}
	return true, st.revalidateExpansion(ctx, e, o, principal, "", false)
}

// Policy changes invalidate pending approvals. An unchanged approved workload can
// retry under current limits without rewriting its immutable demand snapshot.
func (s *Service) refreshPendingExpansion(ctx context.Context, st *PgStore, e *Expansion, token string) error {
	accepted := false
	if e.Status == "ready" || e.Status == "cancelled" {
		return nil
	}
	if e.OperationID != "" {
		o, err := st.GetOperation(ctx, e.OrgID, e.CloudID, e.OperationID)
		if err != nil {
			return err
		}
		if o.Attempted || o.Status == "cancelled" || o.Status == "succeeded" {
			return nil
		}
		accepted = o.Status != "preview"
	}
	p, err := st.GetCapacityPolicy(ctx, e.OrgID, e.CloudID)
	if err != nil {
		return err
	}
	pool, found := expansionPool(p, e.PoolID)
	if !p.Enabled || !found {
		return nil
	}
	if p.Revision == e.PolicyRevision {
		if e.Status == "blocked" && e.Block != nil && (e.Block.Reason == "stale_inventory" || e.Block.Reason == "hard_limit") && !p.RequireExpansionApproval {
			e.Status = "reserved"
			e.Block = nil
			return st.saveExpansion(ctx, e, token)
		}
		return nil
	}
	// A changed template needs a fresh proposal and never authorizes the old size.
	if pool.ConnectionID != e.ConnectionID || pool.Region != e.Region || pool.Architecture != e.Architecture || pool.Image != e.Image || !slices.Contains(pool.AllowedSizes, e.Size) || !reflect.DeepEqual(pool.SystemReserve, e.Template.SystemReserve) {
		return nil
	}
	if e.OperationID != "" && !accepted {
		if lockErr := st.WithOperationLock(ctx, e.OperationID, func() error {
			o, getErr := st.GetOperation(ctx, e.OrgID, e.CloudID, e.OperationID)
			if getErr != nil {
				return getErr
			}
			if o.Status != "preview" {
				return ErrConflict
			}
			o.Status = "cancelled"
			return st.SaveOperation(ctx, o)
		}); lockErr != nil {
			return lockErr
		}
		e.OperationID = ""
	}
	e.PolicyRevision = p.Revision
	e.Template = pool
	e.ApprovedAt = nil
	e.ApprovedBy = ""
	e.Block = nil
	e.Status = "reserved"
	if p.RequireExpansionApproval {
		e.Status = "awaiting_approval"
	}
	return st.saveExpansion(ctx, e, token)
}

func (s *Service) wakeExpansionDemands(ctx context.Context, st *PgStore, e *Expansion) error {
	if s.CapacityDemandWakeup == nil {
		return nil
	}
	demands, err := st.expansionDemands(ctx, e)
	if err != nil {
		return err
	}
	var failures []error
	for _, d := range demands {
		if checkErr := s.CapacityDemandWakeup.WakeCapacityDemand(ctx, d); checkErr != nil {
			failures = append(failures, checkErr)
		}
	}
	return errors.Join(failures...)
}

func (s *Service) cancelUnacceptedExpansion(ctx context.Context, st *PgStore, e *Expansion, token string) (bool, error) {
	if e.OperationID != "" {
		o, err := st.GetOperation(ctx, e.OrgID, e.CloudID, e.OperationID)
		if err != nil {
			return false, err
		}
		if o.Status != "preview" && o.Status != "cancelled" {
			return false, nil
		}
		if lockErr := st.WithOperationLock(ctx, o.ID, func() error {
			current, readErr := st.GetOperation(ctx, e.OrgID, e.CloudID, o.ID)
			if readErr != nil {
				return readErr
			}
			if current.Status != "preview" && current.Status != "cancelled" {
				return ErrConflict
			}
			if current.Status == "cancelled" {
				return nil
			}
			current.Status = "cancelled"
			return st.SaveOperation(ctx, current)
		}); lockErr != nil {
			return false, lockErr
		}
	}
	e.Status = "cancelled"
	e.Block = nil
	return true, st.saveExpansion(ctx, e, token)
}

func (s *Service) expansionExecutionAuthority(ctx context.Context, st *PgStore, e *Expansion, policy *CapacityPolicy, approver *security.Principal) (security.Principal, error) {
	var p security.Principal
	var err error
	if policy.RequireExpansionApproval {
		switch {
		case approver != nil:
			p = *approver
			now := s.now().UTC()
			e.ApprovedBy = p.UserID
			e.ApprovedAt = &now
		case e.ApprovedAt == nil:
			return p, &CapacityAdmissionError{Block: *capacityBlock(policy, CapacityDemand{CloudID: e.CloudID, PoolID: e.PoolID}, "expansion_approval", "policy")}
		default:
			p, err = st.expansionApprovalPrincipal(ctx, e)
		}
	} else {
		p, err = st.expansionAuthority(ctx, e.OrgID, e.CloudID, policy.Revision)
	}
	if err != nil {
		return p, err
	}
	if p.OrgID != e.OrgID || p.ReadOnly {
		return p, security.ErrForbidden
	}
	if _, err = s.authorizeCapacityPrincipal(ctx, p, e.CloudID); err != nil {
		return p, err
	}
	return p, nil
}
func (s *Service) validateExpansionCatalog(ctx context.Context, e *Expansion, p security.Principal) error {
	options, err := s.ServerOptions(ctx, p, e.CloudID, e.ConnectionID)
	if err != nil {
		return err
	}
	pinned := e.Template
	pinned.AllowedSizes = []string{e.Size}
	if _, sizeErr := SelectExpansionSize(options, pinned, CapacityDemand{}); sizeErr != nil {
		return sizeErr
	}
	for _, size := range options.Sizes {
		if size.ID == e.Size {
			resources, sizeErr := sizeResources(size)
			if sizeErr != nil {
				return sizeErr
			}
			if resources == e.Resources {
				return nil
			}
			return ErrConflict
		}
	}
	return ErrConflict
}
func (s *Service) resumeAcceptedExpansion(ctx context.Context, st *PgStore, e *Expansion, approver *security.Principal, token string) error {
	return st.WithOperationLock(ctx, e.OperationID, func() error {
		o, err := st.GetOperation(ctx, e.OrgID, e.CloudID, e.OperationID)
		if err != nil {
			return err
		}
		if o.Attempted {
			return nil
		}
		if !s.EnableAutomaticCapacity || o.Status == "cancelled" {
			return security.ErrForbidden
		}
		if demandErr := s.validateExpansionDemand(ctx, st, e); demandErr != nil {
			return demandErr
		}
		policy, err := st.GetCapacityPolicy(ctx, e.OrgID, e.CloudID)
		if err != nil {
			return err
		}
		principal, err := s.expansionExecutionAuthority(ctx, st, e, policy, approver)
		if err != nil {
			return err
		}
		if catalogErr := s.validateExpansionCatalog(ctx, e, principal); catalogErr != nil {
			return catalogErr
		}
		if _, _, err = s.validateConnection(ctx, o); err != nil {
			return err
		}
		if admissionErr := st.revalidateExpansion(ctx, e, o, principal, token, true); admissionErr != nil {
			return admissionErr
		}
		s.record(ctx, principal, "cloud.capacity_expansion.revalidate", o)
		return nil
	})
}

func (s *Service) checkpointCreateAttempt(ctx context.Context, o *Operation) error {
	st, ok := s.Store.(*PgStore)
	if !ok {
		o.Attempted = true
		return s.checkpoint(ctx, o)
	}
	e, err := st.expansionForOperation(ctx, o)
	if err != nil {
		return err
	}
	if e == nil {
		o.Attempted = true
		return s.checkpoint(ctx, o)
	}
	if !s.EnableAutomaticCapacity {
		return security.ErrForbidden
	}
	policy, err := st.GetCapacityPolicy(ctx, e.OrgID, e.CloudID)
	if err != nil {
		return err
	}
	principal, err := s.expansionExecutionAuthority(ctx, st, e, policy, nil)
	if err != nil {
		return err
	}
	if catalogErr := s.validateExpansionCatalog(ctx, e, principal); catalogErr != nil {
		return catalogErr
	}
	return st.checkpointExpansionAttempt(ctx, e, o, principal, func(ctx context.Context) error {
		if demandErr := s.validateExpansionDemand(ctx, st, e); demandErr != nil {
			return demandErr
		}
		_, authorityErr := s.authorizeCapacityPrincipal(ctx, principal, e.CloudID)
		return authorityErr
	})
}
