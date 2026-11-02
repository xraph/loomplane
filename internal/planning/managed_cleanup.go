package planning

import (
	"context"
	"encoding/json"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/orchestration"
)

type ManagedCleanupKind string

const (
	ManagedCleanupRemoveRelease    ManagedCleanupKind = "remove_release"
	ManagedCleanupRetireDependents ManagedCleanupKind = "retire_release_dependents"
)

// ManagedCleanupTarget pins the old release you reviewed. It grants no writes.
type ManagedCleanupTarget struct {
	MixedCurrentBinding  *ManagedCleanupCurrentBinding `json:"mixed_current_binding,omitempty"`
	Kind                 ManagedCleanupKind            `json:"kind"`
	Origin               string                        `json:"origin"`
	ParentBindingID      string                        `json:"parent_binding_id"`
	SchemaVersion        int                           `json:"schema_version"`
	BindingRefs          []ManagedCleanupBindingRef    `json:"binding_refs,omitempty"`
	Binding              ManagedCleanupBinding         `json:"binding"`
	Scope                kubernetes.ManagedScope       `json:"scope"`
	AuthorityEpoch       int64                         `json:"authority_epoch"`
	AuthorityRevision    int64                         `json:"authority_revision"`
	RevokedAt            *string                       `json:"revoked_at"`
	AttachmentID         string                        `json:"attachment_id"`
	AttachmentRevision   int64                         `json:"attachment_revision"`
	TrustRevision        string                        `json:"trust_revision"`
	ConnectionGeneration int64                         `json:"connection_generation"`
	Accepted             []ManagedCleanupOutcome       `json:"accepted"`
	Unresolved           []string                      `json:"unresolved"`
	IdentityCurrent      bool                          `json:"identity_current"`
}

// ManagedCleanupCurrentBinding pins the current service row when the managed
// targets in a Delete review are all retained predecessors.
type ManagedCleanupCurrentBinding struct {
	ManagedCleanupBinding
	OrgID         string `json:"org_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	Name          string `json:"name"`
	Subject       string `json:"subject"`
	Provider      string `json:"provider"`
	Region        string `json:"region"`
	Engine        string `json:"engine"`
	PhysicalID    string `json:"physical_id"`
	ConfigHash    string `json:"config_hash"`
}

type ManagedCleanupBindingRef struct {
	Origin          string                `json:"origin"`
	ParentBindingID string                `json:"parent_binding_id"`
	Binding         ManagedCleanupBinding `json:"binding"`
}

type ManagedCleanupBinding struct {
	ID           string `json:"id"`
	ExternalID   string `json:"external_id"`
	PlanID       string `json:"plan_id"`
	ConnectionID string `json:"connection_id"`
	Revision     string `json:"revision"`
	UpdatedAt    string `json:"updated_at"`
}

type ManagedCleanupOutcome struct {
	ID      string                    `json:"id"`
	Epoch   int64                     `json:"epoch"`
	Request kubernetes.ManagedRequest `json:"request"`
	Outcome kubernetes.ManagedOutcome `json:"outcome"`
}

// PrepareManagedCleanupTargets runs before the plan's review digest is computed.
// Approval compares the same snapshot and records its own authenticated actor.
func (s *Service) PrepareManagedCleanupTargets(ctx context.Context, plan *Plan, result *Result) error {
	store, ok := s.store.(*PgStore)
	if !ok {
		return nil
	}
	for i := range result.Actions {
		action := &result.Actions[i]
		if action.Subject != ServiceSubject || (action.Op != Delete && action.Op != Replace && action.Op != Update) {
			continue
		}
		var raw []byte
		if err := store.db.QueryRow(ctx, `SELECT loomplane_managed_cleanup_targets($1,$2,$3,$4,$5)`, plan.OrgID, plan.ProjectID, plan.EnvironmentID, action.Name, action.Op).Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &action.CleanupTargets); err != nil {
			return err
		}
		if len(action.CleanupTargets) > 0 {
			result.RequiredRuntimeProtocol = orchestration.ManagedCleanupProtocol
		}
		for _, target := range action.CleanupTargets {
			if !target.IdentityCurrent {
				return workloadBlock("managed_cleanup_identity_changed")
			}
			if len(target.Unresolved) > 0 {
				return workloadBlock("managed_cleanup_writes_unresolved")
			}
		}
	}
	return nil
}
