package access

import (
	"context"
	"slices"

	"github.com/xraph/loomplane/internal/runtimecatalog"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// DistributionStore binds signed deliveries to durable policy and catalog versions.
type DistributionStore interface {
	Input(context.Context, domain.Scope) (domain.PolicyInput, error)
	IssueSnapshot(context.Context, domain.AgentIdentity, uint64, domain.Snapshot, *SnapshotSigner) (domain.Snapshot, error)
}

// SignerSource resolves an operator-configured key for an exact tenant scope.
// Runtime peers never supply key material or select a different scope.
type SignerSource func(context.Context, domain.Scope) (*SnapshotSigner, error)

type Distributor struct {
	policies DistributionStore
	catalog  runtimecatalog.Store
	signers  SignerSource
}

func NewDistributor(policies DistributionStore, catalog runtimecatalog.Store, signers SignerSource) (*Distributor, error) {
	if policies == nil || catalog == nil || signers == nil {
		return nil, ErrSnapshot
	}
	return &Distributor{policies: policies, catalog: catalog, signers: signers}, nil
}
func (d *Distributor) Observe(ctx context.Context, agent domain.AgentIdentity, batch domain.ObservationBatch) error {
	return runtimecatalog.New(d.catalog, nil, nil).Observe(ctx, agent, batch)
}

// Watch uses the catalog's bounded, complete-page watch loop. Each watch owns
// its policy projection, so parallel agent watches cannot share authorization.
// Failed assembly or issuance ends the stream; the agent reconnects with its
// previous configuration usable only until the already-issued lease expires.
func (d *Distributor) Watch(ctx context.Context, agent domain.AgentIdentity, bus runtimecatalog.WakeSource, send func(context.Context, *domain.Snapshot) error) error {
	if stepErr := validDeliveryAgent(agent); stepErr != nil {
		return stepErr
	}
	if send == nil {
		return runtimecatalog.ErrInvalid
	}
	var input domain.PolicyInput
	var policy domain.PolicyBundle
	catalog := runtimecatalog.New(d.catalog, func(ctx context.Context, requester domain.AgentIdentity) (runtimecatalog.Visibility, error) {
		if requester != agent {
			return runtimecatalog.Visibility{}, runtimecatalog.ErrUnauthorized
		}
		var err error
		input, err = d.policies.Input(ctx, agent.Scope)
		if err != nil {
			return runtimecatalog.Visibility{}, err
		}
		if input.EvaluatedAt.IsZero() {
			return runtimecatalog.Visibility{}, ErrPolicy
		}
		policy, err = CompileForAgent(input, agent)
		if err != nil {
			return runtimecatalog.Visibility{}, err
		}
		exports := make(map[string]domain.Scope, len(policy.Exports))
		for _, export := range policy.Exports {
			exports[export.ID] = export.Scope
		}
		return runtimecatalog.Visibility{Revision: policy.Revision, ExpiresAt: policy.ExpiresAt, Exports: exports}, nil
	}, nil)
	return catalog.Watch(ctx, agent, bus, func(delivery context.Context, snapshot *domain.Snapshot) error {
		signer, err := d.signers(delivery, agent.Scope)
		if err != nil {
			return err
		}
		if signer == nil {
			return ErrSnapshot
		}
		cursor := snapshot.Sequence
		snapshot.Sequence = 0
		snapshot.Revision = ""
		snapshot.IssuedAt = input.EvaluatedAt
		snapshot.Policy = policy
		if deadline := input.EvaluatedAt.Add(runtimecatalog.EndpointLease); deadline.Before(snapshot.ExpiresAt) {
			snapshot.ExpiresAt = deadline
		}
		if policy.ExpiresAt.Before(snapshot.ExpiresAt) {
			snapshot.ExpiresAt = policy.ExpiresAt
		}
		if signer.Key().NotAfter.Before(snapshot.ExpiresAt) {
			snapshot.ExpiresAt = signer.Key().NotAfter
		}
		filterDeliveryEndpoints(snapshot)
		signed, err := d.policies.IssueSnapshot(delivery, agent, cursor, *snapshot, signer)
		if err != nil {
			return err
		}
		bounded, cancel := context.WithDeadline(delivery, signed.ExpiresAt.Add(-domain.MaxClockSkew))
		defer cancel()
		if stepErr := bounded.Err(); stepErr != nil {
			return stepErr
		}
		return send(bounded, &signed)
	})
}

func filterDeliveryEndpoints(snapshot *domain.Snapshot) {
	exports := make(map[string]domain.ServiceExport, len(snapshot.Policy.Exports))
	for _, export := range snapshot.Policy.Exports {
		exports[export.ID] = export
	}
	snapshot.Endpoints = slices.DeleteFunc(snapshot.Endpoints, func(endpoint domain.Endpoint) bool {
		export, ok := exports[endpoint.ExportID]
		return !ok || export.Scope != endpoint.Scope || export.Port != endpoint.Port || !slices.Contains(export.DestinationIDs, endpoint.DestinationID)
	})
	snapshot.Tombstones = slices.DeleteFunc(snapshot.Tombstones, func(tombstone domain.EndpointTombstone) bool {
		export, ok := exports[tombstone.ExportID]
		return !ok || export.Scope != tombstone.Scope || !slices.Contains(export.DestinationIDs, tombstone.DestinationID)
	})
}
