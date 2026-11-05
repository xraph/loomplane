package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
)

// Capacity consumers acknowledge work independently.
const (
	CapacityConsumerWorkflow     = "workflow"
	CapacityConsumerNotification = "notification"
)

// CapacityTransition is an immutable outbox record. Each consumer acknowledges
// its own durable processing only after it succeeds. Notification consumers
// enqueue their deduplicated outbox before acknowledging; workflow consumers
// send an idempotent wake before acknowledging and retain a timer fallback.
// Pauses carry all blockers, including updates while the deployment stays paused.
// Workflow delivery wakes the same deployment run, never an ordinary retry.
type CapacityTransition struct {
	OrgID        string        `json:"org_id"`
	DeploymentID string        `json:"deployment_id"`
	PlanID       string        `json:"plan_id"`
	ProjectID    string        `json:"project_id"`
	Revision     int64         `json:"revision"`
	Status       string        `json:"status"`
	Wait         *CapacityWait `json:"wait,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
}

// CapacityStore atomically persists state, transition events and wakeup work.
// Revisions are monotonic across pause/resume cycles, preventing stale commands.
type CapacityStore interface {
	SetCapacityWait(context.Context, string, string, int64, []cloudinfra.CapacityBlock, time.Time) (*Deployment, error)
	TransitionCapacityWait(context.Context, string, string, int64, string, time.Time) (*Deployment, error)
	ListCapacityTransitions(context.Context, string, string, int) ([]CapacityTransition, error)
	AcknowledgeCapacityTransition(context.Context, string, string, int64, string) error
}

var _ CapacityStore = (*PgStore)(nil)

func applyCapacityTransition(d *Deployment, expected int64, status string, blocks []cloudinfra.CapacityBlock, at time.Time) (*CapacityTransition, error) {
	if d.CapacityRevision != expected || (d.Status != StatusRunning && d.Status != StatusPaused) {
		return nil, ErrCapacityWaitConflict
	}
	if status == StatusPaused {
		blocks = canonicalBlocks(blocks)
		if len(blocks) == 0 {
			return nil, errors.New("capacity pause requires blockers")
		}
		if d.Status == StatusPaused && d.CapacityWait != nil && slices.Equal(d.CapacityWait.Blocks, blocks) {
			return nil, nil
		}
	} else if d.Status != StatusPaused || (status != StatusRunning && status != StatusCancelled) {
		return nil, ErrCapacityWaitConflict
	}
	d.CapacityRevision++
	previous := d.CapacityWait
	d.CapacityWait = nil
	d.Status = status
	d.FinishedAt = nil
	if status == StatusPaused {
		d.CapacityWait = &CapacityWait{Revision: d.CapacityRevision, Blocks: blocks, CreatedAt: at.UTC()}
	}
	if status == StatusCancelled {
		finished := at.UTC()
		d.FinishedAt = &finished
	}
	event := &CapacityTransition{OrgID: d.OrgID, DeploymentID: d.ID, PlanID: d.PlanID, ProjectID: d.ProjectID, Revision: d.CapacityRevision, Status: status, Wait: d.CapacityWait, CreatedAt: at.UTC()}
	if event.Wait == nil {
		event.Wait = previous
	}
	return event, nil
}

// requestCancel records a cancel of a running deployment. It isn't cancelled
// yet: the action that's running may be partway through a provider's change,
// and fencing it there would strand that change. The deployment is cancelled
// as its next action starts (stopDeployment), so it needs one that hasn't.
// A request already made stays as it was.
func requestCancel(d *Deployment, at time.Time) error {
	if d.Status != StatusRunning {
		return ErrNotRunning
	}
	if d.CancelRequestedAt != nil {
		return nil
	}
	if !slices.ContainsFunc(d.Steps, unclaimed) {
		return ErrNothingToCancel
	}
	requested := at.UTC()
	d.CancelRequestedAt = &requested
	return nil
}

// unclaimed reports whether a step's action hasn't started: no attempt has
// claimed it (ClaimStep), recorded a start, or left a checkpoint.
func unclaimed(s Step) bool {
	return s.Status == StepPending && !s.Claimed && s.StartedAt == nil && len(s.Checkpoint) == 0
}

// claimStep is ClaimStep on the deployment as read under its row lock. It
// marks the step started and returns false, or returns true, writing
// nothing, when a cancel was requested before the step started. A step
// already claimed, or settled, comes back false: its action runs as it
// would have.
func claimStep(d *Deployment, actionID string) (stop, changed bool, err error) {
	if d.Status != StatusRunning {
		return false, false, nil
	}
	step := d.Step(actionID)
	if step == nil {
		return false, false, fmt.Errorf("%w: deployment %s has no step %s", ErrNotFound, d.ID, actionID)
	}
	if !unclaimed(*step) {
		return false, false, nil
	}
	if d.CancelRequestedAt != nil {
		return true, false, nil
	}
	step.Claimed = true
	return false, true, nil
}

// ClaimStep implements StepClaimer. It holds the row lock CancelDeployment
// takes, so a request and an action's start can't pass each other.
func (s *PgStore) ClaimStep(ctx context.Context, org, id, actionID string) (stop bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { err = rollback(tx, err) }()
	d, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_deployments WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	stop, changed, err := claimStep(d, actionID)
	if err != nil {
		return false, err
	}
	if !changed {
		return stop, tx.Commit()
	}
	raw, err := encodeSteps(d.Steps)
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_deployments SET steps=$3 WHERE org_id=$1 AND id=$2`, org, id, raw); err != nil {
		return false, err
	}
	return false, tx.Commit()
}

// stopDeployment cancels a paused deployment, or a running one whose cancel
// was requested, which only happens between its actions. One already
// cancelled stays as it is.
func stopDeployment(d *Deployment, at time.Time) (*CapacityTransition, error) {
	switch {
	case d.Status == StatusCancelled:
		return nil, nil
	case d.Status == StatusPaused:
		return applyCapacityTransition(d, d.CapacityRevision, StatusCancelled, nil, at)
	case d.Status != StatusRunning || d.CancelRequestedAt == nil:
		return nil, ErrCapacityWaitConflict
	}
	d.CapacityRevision++
	finished := at.UTC()
	d.Status, d.FinishedAt = StatusCancelled, &finished
	return &CapacityTransition{OrgID: d.OrgID, DeploymentID: d.ID, PlanID: d.PlanID, ProjectID: d.ProjectID, Revision: d.CapacityRevision, Status: StatusCancelled, CreatedAt: finished}, nil
}

// SetCapacityWait implements CapacityStore.
func (s *PgStore) SetCapacityWait(ctx context.Context, org, id string, expected int64, blocks []cloudinfra.CapacityBlock, at time.Time) (*Deployment, error) {
	return s.capacityTransition(ctx, org, id, StatusPaused, func(d *Deployment) (*CapacityTransition, error) {
		return applyCapacityTransition(d, expected, StatusPaused, blocks, at)
	})
}

// TransitionCapacityWait implements CapacityStore.
func (s *PgStore) TransitionCapacityWait(ctx context.Context, org, id string, expected int64, status string, at time.Time) (*Deployment, error) {
	if status != StatusRunning && status != StatusCancelled {
		return nil, ErrCapacityWaitConflict
	}
	return s.capacityTransition(ctx, org, id, status, func(d *Deployment) (*CapacityTransition, error) {
		return applyCapacityTransition(d, expected, status, nil, at)
	})
}

// CancelDeployment implements Canceller. A paused deployment has no action
// in flight, so it's cancelled at once, as a capacity cancel would. A running
// one records the request, and Run cancels it before its next action.
func (s *PgStore) CancelDeployment(ctx context.Context, org, id string, at time.Time) (out *Deployment, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = rollback(tx, err) }()
	// Finish, Reopen and capacity transitions take this lock before the row.
	if lockErr := cloudprofiles.LockOrganization(ctx, tx, org); lockErr != nil {
		return nil, lockErr
	}
	d, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_deployments WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	switch d.Status {
	case StatusPaused:
		if rerr := tx.Rollback(); rerr != nil {
			return nil, rerr
		}
		return s.StopDeployment(ctx, org, id, at)
	case StatusCancelled:
		return d, tx.Commit()
	}
	requested := d.CancelRequestedAt
	if requestErr := requestCancel(d, at); requestErr != nil {
		return nil, requestErr
	}
	if requested == nil {
		if readErr := tx.QueryRow(ctx, `UPDATE loomplane_deployments SET cancel_requested_at=$3 WHERE org_id=$1 AND id=$2 RETURNING cancel_requested_at`, org, id, d.CancelRequestedAt).Scan(d.CancelRequestedAt); readErr != nil {
			return nil, readErr
		}
	}
	return d, tx.Commit()
}

// StopDeployment implements Stopper, in the same transaction as a capacity
// cancel: capacity the deployment hasn't started is released, its demands
// are detached, and none of its actions start again.
func (s *PgStore) StopDeployment(ctx context.Context, org, id string, at time.Time) (*Deployment, error) {
	return s.capacityTransition(ctx, org, id, StatusCancelled, func(d *Deployment) (*CapacityTransition, error) {
		return stopDeployment(d, at)
	})
}

// capacityTransition moves a deployment to status with apply, which checks
// the move and returns its event, or nil when there's nothing to write.
func (s *PgStore) capacityTransition(ctx context.Context, org, id, status string, apply func(*Deployment) (*CapacityTransition, error)) (out *Deployment, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = rollback(tx, err) }()
	// Admission, demand attachment and cancellation use this same first lock.
	if lockErr := cloudprofiles.LockOrganization(ctx, tx, org); lockErr != nil {
		return nil, lockErr
	}
	// Step writes lock pool/workload receipts before the deployment row.
	// Fence in that order; stale CAS or event failures roll the whole change back.
	if status == StatusCancelled {
		var plan, environment string
		readErr := tx.QueryRow(ctx, `SELECT plan_id,environment_id FROM loomplane_deployments WHERE org_id=$1 AND id=$2`, org, id).Scan(&plan, &environment)
		if errors.Is(readErr, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if readErr != nil {
			return nil, readErr
		}
		if fenceErr := cloudinfra.CancelUnstartedCapacity(ctx, tx, org, plan, id, environment); fenceErr != nil {
			return nil, fenceErr
		}
	}
	d, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_deployments WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	event, err := apply(d)
	if err != nil {
		return nil, err
	}
	if event != nil {
		var wait []byte
		if d.CapacityWait != nil {
			wait, err = json.Marshal(d.CapacityWait)
			if err != nil {
				return nil, err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE loomplane_deployments SET status=$3,capacity_revision=$4,capacity_wait=$5,finished_at=$6 WHERE org_id=$1 AND id=$2`, org, id, d.Status, d.CapacityRevision, wait, d.FinishedAt); err != nil {
			return nil, err
		}
		raw, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if _, err = tx.Exec(ctx, `INSERT INTO loomplane_deployment_capacity_events(org_id,deployment_id,revision,body,created_at) VALUES($1,$2,$3,$4,$5)`, org, id, event.Revision, raw, event.CreatedAt); err != nil {
			return nil, err
		}
		if status == StatusCancelled {
			if _, err = tx.Exec(ctx, `DELETE FROM loomplane_cloud_expansion_demands WHERE org_id=$1 AND plan_id=$2`, org, d.PlanID); err != nil {
				return nil, err
			}
		}
	}
	var frontendClouds []string
	if status == StatusCancelled && s.frontendWake != nil {
		rows, queryErr := tx.Query(ctx, `SELECT DISTINCT cloud_id FROM loomplane_frontend_uses WHERE org_id=$1 AND plan_id=$2`, org, d.PlanID)
		if queryErr != nil {
			return nil, queryErr
		}
		for rows.Next() {
			var cloud string
			if scanErr := rows.Scan(&cloud); scanErr != nil {
				rows.Close()
				return nil, scanErr
			}
			frontendClouds = append(frontendClouds, cloud)
		}
		queryErr = rows.Err()
		rows.Close()
		if queryErr != nil {
			return nil, queryErr
		}
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, commitErr
	}
	for _, cloud := range frontendClouds {
		if wakeErr := s.frontendWake.WakeCapacityCloud(ctx, org, cloud, fmt.Sprintf("frontend-cancel/%s/%d", id, d.CapacityRevision)); wakeErr != nil {
			continue
		}
	}
	return d, nil
}
func capacityConsumerColumn(consumer string) (string, error) {
	switch consumer {
	case CapacityConsumerWorkflow:
		return "workflow_delivered", nil
	case CapacityConsumerNotification:
		return "notification_delivered", nil
	default:
		return "", fmt.Errorf("unknown capacity consumer %q", consumer)
	}
}

// ListCapacityTransitions lists unacknowledged work in commit-independent order.
// Consumers must not use a global sequence cursor: transactions can commit late.
func (s *PgStore) ListCapacityTransitions(ctx context.Context, org, consumer string, limit int) ([]CapacityTransition, error) {
	column, err := capacityConsumerColumn(consumer)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `SELECT body FROM loomplane_deployment_capacity_events WHERE org_id=$1 AND NOT `+column+` ORDER BY created_at,deployment_id,revision LIMIT $2`, org, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CapacityTransition{}
	for rows.Next() {
		var raw []byte
		if scanErr := rows.Scan(&raw); scanErr != nil {
			return nil, scanErr
		}
		var event CapacityTransition
		if decodeErr := json.Unmarshal(raw, &event); decodeErr != nil {
			return nil, decodeErr
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

// AcknowledgeCapacityTransition records one consumer's durable processing.
func (s *PgStore) AcknowledgeCapacityTransition(ctx context.Context, org, id string, revision int64, consumer string) error {
	column, err := capacityConsumerColumn(consumer)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE loomplane_deployment_capacity_events SET `+column+`=true WHERE org_id=$1 AND deployment_id=$2 AND revision=$3`, org, id, revision)
	return err
}
