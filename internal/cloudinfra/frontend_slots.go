package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/pkg/provider"
)

// FrontendDemand identifies one logical service and its reviewed destination.
type FrontendDemand struct {
	OrgID                string `json:"org_id"`
	CloudID              string `json:"cloud_id"`
	TargetID             string `json:"target_id"`
	ProjectID            string `json:"project_id"`
	EnvironmentID        string `json:"environment_id"`
	Service              string `json:"service"`
	PlanID               string `json:"plan_id"`
	Provider             string `json:"provider"`
	ConnectionID         string `json:"connection_id"`
	AccountID            string `json:"account_id"`
	TargetRevision       int64  `json:"target_revision"`
	ConnectionGeneration int64  `json:"connection_generation"`
	GrantRevision        int64  `json:"grant_revision"`
}

// ManagedFrontend matches the actual workload and execution capability.
func ManagedFrontend(name string, workload provider.Workload, execution provider.Execution) bool {
	if name == "server" || workload != provider.WebApp && workload != provider.StaticSite {
		return false
	}
	for _, p := range providers.Catalog() {
		if p.Name == name {
			for _, run := range p.Runs {
				if run.Workload == workload && run.Execution == execution {
					return true
				}
			}
		}
	}
	return false
}

type frontendQuery interface {
	Query(context.Context, string, ...any) (driver.Rows, error)
	QueryRow(context.Context, string, ...any) driver.Row
}

// ResolveFrontendDemand includes explicit connections in target governance.
// An ungranted or ambiguous association cannot become a direct deployment.
func (s *PgStore) ResolveFrontendDemand(ctx context.Context, d FrontendDemand) (*FrontendDemand, error) {
	return resolveFrontendDemand(ctx, s.db, d)
}
func resolveFrontendDemand(ctx context.Context, q frontendQuery, d FrontendDemand) (*FrontendDemand, error) {
	var account, cloud, status string
	var raw []byte
	var generation int64
	if err := q.QueryRow(ctx, `SELECT cloud,account,details,generation,status FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2`, d.OrgID, d.ConnectionID).Scan(&cloud, &account, &raw, &generation, &status); err != nil {
		return nil, err
	}
	var details map[string]string
	if err := json.Unmarshal(raw, &details); err != nil {
		return nil, err
	}
	identity := connections.AccountIdentity(cloud, account, details)
	if identity == "" || status != "ready" {
		return nil, fmt.Errorf("%w: frontend account is unavailable", ErrConflict)
	}
	rows, err := q.Query(ctx, `SELECT t.cloud_id,t.id,t.revision,c.cloud,c.account,c.details,c.status,coalesce(g.revision,0),t.connection_id
 FROM loomplane_cloud_targets t JOIN loomplane_cloud_connections c ON c.org_id=t.org_id AND c.id=t.connection_id
 LEFT JOIN loomplane_cloud_project_grants g ON g.org_id=t.org_id AND g.cloud_id=t.cloud_id AND g.project_id=$2 AND NOT g.revoked AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids))
 WHERE t.org_id=$1 AND t.runtime='managed' AND t.provider=$3 AND ($4='' OR t.cloud_id=$4) AND ($5='' OR t.id=$5) ORDER BY t.cloud_id,t.id`, d.OrgID, d.ProjectID, d.Provider, d.CloudID, d.TargetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []FrontendDemand
	for rows.Next() {
		v := d
		var connection string
		if checkErr := rows.Scan(&v.CloudID, &v.TargetID, &v.TargetRevision, &cloud, &account, &raw, &status, &v.GrantRevision, &connection); checkErr != nil {
			return nil, checkErr
		}
		details = nil
		if checkErr := json.Unmarshal(raw, &details); checkErr != nil {
			return nil, checkErr
		}
		targetIdentity := connections.AccountIdentity(cloud, account, details)
		if targetIdentity != identity {
			if connection == d.ConnectionID || d.TargetID != "" {
				return nil, fmt.Errorf("%w: frontend target account changed", ErrConflict)
			}
			continue
		}
		if status != "ready" {
			return nil, fmt.Errorf("%w: frontend target is unavailable", ErrConflict)
		}
		v.AccountID = identity
		v.ConnectionGeneration = generation
		found = append(found, v)
	}
	if checkErr := rows.Err(); checkErr != nil {
		return nil, checkErr
	}
	if len(found) == 0 && d.TargetID == "" {
		return nil, nil
	}
	if len(found) != 1 || found[0].GrantRevision < 1 {
		return nil, fmt.Errorf("%w: select one granted managed frontend target", ErrConflict)
	}
	return &found[0], nil
}

func frontendReview(d FrontendDemand) *CapacityBlock {
	return &CapacityBlock{Reason: "review_required", CloudID: d.CloudID, TargetID: d.TargetID}
}

// ReserveFrontendSlot must run before effects, under the shared organization gate.
func (s *PgStore) ReserveFrontendSlot(ctx context.Context, tx driver.Tx, d FrontendDemand) (*CapacityBlock, error) {
	if d.OrgID == "" || d.CloudID == "" || d.TargetID == "" || d.ProjectID == "" || d.EnvironmentID == "" || d.Service == "" || d.PlanID == "" || d.ConnectionID == "" || d.AccountID == "" {
		return nil, ErrInvalid
	}
	if err := lockCapacityCloud(ctx, tx, d.OrgID, d.CloudID); err != nil {
		return nil, err
	}
	current, err := resolveFrontendDemand(ctx, tx, d)
	if errors.Is(err, ErrConflict) {
		return frontendReview(d), nil
	}
	if err != nil {
		return nil, err
	}
	if current == nil || *current != d {
		return frontendReview(d), nil
	}
	var cancelled bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_deployments WHERE org_id=$1 AND plan_id=$2 AND status='cancelled')`, d.OrgID, d.PlanID).Scan(&cancelled); checkErr != nil {
		return nil, checkErr
	}
	if cancelled {
		return nil, ErrWorkloadCancelled
	}
	var raw []byte
	revision := int64(1)
	err = tx.QueryRow(ctx, `SELECT body FROM loomplane_cloud_capacity_policies WHERE org_id=$1 AND cloud_id=$2`, d.OrgID, d.CloudID).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var maximum *int64
	if len(raw) > 0 {
		var p CapacityPolicy
		if checkErr := json.Unmarshal(raw, &p); checkErr != nil {
			return nil, checkErr
		}
		revision = p.Revision
		for _, l := range p.FrontendLimits {
			if l.TargetID == d.TargetID {
				maximum = l.Maximum
			}
		}
	}
	var exists bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_frontend_slots WHERE org_id=$1 AND cloud_id=$2 AND target_id=$3 AND project_id=$4 AND environment_id=$5 AND service=$6 AND status<>'released')`, d.OrgID, d.CloudID, d.TargetID, d.ProjectID, d.EnvironmentID, d.Service).Scan(&exists); checkErr != nil {
		return nil, checkErr
	}
	if !exists && maximum != nil {
		var count int64
		if checkErr := tx.QueryRow(ctx, `SELECT count(*) FROM loomplane_frontend_slots WHERE org_id=$1 AND cloud_id=$2 AND target_id=$3 AND status<>'released'`, d.OrgID, d.CloudID, d.TargetID).Scan(&count); checkErr != nil {
			return nil, checkErr
		}
		if count >= *maximum {
			return &CapacityBlock{Reason: "hard_limit", Resource: "frontends", CloudID: d.CloudID, TargetID: d.TargetID, Limit: *maximum, Counted: count, Requested: 1, PolicyRevision: revision, Retryable: true}, nil
		}
	}
	return nil, putFrontendUse(ctx, tx, d, revision, "pending", "")
}

func putFrontendUse(ctx context.Context, tx driver.Tx, d FrontendDemand, revision int64, status, external string) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	var slot string
	err = tx.QueryRow(ctx, `INSERT INTO loomplane_frontend_slots(org_id,cloud_id,id,target_id,project_id,environment_id,service,plan_id,policy_revision,status,body)
 VALUES($1,$2,md5($3||'/'||$4||'/'||$5||'/'||$6),$3,$4,$5,$6,$7,$8,'reserved',$9)
 ON CONFLICT(org_id,cloud_id,target_id,project_id,environment_id,service) DO UPDATE SET status='reserved' RETURNING id`, d.OrgID, d.CloudID, d.TargetID, d.ProjectID, d.EnvironmentID, d.Service, d.PlanID, revision, raw).Scan(&slot)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_frontend_uses(org_id,cloud_id,slot_id,plan_id,service,connection_id,provider,account_id,external_id,status)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(org_id,cloud_id,slot_id,plan_id) DO UPDATE SET external_id=CASE WHEN excluded.external_id<>'' THEN excluded.external_id ELSE loomplane_frontend_uses.external_id END,status=CASE WHEN excluded.status='active' THEN 'active' WHEN loomplane_frontend_uses.status='unused' THEN 'pending' ELSE loomplane_frontend_uses.status END,updated_at=now()`, d.OrgID, d.CloudID, slot, d.PlanID, d.Service, d.ConnectionID, d.Provider, d.AccountID, external, status)
	return err
}

// ReleaseFrontendSlot only releases a slot whose uses all have durable retirement evidence.
func (s *PgStore) ReleaseFrontendSlot(ctx context.Context, tx driver.Tx, d FrontendDemand) error {
	if err := lockCapacityCloud(ctx, tx, d.OrgID, d.CloudID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE loomplane_frontend_slots s SET status='released' WHERE org_id=$1 AND cloud_id=$2 AND target_id=$3 AND project_id=$4 AND environment_id=$5 AND service=$6 AND NOT EXISTS(SELECT 1 FROM loomplane_frontend_uses u WHERE u.org_id=s.org_id AND u.cloud_id=s.cloud_id AND u.slot_id=s.id AND u.status NOT IN ('removed','unused'))`, d.OrgID, d.CloudID, d.TargetID, d.ProjectID, d.EnvironmentID, d.Service)
	return err
}

func refreshFrontendSlots(ctx context.Context, tx driver.Tx, org string) error {
	_, err := tx.Exec(ctx, `UPDATE loomplane_frontend_slots s SET status=CASE WHEN EXISTS(SELECT 1 FROM loomplane_frontend_uses u WHERE u.org_id=s.org_id AND u.cloud_id=s.cloud_id AND u.slot_id=s.id AND u.status NOT IN ('removed','unused')) THEN 'reserved' ELSE 'released' END WHERE org_id=$1`, org)
	return err
}

// AcceptFrontendEffects repeats whole-plan admission inside the first-effect transaction.
func (s *PgStore) AcceptFrontendEffects(ctx context.Context, tx driver.Tx, a WorkloadAttempt) error {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT result FROM loomplane_plans WHERE org_id=$1 AND id=$2 AND status='approved'`, a.Org, a.Plan).Scan(&raw); err != nil {
		return err
	}
	var result struct {
		Actions []struct {
			Name         string `json:"name"`
			Op           string `json:"op"`
			Provider     string `json:"provider"`
			ConnectionID string `json:"connection_id"`
			Service      *struct {
				Workload  provider.Workload  `json:"workload"`
				Execution provider.Execution `json:"execution"`
				Demand    *FrontendDemand    `json:"frontend_demand"`
			} `json:"service"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	for _, action := range result.Actions {
		if action.Service == nil || action.Op == "delete" || action.Op == "keep" || !ManagedFrontend(action.Provider, action.Service.Workload, action.Service.Execution) {
			continue
		}
		d := action.Service.Demand
		if d == nil {
			block, err := checkLegacyFrontend(ctx, tx, FrontendDemand{OrgID: a.Org, ProjectID: a.Project, EnvironmentID: a.Environment, PlanID: a.Plan, Service: action.Name, Provider: action.Provider, ConnectionID: action.ConnectionID})
			if err != nil {
				return err
			}
			if block != nil {
				return &CapacityAdmissionError{Block: *block}
			}
			continue
		}

		var approved bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_frontend_intents i JOIN loomplane_plans p ON p.org_id=i.org_id AND p.id=i.plan_id AND p.revision=i.plan_revision WHERE i.org_id=$1 AND i.plan_id=$2 AND i.service=$3 AND i.body=$4::jsonb)`, a.Org, a.Plan, action.Name, mustFrontendJSON(*d)).Scan(&approved); err != nil {
			return err
		}
		if !approved {
			return &CapacityAdmissionError{Block: *frontendReview(*d)}
		}
		block, err := s.ReserveFrontendSlot(ctx, tx, *d)
		if err != nil {
			return err
		}
		if block != nil {
			return &CapacityAdmissionError{Block: *block}
		}
	}
	_, err := tx.Exec(ctx, `UPDATE loomplane_frontend_uses SET status='accepted',updated_at=now() WHERE org_id=$1 AND plan_id=$2 AND service=$3 AND status='pending'`, a.Org, a.Plan, a.Service)
	return err
}
func mustFrontendJSON(d FrontendDemand) []byte {
	raw, _ := json.Marshal(d) //nolint:errcheck // FrontendDemand contains only strings and integers.
	return raw
}

// ActivateFrontendBinding shares the transaction that publishes a binding.
func ActivateFrontendBinding(ctx context.Context, tx driver.Tx, org, plan, service, connection, external string) error {
	_, err := tx.Exec(ctx, `UPDATE loomplane_frontend_uses SET status='active',external_id=$5,updated_at=now() WHERE org_id=$1 AND plan_id=$2 AND service=$3 AND connection_id=$4 AND status IN ('pending','accepted','active')`, org, plan, service, connection, external)
	return err
}

func cancelFrontendSlots(ctx context.Context, tx driver.Tx, org, plan string) error {
	_, err := tx.Exec(ctx, `UPDATE loomplane_frontend_uses u SET status='unused',evidence=jsonb_build_object('cancelled_before_effects',true),updated_at=now() WHERE org_id=$1 AND plan_id=$2 AND status='pending' AND external_id='' AND NOT EXISTS(SELECT 1 FROM loomplane_workload_attempts w JOIN loomplane_frontend_slots s ON s.org_id=u.org_id AND s.cloud_id=u.cloud_id AND s.id=u.slot_id WHERE w.org_id=u.org_id AND w.environment_id=s.environment_id AND w.service=u.service AND (w.effects_started OR w.legacy_unfenced) AND (w.plan_id=u.plan_id OR NOT w.settled))`, org, plan)
	if err != nil {
		return err
	}
	return refreshFrontendSlots(ctx, tx, org)
}

// FrontendAbsence records the exact provider resource checked after removal.
type FrontendAbsence struct{ OrgID, ProjectID, EnvironmentID, Service, Provider, ConnectionID, AccountID, ExternalID, PlanID string }

func ConfirmFrontendAbsence(ctx context.Context, tx driver.Tx, e FrontendAbsence) error {
	if e.OrgID == "" || e.Provider == "" || e.ConnectionID == "" || e.AccountID == "" || e.ExternalID == "" {
		return ErrInvalid
	}
	if err := cloudprofiles.LockOrganization(ctx, tx, e.OrgID); err != nil {
		return err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE loomplane_frontend_uses u SET status='removed',evidence=$9,updated_at=now() FROM loomplane_frontend_slots s WHERE s.org_id=u.org_id AND s.cloud_id=u.cloud_id AND s.id=u.slot_id AND s.org_id=$1 AND s.project_id=$2 AND s.environment_id=$3 AND s.service=$4 AND u.provider=$5 AND u.connection_id=$6 AND u.account_id=$7 AND (u.external_id=$8 OR u.external_id='' AND u.plan_id=$10 AND u.status='accepted')`, e.OrgID, e.ProjectID, e.EnvironmentID, e.Service, e.Provider, e.ConnectionID, e.AccountID, e.ExternalID, raw, e.PlanID)
	if err != nil {
		return err
	}
	return refreshFrontendSlots(ctx, tx, e.OrgID)
}

// backfillFrontendSlots counts serving and retained resources before a finite limit is saved.
func (s *PgStore) backfillFrontendSlots(ctx context.Context, tx driver.Tx, p *CapacityPolicy) error {
	finite := false
	for _, l := range p.FrontendLimits {
		finite = finite || l.Maximum != nil
	}
	if !finite {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT to_jsonb(b) FROM loomplane_bindings b WHERE b.org_id=$1 AND b.subject='service' UNION ALL SELECT r FROM loomplane_bindings b CROSS JOIN LATERAL jsonb_array_elements(b.replaced) r WHERE b.org_id=$1 AND b.subject='service'`, p.OrgID)
	if err != nil {
		return err
	}
	var records [][]byte
	for rows.Next() {
		var raw []byte
		if checkErr := rows.Scan(&raw); checkErr != nil {
			rows.Close()
			return checkErr
		}
		records = append(records, raw)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, raw := range records {
		var b struct {
			OrgID         string `json:"org_id"`
			ProjectID     string `json:"project_id"`
			EnvironmentID string `json:"environment_id"`
			Name          string `json:"name"`
			Provider      string `json:"provider"`
			ConnectionID  string `json:"connection_id"`
			PlanID        string `json:"plan_id"`
			ExternalID    string `json:"external_id"`
			Config        struct {
				Workload  provider.Workload  `json:"workload"`
				Execution provider.Execution `json:"execution"`
				Placement *struct {
					CloudID   string `json:"cloud_id"`
					TargetID  string `json:"target_id"`
					AccountID string `json:"account_identity"`
				} `json:"cloud_placement"`
			} `json:"config"`
		}
		if checkErr := json.Unmarshal(raw, &b); checkErr != nil {
			return checkErr
		}
		var relevant bool
		if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cloud_targets WHERE org_id=$1 AND cloud_id=$2 AND runtime='managed' AND (connection_id=$3 OR id=$4))`, p.OrgID, p.CloudID, b.ConnectionID, func() string {
			if b.Config.Placement != nil {
				return b.Config.Placement.TargetID
			}
			return ""
		}()).Scan(&relevant); checkErr != nil {
			return checkErr
		}
		if !relevant && ManagedFrontend(b.Provider, b.Config.Workload, b.Config.Execution) && b.Config.Placement == nil {
			resolved, resolveErr := resolveFrontendDemand(ctx, tx, FrontendDemand{OrgID: b.OrgID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID, PlanID: b.PlanID, Service: b.Name, Provider: b.Provider, ConnectionID: b.ConnectionID})
			if resolveErr != nil {
				return resolveErr
			}
			relevant = resolved != nil && resolved.CloudID == p.CloudID
		}
		if !relevant {
			continue
		}
		if b.Config.Workload == "" || b.Config.Execution == "" {
			return fmt.Errorf("%w: existing frontend workload scope is unknown", ErrConflict)
		}
		if !ManagedFrontend(b.Provider, b.Config.Workload, b.Config.Execution) {
			continue
		}
		d := FrontendDemand{OrgID: b.OrgID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID, PlanID: b.PlanID, Service: b.Name, Provider: b.Provider, ConnectionID: b.ConnectionID}
		if pin := b.Config.Placement; pin != nil && pin.TargetID != "" {
			if pin.CloudID != p.CloudID {
				continue
			}
			d.CloudID, d.TargetID, d.AccountID = pin.CloudID, pin.TargetID, pin.AccountID
			if d.AccountID == "" {
				return fmt.Errorf("%w: existing frontend account is unknown", ErrConflict)
			}
		} else {
			resolved, e := resolveFrontendDemand(ctx, tx, d)
			if e != nil {
				return e
			}
			if resolved == nil {
				return fmt.Errorf("%w: existing frontend target is unknown", ErrConflict)
			}
			d = *resolved
			if d.CloudID != p.CloudID {
				continue
			}
		}
		if d.PlanID == "" || b.ExternalID == "" {
			return fmt.Errorf("%w: existing frontend identity is incomplete", ErrConflict)
		}
		var removed bool
		if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_frontend_uses u JOIN loomplane_frontend_slots s ON s.org_id=u.org_id AND s.cloud_id=u.cloud_id AND s.id=u.slot_id WHERE u.org_id=$1 AND u.provider=$2 AND u.connection_id=$3 AND u.account_id=$4 AND u.external_id=$5 AND u.status='removed' AND s.project_id=$6 AND s.environment_id=$7 AND s.service=$8)`, d.OrgID, d.Provider, d.ConnectionID, d.AccountID, b.ExternalID, d.ProjectID, d.EnvironmentID, d.Service).Scan(&removed); checkErr != nil {
			return checkErr
		}
		if removed {
			continue
		}
		if checkErr := putFrontendUse(ctx, tx, d, p.Revision, "active", b.ExternalID); checkErr != nil {
			return checkErr
		}
	}
	var unresolved bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_workload_attempts w JOIN loomplane_plans plan ON plan.org_id=w.org_id AND plan.id=w.plan_id CROSS JOIN LATERAL jsonb_array_elements(plan.result->'actions') a JOIN loomplane_cloud_targets t ON t.org_id=w.org_id AND t.cloud_id=$2 AND t.runtime='managed' AND t.provider=a->>'provider'
 WHERE w.org_id=$1 AND w.service=a->>'name' AND NOT w.settled AND (w.effects_started OR w.legacy_unfenced) AND a->'service'->>'workload' IN ('web-app','static-site') AND NOT EXISTS(SELECT 1 FROM loomplane_frontend_uses u WHERE u.org_id=w.org_id AND u.plan_id=w.plan_id AND u.service=w.service))`, p.OrgID, p.CloudID).Scan(&unresolved); checkErr != nil {
		return checkErr
	}
	if unresolved {
		return fmt.Errorf("%w: reconcile the unfinished frontend attempt before setting a maximum", ErrConflict)
	}
	return nil
}

// FrontendUsage counts each logical slot once, even when several revisions or
// source resources remain. Pending includes accepted effects awaiting a binding.
type FrontendUsage struct {
	TargetID                           string
	Maximum                            *int64
	Counted, Active, Pending, Retained int64
}

func (s *PgStore) FrontendUsage(ctx context.Context, org, cloud string) ([]FrontendUsage, error) {
	policy, err := s.GetCapacityPolicy(ctx, org, cloud)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT t.id,
 count(s.id),
 count(s.id) FILTER(WHERE EXISTS(SELECT 1 FROM loomplane_frontend_uses u JOIN loomplane_bindings b ON b.org_id=u.org_id AND b.plan_id=u.plan_id AND b.name=u.service AND b.connection_id=u.connection_id AND b.external_id=u.external_id WHERE u.org_id=s.org_id AND u.cloud_id=s.cloud_id AND u.slot_id=s.id AND u.status='active')),
 count(s.id) FILTER(WHERE NOT EXISTS(SELECT 1 FROM loomplane_frontend_uses u WHERE u.org_id=s.org_id AND u.cloud_id=s.cloud_id AND u.slot_id=s.id AND u.status='active'))
 FROM loomplane_cloud_targets t LEFT JOIN loomplane_frontend_slots s ON s.org_id=t.org_id AND s.cloud_id=t.cloud_id AND s.target_id=t.id AND s.status<>'released'
 WHERE t.org_id=$1 AND t.cloud_id=$2 AND t.runtime='managed' GROUP BY t.id ORDER BY t.id`, org, cloud)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FrontendUsage{}
	for rows.Next() {
		var u FrontendUsage
		if checkErr := rows.Scan(&u.TargetID, &u.Counted, &u.Active, &u.Pending); checkErr != nil {
			return nil, checkErr
		}
		u.Retained = u.Counted - u.Active - u.Pending
		for _, l := range policy.FrontendLimits {
			if l.TargetID == u.TargetID {
				u.Maximum = l.Maximum
			}
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CheckLegacyFrontend keeps finite governance from exempting an older review.
func (s *PgStore) CheckLegacyFrontend(ctx context.Context, d FrontendDemand) (*CapacityBlock, error) {
	return checkLegacyFrontend(ctx, s.db, d)
}
func checkLegacyFrontend(ctx context.Context, q frontendQuery, d FrontendDemand) (*CapacityBlock, error) {
	resolved, err := resolveFrontendDemand(ctx, q, d)
	if resolved == nil && err == nil {
		return nil, nil
	}
	scope := d
	if resolved != nil {
		scope = *resolved
	}
	var limited bool
	if queryErr := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cloud_targets t JOIN loomplane_cloud_capacity_policies p ON p.org_id=t.org_id AND p.cloud_id=t.cloud_id CROSS JOIN LATERAL jsonb_array_elements(coalesce(nullif(p.body->'frontend_limits','null'::jsonb),'[]'::jsonb)) l WHERE t.org_id=$1 AND t.provider=$2 AND l->>'target_id'=t.id AND l->'maximum' IS NOT NULL AND l->'maximum'<>'null'::jsonb AND ($3='' OR t.id=$3))`, d.OrgID, d.Provider, scope.TargetID).Scan(&limited); queryErr != nil {
		return nil, queryErr
	}
	if limited {
		return frontendReview(scope), nil
	}
	return nil, nil
}
