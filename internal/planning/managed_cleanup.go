package planning

import (
	"context"
	"encoding/json"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
)

type ManagedCleanupKind string

const (
	ManagedCleanupRemoveRelease    ManagedCleanupKind = "remove_release"
	ManagedCleanupRetireDependents ManagedCleanupKind = "retire_release_dependents"
)

// ManagedCleanupTarget pins the old release you reviewed. It grants no writes.
type ManagedCleanupTarget struct {
	Kind                 ManagedCleanupKind      `json:"kind"`
	Origin               string                  `json:"origin"`
	ParentBindingID      string                  `json:"parent_binding_id"`
	SchemaVersion        int                     `json:"schema_version"`
	Binding              ManagedCleanupBinding   `json:"binding"`
	Scope                kubernetes.ManagedScope `json:"scope"`
	AuthorityEpoch       int64                   `json:"authority_epoch"`
	AuthorityRevision    int64                   `json:"authority_revision"`
	RevokedAt            *string                 `json:"revoked_at"`
	AttachmentID         string                  `json:"attachment_id"`
	AttachmentRevision   int64                   `json:"attachment_revision"`
	TrustRevision        string                  `json:"trust_revision"`
	ConnectionGeneration int64                   `json:"connection_generation"`
	Accepted             []ManagedCleanupOutcome `json:"accepted"`
	Unresolved           []string                `json:"unresolved"`
	IdentityCurrent      bool                    `json:"identity_current"`
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
