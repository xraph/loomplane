package access

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// DeliveryState separates issuance from installation. LeaseUntil bounds every
// snapshot issued to this enrollment, including deliveries never acknowledged.
// AppliedAt is an agent claim; ReceivedAt is the controller's observation time.
type DeliveryState struct {
	Agent                        domain.AgentIdentity `json:"agent"`
	Sequence                     uint64               `json:"sequence"`
	Revision                     string               `json:"revision"`
	PolicyGeneration             uint64               `json:"policy_generation"`
	IssuedAt                     time.Time            `json:"issued_at"`
	ExpiresAt                    time.Time            `json:"expires_at"`
	LeaseUntil                   time.Time            `json:"lease_until"`
	AcknowledgedSequence         uint64               `json:"acknowledged_sequence"`
	AcknowledgedPolicyGeneration uint64               `json:"acknowledged_policy_generation"`
	AppliedAt                    time.Time            `json:"applied_at,omitempty"`
	ReceivedAt                   time.Time            `json:"received_at,omitempty"`
}

// IssueSnapshot is internal to the trusted policy/catalog coordinator. The
// caller supplies scoped content and the catalog revision it read. Durable
// approval records are recompiled for this agent before the signature is issued.
// Signing occurs under policy and catalog locks; no signature escapes a failed
// commit. This method is not a management or runtime request-body interface.
func (s *PolicyStore) IssueSnapshot(ctx context.Context, agent domain.AgentIdentity, catalogSequence uint64, prepared domain.Snapshot, signer *SnapshotSigner) (signed domain.Snapshot, err error) {
	if checkErr := validDeliveryAgent(agent); checkErr != nil {
		return signed, checkErr
	}
	if signer == nil || prepared.Scope != agent.Scope || prepared.DestinationID != agent.DestinationID || prepared.Epoch != agent.Epoch || prepared.Sequence != 0 || prepared.Revision != "" || prepared.KeyID != "" || len(prepared.Signature) != 0 || catalogSequence == 0 || catalogSequence > math.MaxInt64 {
		return signed, ErrSnapshot
	}
	if checkErr := validateCompiledPolicy(prepared); checkErr != nil {
		return signed, checkErr
	}
	deadline := prepared.ExpiresAt
	if prepared.Policy.ExpiresAt.Before(deadline) {
		deadline = prepared.Policy.ExpiresAt
	}
	if signer.Key().NotAfter.Before(deadline) {
		deadline = signer.Key().NotAfter
	}
	ctx, cancel := context.WithDeadline(ctx, deadline.Add(-domain.MaxClockSkew))
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return signed, err
	}
	defer func() {
		err = finishPolicyTx(tx, err)
		if err != nil {
			signed = domain.Snapshot{}
		}
	}()
	if stepErr := lockPolicyOrganization(ctx, tx, agent.OrgID); stepErr != nil {
		return signed, stepErr
	}
	if stepErr := runtimecatalog.CheckAgentTx(ctx, tx, agent); stepErr != nil {
		return signed, stepErr
	}
	now := time.Now().UTC()
	if prepared.IssuedAt.After(now) || !prepared.ExpiresAt.Add(-domain.MaxClockSkew).After(now) {
		return signed, ErrSnapshot
	}
	if stepErr := expirePolicyGrants(ctx, tx, agent.OrgID, now); stepErr != nil {
		return signed, stepErr
	}
	var generation, cursor uint64
	if stepErr := tx.QueryRow(ctx, `SELECT generation FROM loomplane_federation_policy_versions WHERE org_id=$1`, agent.OrgID).Scan(&generation); stepErr != nil {
		return signed, stepErr
	}
	if generation != prepared.Policy.Generation {
		return signed, ErrPolicyConflict
	}
	// Observation and enrollment changes advance this cursor in their transaction.
	// Holding it prevents a new catalog revision from committing before issuance.
	if stepErr := tx.QueryRow(ctx, `SELECT next_seq FROM loomplane_event_cursors WHERE org_id=$1 FOR SHARE`, agent.OrgID).Scan(&cursor); stepErr != nil {
		return signed, stepErr
	}
	if cursor != catalogSequence {
		return signed, ErrPolicyConflict
	}
	input, inputErr := policyInputTx(ctx, tx, agent.Scope, prepared.IssuedAt)
	if inputErr != nil {
		return signed, inputErr
	}
	current, compileErr := CompileForAgent(input, agent)
	if compileErr != nil {
		return signed, compileErr
	}
	if prepared.Policy.ExpiresAt.After(current.ExpiresAt) {
		return signed, ErrPolicyConflict
	}
	current.ExpiresAt = prepared.Policy.ExpiresAt
	expected, encodeErr := json.Marshal(current)
	if encodeErr != nil {
		return signed, encodeErr
	}
	actual, encodeErr := json.Marshal(prepared.Policy)
	if encodeErr != nil || !bytes.Equal(actual, expected) {
		return signed, ErrPolicyConflict
	}
	state, err := loadDelivery(ctx, tx, agent)
	if errors.Is(err, ErrPolicyNotFound) {
		state = &DeliveryState{Agent: agent}
	} else if err != nil {
		return signed, err
	}
	if state.Sequence >= math.MaxInt64 {
		return signed, ErrPolicyConflict
	}
	prepared.Sequence = state.Sequence + 1
	prepared.Revision, err = policyDigest("delivery", prepared)
	if err != nil {
		return signed, err
	}
	signed, err = signer.Sign(prepared, time.Now().UTC())
	if err != nil {
		return domain.Snapshot{}, err
	}
	state.Sequence = signed.Sequence
	state.Revision = signed.Revision
	state.PolicyGeneration = signed.Policy.Generation
	state.IssuedAt = signed.IssuedAt
	state.ExpiresAt = signed.ExpiresAt
	if signed.ExpiresAt.After(state.LeaseUntil) {
		state.LeaseUntil = signed.ExpiresAt
	}
	if stepErr := saveDelivery(ctx, tx, state); stepErr != nil {
		return domain.Snapshot{}, stepErr
	}
	if stepErr := ctx.Err(); stepErr != nil {
		return domain.Snapshot{}, stepErr
	}
	return signed, tx.Commit()
}

func validDeliveryAgent(agent domain.AgentIdentity) error {
	parsed, err := workloadidentity.ParseAgentURI(agent.URI)
	if err != nil || parsed != agent {
		return runtimecatalog.ErrUnauthorized
	}
	return nil
}
func loadDelivery(ctx context.Context, tx policyReader, agent domain.AgentIdentity) (*DeliveryState, error) {
	state, err := readPolicyRecord[DeliveryState](tx.QueryRow(ctx, `SELECT state FROM loomplane_federation_deliveries WHERE org_id=$1 AND agent_id=$2`, agent.OrgID, agent.AgentID))
	if err != nil {
		return nil, err
	}
	if state.Agent != agent {
		return nil, runtimecatalog.ErrUnauthorized
	}
	return state, nil
}
func saveDelivery(ctx context.Context, tx driver.Tx, state *DeliveryState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_federation_deliveries(org_id,agent_id,state) VALUES($1,$2,$3) ON CONFLICT(org_id,agent_id) DO UPDATE SET state=EXCLUDED.state`, state.Agent.OrgID, state.Agent.AgentID, raw)
	return err
}

// AcknowledgeSnapshot accepts only the latest exact issued revision. A delayed
// acknowledgment cannot mark a newer revocation as installed. Repeated receipt
// of the same acknowledgment is idempotent while that delivery is current.
func (s *PolicyStore) AcknowledgeSnapshot(ctx context.Context, agent domain.AgentIdentity, ack domain.Acknowledgment) (err error) {
	if stepErr := validDeliveryAgent(agent); stepErr != nil {
		return stepErr
	}
	if ack.Agent != agent {
		return runtimecatalog.ErrUnauthorized
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = finishPolicyTx(tx, err) }()
	if stepErr := runtimecatalog.CheckAgentTx(ctx, tx, agent); stepErr != nil {
		return stepErr
	}
	state, err := loadDelivery(ctx, tx, agent)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if ack.Sequence != state.Sequence || ack.Revision != state.Revision || ack.AppliedAt.IsZero() || ack.AppliedAt.Before(state.IssuedAt.Add(-domain.MaxClockSkew)) || ack.AppliedAt.After(now.Add(domain.MaxClockSkew)) || !state.ExpiresAt.After(now) {
		return ErrPolicyConflict
	}
	if state.AcknowledgedSequence == ack.Sequence {
		return tx.Commit()
	}
	state.AcknowledgedSequence = ack.Sequence
	state.AcknowledgedPolicyGeneration = state.PolicyGeneration
	state.AppliedAt = ack.AppliedAt
	state.ReceivedAt = now
	if stepErr := saveDelivery(ctx, tx, state); stepErr != nil {
		return stepErr
	}
	return tx.Commit()
}

// Delivery reads controller receipt state only for the current enrollment.
func (s *PolicyStore) Delivery(ctx context.Context, agent domain.AgentIdentity) (state *DeliveryState, err error) {
	if stepErr := validDeliveryAgent(agent); stepErr != nil {
		return nil, stepErr
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = finishPolicyTx(tx, err) }()
	if stepErr := runtimecatalog.CheckAgentTx(ctx, tx, agent); stepErr != nil {
		return nil, stepErr
	}
	state, err = loadDelivery(ctx, tx, agent)
	if err != nil {
		return nil, err
	}
	return state, tx.Commit()
}

// ReadDeliveryTx reads a receipt for a currently active agent within the caller's
// snapshot. It neither updates lease retention nor locks lifecycle resources.
func ReadDeliveryTx(ctx context.Context, tx driver.Tx, agent domain.AgentIdentity) (*DeliveryState, error) {
	if err := validDeliveryAgent(agent); err != nil {
		return nil, err
	}
	if err := runtimecatalog.CheckAgentReadTx(ctx, tx, agent); err != nil {
		return nil, err
	}
	return loadDelivery(ctx, tx, agent)
}
