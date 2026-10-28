package cloudinfra

import (
	"context"
	"slices"
	"strings"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/security"
)

// ScalingPoolInput creates a pool with no servers together with the capacity
// policy that grows it. Policy is the whole policy to save over
// ExpectedRevision, without the new pool. Template is how the new pool grows:
// its PoolID is ignored and its AcceleratorKind must equal the pool's.
type ScalingPoolInput struct {
	Name             string              `json:"name"`
	AcceleratorKind  string              `json:"accelerator_kind,omitempty"`
	ExpectedRevision int64               `json:"expected_revision"`
	Policy           CapacityPolicy      `json:"policy"`
	Template         PoolExpansionPolicy `json:"template"`
}

// ScalingPool is the created pool and the policy saved with it.
type ScalingPool struct {
	Pool   *Pool           `json:"pool"`
	Policy *CapacityPolicy `json:"policy"`
}

// ScalingPoolStore writes a new pool and its policy in one transaction, so a
// failure leaves neither behind.
type ScalingPoolStore interface {
	CreateScalingPool(ctx context.Context, pool *Pool, policy *CapacityPolicy, previous int64) error
}

var _ ScalingPoolStore = (*PgStore)(nil)

// CreateScalingPool creates a pool that starts with no servers and grows from
// zero under an enabled policy. SavePool refuses an empty new pool because no
// policy can name a pool before it exists; this saves both at once.
func (s *Service) CreateScalingPool(ctx context.Context, p security.Principal, cloudID string, in ScalingPoolInput) (*ScalingPool, error) {
	if _, err := s.authorize(ctx, p, cloudID); err != nil {
		return nil, err
	}
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	st, ok := s.Store.(ScalingPoolStore)
	if !ok {
		return nil, ErrInvalid
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(in.Name) > 64 {
		return nil, ErrInvalid
	}
	if err := validatePoolGPUs(PoolInput{AcceleratorKind: in.AcceleratorKind}); err != nil {
		return nil, err
	}
	if !in.Policy.Enabled {
		return nil, invalidSentence("A pool with no servers grows only under an enabled capacity policy.")
	}
	v, err := typeid.Generate("pool")
	if err != nil {
		return nil, err
	}
	id := v.String()
	// Expansions add servers of the template's kind, so the pool holds the same kind.
	if checkErr := poolKindMatchesTemplate(id, in.AcceleratorKind, in.Template.AcceleratorKind); checkErr != nil {
		return nil, checkErr
	}
	template := in.Template
	template.PoolID = id
	policyIn := CapacityPolicyInput{ExpectedRevision: in.ExpectedRevision, Policy: in.Policy}
	policyIn.Policy.Pools = append(slices.Clone(in.Policy.Pools), template)
	// Provider options are read here, before the store opens its transaction.
	policy, err := s.prepareCapacityPolicy(ctx, p, cloudID, policyIn)
	if err != nil {
		return nil, err
	}
	pool := &Pool{ID: id, OrgID: p.OrgID, CloudID: cloudID, Name: name, Revision: 1, Members: []PoolMember{}, AcceleratorKind: in.AcceleratorKind}
	if checkErr := st.CreateScalingPool(ctx, pool, policy, in.ExpectedRevision); checkErr != nil {
		return nil, checkErr
	}
	s.capacityPolicySaved(ctx, p, policy)
	return &ScalingPool{Pool: pool, Policy: withPolicyLists(policy)}, nil
}

// CreateScalingPool takes the Cloud's capacity lock, checks the policy
// revision, inserts the empty pool, then saves the policy that names it.
func (s *PgStore) CreateScalingPool(ctx context.Context, pool *Pool, policy *CapacityPolicy, previous int64) error {
	if previous < 0 || policy.Revision != previous+1 || pool.Revision != 1 || len(pool.Members) != 0 {
		return ErrInvalid
	}
	if err := validateCapacityPolicyShape(*policy); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, pool.OrgID, pool.CloudID); checkErr != nil {
		return checkErr
	}
	if checkErr := capacityPolicyRevisionTx(ctx, tx, pool.OrgID, pool.CloudID, previous); checkErr != nil {
		return checkErr
	}
	if checkErr := savePoolTx(ctx, tx, pool, 0); checkErr != nil {
		return checkErr
	}
	if checkErr := s.saveCapacityPolicyTx(ctx, tx, policy, previous); checkErr != nil {
		return checkErr
	}
	return tx.Commit()
}
