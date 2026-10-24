package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/dispatch/durable"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/cloudinfra"
)

type DeploymentRun struct {
	OrgID, PlanID string
	Request       durable.StartRequest
}
type RunStore interface {
	Save(context.Context, DeploymentRun) (DeploymentRun, error)
	AcknowledgeStart(context.Context, string, string) error
	AttachPlan(context.Context, string, string, string) error
	ForPlan(context.Context, string) (DeploymentRun, error)
	ForDeployment(context.Context, string, string) (DeploymentRun, error)
}
type PgRunStore struct{ db *pgdriver.PgDB }

func NewPgRunStore(db *pgdriver.PgDB) *PgRunStore  { return &PgRunStore{db: db} }
func (d *Deployments) BindRunStore(store RunStore) { d.runs = store }
func (s *PgRunStore) Save(ctx context.Context, r DeploymentRun) (DeploymentRun, error) {
	raw, err := json.Marshal(r.Request)
	if err != nil {
		return r, err
	}
	var org, plan string
	var saved []byte
	err = s.db.QueryRow(ctx, `INSERT INTO loomplane_deployment_workflow_runs(org_id,plan_id,workflow_id,request) VALUES($1,$2,$3,$4) ON CONFLICT(workflow_id) DO UPDATE SET workflow_id=EXCLUDED.workflow_id RETURNING org_id,plan_id,request`, r.OrgID, r.PlanID, r.Request.WorkflowID, raw).Scan(&org, &plan, &saved)
	if err != nil {
		return r, err
	}
	if org != r.OrgID || plan != r.PlanID {
		return r, errors.New("workflow identity belongs to another deployment scope")
	}
	err = json.Unmarshal(saved, &r.Request)
	return r, err
}
func (s *PgRunStore) ForPlan(ctx context.Context, plan string) (DeploymentRun, error) {
	r := DeploymentRun{PlanID: plan}
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT r.org_id,r.request FROM loomplane_deployment_workflow_runs r WHERE r.plan_id=$1 OR EXISTS(SELECT 1 FROM loomplane_deployment_workflow_participants p WHERE p.org_id=r.org_id AND p.plan_id=$1 AND p.workflow_id=r.workflow_id) ORDER BY r.sequence DESC LIMIT 1`, plan).Scan(&r.OrgID, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, durable.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(raw, &r.Request)
	return r, err
}
func (s *PgRunStore) ForDeployment(ctx context.Context, org, dep string) (DeploymentRun, error) {
	var plan string
	if err := s.db.QueryRow(ctx, `SELECT plan_id FROM loomplane_deployments WHERE org_id=$1 AND id=$2`, org, dep).Scan(&plan); err != nil {
		return DeploymentRun{}, err
	}
	r, err := s.ForPlan(ctx, plan)
	if errors.Is(err, durable.ErrNotFound) {
		return DeploymentRun{OrgID: org, PlanID: plan, Request: durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: plan, RunID: runID}, BuildID: DeploymentBuild}}, nil
	}
	if err == nil && r.OrgID != org {
		return r, errors.New("workflow organization mismatch")
	}
	return r, err
}
func (d *Deployments) currentExecution(ctx context.Context, key durable.Key) (durable.Execution, error) {
	eng, err := d.engine()
	if err != nil {
		return durable.Execution{}, err
	}
	st, ok := eng.Dispatcher().Store().(durable.Store)
	if !ok {
		return durable.Execution{}, errors.New("durable store is unavailable")
	}
	for {
		e, readErr := st.GetExecution(ctx, key)
		if readErr != nil {
			return e, readErr
		}
		if e.NextRunID == "" {
			return e, nil
		}
		key.RunID = e.NextRunID
	}
}
func (d *Deployments) signalRun(ctx context.Context, r durable.SignalRequest) error {
	if r.BuildID == CapacityBuild {
		w, err := d.capacityWorker()
		if err != nil {
			return err
		}
		_, err = w.SignalExecution(ctx, r)
		return err
	}
	if r.BuildID != DeploymentBuild {
		return fmt.Errorf("unsupported workflow build %q", r.BuildID)
	}
	eng, err := d.engine()
	if err != nil {
		return err
	}
	_, err = eng.SignalDurableWorkflow(ctx, r)
	return err
}

func (d *Deployments) startRun(ctx context.Context, r DeploymentRun) error {
	if d.runs != nil {
		var err error
		r, err = d.runs.Save(ctx, r)
		if err != nil {
			return err
		}
	}
	if r.Request.BuildID == CapacityBuild {
		w, err := d.capacityWorker()
		if err != nil {
			return err
		}
		if _, err = w.StartExecution(ctx, r.Request); err != nil {
			return err
		}
	} else {
		eng, err := d.engine()
		if err != nil {
			return err
		}
		if _, err = eng.StartDurableWorkflow(ctx, r.Request); err != nil {
			return err
		}
	}
	if d.runs != nil {
		return d.runs.AcknowledgeStart(ctx, r.OrgID, r.Request.WorkflowID)
	}
	return nil
}

func (d *Deployments) planRun(ctx context.Context, plan string) (durable.Execution, error) {
	if d.runs != nil {
		r, err := d.runs.ForPlan(ctx, plan)
		if err == nil {
			return d.currentExecution(ctx, r.Request.Key)
		}
		if !errors.Is(err, durable.ErrNotFound) {
			return durable.Execution{}, err
		}
	}
	ids := []string{plan, "resume-release:" + plan, "resume-release-v2:" + plan}
	if st, ok := d.runs.(*PgRunStore); ok {
		var id string
		var retries int
		err := st.db.QueryRow(ctx, `SELECT id,retries FROM loomplane_deployments WHERE plan_id=$1`, plan).Scan(&id, &retries)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return durable.Execution{}, err
		}
		if retries > 0 {
			ids = append(ids, fmt.Sprintf("%s.retry.%d", id, retries))
		}
	}
	var latest durable.Execution
	found := false
	for _, id := range ids {
		e, err := d.currentExecution(ctx, durable.Key{Namespace: DurableNamespace, WorkflowID: id, RunID: runID})
		if errors.Is(err, durable.ErrNotFound) {
			continue
		}
		if err != nil {
			return e, err
		}
		if !found || e.State == durable.StateRunning || latest.State != durable.StateRunning && e.CreatedAt.After(latest.CreatedAt) {
			latest = e
			found = true
		}
	}
	if !found {
		return latest, durable.ErrNotFound
	}
	return latest, nil
}

// WakeCapacityDemand is a post-commit hint. The durable timer covers lost hints.
func (d *Deployments) WakeCapacityDemand(ctx context.Context, demand cloudinfra.CapacityDemand) error {
	st, ok := d.runs.(*PgRunStore)
	if !ok {
		return errors.New("capacity wake store is unavailable")
	}
	var dep string
	var revision int64
	err := st.db.QueryRow(ctx, `SELECT id,capacity_revision FROM loomplane_deployments WHERE org_id=$1 AND plan_id=$2 AND status='paused'`, demand.OrgID, demand.PlanID).Scan(&dep, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return d.SignalCapacity(ctx, demand.OrgID, dep, revision)
}

// RecoverCapacity acknowledges only wakes that Dispatch durably accepted.
func (d *Deployments) RecoverCapacity(ctx context.Context) error {
	return errors.Join(d.recoverStarts(ctx), d.recoverCapacityEvents(ctx))
}
func (d *Deployments) recoverCapacityEvents(ctx context.Context) error {
	st, ok := d.runs.(*PgRunStore)
	if !ok {
		return nil
	}
	rows, err := st.db.Query(ctx, `SELECT org_id,deployment_id,revision FROM loomplane_deployment_capacity_events WHERE NOT workflow_delivered ORDER BY created_at,org_id,deployment_id,revision LIMIT 100`)
	if err != nil {
		return err
	}
	type wake struct {
		org, dep string
		revision int64
	}
	var pending []wake
	for rows.Next() {
		var e wake
		if err = rows.Scan(&e.org, &e.dep, &e.revision); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, e := range pending {
		if err = d.SignalCapacity(ctx, e.org, e.dep, e.revision); err != nil {
			failures = append(failures, err)
			continue
		}
		if _, err = st.db.Exec(ctx, `UPDATE loomplane_deployment_capacity_events SET workflow_delivered=true WHERE org_id=$1 AND deployment_id=$2 AND revision=$3`, e.org, e.dep, e.revision); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *PgRunStore) AttachPlan(ctx context.Context, org, plan, workflow string) error {
	result, err := s.db.Exec(ctx, `INSERT INTO loomplane_deployment_workflow_participants(org_id,plan_id,workflow_id) SELECT $1,$2,$3 WHERE EXISTS(SELECT 1 FROM loomplane_deployment_workflow_runs WHERE org_id=$1 AND workflow_id=$3) AND EXISTS(SELECT 1 FROM loomplane_plans WHERE org_id=$1 AND id=$2) ON CONFLICT DO NOTHING`, org, plan, workflow)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		var exists bool
		err = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_deployment_workflow_participants WHERE org_id=$1 AND plan_id=$2 AND workflow_id=$3)`, org, plan, workflow).Scan(&exists)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("workflow participant scope mismatch")
		}
	}
	return nil
}

type recordedRecovery struct {
	releaseRecoveryEngine
	workflows *Deployments
	org, plan string
}

func (r recordedRecovery) StartDurableWorkflow(ctx context.Context, request durable.StartRequest) (durable.Receipt, error) {
	saved, err := r.workflows.runs.Save(ctx, DeploymentRun{OrgID: r.org, PlanID: r.plan, Request: request})
	if err != nil {
		return durable.Receipt{}, err
	}
	receipt, err := r.releaseRecoveryEngine.StartDurableWorkflow(ctx, saved.Request)
	if err != nil {
		return receipt, err
	}
	return receipt, r.workflows.runs.AcknowledgeStart(ctx, r.org, saved.Request.WorkflowID)
}

// WakeCapacityCloud lets policy, inventory, approval and slot owners wake waiters.
// change is the owner's stable event identity, such as policy revision or slot ID.
func (d *Deployments) WakeCapacityCloud(ctx context.Context, org, cloud, change string) error {
	st, ok := d.runs.(*PgRunStore)
	if !ok {
		return errors.New("capacity wake store is unavailable")
	}
	rows, err := st.db.Query(ctx, `SELECT d.id FROM loomplane_deployments d JOIN loomplane_plans p ON p.org_id=d.org_id AND p.id=d.plan_id WHERE d.org_id=$1 AND d.status='paused' AND EXISTS(SELECT 1 FROM jsonb_array_elements(p.result->'actions') a WHERE coalesce(a->'service'->'capacity_intent'->'demand'->>'cloud_id',a->'service'->'cloud_placement'->>'cloud_id',a->'resource'->'cloud_placement'->>'cloud_id')=$2)`, org, cloud)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		route, e := st.ForDeployment(ctx, org, id)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		execution, e := d.currentExecution(ctx, route.Request.Key)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if execution.State != durable.StateRunning {
			continue
		}
		hash := sha256.Sum256([]byte(org + "/" + cloud + "/" + id + "/" + change + "/" + execution.WorkflowID + "/" + execution.RunID))
		if e = d.signalRun(ctx, durable.SignalRequest{Key: execution.Key, BuildID: execution.BuildID, RequestID: fmt.Sprintf("capacity-change:%x", hash), Name: SignalCapacity, Input: []byte("{}")}); e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}

func (s *PgRunStore) AcknowledgeStart(ctx context.Context, org, workflow string) error {
	_, err := s.db.Exec(ctx, `UPDATE loomplane_deployment_workflow_runs SET start_delivered=true WHERE org_id=$1 AND workflow_id=$2`, org, workflow)
	return err
}
func (d *Deployments) recoverStarts(ctx context.Context) error {
	st, ok := d.runs.(*PgRunStore)
	if !ok {
		return nil
	}
	rows, err := st.db.Query(ctx, `SELECT org_id,plan_id,request FROM loomplane_deployment_workflow_runs WHERE NOT start_delivered ORDER BY start_attempted_at NULLS FIRST,sequence LIMIT 100`)
	if err != nil {
		return err
	}
	var pending []DeploymentRun
	for rows.Next() {
		var r DeploymentRun
		var raw []byte
		if err = rows.Scan(&r.OrgID, &r.PlanID, &raw); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(raw, &r.Request); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, r := range pending {
		// Rotate failed requests behind unattempted work so one outage cannot starve later starts.
		if _, err = st.db.Exec(ctx, `UPDATE loomplane_deployment_workflow_runs SET start_attempted_at=now() WHERE org_id=$1 AND workflow_id=$2`, r.OrgID, r.Request.WorkflowID); err != nil {
			failures = append(failures, err)
			continue
		}
		if err = d.startRun(ctx, r); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
