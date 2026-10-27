package planning

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
)

func HasCapacityIntent(a Action) bool {
	return a.Service != nil && (a.Service.CapacityIntent != nil || a.Service.FrontendDemand != nil)
}

func (s *Service) pinFrontendDemands(ctx context.Context, plan *Plan, result *Result) error {
	st, ok := s.store.(*PgStore)
	if !ok {
		return nil
	}
	for i := range result.Actions {
		a := &result.Actions[i]
		if a.Service == nil {
			continue
		}
		a.Service.FrontendDemand = nil
		if a.Op == Delete || a.Op == Keep || !cloudinfra.ManagedFrontend(a.Provider, a.Service.Workload, a.Service.Execution) {
			continue
		}
		d := cloudinfra.FrontendDemand{OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, PlanID: plan.ID, Service: a.Name, Provider: a.Provider, ConnectionID: a.ConnectionID}
		var resolved *cloudinfra.FrontendDemand
		var err error
		if pin := a.Service.CloudPlacement; pin != nil && pin.TargetID != "" {
			d.CloudID, d.TargetID, d.TargetRevision = pin.CloudID, pin.TargetID, pin.TargetRevision
			d.GrantRevision = pin.GrantRevision
			var account, connectionCloud string
			var raw []byte
			err = st.db.QueryRow(ctx, `SELECT generation,account,details,cloud FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2 AND status='ready'`, plan.OrgID, a.ConnectionID).Scan(&d.ConnectionGeneration, &account, &raw, &connectionCloud)
			if err != nil {
				return err
			}
			var details map[string]string
			if checkErr := json.Unmarshal(raw, &details); checkErr != nil {
				return checkErr
			}
			d.AccountID = connections.AccountIdentity(connectionCloud, account, details)
			if d.AccountID == "" || d.AccountID != pin.AccountIdentity || d.GrantRevision < 1 {
				return fmt.Errorf("%w: frontend account or grant is missing", ErrConflict)
			}
			resolved = &d
		} else {
			if pin := a.Service.CloudPlacement; pin != nil {
				d.CloudID = pin.CloudID
			}
			resolved, err = cloudinfra.NewPgStore(st.db).ResolveFrontendDemand(ctx, d)
			if err != nil {
				return err
			}
		}
		a.Service.FrontendDemand = resolved
	}
	return nil
}

func (s *Service) resolveFrontendIntent(ctx context.Context, st *PgStore, plan *Plan, a Action) error {
	d := a.Service.FrontendDemand
	var raw, actor []byte
	err := st.db.QueryRow(ctx, `SELECT i.body,i.actor FROM loomplane_frontend_intents i JOIN loomplane_plans p ON p.org_id=i.org_id AND p.id=i.plan_id AND p.revision=i.plan_revision AND p.status='approved' WHERE i.org_id=$1 AND i.plan_id=$2 AND i.service=$3`, plan.OrgID, plan.ID, a.Name).Scan(&raw, &actor)
	if err != nil {
		return capacityReview(cloudinfra.CapacityDemand{CloudID: d.CloudID, TargetID: d.TargetID})
	}
	var saved cloudinfra.FrontendDemand
	var principal security.Principal
	if checkErr := json.Unmarshal(raw, &saved); checkErr != nil {
		return checkErr
	}
	if checkErr := json.Unmarshal(actor, &principal); checkErr != nil {
		return checkErr
	}
	review := func() error {
		return capacityReview(cloudinfra.CapacityDemand{CloudID: d.CloudID, TargetID: d.TargetID})
	}
	if saved != *d || principal.OrgID != plan.OrgID || principal.UserID == "" || principal.ReadOnly || s.authz == nil {
		return review()
	}
	if principal.KeyID != "" {
		if s.capacityPrincipal == nil {
			return review()
		}
		if checkErr := s.capacityPrincipal.ValidatePrincipal(ctx, principal); checkErr != nil {
			return review()
		}
	}
	if checkErr := s.authz.Require(ctx, principal, security.DeploymentApprove, plan.ProjectID); checkErr != nil {
		return review()
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // rollback is harmless after commit
	block, err := cloudinfra.NewPgStore(st.db).ReserveFrontendSlot(ctx, tx, *d)
	if err != nil {
		return err
	}
	if block != nil {
		return &cloudinfra.CapacityAdmissionError{Block: *block}
	}
	if checkErr := tx.Commit(); checkErr != nil {
		return checkErr
	}
	st.wakeFrontendPlan(ctx, plan.OrgID, plan.ID, "frontend-reserved/"+plan.ID+"/"+a.Name)
	return nil
}

func (s *PgStore) RecordFrontendAbsence(ctx context.Context, e cloudinfra.FrontendAbsence) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // rollback is harmless after commit
	if checkErr := cloudinfra.ConfirmFrontendAbsence(ctx, tx, e); checkErr != nil {
		return checkErr
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT cloud_id FROM loomplane_frontend_slots WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND service=$4`, e.OrgID, e.ProjectID, e.EnvironmentID, e.Service)
	if err != nil {
		return err
	}
	var clouds []string
	for rows.Next() {
		var c string
		if checkErr := rows.Scan(&c); checkErr != nil {
			rows.Close()
			return checkErr
		}
		clouds = append(clouds, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if checkErr := tx.Commit(); checkErr != nil {
		return checkErr
	}
	if s.frontendWake != nil {
		for _, c := range clouds {
			if wakeErr := s.frontendWake.WakeCapacityCloud(ctx, e.OrgID, c, "frontend-removed/"+e.ConnectionID+"/"+e.ExternalID); wakeErr != nil {
				continue
			}
		}
	}
	return nil
}

func (s *PgStore) wakeFrontendPlan(ctx context.Context, org, plan, change string) {
	if s.frontendWake == nil {
		return
	}
	rows, err := s.db.Query(ctx, `SELECT DISTINCT cloud_id FROM loomplane_frontend_uses WHERE org_id=$1 AND plan_id=$2`, org, plan)
	if err != nil {
		return
	}
	var clouds []string
	for rows.Next() {
		var cloud string
		if err = rows.Scan(&cloud); err != nil {
			rows.Close()
			return
		}
		clouds = append(clouds, cloud)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return
	}
	for _, cloud := range clouds {
		if wakeErr := s.frontendWake.WakeCapacityCloud(ctx, org, cloud, change); wakeErr != nil {
			continue
		}
	}
}
