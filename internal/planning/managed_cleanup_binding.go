package planning

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
)

var ErrManagedCleanupBindingChanged = errors.New("the old release was cleaned up, but the service binding changed; review its current owner")
var ErrManagedCleanupCurrentReview = errors.New("review the current service binding before removing it")

func (s *ManagedCleanupStore) ReviewedCleanupTargets(ctx context.Context, sel ManagedCleanupSelection) (out []ManagedCleanupTarget, err error) {
	selections, err := s.effectSelections(ctx, sel)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, sel.Org); checkErr != nil {
		return nil, checkErr
	}
	for _, selection := range selections {
		t, readErr := s.loadTarget(ctx, tx, selection)
		if readErr != nil {
			return nil, readErr
		}
		var raw []byte
		if readErr := tx.QueryRow(ctx, `SELECT a->'service' FROM loomplane_plans p CROSS JOIN LATERAL jsonb_array_elements(p.result->'actions') a WHERE p.org_id=$1 AND p.project_id=$2 AND p.environment_id=$3 AND p.id=$4 AND a->>'id'=$5 AND a->>'name'=$6`, t.Scope.Org, t.Scope.Project, t.Scope.Environment, t.Scope.Plan, t.Scope.Action, t.Scope.Service).Scan(&raw); readErr != nil {
			return nil, readErr
		}
		var cfg ServiceConfig
		if decodeErr := json.Unmarshal(raw, &cfg); decodeErr != nil {
			return nil, decodeErr
		}
		if checkErr := cfg.CheckManagedServiceFinalizers(); checkErr != nil {
			return nil, checkErr
		}
		out = append(out, t)
	}
	return out, tx.Commit()
}

func reviewedCurrent(t ManagedCleanupTarget) *ManagedCleanupBinding {
	if t.Origin == "current" {
		return &t.Binding
	}
	for i := range t.BindingRefs {
		if t.BindingRefs[i].Origin == "current" {
			return &t.BindingRefs[i].Binding
		}
	}
	return nil
}

// GuardManagedDeleteBinding compares the current service row with the accepted
// action, both before its removal and when deleting the binding. A historical
// retained-only review can finish its old targets but cannot remove the current row.
func (s *ManagedCleanupStore) GuardManagedDeleteBinding(ctx context.Context, sel ManagedCleanupSelection, b *Binding, remove bool) (managed bool, err error) {
	if sel.FailedReviewID != "" {
		return false, kube.ErrManagedAuthority
	}
	selections, err := s.effectSelections(ctx, sel)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, sel.Org); checkErr != nil {
		return false, checkErr
	}
	if checkErr := cloudinfra.GuardPoolWrite(ctx, tx); checkErr != nil {
		return false, checkErr
	}
	var signed *ManagedCleanupBinding
	var mixed *ManagedCleanupCurrentBinding
	var scope kube.ManagedScope
	for _, selection := range selections {
		t, readErr := s.loadTarget(ctx, tx, selection)
		if readErr != nil {
			return false, readErr
		}
		scope = t.Scope
		if ref := reviewedCurrent(t); ref != nil {
			signed = ref
			managed = true
		}
		if t.MixedCurrentBinding != nil {
			mixed = t.MixedCurrentBinding
		}
	}
	var deleteAction bool
	if readErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_plans p,jsonb_array_elements(p.result->'actions') a WHERE p.org_id=$1 AND p.id=$2 AND a->>'id'=$3 AND a->>'op'='delete')`, sel.Org, sel.Plan, sel.Action).Scan(&deleteAction); readErr != nil {
		return false, readErr
	}
	if !deleteAction {
		return false, kube.ErrManagedAuthority
	}
	current, readErr := scanBinding(tx.QueryRow(ctx, `SELECT `+bindingColumns+` FROM loomplane_bindings WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND subject='service' AND name=$4 FOR UPDATE`, scope.Org, scope.Project, scope.Environment, scope.Service))
	if errors.Is(readErr, pgx.ErrNoRows) || errors.Is(readErr, ErrNotFound) {
		return managed, tx.Commit()
	}
	if readErr != nil {
		return false, readErr
	}
	if mixed != nil && signed == nil {
		signed = &mixed.ManagedCleanupBinding
	}
	if signed == nil {
		return false, ErrManagedCleanupCurrentReview
	}
	stamp, parseErr := time.Parse(time.RFC3339Nano, signed.UpdatedAt)
	if parseErr != nil || b == nil || current.ID != signed.ID || current.PlanID != signed.PlanID || current.ConnectionID != signed.ConnectionID || current.ExternalID != signed.ExternalID || current.Revision != signed.Revision || !current.UpdatedAt.Equal(stamp) || !sameRemovalBinding(*current, *b) {
		return false, ErrManagedCleanupBindingChanged
	}
	if managed && current.Provider != "kubernetes" {
		return false, ErrManagedCleanupBindingChanged
	}
	if mixed != nil {
		var matches bool
		if readErr := tx.QueryRow(ctx, `SELECT org_id=$2 AND project_id=$3 AND environment_id=$4 AND subject=$5 AND name=$6 AND provider=$7 AND region=$8 AND engine=$9 AND physical_id=$10 AND encode(sha256(convert_to(config::text,'UTF8')),'hex')=$11 FROM loomplane_bindings WHERE id=$1`, current.ID, mixed.OrgID, mixed.ProjectID, mixed.EnvironmentID, mixed.Subject, mixed.Name, mixed.Provider, mixed.Region, mixed.Engine, mixed.PhysicalID, mixed.ConfigHash).Scan(&matches); readErr != nil {
			return false, readErr
		}
		if !matches {
			return false, ErrManagedCleanupBindingChanged
		}
	}
	if remove {
		if len(current.Replaced) > 0 {
			return false, errors.New("the binding still tracks a retained workload")
		}
		if managed {
			var absent bool
			if readErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_managed_retired_instances WHERE org_id=$1 AND instance_id=$2) AND NOT EXISTS(SELECT 1 FROM cp_instances WHERE tenant_id=$1 AND id=$2)`, scope.Org, current.ExternalID).Scan(&absent); readErr != nil {
				return false, readErr
			}
			if !absent {
				return false, kube.ErrManagedAuthority
			}
		}
		if _, checkErr := tx.Exec(ctx, `SELECT set_config('loomplane.reviewed_binding_delete',jsonb_build_object('plan',$1::text,'action',$2::text,'deployment',$3::text)::text,true)`, sel.Plan, sel.Action, sel.Deployment); checkErr != nil {
			return false, checkErr
		}
		result, deleteErr := tx.Exec(ctx, `DELETE FROM loomplane_bindings WHERE org_id=$1 AND id=$2 AND project_id=$3 AND environment_id=$4 AND subject='service' AND name=$5 AND provider=$6 AND connection_id=$7 AND external_id=$8 AND physical_id=$9 AND plan_id=$10 AND revision=$11 AND updated_at=$12 AND region=$13 AND engine=$14 AND replaced='[]'::jsonb`, scope.Org, signed.ID, scope.Project, scope.Environment, scope.Service, current.Provider, signed.ConnectionID, signed.ExternalID, current.PhysicalID, signed.PlanID, signed.Revision, stamp, current.Region, current.Engine)
		if deleteErr != nil {
			return false, deleteErr
		}
		if n, countErr := result.RowsAffected(); countErr != nil || n != 1 {
			return false, errors.Join(ErrManagedCleanupBindingChanged, countErr)
		}
	}
	return managed, tx.Commit()
}

func sameRemovalBinding(a, b Binding) bool {
	return a.ID == b.ID && a.OrgID == b.OrgID && a.ProjectID == b.ProjectID && a.EnvironmentID == b.EnvironmentID && a.Subject == b.Subject && a.Name == b.Name && a.Provider == b.Provider && a.Region == b.Region && a.Engine == b.Engine && a.ConnectionID == b.ConnectionID && a.ExternalID == b.ExternalID && a.PhysicalID == b.PhysicalID && a.PlanID == b.PlanID && a.Revision == b.Revision && a.UpdatedAt.Equal(b.UpdatedAt)
}

func (s *ManagedCleanupStore) SetReviewedReplaced(ctx context.Context, sel ManagedCleanupSelection, b *Binding, left []Binding) (err error) {
	old, err := encodeReplaced(b.Replaced)
	if err != nil {
		return err
	}
	newRefs, err := encodeReplaced(left)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, b.OrgID); checkErr != nil {
		return checkErr
	}
	if checkErr := cloudinfra.GuardPoolWrite(ctx, tx); checkErr != nil {
		return checkErr
	}
	if _, readErr := s.loadTarget(ctx, tx, sel); readErr != nil {
		return readErr
	}
	if _, markErr := tx.Exec(ctx, `SELECT set_config('loomplane.reviewed_retained_deployment',$1,true)`, sel.Deployment); markErr != nil {
		return markErr
	}
	result, err := tx.Exec(ctx, `UPDATE loomplane_bindings SET replaced=$13 WHERE org_id=$1 AND id=$2 AND project_id=$3 AND environment_id=$4 AND subject='service' AND name=$5 AND provider=$6 AND connection_id=$7 AND external_id=$8 AND physical_id=$9 AND plan_id=$10 AND revision=$11 AND updated_at=$12 AND replaced=$14::jsonb AND region=$15 AND engine=$16`, b.OrgID, b.ID, b.ProjectID, b.EnvironmentID, b.Name, b.Provider, b.ConnectionID, b.ExternalID, b.PhysicalID, b.PlanID, b.Revision, b.UpdatedAt, newRefs, old, b.Region, b.Engine)
	if err != nil {
		return err
	}
	if n, countErr := result.RowsAffected(); countErr != nil || n != 1 {
		return errors.Join(ErrManagedCleanupBindingChanged, countErr)
	}
	return tx.Commit()
}

// RequiresReviewedRemoval recognizes journal ownership after its connection or
// original binding has disappeared. A stale action cannot forget that runtime
// through the legacy missing-connection path.
func (s *PgStore) RequiresReviewedRemoval(ctx context.Context, b *Binding) (bool, error) {
	if b == nil || b.Subject != ServiceSubject || b.ExternalID == "" {
		return false, nil
	}
	var required bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_managed_workload_authority a WHERE a.org_id=$1 AND a.body->>'Instance'=$2)
 OR EXISTS(SELECT 1 FROM loomplane_workload_controllers c WHERE c.org_id=$1 AND c.instance_id=$2 AND coalesce(c.body->>'release_id','')<>'')
 OR ($3='kubernetes' AND EXISTS(SELECT 1 FROM loomplane_plans p WHERE p.org_id=$1 AND p.id=$4 AND p.required_runtime_protocol<>'' AND EXISTS(SELECT 1 FROM jsonb_array_elements(p.result->'actions') a WHERE a->>'name'=$5 AND a->>'subject'='service' AND coalesce(a->'service'->'target_settings'->>'managed_attachment_id','')<>'')))`, b.OrgID, b.ExternalID, b.Provider, b.PlanID, b.Name).Scan(&required)
	return required, err
}
