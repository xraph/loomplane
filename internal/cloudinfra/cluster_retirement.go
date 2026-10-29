package cloudinfra

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type ClusterRetirementInput struct {
	ResourceFingerprint string                               `json:"resource_fingerprint,omitempty"`
	ReviewDigest        string                               `json:"review_digest"`
	Inventory           provider.AssociatedResourceInventory `json:"inventory"`
	Attachment          *RuntimeAttachment                   `json:"attachment,omitempty"`
}

type ClusterRetirementReview struct {
	ResourceFingerprint              string                               `json:"resource_fingerprint"`
	ResourceID                       string                               `json:"resource_id"`
	Blockers                         []string                             `json:"blockers"`
	DeletionCandidates               []provider.AssociatedResource        `json:"deletion_candidates"`
	RetainedResources                []provider.AssociatedResource        `json:"retained_resources"`
	EstimatedRemainingHourlyMicroUSD int64                                `json:"estimated_remaining_hourly_micro_usd"`
	UnknownCosts                     []string                             `json:"unknown_costs"`
	Inventory                        provider.AssociatedResourceInventory `json:"inventory"`
	Digest                           string                               `json:"digest"`
}

type ClusterRetirementStore interface {
	ClusterRetirementDependencies(context.Context, string, string, string, string) ([]string, error)
	ValidateClusterRetirementEffect(context.Context, *ClusterOperation) error
	DisableClusterAttachment(context.Context, *ClusterOperation) error
}

func retirementKind(kind string) bool { return kind == "retire-cluster" || kind == "detach-runtime" }

func retirementReview(r *ManagedResource, inventory provider.AssociatedResourceInventory, dependencies []string, now time.Time) *ClusterRetirementReview {
	inventory.Resources = append([]provider.AssociatedResource(nil), inventory.Resources...)
	sort.Slice(inventory.Resources, func(i, j int) bool {
		return inventory.Resources[i].Kind+inventory.Resources[i].ID < inventory.Resources[j].Kind+inventory.Resources[j].ID
	})
	out := &ClusterRetirementReview{ResourceFingerprint: retirementResourceFingerprint(r), ResourceID: r.ID, Blockers: append([]string{}, dependencies...), Inventory: inventory, DeletionCandidates: []provider.AssociatedResource{}, RetainedResources: []provider.AssociatedResource{}, UnknownCosts: []string{}}
	if !inventory.Complete || inventory.ObservedAt.IsZero() || inventory.ObservedAt.After(now) || now.Sub(inventory.ObservedAt) >= 2*time.Minute {
		out.Blockers = append(out.Blockers, "inventory_incomplete")
	}
	if !inventory.ChildrenQualified {
		out.Blockers = append(out.Blockers, "child_ownership_unproven")
	}
	if r.Observation == nil || inventory.Ref != r.Observation.Ref {
		out.Blockers = append(out.Blockers, "identity_changed")
	}
	seen := map[string]bool{}
	for _, item := range inventory.Resources {
		key := item.Kind + ":" + item.ID
		if item.ID == "" || item.Kind == "" || seen[key] {
			out.Blockers = append(out.Blockers, "inventory_identity_incomplete")
		}
		seen[key] = true
		if !item.SurvivesClusterDeletion && item.Ownership != "managed" {
			out.Blockers = append(out.Blockers, "unowned_child:"+key)
		}
		if item.Ownership == "managed" {
			out.DeletionCandidates = append(out.DeletionCandidates, item)
		}
		if item.SurvivesClusterDeletion {
			out.RetainedResources = append(out.RetainedResources, item)
			if item.EstimatedHourlyMicroUSD == nil || *item.EstimatedHourlyMicroUSD < 0 {
				out.UnknownCosts = append(out.UnknownCosts, key)
			} else {
				if *item.EstimatedHourlyMicroUSD > math.MaxInt64-out.EstimatedRemainingHourlyMicroUSD {
					out.Blockers = append(out.Blockers, "cost_overflow")
				} else {
					out.EstimatedRemainingHourlyMicroUSD += *item.EstimatedHourlyMicroUSD
				}
			}
		}
	}
	sort.Strings(out.Blockers)
	sort.Strings(out.UnknownCosts)
	sort.Slice(out.Inventory.Resources, func(i, j int) bool {
		return out.Inventory.Resources[i].Kind+out.Inventory.Resources[i].ID < out.Inventory.Resources[j].Kind+out.Inventory.Resources[j].ID
	})
	material := *out
	material.Inventory.ObservedAt = time.Time{}
	out.Digest = maintenanceHash(struct {
		Review     ClusterRetirementReview
		Revision   int64
		Org, Cloud string
	}{material, r.Revision, r.OrgID, r.CloudID})
	return out
}

func (s *Service) retirementResource(ctx context.Context, p security.Principal, cloud string, in ClusterInput) (*ManagedResource, error) {
	return s.retirementResourceChecked(ctx, p, cloud, in, false)
}
func (s *Service) retirementOperationResource(ctx context.Context, o *ClusterOperation) (*ManagedResource, error) {
	return s.retirementResourceChecked(ctx, o.Principal, o.CloudID, o.Input, retirementRecoveryStarted(o))
}
func (s *Service) retirementResourceChecked(ctx context.Context, p security.Principal, cloud string, in ClusterInput, recovery bool) (*ManagedResource, error) {
	r, err := s.Clusters.GetManagedResource(ctx, p.OrgID, in.ResourceID)
	if err != nil {
		return nil, err
	}
	if checkErr := checkRetirementResourcePins(r, cloud, in, recovery); checkErr != nil {
		return nil, checkErr
	}
	if in.Kind == "retire-cluster" {
		if r.Ownership != "managed" || r.CreationReceipt == nil {
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
	}
	return r, nil
}

func (s *Service) PreviewClusterRetirement(ctx context.Context, p security.Principal, cloudID, resourceID string) (*ClusterRetirementReview, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if checkErr := s.clusterEnabled(); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := s.Authz.Require(ctx, p, security.ClusterDelete, ""); checkErr != nil {
		return nil, checkErr
	}
	r, err := s.Clusters.GetManagedResource(ctx, p.OrgID, resourceID)
	if err != nil {
		return nil, err
	}
	if r.CloudID != cloudID || r.DeletedAt != nil || r.Observation == nil {
		return nil, ErrConflict
	}
	profile, err := s.authorize(ctx, p, cloudID)
	if err != nil {
		return nil, err
	}
	_, access, err := s.clusterAccount(ctx, p, r.ConnectionID, provider.ClusterDiscovery)
	if err != nil {
		return nil, err
	}
	in := ClusterInput{Kind: "retire-cluster", Provider: r.Provider, AccountID: r.AccountID, ConnectionID: r.ConnectionID, ConnectionGeneration: access.Generation, CloudRevision: profile.Revision, ResourceID: r.ID, ResourceRevision: r.Revision, Ref: r.Observation.Ref}
	reader, access, err := s.clusterAuthority(ctx, p, cloudID, in)
	if err != nil {
		return nil, err
	}
	return s.prepareClusterRetirement(ctx, p, cloudID, in, reader, access, "")
}

func (s *Service) prepareClusterRetirement(ctx context.Context, p security.Principal, cloud string, in ClusterInput, reader provider.ClusterReader, access provider.AccountAccess, operation string) (*ClusterRetirementReview, error) {
	r, err := s.retirementResource(ctx, p, cloud, in)
	if err != nil {
		return nil, err
	}
	store, ok := s.Clusters.(ClusterRetirementStore)
	if !ok {
		return nil, ErrInvalid
	}
	attachment := ""
	if in.Retirement != nil && in.Retirement.Attachment != nil {
		attachment = in.Retirement.Attachment.ID
	}
	dependencies, err := store.ClusterRetirementDependencies(ctx, p.OrgID, r.ID, attachment, operation)
	if err != nil {
		return nil, err
	}
	if in.Kind == "detach-runtime" {
		if attachment == "" || !validDetachObjects(in.Retirement.Attachment) {
			return nil, ErrInvalid
		}
		a, getErr := s.Clusters.GetRuntimeAttachment(ctx, p.OrgID, attachment)
		if getErr != nil {
			return nil, getErr
		}
		expected := in.Retirement.Attachment
		if a.CloudID != cloud || a.ClusterID != r.ID || a.Revision != expected.Revision || a.DeletedAt != nil || maintenanceHash(a) != maintenanceHash(expected) {
			return nil, ErrConflict
		}
		return &ClusterRetirementReview{ResourceFingerprint: retirementResourceFingerprint(r), ResourceID: r.ID, Blockers: dependencies, Digest: maintenanceHash(struct {
			Attachment *RuntimeAttachment
			Revision   int64
		}{a, r.Revision})}, nil
	}
	retirer, ok := reader.(provider.ClusterRetirer)
	if !ok {
		return nil, provider.ErrInfrastructureUnsupported
	}
	inventory, err := retirer.AssociatedResources(ctx, access, in.Ref)
	if err != nil {
		return nil, err
	}
	review := retirementReview(r, inventory, dependencies, s.now())
	review.Digest = maintenanceHash(struct {
		Review, Connection        string
		Generation, CloudRevision int64
	}{review.Digest, in.ConnectionID, in.ConnectionGeneration, in.CloudRevision})
	return review, nil
}

// retirementResourceFingerprint excludes observation revisions and provider state.
// Trust, ownership and immutable creation evidence remain approval inputs.
func retirementResourceFingerprint(r *ManagedResource) string {
	if r == nil || r.Observation == nil {
		return ""
	}
	authority := append([]string(nil), r.LifecycleAuthority...)
	sort.Strings(authority)
	return maintenanceHash(struct {
		Org, Cloud, ID, Kind, Provider, Account, Connection, External, Incarnation, Ownership, Endpoint, CA string
		Ref                                                                                                 provider.ClusterRef
		Receipt                                                                                             *provider.ClusterMutation
		Authority                                                                                           []string
	}{r.OrgID, r.CloudID, r.ID, r.Kind, r.Provider, r.AccountID, r.ConnectionID, r.ExternalID, r.Incarnation, r.Ownership, r.Observation.Endpoint, r.Observation.CAHash, r.Observation.Ref, r.CreationReceipt, authority})
}
func retirementRecoveryStarted(o *ClusterOperation) bool {
	if o == nil || o.Input.Retirement == nil || len(o.Steps) == 0 {
		return false
	}
	if o.Kind == "detach-runtime" {
		return o.Steps[0].Name == "disable" && o.Steps[0].State == "completed"
	}
	return o.Kind == "retire-cluster" && len(o.Receipts) == 1 && o.Receipts[0].Kind == "retire-cluster" && o.Receipts[0].Ref == o.Input.Ref && o.Receipts[0].OperationID == o.ID && o.Receipts[0].RequestDigest == o.Digest
}
func checkRetirementResourcePins(r *ManagedResource, cloud string, in ClusterInput, recovery bool) error {
	if r == nil || r.CloudID != cloud || r.DeletedAt != nil || r.Status == "identity_changed" || r.Observation == nil || r.Observation.Ref != in.Ref || r.ExternalID != in.Ref.ID || r.Incarnation != in.Ref.Incarnation || r.ConnectionID != in.ConnectionID || r.Provider != in.Provider || r.AccountID != in.AccountID {
		return ErrConflict
	}
	pin := ""
	if in.Retirement != nil {
		pin = in.Retirement.ResourceFingerprint
	}
	if pin != "" && pin != retirementResourceFingerprint(r) {
		return ErrConflict
	}
	// Earlier unpinned approvals cannot gain new recovery authority retroactively.
	if r.Revision != in.ResourceRevision && (!recovery || pin == "") {
		return ErrConflict
	}
	return nil
}
