package orchestration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
)

type legacyCleanupRun struct {
	org, plan, workflow string
	cutoff              time.Time
	route               durable.StartRequest
	actions             map[string]bool
	routed, canary      bool
}

func (s *PgRunStore) pendingLegacyCleanup(ctx context.Context) ([]legacyCleanupRun, error) {
	rows, err := s.db.Query(ctx, `SELECT q.org_id,q.plan_id,q.workflow_id,q.request,q.detected_at,
 (SELECT jsonb_agg(i.action_id) FROM loomplane_managed_cleanup_intents i WHERE i.org_id=q.org_id AND i.plan_id=q.plan_id),
 EXISTS(SELECT 1 FROM jsonb_array_elements(p.result->'actions') a WHERE jsonb_array_length(coalesce(nullif(a->'routes','null'),'[]'))>0 AND EXISTS(SELECT 1 FROM loomplane_managed_cleanup_intents i WHERE i.org_id=q.org_id AND i.plan_id=q.plan_id AND i.action_id=a->>'id')),
 EXISTS(SELECT 1 FROM loomplane_deployments d WHERE d.org_id=q.org_id AND d.plan_id=q.plan_id AND EXISTS(SELECT 1 FROM jsonb_array_elements(coalesce(nullif(d.canary->'services','null'),'[]')) s JOIN loomplane_managed_cleanup_intents i ON i.org_id=q.org_id AND i.plan_id=q.plan_id AND i.action_id=s->>'action_id'))
 FROM loomplane_legacy_cleanup_runs q JOIN loomplane_plans p ON p.org_id=q.org_id AND p.id=q.plan_id WHERE q.state='pending' ORDER BY q.org_id,q.plan_id,q.workflow_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []legacyCleanupRun
	for rows.Next() {
		var r legacyCleanupRun
		var raw, ids []byte
		if readErr := rows.Scan(&r.org, &r.plan, &r.workflow, &raw, &r.cutoff, &ids, &r.routed, &r.canary); readErr != nil {
			return nil, readErr
		}
		var actions []string
		if readErr := json.Unmarshal(raw, &r.route); readErr != nil {
			return nil, readErr
		}
		if readErr := json.Unmarshal(ids, &actions); readErr != nil {
			return nil, readErr
		}
		r.actions = map[string]bool{}
		for _, a := range actions {
			r.actions[a] = true
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func (r legacyCleanupRun) affected(command drt.Command) (bool, error) {
	if command.Kind != durable.TaskActivity {
		return false, nil
	}
	switch command.Name {
	case ActivityDeployAction:
		var in struct {
			ActionID string `json:"action_id"`
		}
		if err := json.Unmarshal(command.Input, &in); err != nil || in.ActionID == "" {
			return false, errors.New("invalid historical deployment activity")
		}
		return r.actions[in.ActionID], nil
	case ActivityDeployFinish:
		return r.routed, nil
	case ActivityCanaryPromote:
		return r.canary, nil
	case cloudSyncActivity:
		return true, nil
	default:
		return false, nil
	}
}

// A cancellation request alone does not fence an activity. Read the terminal
// run and its retained task attempts after the worker's cancellation fence.
// Claims racing that fence remain visible even if no HTTP response was saved.
func (r legacyCleanupRun) inspect(ctx context.Context, st durable.Store, e durable.Execution) (status, description string, readError error) {
	if e.BuildID != LegacyCleanupBuild || e.WorkflowID != r.workflow || e.Namespace != DurableNamespace {
		return "uncertain", "original workflow identity changed", nil
	}
	var scheduled []drt.Command
	known := map[string]bool{}
	var fenced, ack, previouslyClosed bool
	for cursor := int64(0); cursor < e.LastSequence; {
		page, err := st.ReadHistory(ctx, e.Key, cursor, 256)
		if err != nil {
			return "", "", err
		}
		if len(page) == 0 {
			return "uncertain", "incomplete historical workflow", nil
		}
		for _, event := range page {
			if event.Sequence != cursor+1 {
				return "uncertain", "historical workflow sequence has a gap", nil
			}
			cursor = event.Sequence
			switch event.Type {
			case drt.EventCommandScheduled:
				var c drt.Command
				if err := json.Unmarshal(event.Payload, &c); err != nil {
					return "", "", err
				}
				relevant, err := r.affected(c)
				if err != nil {
					return "", "", err
				}
				if relevant {
					scheduled = append(scheduled, c)
				}
			case drt.EventActivityCompleted:
				var o drt.Outcome
				if err := json.Unmarshal(event.Payload, &o); err != nil {
					return "", "", err
				}
				if event.Time.Before(r.cutoff) && o.Failure == nil && o.Timeout == "" {
					known[o.CommandID] = true
				}
			case drt.EventCancellationStarted:
				fenced = true
			case drt.EventWorkflowCancelled:
				ack = true
				previouslyClosed = event.Time.Before(r.cutoff)
			case drt.EventWorkflowCompleted, drt.EventWorkflowFailed:
				previouslyClosed = event.Time.Before(r.cutoff)
			}
		}
	}
	if !previouslyClosed && (!fenced || !ack) {
		return "uncertain", "original execution did not acknowledge its cancellation fence", nil
	}
	for _, c := range scheduled {
		t, err := st.GetTask(ctx, e.Key, fmt.Sprintf("command:%d", c.Index))
		if errors.Is(err, durable.ErrNotFound) {
			return "uncertain", "historical cleanup task is unavailable", nil
		}
		if err != nil {
			return "", "", err
		}
		var payload struct {
			Version int
			Command drt.Command
		}
		if json.Unmarshal(t.Payload, &payload) != nil || payload.Version != 1 || !reflect.DeepEqual(c, payload.Command) || !t.Done || t.Key != e.Key || t.Kind != durable.TaskActivity {
			return "uncertain", "historical cleanup task identity or completion is unknown", nil
		}
		if t.Attempt > 1 || t.Attempt > 0 && !known[c.ID] {
			return "uncertain", "a historical cleanup activity may have sent a request", nil
		}
	}
	return "drained", "original execution and its task claims were checked", nil
}

// ReconcileHistoricalCleanup cancels only saved pre-upgrade managed cleanup runs.
func (d *Deployments) ReconcileHistoricalCleanup(ctx context.Context) error {
	s, ok := d.runs.(*PgRunStore)
	if !ok {
		return nil
	}
	runs, err := s.pendingLegacyCleanup(ctx)
	if err != nil || len(runs) == 0 {
		return err
	}
	eng, err := d.engine()
	if err != nil {
		return err
	}
	st, ok := eng.Dispatcher().Store().(durable.Store)
	if !ok {
		return errors.New("historical cleanup workflow storage is unavailable")
	}
	var failures []error
	for _, r := range runs {
		state, detail, readErr := r.reconcile(ctx, st)
		if readErr != nil {
			failures = append(failures, readErr)
			continue
		}
		if state == "pending" {
			continue
		}
		if _, writeErr := s.db.Exec(ctx, `UPDATE loomplane_legacy_cleanup_runs q SET state=$4,detail=$5 WHERE org_id=$1 AND plan_id=$2 AND workflow_id=$3 AND state='pending'
 AND ($4<>'drained' OR NOT EXISTS(SELECT 1 FROM loomplane_managed_cleanup_intents i CROSS JOIN LATERAL jsonb_array_elements(i.body) t JOIN loomplane_managed_workload_issuances w ON w.org_id=i.org_id AND w.release_id=t->'scope'->>'Release' WHERE i.org_id=q.org_id AND i.plan_id=q.plan_id AND w.outcome IS NULL))`, r.org, r.plan, r.workflow, state, detail); writeErr != nil {
			failures = append(failures, writeErr)
		}
	}
	return errors.Join(failures...)
}

func (r legacyCleanupRun) reconcile(ctx context.Context, st durable.Store) (status, description string, readError error) {
	if r.route.BuildID != LegacyCleanupBuild || r.route.Namespace != DurableNamespace || r.route.WorkflowID != r.workflow || r.route.RunID == "" {
		return "uncertain", "original cleanup route is incomplete", nil
	}
	switch r.route.WorkflowType {
	case WorkflowDeployment, WorkflowRetry, cloudSyncWorkflow, resumeReleaseWorkflow:
	default:
		return "uncertain", "original workflow does not execute reviewed deployment cleanup", nil
	}
	key := r.route.Key
	seen := map[string]bool{}
	uncertain := ""
	previous := ""
	for {
		if seen[key.RunID] {
			return "uncertain", "invalid historical continuation chain", nil
		}
		seen[key.RunID] = true
		e, err := st.GetExecution(ctx, key)
		if errors.Is(err, durable.ErrNotFound) {
			return "uncertain", "original execution is unavailable", nil
		}
		if err != nil {
			return "", "", err
		}
		if e.BuildID != LegacyCleanupBuild || e.Key != key || e.WorkflowType != r.route.WorkflowType || e.FirstRunID != r.route.RunID || e.PreviousRunID != previous || previous == "" && !bytes.Equal(e.Input, r.route.Input) {
			return "uncertain", "original execution identity changed", nil
		}
		if e.State == durable.StateRunning {
			hash := sha256.Sum256([]byte(r.org + "\n" + key.WorkflowID + "\n" + key.RunID))
			_, cancelErr := st.RequestCancelExecution(ctx, durable.CancelExecutionRequest{Key: key, BuildID: e.BuildID, RequestID: "legacy-cleanup:" + hex.EncodeToString(hash[:]), Reason: "Managed cleanup needs a fresh execution review."})
			if errors.Is(cancelErr, durable.ErrClosed) {
				return "pending", "", nil
			}
			return "pending", "", cancelErr
		}
		state, detail, readErr := r.inspect(ctx, st, e)
		if readErr != nil {
			return "", "", readErr
		}
		if state == "uncertain" {
			uncertain = detail
		}
		if e.NextRunID == "" {
			if uncertain != "" {
				return "uncertain", uncertain, nil
			}
			return state, detail, nil
		}
		previous, key.RunID = key.RunID, e.NextRunID
	}
}
