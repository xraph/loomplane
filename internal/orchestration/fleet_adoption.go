package orchestration

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

// ErrFleetAdoptionTransfer requires a fresh, qualified ownership transfer review.
var ErrFleetAdoptionTransfer = errors.New("ownership transfer needs a current lease, verified writer fence and no-change plan")

// FleetAdoptionTransfer records evidence supplied by trusted inventory, fencing
// and planning adapters. It is not request input or permission to perform effects.
// The durable runner must reload receipts and current authority at each boundary.
type FleetAdoptionTransfer struct {
	OrgID, FleetID, ReviewID                             string
	ReviewRevision                                       int64
	InventoryObservedAt                                  time.Time
	InventoryDigest, NamespaceUID                        string
	LeaseID                                              string
	LeaseExpiresAt                                       time.Time
	PriorEpoch, Epoch                                    int64
	OldControllerID, OldAuthorityID, NewAuthorityID      string
	FenceReceiptID, FencedAuthorityID, FenceNamespaceUID string
	FenceEpoch                                           int64
	FenceVerifiedAt                                      time.Time
	OldWriterRevoked, NewWriterRevoked                   bool
	ActiveWriter                                         string
	NoChangePlanID, PlanInventoryDigest                  string
	PlanVerifiedAt                                       time.Time
	PlanComplete                                         bool
	PlanChanges                                          int
}

// ValidateFleetAdoptionTransfer checks forward and reversed transfer evidence.
// Reversing ownership needs a fence for the new writer before the old one resumes.
// This check neither grants write access nor changes ownership or resource retention.
func ValidateFleetAdoptionTransfer(e FleetAdoptionTransfer, now time.Time) error {
	if now.IsZero() || e.OrgID == "" || e.FleetID == "" || e.ReviewID == "" || e.ReviewRevision < 1 || e.InventoryDigest == "" || e.NamespaceUID == "" || e.LeaseID == "" || !e.LeaseExpiresAt.After(now) || e.LeaseExpiresAt.After(now.Add(5*time.Minute)) || e.PriorEpoch < 1 || e.Epoch <= e.PriorEpoch || e.Epoch-e.PriorEpoch != 1 {
		return ErrFleetAdoptionTransfer
	}
	if e.OldControllerID == "" || e.OldAuthorityID == "" || e.NewAuthorityID == "" || e.NewAuthorityID == e.OldAuthorityID || e.FenceReceiptID == "" || e.FenceEpoch != e.Epoch || e.FenceNamespaceUID != e.NamespaceUID || !freshAdoptionEvidence(e.FenceVerifiedAt, now) {
		return ErrFleetAdoptionTransfer
	}
	if !freshAdoptionEvidence(e.InventoryObservedAt, now) || e.InventoryObservedAt.Before(e.FenceVerifiedAt) || e.PlanVerifiedAt.Before(e.InventoryObservedAt) || e.NoChangePlanID == "" || !e.PlanComplete || e.PlanChanges != 0 || e.PlanInventoryDigest != e.InventoryDigest || !freshAdoptionEvidence(e.PlanVerifiedAt, now) {
		return ErrFleetAdoptionTransfer
	}
	switch e.ActiveWriter {
	case "new":
		if !e.OldWriterRevoked || e.NewWriterRevoked || e.FencedAuthorityID != e.OldAuthorityID {
			return ErrFleetAdoptionTransfer
		}
	case "old":
		if e.OldWriterRevoked || !e.NewWriterRevoked || e.FencedAuthorityID != e.NewAuthorityID {
			return ErrFleetAdoptionTransfer
		}
	default:
		return ErrFleetAdoptionTransfer
	}
	return nil
}

func freshAdoptionEvidence(observed, now time.Time) bool {
	return !observed.IsZero() && !observed.After(now) && now.Sub(observed) <= 5*time.Minute
}

// FleetAdoptionResourceReader exposes only the existing provider inventory read.
type FleetAdoptionResourceReader interface {
	Get(context.Context, security.Principal, string) (*providerresources.ProviderResource, error)
}

// NewFleetAdoptionInventory reuses inventory without refreshing it or reading secrets.
// A missing namespace reader leaves namespace identity explicitly unverified.
func NewFleetAdoptionInventory(resources FleetAdoptionResourceReader, namespace fleets.AdoptionNamespaceReader) fleets.AdoptionInventory {
	return &fleetAdoptionInventory{resources: resources, namespace: namespace}
}

type fleetAdoptionInventory struct {
	resources FleetAdoptionResourceReader
	namespace fleets.AdoptionNamespaceReader
}

func (r *fleetAdoptionInventory) InspectAdoption(ctx context.Context, p security.Principal, scope fleets.FleetScope, in fleet.AdoptionInput) (fleets.AdoptionInventorySnapshot, error) {
	out := fleets.AdoptionInventorySnapshot{Resources: make([]fleets.AdoptionResourceObservation, 0, len(in.Resources))}
	if p.OrgID == "" || p.OrgID != scope.OrgID || p.IsServiceAccount() {
		return out, security.ErrForbidden
	}
	if len(in.Resources) > 64 {
		return out, fleets.ErrInvalid
	}
	if r.namespace != nil {
		namespace, err := r.namespace(ctx, p, in)
		if err != nil {
			return out, err
		}
		out.Namespace = namespace
	}
	if len(in.Resources) > 0 && r.resources == nil {
		return out, fleets.ErrAdoptionInventory
	}
	for _, selected := range in.Resources {
		resource, err := r.resources.Get(ctx, p, selected.InventoryID)
		if err != nil {
			if errors.Is(err, providerresources.ErrNotFound) {
				return out, fleets.ErrNotFound
			}
			return out, err
		}
		if resource == nil || resource.OrgID != scope.OrgID || resource.ID != selected.InventoryID {
			return out, fleets.ErrNotFound
		}
		out.Resources = append(out.Resources, fleets.AdoptionResourceObservation{
			ID: resource.ID, OrgID: resource.OrgID, Provider: resource.Provider, ConnectionID: resource.ConnectionID,
			AccountIdentity: resource.AccountIdentity, Scope: resource.Scope, ExternalID: resource.ExternalID,
			Incarnation: resource.Incarnation, ObservationState: resource.ObservationState, Status: resource.Status,
			ConnectionGeneration: resource.ConnectionGeneration, Revision: resource.Revision, ObservedAt: resource.ObservedAt,
			Ownership: resource.Ownership, ConsumerCount: len(resource.Consumers),
		})
	}
	return out, nil
}
