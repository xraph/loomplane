package planning

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
	"github.com/xraph/loomplane/pkg/manifest"
)

// GroupExecutionConfiguration is private retained review input. Public plans
// contain only Revision, never the source keys or Vault version identities.
type GroupExecutionConfiguration struct {
	Revision         string                        `json:"revision"`
	ManifestRevision string                        `json:"manifest_revision"`
	Kind             string                        `json:"kind"`
	Needs            []string                      `json:"needs"`
	Settings         manifest.Settings             `json:"settings"`
	Selection        environments.RuntimeSelection `json:"selection"`
}

type GroupVariableSelection interface {
	SelectRuntime(context.Context, security.Principal, string, string, string) (environments.RuntimeSelection, error)
}

func GroupEffectiveConfigurationRevision(manifestRevision, selectionRevision string) (string, error) {
	if manifestRevision == "" || selectionRevision == "" {
		return "", domain.ErrGroupProfile
	}
	return Digest(struct{ Manifest, Selection string }{manifestRevision, selectionRevision})
}
func groupAnalyzedService(g *graph.Graph, name string, service manifest.Service) (string, error) {
	if service.Source == "" && service.Repo == "" {
		return "", nil
	}
	if g == nil {
		return "", domain.ErrGroupProfile
	}
	p := &planner{in: Input{Graph: g}}
	repo, ok := p.repoOf(name, "services."+name, service)
	if !ok {
		return "", domain.ErrGroupProfile
	}
	matched := p.match(name, "services."+name, repo, service)
	if matched == nil {
		return "", domain.ErrGroupProfile
	}
	return matched.ID, nil
}
func (s *MemberPrerequisiteService) groupConfiguration(ctx context.Context, p *Plan, m *manifest.Manifest, g *graph.Graph, name string) (GroupExecutionConfiguration, error) {
	var out GroupExecutionConfiguration
	manifestRevision, err := GroupConfigurationRevision(m, p.Environment, name)
	if err != nil {
		return out, err
	}
	if s.Variables == nil {
		return out, ErrGroupVariableSnapshotsUnavailable
	}
	service, err := groupAnalyzedService(g, name, m.Services[name])
	if err != nil {
		return out, err
	}
	selection, err := s.Variables.SelectRuntime(ctx, security.Principal{OrgID: p.OrgID, UserID: p.CreatedBy}, p.ProjectID, p.Environment, service)
	if err != nil {
		return out, err
	}
	revision, err := GroupEffectiveConfigurationRevision(manifestRevision, selection.Revision)
	if err != nil {
		return out, err
	}
	return GroupExecutionConfiguration{Revision: revision, ManifestRevision: manifestRevision, Kind: m.Services[name].Kind, Needs: m.Services[name].Needs, Settings: m.Services[name].For(p.Environment, m.Defaults), Selection: selection}, nil
}
func (s *PgStore) SavePlacementGroupConfiguration(ctx context.Context, group domain.QualifiedGroupPlan, c GroupExecutionConfiguration) error {
	manifestRevision, manifestErr := Digest(struct {
		Kind     string
		Settings manifest.Settings
		Needs    []string
	}{c.Kind, c.Settings, c.Needs})
	if c.Settings.Commands != nil || c.Settings.Vercel != nil || manifestErr != nil || manifestRevision != c.ManifestRevision || c.Kind != "http-service" || len(c.Needs) != 0 || c.Settings.Build == nil || c.Settings.Build.Image != group.Image || c.Settings.PodGroup != nil || c.Settings.Worker != nil {
		return ErrConflict
	}
	expected, err := GroupEffectiveConfigurationRevision(c.ManifestRevision, c.Selection.Revision)
	if err != nil || expected != c.Revision || c.Revision != group.ConfigRevision || c.Selection.OrgID != group.OrgID || c.Selection.ProjectID != group.ProjectID || c.Selection.EnvironmentID != group.EnvironmentID {
		return ErrConflict
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.groupTx(ctx, group.OrgID, func(tx driver.Tx) error {
		var revision string
		if err := tx.QueryRow(ctx, `SELECT body->>'config_revision' FROM loomplane_placement_group_revisions WHERE org_id=$1 AND group_id=$2 AND generation=$3`, group.OrgID, group.GroupID, group.Generation).Scan(&revision); err != nil {
			return err
		}
		if revision != c.Revision {
			return ErrConflict
		}
		_, err := tx.Exec(ctx, `INSERT INTO loomplane_group_configurations(org_id,group_id,generation,config_revision,body) VALUES($1,$2,$3,$4,$5) ON CONFLICT(org_id,group_id,generation) DO NOTHING`, group.OrgID, group.GroupID, group.Generation, c.Revision, raw)
		if err != nil {
			return err
		}
		var same bool
		if readErr := tx.QueryRow(ctx, `SELECT body=$4::jsonb FROM loomplane_group_configurations WHERE org_id=$1 AND group_id=$2 AND generation=$3`, group.OrgID, group.GroupID, group.Generation, raw).Scan(&same); readErr != nil {
			return readErr
		}
		if !same {
			return ErrConflict
		}
		return nil
	})
}

// GroupAcceptanceAuthority must evaluate the current saved actor and API key
// through the installed identity engine on tx, including current memberships,
// restrictions and uncached permissions. A precheck alone cannot implement it.
type GroupAcceptanceAuthority interface {
	// LockGroupAuthorityTx runs after the source gate and before Cloud or row locks.
	LockGroupAuthorityTx(context.Context, driver.Tx) error
	AuthorizeGroupAcceptanceTx(context.Context, driver.Tx, security.Principal, string) error
}

// BindGroupAcceptanceAuthority is startup-only. Until an implementation is
// installed, group approval fails closed even for an executable saved plan.
func (s *PgStore) BindGroupAcceptanceAuthority(authority GroupAcceptanceAuthority) {
	s.groupAcceptance = authority
}

func (s *PgStore) acceptPlacementGroupsTx(ctx context.Context, tx driver.Tx, org, planID string, now time.Time) error {
	var raw []byte
	var revision, project, environment string
	var executable bool
	if err := tx.QueryRow(ctx, `SELECT result,revision,project_id,environment_id,executable FROM loomplane_plans WHERE org_id=$1 AND id=$2 AND status='ready'`, org, planID).Scan(&raw, &revision, &project, &environment, &executable); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if len(result.PlacementGroups) == 0 {
		return nil
	}
	actor, ok := security.PrincipalFrom(ctx)
	if !ok || actor.OrgID != org || actor.UserID == "" || actor.ReadOnly || actor.Kind != security.KindUser || actor.Fleet != nil || !executable || len(result.Actions) != 0 {
		return ErrConflict
	}
	if s.groupAcceptance == nil {
		return ErrUnavailable
	}
	if err := s.groupAcceptance.AuthorizeGroupAcceptanceTx(ctx, tx, actor, project); err != nil {
		return err
	}
	actorRaw, err := json.Marshal(actor)
	if err != nil {
		return err
	}
	for _, group := range result.PlacementGroups {
		if err := ValidatePlacementGroupReviewTx(ctx, tx, org, project, environment, group, now); err != nil {
			return err
		}
		var configRaw []byte
		if err := tx.QueryRow(ctx, `SELECT body FROM loomplane_group_configurations WHERE org_id=$1 AND group_id=$2 AND generation=$3 AND config_revision=$4`, org, group.GroupID, group.Generation, group.ConfigRevision).Scan(&configRaw); err != nil {
			return err
		}
		var config GroupExecutionConfiguration
		if json.Unmarshal(configRaw, &config) != nil {
			return ErrConflict
		}
		if err := environments.ValidateRuntimeSelectionTx(ctx, tx, config.Selection); err != nil {
			return ErrStale
		}
		_, err := tx.Exec(ctx, `INSERT INTO loomplane_group_executions(org_id,group_id,generation,plan_id,plan_revision,actor,snapshot_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, org, group.GroupID, group.Generation, planID, revision, actorRaw, typeid.MustGenerate("pgsnap").String())
		if err != nil {
			return err
		}
	}
	return nil
}

// GuardAcceptedPlacementGroupTx checks immutable acceptance and current target
// facts using only tx. The caller must also run its required current actor/key
// authority check on this transaction; this target guard cannot replace it.
func GuardAcceptedPlacementGroupTx(ctx context.Context, tx driver.Tx, group domain.QualifiedGroupPlan, actor security.Principal) error {
	if actor.OrgID != group.OrgID || actor.UserID == "" || actor.ReadOnly || actor.Kind != security.KindUser || actor.Fleet != nil {
		return security.ErrForbidden
	}
	actorRaw, err := json.Marshal(actor)
	if err != nil {
		return err
	}
	var accepted bool
	err = tx.QueryRow(ctx, `SELECT true FROM loomplane_group_executions e JOIN loomplane_plans p ON p.org_id=e.org_id AND p.id=e.plan_id JOIN loomplane_placement_group_revisions r ON r.org_id=e.org_id AND r.group_id=e.group_id AND r.generation=e.generation WHERE e.org_id=$1 AND e.group_id=$2 AND e.generation=$3 AND e.cancelled_at IS NULL AND e.actor=$4::jsonb AND p.status='approved' AND p.revision=e.plan_revision AND p.project_id=$5 AND p.environment_id=$6 AND r.revision=$7 FOR SHARE OF e,p,r`, group.OrgID, group.GroupID, group.Generation, actorRaw, group.ProjectID, group.EnvironmentID, group.Revision).Scan(&accepted)
	if err != nil || !accepted {
		return ErrConflict
	}
	return ValidatePlacementGroupReviewTx(ctx, tx, group.OrgID, group.ProjectID, group.EnvironmentID, group, time.Now().UTC())
}

// AuthorizePlacementGroup consumes the saved approving actor. No caller-supplied
// actor, current variable values or old receipt can substitute for this authority.
func (s *Service) AuthorizePlacementGroup(ctx context.Context, group domain.QualifiedGroupPlan) (actor security.Principal, err error) {
	store, ok := s.store.(*PgStore)
	if !ok || s.authz == nil || store.groupAcceptance == nil {
		return actor, ErrUnavailable
	}
	var raw []byte
	if readErr := store.db.QueryRow(ctx, `SELECT actor FROM loomplane_group_executions WHERE org_id=$1 AND group_id=$2 AND generation=$3 AND cancelled_at IS NULL`, group.OrgID, group.GroupID, group.Generation).Scan(&raw); readErr != nil {
		return actor, readErr
	}
	if json.Unmarshal(raw, &actor) != nil || actor.OrgID != group.OrgID || actor.Kind != security.KindUser || actor.Fleet != nil || actor.ReadOnly || actor.UserID == "" {
		return security.Principal{}, security.ErrForbidden
	}
	if actor.KeyID != "" {
		if s.capacityPrincipal == nil {
			return actor, ErrUnavailable
		}
		if principalErr := s.capacityPrincipal.ValidatePrincipal(ctx, actor); principalErr != nil {
			return actor, principalErr
		}
	}
	if permissionErr := s.authz.Require(ctx, actor, security.DeploymentApprove, group.ProjectID); permissionErr != nil {
		return actor, permissionErr
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return actor, err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	if sourceLockErr := environments.LockRuntimeSourcesTx(ctx, tx); sourceLockErr != nil {
		return actor, sourceLockErr
	}
	if authorityLockErr := store.groupAcceptance.LockGroupAuthorityTx(ctx, tx); authorityLockErr != nil {
		return actor, authorityLockErr
	}
	if organizationLockErr := cloudprofiles.LockOrganization(ctx, tx, group.OrgID); organizationLockErr != nil {
		return actor, organizationLockErr
	}
	if authorityErr := store.groupAcceptance.AuthorizeGroupAcceptanceTx(ctx, tx, actor, group.ProjectID); authorityErr != nil {
		return actor, authorityErr
	}
	if guardErr := GuardAcceptedPlacementGroupTx(ctx, tx, group, actor); guardErr != nil {
		return actor, guardErr
	}
	return actor, tx.Commit()
}
