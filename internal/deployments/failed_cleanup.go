package deployments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// ErrCleanupConflict asks you to review the current failed deployment again.
var ErrCleanupConflict = errors.New("cleanup requires a current review of one unpublished failed Cloud SQL create")

// CleanupReview describes the exact failed attempt you can abandon.
type CleanupReview struct {
	Blockers         []string                        `json:"blockers,omitempty"`
	PreviousReviewID string                          `json:"previous_review_id,omitempty"`
	Variant          string                          `json:"variant,omitempty"`
	ReviewSequence   int64                           `json:"review_sequence,omitempty"`
	Targets          []planning.ManagedCleanupTarget `json:"targets,omitempty"`
	DeploymentID     string                          `json:"deployment_id"`
	Revision         string                          `json:"revision"`
	Resource         string                          `json:"resource"`
	Provider         string                          `json:"provider"`
	Region           string                          `json:"region"`
	ConnectionID     string                          `json:"connection_id"`
	Effect           string                          `json:"effect"`
}

// FailedCleanup keeps deletion progress separate from the failed create evidence.
type FailedCleanup struct {
	Variant        string             `json:"variant,omitempty"`
	ReviewID       string             `json:"review_id,omitempty"`
	ReviewSequence int64              `json:"review_sequence,omitempty"`
	DeploymentID   string             `json:"deployment_id"`
	Revision       string             `json:"revision"`
	Status         string             `json:"status"`
	Attempt        int                `json:"attempt"`
	Error          string             `json:"error,omitempty"`
	Actor          security.Principal `json:"-"`
	Checkpoint     []byte             `json:"-"`
}

// CleanupStarter delivers an accepted cleanup to the durable worker.
type CleanupStarter interface {
	StartFailedCleanup(context.Context, string, string, int) error
}

// FailedCleanups reviews and executes abandonment of a single failed SQL create.
type FailedCleanups struct {
	runner        *Runner
	store         *PgStore
	start         CleanupStarter
	managed       *planning.ManagedCleanupStore
	managedEngine ManagedFailedCleanupEngine
}

func NewFailedCleanups(r *Runner, store *PgStore, start CleanupStarter) *FailedCleanups {
	return &FailedCleanups{runner: r, store: store, start: start}
}

func (s *FailedCleanups) candidate(ctx context.Context, org, id string, accepted bool) (*Deployment, *planning.Plan, planning.Action, string, error) {
	d, err := s.store.GetDeployment(ctx, org, id)
	if err != nil {
		return nil, nil, planning.Action{}, "", err
	}
	plan, err := s.runner.plans.GetPlan(ctx, org, d.PlanID)
	if err != nil {
		return nil, nil, planning.Action{}, "", err
	}
	status := StatusFailed
	if accepted {
		status = StatusCancelled
	}
	if d.Status != status || plan.Status != planning.StatusApproved || !plan.Catalog || plan.CloudSyncID != "" || len(plan.Actions) != 1 || len(d.Steps) != 1 {
		return nil, nil, planning.Action{}, "", ErrCleanupConflict
	}
	a := plan.Actions[0]
	step := d.Step(a.ID)
	if a.Subject != planning.ResourceSubject || a.Provider != "cloud-sql" || a.Op != planning.Create || a.Resource == nil || step == nil || step.Status == StepSucceeded || step.Replaced != nil || len(step.Checkpoint) == 0 {
		return nil, nil, planning.Action{}, "", ErrCleanupConflict
	}
	bindings, err := s.runner.plans.ListBindings(ctx, org, d.EnvironmentID)
	if err != nil {
		return nil, nil, a, "", err
	}
	for _, b := range bindings {
		if b.Subject == a.Subject && b.Name == a.Name {
			return nil, nil, a, "", ErrCleanupConflict
		}
	}
	// Neither credentials nor raw provider evidence leave this API. The digest
	// includes the original checkpoint so a retry invalidates an old review.
	raw, err := json.Marshal(struct {
		Plan       *planning.Plan
		Deployment string
		Retries    int
		Checkpoint []byte
	}{plan, d.ID, d.Retries, step.Checkpoint})
	if err != nil {
		return nil, nil, a, "", err
	}
	sum := sha256.Sum256(raw)
	return d, plan, a, hex.EncodeToString(sum[:]), nil
}

func (s *FailedCleanups) Preview(ctx context.Context, p security.Principal, id string) (*CleanupReview, error) {
	if err := s.runner.authz.Require(ctx, p, security.DeploymentApprove, ""); err != nil {
		return nil, err
	}
	if s.managed != nil {
		managed, err := s.managed.HasFailedCleanupEvidence(ctx, p.OrgID, id)
		if err != nil {
			return nil, err
		}
		if managed {
			r, previewErr := s.managed.PreviewFailedCleanup(ctx, p, id)
			if previewErr != nil {
				return nil, previewErr
			}
			return managedCleanupPreview(r), nil
		}
	}

	_, plan, a, revision, err := s.candidate(ctx, p.OrgID, id, false)
	if err != nil {
		return nil, err
	}
	return &CleanupReview{DeploymentID: id, Revision: revision, Resource: a.Name, Provider: a.Provider, Region: a.Region, ConnectionID: cleanupConnection(plan, a), Effect: "Permanently stop deployment retries and delete its unpublished SQL instance and owned private endpoint. Request a final backup retained for 30 days. If the instance is already missing, no final backup can be created. Unknown ownership blocks cleanup."}, nil
}

func (s *FailedCleanups) load(ctx context.Context, org, id string) (*FailedCleanup, error) {
	if s.managed != nil {
		r, err := s.managed.CurrentFailedCleanup(ctx, org, id)
		if err == nil {
			return managedCleanupResult(r), nil
		}
		if !planning.IsManagedFailedCleanupMissing(err) {
			return nil, err
		}
	}
	var c FailedCleanup
	var actor []byte
	err := s.store.db.QueryRow(ctx, `SELECT deployment_id,revision,status,attempt,error,actor,checkpoint FROM loomplane_failed_cleanups WHERE org_id=$1 AND deployment_id=$2`, org, id).Scan(&c.DeploymentID, &c.Revision, &c.Status, &c.Attempt, &c.Error, &actor, &c.Checkpoint)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if decodeErr := json.Unmarshal(actor, &c.Actor); decodeErr != nil {
		return nil, decodeErr
	}
	return &c, nil
}

func (s *FailedCleanups) Get(ctx context.Context, p security.Principal, id string) (*FailedCleanup, error) {
	if err := s.runner.authz.Require(ctx, p, security.DeploymentRead, ""); err != nil {
		return nil, err
	}
	return s.load(ctx, p.OrgID, id)
}

func (s *FailedCleanups) Apply(ctx context.Context, p security.Principal, id, revision string) (*FailedCleanup, error) {
	if err := s.runner.authz.Require(ctx, p, security.DeploymentApprove, ""); err != nil {
		return nil, err
	}
	if revision == "" {
		return nil, ErrCleanupConflict
	}
	if s.managed != nil {
		managed, err := s.managed.HasFailedCleanupEvidence(ctx, p.OrgID, id)
		if err != nil {
			return nil, err
		}
		if managed {
			r, acceptErr := s.managed.AcceptFailedCleanup(ctx, p, id, revision)
			if acceptErr != nil {
				return nil, acceptErr
			}
			if r.Status == "pending" {
				if deliveryErr := s.deliverManaged(ctx, r); deliveryErr != nil {
					return nil, deliveryErr
				}
			}
			return managedCleanupResult(r), nil
		}
	}

	d, err := s.store.GetDeployment(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	plan, err := s.runner.plans.GetPlan(ctx, p.OrgID, d.PlanID)
	if err != nil {
		return nil, err
	}
	if len(plan.Actions) != 1 {
		return nil, ErrCleanupConflict
	}
	err = s.runner.withSQLActionLock(ctx, d, plan.Actions[0], func(work context.Context) error {
		existing, loadErr := s.load(work, p.OrgID, id)
		if loadErr == nil {
			if existing.Revision != revision {
				return ErrCleanupConflict
			}
			return nil
		}
		if !errors.Is(loadErr, ErrNotFound) {
			return loadErr
		}
		current, _, _, got, readErr := s.candidate(work, p.OrgID, id, false)
		if readErr != nil {
			return readErr
		}
		if got != revision {
			return ErrCleanupConflict
		}
		actor, marshalErr := json.Marshal(p)
		if marshalErr != nil {
			return marshalErr
		}
		tx, beginErr := s.store.db.BeginTx(work, nil)
		if beginErr != nil {
			return beginErr
		}
		defer func() { _ = tx.Rollback() }() //nolint:errcheck // transaction cleanup after commit or connection loss
		result, updateErr := tx.Exec(work, `UPDATE loomplane_deployments SET status='cancelled' WHERE org_id=$1 AND id=$2 AND status='failed' AND retries=$3`, p.OrgID, id, current.Retries)
		if updateErr != nil {
			return updateErr
		}
		if n, e := result.RowsAffected(); e != nil {
			return e
		} else if n != 1 {
			return ErrCleanupConflict
		}
		_, insertErr := tx.Exec(work, `INSERT INTO loomplane_failed_cleanups(org_id,deployment_id,revision,actor,checkpoint) VALUES($1,$2,$3,$4,$5)`, p.OrgID, id, revision, actor, current.Steps[0].Checkpoint)
		if insertErr != nil {
			return insertErr
		}
		return tx.Commit()
	})
	if err != nil {
		return nil, err
	}
	c, err := s.load(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if c.Status == "pending" {
		if deliveryErr := s.deliver(ctx, p.OrgID, id, c.Attempt); deliveryErr != nil {
			return nil, deliveryErr
		}
	}
	return c, nil
}

func (s *FailedCleanups) Retry(ctx context.Context, p security.Principal, id string) (*FailedCleanup, error) {
	if err := s.runner.authz.Require(ctx, p, security.DeploymentApprove, ""); err != nil {
		return nil, err
	}
	if s.managed != nil {
		_, err := s.managed.CurrentFailedCleanup(ctx, p.OrgID, id)
		if err == nil {
			r, retryErr := s.managed.RetryFailedCleanup(ctx, p, id)
			if retryErr != nil {
				return nil, retryErr
			}
			if deliveryErr := s.deliverManaged(ctx, r); deliveryErr != nil {
				return nil, deliveryErr
			}
			return managedCleanupResult(r), nil
		}
		if !planning.IsManagedFailedCleanupMissing(err) {
			return nil, err
		}
	}
	result, err := s.store.db.Exec(ctx, `UPDATE loomplane_failed_cleanups SET status='pending',attempt=attempt+1,error='',updated_at=now() WHERE org_id=$1 AND deployment_id=$2 AND status='failed'`, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if n, e := result.RowsAffected(); e != nil {
		return nil, e
	} else if n != 1 {
		return nil, ErrCleanupConflict
	}
	c, err := s.load(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if deliveryErr := s.deliver(ctx, p.OrgID, id, c.Attempt); deliveryErr != nil {
		return nil, deliveryErr
	}
	return c, nil
}

// RecoverFailedCleanups repairs the accept-to-enqueue gap after a process restart.
func (s *FailedCleanups) RecoverFailedCleanups(ctx context.Context) error {
	if err := s.recoverManaged(ctx); err != nil {
		return err
	}
	rows, err := s.store.db.Query(ctx, `SELECT org_id,deployment_id,attempt FROM loomplane_failed_cleanups WHERE status='pending' AND delivered_attempt<attempt ORDER BY updated_at LIMIT 100`)
	if err != nil {
		return err
	}
	type pending struct {
		org, id string
		attempt int
	}
	var list []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.org, &p.id, &p.attempt); err != nil {
			rows.Close()
			return err
		}
		list = append(list, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range list {
		if deliveryErr := s.deliver(ctx, p.org, p.id, p.attempt); deliveryErr != nil {
			return deliveryErr
		}
	}
	return nil
}

// RunFailedCleanup replays only the accepted deletion, never its creation stages.
func (s *FailedCleanups) RunFailedCleanup(ctx context.Context, org, id string, attempt int, heartbeat func([]byte) error) error {
	if s.managed != nil {
		_, err := s.managed.CurrentFailedCleanup(ctx, org, id)
		if err == nil {
			return ErrManagedCleanupProtocol
		}
		if !planning.IsManagedFailedCleanupMissing(err) {
			return err
		}
	}
	if heartbeat == nil {
		return errors.New("cleanup requires an activity heartbeat")
	}
	ctx, heartbeat, stop := keepHeartbeat(ctx, nil, heartbeat, 30*time.Second)
	defer stop()
	d, err := s.store.GetDeployment(ctx, org, id)
	if err != nil {
		return err
	}
	plan, err := s.runner.plans.GetPlan(ctx, org, d.PlanID)
	if err != nil {
		return err
	}
	if len(plan.Actions) != 1 {
		return ErrCleanupConflict
	}
	return s.runner.withSQLActionLock(ctx, d, plan.Actions[0], func(work context.Context) error {
		c, loadErr := s.load(work, org, id)
		if loadErr != nil {
			return loadErr
		}
		if c.Attempt != attempt || c.Status != "pending" {
			return nil
		}
		runErr := s.execute(work, c, heartbeat)
		if work.Err() != nil {
			return errors.Join(runErr, work.Err())
		}
		if beatErr := heartbeat(nil); beatErr != nil {
			return beatErr
		}
		state, message := "succeeded", ""
		if runErr != nil {
			state, message = "failed", runErr.Error()
		}
		_, saveErr := s.store.db.Exec(work, `UPDATE loomplane_failed_cleanups SET status=$4,error=$5,updated_at=now() WHERE org_id=$1 AND deployment_id=$2 AND attempt=$3 AND status='pending'`, org, id, attempt, state, message)
		return saveErr
	})
}

func (s *FailedCleanups) execute(ctx context.Context, c *FailedCleanup, heartbeat func([]byte) error) error {
	d, plan, a, revision, err := s.candidate(ctx, c.Actor.OrgID, c.DeploymentID, true)
	if err != nil {
		return err
	}
	if revision != c.Revision {
		return ErrCleanupConflict
	}
	authorize := func(work context.Context) error {
		if lockErr := checkSQLActionLock(work); lockErr != nil {
			return lockErr
		}
		if authErr := s.runner.authz.Require(work, c.Actor, security.DeploymentApprove, d.ProjectID); authErr != nil {
			return authErr
		}
		current, loadErr := s.load(work, d.OrgID, d.ID)
		if loadErr != nil {
			return loadErr
		}
		if current.Status != "pending" || current.Attempt != c.Attempt || current.Revision != c.Revision {
			return ErrCleanupConflict
		}
		_, _, _, got, candidateErr := s.candidate(work, d.OrgID, d.ID, true)
		if candidateErr != nil {
			return candidateErr
		}
		if got != c.Revision {
			return ErrCleanupConflict
		}
		return nil
	}
	if authErr := authorize(ctx); authErr != nil {
		return authErr
	}
	conn, credential, err := s.runner.credentials.Credential(ctx, d.OrgID, cleanupConnection(plan, a))
	if err != nil {
		return err
	}
	job := &Job{Deployment: d, Plan: plan, Action: a, Connection: conn, Credential: credential, cleanupCheck: authorize, Progress: func(string, string) {}}
	save := func(raw []byte) error {
		if authErr := authorize(ctx); authErr != nil {
			return authErr
		}
		if beatErr := heartbeat(raw); beatErr != nil {
			return beatErr
		}
		result, saveErr := s.store.db.Exec(ctx, `UPDATE loomplane_failed_cleanups SET checkpoint=$4,updated_at=now() WHERE org_id=$1 AND deployment_id=$2 AND attempt=$3 AND status='pending'`, d.OrgID, d.ID, c.Attempt, raw)
		if saveErr != nil {
			return saveErr
		}
		if n, e := result.RowsAffected(); e != nil {
			return e
		} else if n != 1 {
			return ErrCleanupConflict
		}
		return nil
	}
	rc := readReplaceCheckpoint(c.Checkpoint)
	bound, err := s.runner.resolvePendingIdentity(ctx, job, rc, save)
	if err != nil {
		return err
	}
	if bound.ExternalID == "" {
		return nil
	}
	done, err := s.runner.removeOne(ctx, job, bound, rc, save)
	if err != nil {
		return err
	}
	if !done {
		return fmt.Errorf("cleanup of %s is incomplete", a.Name)
	}
	return nil
}

type sqlActionLockKey struct{}

func checkSQLActionLock(ctx context.Context) error {
	if check, ok := ctx.Value(sqlActionLockKey{}).(func(context.Context) error); ok {
		return check(ctx)
	}
	return errors.New("SQL action lock is unavailable")
}

func (r *Runner) withSQLActionLock(ctx context.Context, d *Deployment, a planning.Action, fn func(context.Context) error) error {
	store, ok := r.store.(*PgStore)
	if !ok {
		return fn(ctx)
	} // In-memory runner tests have no competing processes.
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // transaction cleanup after commit or connection loss
	key := "sql-action/" + d.OrgID + "/" + d.EnvironmentID + "/" + a.Name
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
		return err
	}
	var mu sync.Mutex
	check := func(work context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		_, checkErr := tx.Exec(work, `SELECT 1`)
		return checkErr
	}
	return fn(context.WithValue(ctx, sqlActionLockKey{}, check))
}

func cleanupConnection(plan *planning.Plan, a planning.Action) string {
	if a.ConnectionID != "" {
		return a.ConnectionID
	}
	return plan.Connections["gcp"]
}

// Delivery is recorded only after Dispatch accepts its stable workflow key.
// A crash before this write safely repeats delivery; active workflows no longer
// occupy the bounded recovery batch.
func (s *FailedCleanups) deliver(ctx context.Context, org, id string, attempt int) error {
	if err := s.start.StartFailedCleanup(ctx, org, id, attempt); err != nil {
		return err
	}
	_, err := s.store.db.Exec(ctx, `UPDATE loomplane_failed_cleanups SET delivered_attempt=$3 WHERE org_id=$1 AND deployment_id=$2 AND attempt=$3 AND delivered_attempt<$3`, org, id, attempt)
	return err
}
