package fleets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

// AdoptionInventory reads observations under the caller's current permissions.
// Implementations must not sync, attach, relabel, mint credentials or grant access.
type AdoptionInventory interface {
	InspectAdoption(context.Context, security.Principal, FleetScope, fleet.AdoptionInput) (AdoptionInventorySnapshot, error)
}

// AdoptionNamespaceObservation carries independently observed immutable pins.
// Leave unknown values empty. A caller-supplied UID is not an observation.
type AdoptionNamespaceObservation struct {
	CloudID, ClusterID, AttachmentID string
	AttachmentRevision               int64
	Namespace, NamespaceUID          string
	ObservedAt                       time.Time
}

// AdoptionResourceObservation is a secret-free inventory projection.
type AdoptionResourceObservation struct {
	ID, OrgID, Provider, ConnectionID, AccountIdentity, Scope    string
	ExternalID, Incarnation, ObservationState, Status, Ownership string
	ConnectionGeneration, Revision                               int64
	ObservedAt                                                   time.Time
	ConsumerCount                                                int
}

// AdoptionInventorySnapshot contains server-side observations, never request evidence.
type AdoptionInventorySnapshot struct {
	Namespace AdoptionNamespaceObservation
	Resources []AdoptionResourceObservation
}

// AdoptionNamespaceReader inspects namespace identity without changing its owner.
type AdoptionNamespaceReader func(context.Context, security.Principal, fleet.AdoptionInput) (AdoptionNamespaceObservation, error)

// ErrAdoptionBlocked is returned before effects while transfer adapters are unavailable.
var ErrAdoptionBlocked = errors.New("adoption requires qualified old-writer fencing, a transfer lease and a verified no-change plan; recreate and cut over instead")

// ErrAdoptionInventory hides provider error details while allowing a read retry.
var ErrAdoptionInventory = errors.New("adoption inventory could not be read; check access and retry")

// PreviewAdoption reads current inventory and reports unsupported transfer capabilities.
// Human inventory permissions are required until a scoped adoption grant is available.
func (s *Service) PreviewAdoption(ctx context.Context, p security.Principal, fleetID string, in fleet.AdoptionInput) (*fleet.AdoptionReview, error) {
	if p.OrgID == "" {
		return nil, security.ErrNoOrganization
	}
	if p.UserID == "" || p.IsServiceAccount() {
		return nil, security.ErrForbidden
	}
	f, err := s.store.GetFleet(ctx, p.OrgID, fleetID)
	if err != nil {
		return nil, err
	}
	if s.authz == nil {
		return nil, security.ErrForbidden
	}
	if authErr := s.authz.Require(ctx, p, security.ProjectRead, f.ProjectID); authErr != nil {
		return nil, authErr
	}
	if validationErr := validateAdoptionInput(in); validationErr != nil {
		return nil, validationErr
	}
	if f.Status != "active" {
		return nil, ErrConflict
	}
	review := &fleet.AdoptionReview{FleetID: f.ID, Revision: f.Revision, InventoryState: "unavailable", RequiresRecreateCutover: true, Resources: []fleet.AdoptionResourceReview{}, Blockers: []fleet.AdoptionBlocker{}}
	block := func(code, id, message string) {
		review.Blockers = append(review.Blockers, fleet.AdoptionBlocker{Code: code, ResourceID: id, Message: message})
	}
	var snapshot AdoptionInventorySnapshot
	if s.adoptionInventory == nil {
		block("inventory_unavailable", "", "Inventory is unavailable. Configure read access and retry.")
	} else {
		snapshot, err = s.adoptionInventory.InspectAdoption(ctx, p, FleetScope{OrgID: f.OrgID, ProjectID: f.ProjectID, FleetID: f.ID}, in)
		if err != nil {
			return nil, adoptionReadError(err)
		}
		review.InventoryState = "observed"
	}
	// Recheck authorization and fleet identity after inventory calls may have waited.
	if authErr := s.authz.Require(ctx, p, security.ProjectRead, f.ProjectID); authErr != nil {
		return nil, authErr
	}
	current, err := s.store.GetFleet(ctx, p.OrgID, fleetID)
	if err != nil {
		return nil, err
	}
	if current.OrgID != f.OrgID || current.ProjectID != f.ProjectID || current.Revision != f.Revision || current.Status != "active" {
		return nil, ErrConflict
	}
	now := time.Now().UTC()
	review.ReviewedAt = now
	if review.InventoryState == "observed" {
		reviewAdoptionResources(review, in, snapshot, f.OrgID, now)
	}
	ns := snapshot.Namespace
	if ns.CloudID != in.CloudID || ns.ClusterID != in.ClusterID || ns.AttachmentID != in.AttachmentID || ns.AttachmentRevision != in.AttachmentRevision || ns.Namespace != in.Namespace || ns.NamespaceUID == "" || ns.NamespaceUID != in.NamespaceUID || !freshAdoptionObservation(ns.ObservedAt, now) {
		block("namespace_identity_unverified", "", "Verify the current namespace UID and attachment revision. A namespace name does not establish identity.")
	}
	block("workload_inventory_unavailable", "", "Images, active controllers, routes, secret grants and backup state need a qualified workload inventory reader.")
	block("old_writer_fencing_unavailable", "", "The old controller and scheduled reconcilers cannot be fenced by a qualified adapter. Recreate and cut over instead.")
	block("no_change_plan_unavailable", "", "Import observed state and verify a no-change plan before transferring ownership.")
	block("adoption_authority_unavailable", "", "Current provisioning grants do not authorize ownership transfer.")
	raw, err := json.Marshal(struct {
		Scope    FleetScope
		Revision int64
		Input    fleet.AdoptionInput
		Snapshot AdoptionInventorySnapshot
	}{FleetScope{OrgID: f.OrgID, ProjectID: f.ProjectID, FleetID: f.ID}, f.Revision, in, snapshot})
	if err != nil {
		return nil, ErrAdoptionInventory
	}
	digest := sha256.Sum256(raw)
	review.InventoryDigest = hex.EncodeToString(digest[:])
	review.ID = "adoption-" + review.InventoryDigest
	return review, nil
}

func validateAdoptionInput(in fleet.AdoptionInput) error {
	for _, v := range []string{in.CloudID, in.ClusterID, in.AttachmentID, in.Namespace, in.OldControllerID, in.OldAuthorityID} {
		if strings.TrimSpace(v) == "" || len(v) > 512 {
			return ErrInvalid
		}
	}
	if len(in.NamespaceUID) > 512 || in.AttachmentRevision < 1 || in.OwnershipEpoch < 1 || len(in.Resources) > 64 {
		return ErrInvalid
	}
	seen := make(map[string]bool, len(in.Resources))
	for _, r := range in.Resources {
		if r.InventoryID == "" || len(r.InventoryID) > 512 || r.ExternalID == "" || len(r.ExternalID) > 2048 || len(r.Incarnation) > 512 || r.Revision < 1 || seen[r.InventoryID] || (r.Ownership != "owned" && r.Ownership != "attached" && r.Ownership != "shared") {
			return ErrInvalid
		}
		seen[r.InventoryID] = true
	}
	return nil
}

func reviewAdoptionResources(review *fleet.AdoptionReview, in fleet.AdoptionInput, snapshot AdoptionInventorySnapshot, org string, now time.Time) {
	observed := make(map[string]AdoptionResourceObservation, len(snapshot.Resources))
	duplicate := map[string]bool{}
	physical := make(map[[4]string]int, len(snapshot.Resources))
	for _, r := range snapshot.Resources {
		if _, exists := observed[r.ID]; exists {
			duplicate[r.ID] = true
		}
		observed[r.ID] = r
		physical[[4]string{r.Provider, r.AccountIdentity, r.ExternalID, r.Incarnation}]++
	}
	for _, selected := range in.Resources {
		r, exists := observed[selected.InventoryID]
		item := fleet.AdoptionResourceReview{InventoryID: selected.InventoryID, Ownership: selected.Ownership, ObservationState: "unknown", PreserveOnFailure: true}
		valid := exists && selected.Incarnation != "" && !duplicate[selected.InventoryID] && physical[[4]string{r.Provider, r.AccountIdentity, r.ExternalID, r.Incarnation}] == 1 && r.OrgID == org && r.ExternalID == selected.ExternalID && r.Incarnation == selected.Incarnation && r.Revision == selected.Revision && r.Provider != "" && r.ConnectionID != "" && r.ConnectionGeneration > 0 && r.AccountIdentity != "" && r.Scope != "" && r.ObservationState == "observed" && r.Status != "missing" && freshAdoptionObservation(r.ObservedAt, now) && r.ConsumerCount == 0
		if exists && r.OrgID == org {
			item.ExternalID = r.ExternalID
			item.Incarnation = r.Incarnation
			item.Revision = r.Revision
			item.ObservationState = r.ObservationState
			item.ObservedAt = r.ObservedAt
		}
		review.Resources = append(review.Resources, item)
		if !valid {
			review.Blockers = append(review.Blockers, fleet.AdoptionBlocker{Code: "resource_identity_or_use_conflict", ResourceID: selected.InventoryID, Message: "The resource identity, observation or existing consumers changed. Refresh inventory and review its use."})
		}
		if selected.Ownership == "owned" {
			review.Blockers = append(review.Blockers, fleet.AdoptionBlocker{Code: "physical_ownership_transfer_unverified", ResourceID: selected.InventoryID, Message: "Physical ownership is not proven. Preserve this resource until a separate transfer is verified."})
		}
	}
	if len(snapshot.Resources) != len(in.Resources) {
		review.Blockers = append(review.Blockers, fleet.AdoptionBlocker{Code: "inventory_incomplete", Message: "Inventory does not match the selected resources. Retry the review."})
	}
}

func freshAdoptionObservation(observed, now time.Time) bool {
	return !observed.IsZero() && !observed.After(now) && now.Sub(observed) <= 5*time.Minute
}

func adoptionReadError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, security.ErrForbidden):
		return security.ErrForbidden
	case errors.Is(err, security.ErrUnauthenticated):
		return security.ErrUnauthenticated
	case errors.Is(err, security.ErrNoOrganization):
		return security.ErrNoOrganization
	case errors.Is(err, ErrNotFound):
		return ErrNotFound
	default:
		return ErrAdoptionInventory
	}
}

// ApplyAdoption refuses all effects until reviewed transfer authority, durable
// leases, verified fencing and a no-change importer are installed together.
func (s *Service) ApplyAdoption(ctx context.Context, p security.Principal, reviewID string, expectedRevision int64) (*fleet.Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.OrgID == "" {
		return nil, security.ErrNoOrganization
	}
	if p.UserID == "" || p.IsServiceAccount() || p.ReadOnly {
		return nil, security.ErrForbidden
	}
	if reviewID == "" || len(reviewID) > 512 || expectedRevision < 1 {
		return nil, ErrInvalid
	}
	return nil, ErrAdoptionBlocked
}
