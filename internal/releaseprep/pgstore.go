package releaseprep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/security"
)

// PgStore is the Postgres Store, over loomplane_release_reports
// (database/migrations 20261220000700).
type PgStore struct {
	db *pgdriver.PgDB
}

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

// Bounds on a List.
const (
	defaultListLimit = 50
	maxListLimit     = 200
)

const reportColumns = `id, org_id, project_id, from_environment_id, from_environment, to_environment_id, to_environment,
	scope, status, state, state_reason, requested_by, via, plan_id, plan_revision, reused_plan, task_id, run_id,
	evidence, gates, preview, decision, evaluated_at, concerns, error, created_at, updated_at, finished_at`

// unsettled is the condition Unsettled reads, and the unsettled index's.
const unsettled = `(status IN ('planning', 'reviewing') OR state IN ('planning', 'reviewing', 'promoting', 'waiting_for_person'))`

func scanReport(row driver.Row) (*ReleaseReport, error) {
	var r ReleaseReport
	var scope, evidence, gates, concerns []byte
	if err := row.Scan(&r.ID, &r.OrgID, &r.ProjectID, &r.FromEnvironmentID, &r.FromEnvironment, &r.ToEnvironmentID,
		&r.ToEnvironment, &scope, &r.Status, &r.State, &r.StateReason, &r.RequestedBy, &r.Via, &r.PlanID, &r.PlanRevision,
		&r.ReusedPlan, &r.TaskID, &r.RunID, &evidence, &gates, &r.Preview, &r.Decision, &r.EvaluatedAt, &concerns,
		&r.Error, &r.CreatedAt, &r.UpdatedAt, &r.FinishedAt); err != nil {
		return nil, err
	}
	r.Scope, r.Gates, r.Concerns = []string{}, []GateCheck{}, []Concern{}
	for _, part := range []struct {
		raw []byte
		to  any
	}{{scope, &r.Scope}, {gates, &r.Gates}, {concerns, &r.Concerns}} {
		if len(part.raw) > 0 {
			if err := json.Unmarshal(part.raw, part.to); err != nil {
				return nil, fmt.Errorf("read release report %s: %w", r.ID, err)
			}
		}
	}
	// A JSON null in an array column reads as empty, never nil.
	if r.Scope == nil {
		r.Scope = []string{}
	}
	if r.Gates == nil {
		r.Gates = []GateCheck{}
	}
	if r.Concerns == nil {
		r.Concerns = []Concern{}
	}
	if len(evidence) > 0 && string(evidence) != "null" {
		r.Evidence = &ReadinessEvidence{}
		if err := json.Unmarshal(evidence, r.Evidence); err != nil {
			return nil, fmt.Errorf("read release report %s evidence: %w", r.ID, err)
		}
	}
	r.CreatedAt, r.UpdatedAt = r.CreatedAt.UTC(), r.UpdatedAt.UTC()
	r.EvaluatedAt, r.FinishedAt = utcPtr(r.EvaluatedAt), utcPtr(r.FinishedAt)
	return &r, nil
}

func (s *PgStore) one(ctx context.Context, q string, args ...any) (*ReleaseReport, error) {
	r, err := scanReport(s.db.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (s *PgStore) many(ctx context.Context, q string, args ...any) ([]*ReleaseReport, error) {
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ReleaseReport{}
	for rows.Next() {
		r, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Create implements Store.
func (s *PgStore) Create(ctx context.Context, r *ReleaseReport) error {
	if err := validNew(r); err != nil {
		return err
	}
	generated := r.ID == ""
	if generated {
		id, err := typeid.Generate("rrep")
		if err != nil {
			return fmt.Errorf("generate release report id: %w", err)
		}
		r.ID = id.String()
	}
	err := s.insert(ctx, r)
	if err != nil && generated {
		// A report that wasn't stored has no ID to point anyone at.
		r.ID = ""
	}
	return err
}

func (s *PgStore) insert(ctx context.Context, r *ReleaseReport) error {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = r.CreatedAt
	}
	// What the caller holds is what a Get returns.
	r.CreatedAt, r.UpdatedAt = stored(r.CreatedAt), stored(r.UpdatedAt)
	r.EvaluatedAt, r.FinishedAt = storedPtr(r.EvaluatedAt), storedPtr(r.FinishedAt)
	if r.Scope == nil {
		r.Scope = []string{}
	}
	if r.Gates == nil {
		r.Gates = []GateCheck{}
	}
	if r.Concerns == nil {
		r.Concerns = []Concern{}
	}
	scope, err := json.Marshal(r.Scope)
	if err != nil {
		return err
	}
	gates, err := json.Marshal(r.Gates)
	if err != nil {
		return err
	}
	concerns, err := json.Marshal(r.Concerns)
	if err != nil {
		return err
	}
	evidence, err := evidenceParam(r.Evidence)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_release_reports (id, org_id, project_id, from_environment_id,
			from_environment, to_environment_id, to_environment, scope, status, state, state_reason, requested_by, via,
			plan_id, plan_revision, reused_plan, task_id, run_id, evidence, gates, preview, decision, evaluated_at,
			concerns, error, created_at, updated_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23,
			$24, $25, $26, $27, $28)`,
		r.ID, r.OrgID, r.ProjectID, r.FromEnvironmentID, r.FromEnvironment, r.ToEnvironmentID, r.ToEnvironment, scope,
		r.Status, r.State, r.StateReason, r.RequestedBy, r.Via, r.PlanID, r.PlanRevision, r.ReusedPlan, r.TaskID, r.RunID,
		evidence, gates, r.Preview, r.Decision, r.EvaluatedAt, concerns, r.Error, r.CreatedAt, r.UpdatedAt, r.FinishedAt)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "loomplane_release_reports_active" {
		return ErrInProgress
	}
	if err != nil {
		return fmt.Errorf("create release report: %w", err)
	}
	return nil
}

func validNew(r *ReleaseReport) error {
	switch {
	case r.OrgID == "" || r.ProjectID == "" || r.ToEnvironmentID == "":
		return errors.New("a release report needs an organization, a project and an environment")
	case !person(r.RequestedBy):
		return errors.New("a release report is requested by a person")
	case !knownStatus(r.Status) || !knownState(r.State):
		return fmt.Errorf("unknown release report status %q or state %q", r.Status, r.State)
	case r.Via != ViaDashboard && r.Via != ViaCLI && r.Via != ViaChat:
		return fmt.Errorf("unknown release report channel %q", r.Via)
	case !knownDecision(r.Decision):
		return fmt.Errorf("unknown release decision %q", r.Decision)
	}
	return nil
}

// person reports whether a user ID names a person, not the agent or
// Loomplane itself.
func person(userID string) bool {
	if userID == "" {
		return false
	}
	if _, ok := security.IsAgentSubject(userID); ok {
		return false
	}
	_, ok := security.IsSystemSubject(userID)
	return !ok
}

func knownStatus(status string) bool {
	switch status {
	case StatusPlanning, StatusReviewing, StatusDone, StatusFailed, StatusCancelled:
		return true
	}
	return false
}

func knownState(state string) bool {
	switch state {
	case StatePlanning, StateReviewing, StatePromoting, StatePromoted, StateWaiting, StateApproved, StateRejected,
		StateStale, StateExpired, StateSuperseded, StateBlocked, StateStopped, StateFailed, StateCancelled:
		return true
	}
	return false
}

func knownDecision(decision string) bool {
	return decision == DecisionNone || decision == DecisionPromote || decision == DecisionAsk
}

// Get implements Store.
func (s *PgStore) Get(ctx context.Context, orgID, id string) (*ReleaseReport, error) {
	return s.one(ctx, `SELECT `+reportColumns+` FROM loomplane_release_reports WHERE org_id = $1 AND id = $2`, orgID, id)
}

// List implements Store.
func (s *PgStore) List(ctx context.Context, orgID, projectID string, limit int) ([]*ReleaseReport, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	limit = min(limit, maxListLimit)
	return s.many(ctx, `SELECT `+reportColumns+` FROM loomplane_release_reports
		WHERE org_id = $1 AND project_id = $2
		ORDER BY created_at DESC, id DESC LIMIT $3`, orgID, projectID, limit)
}

// Active implements Store.
func (s *PgStore) Active(ctx context.Context, orgID, toEnvironmentID string) (*ReleaseReport, error) {
	return s.one(ctx, `SELECT `+reportColumns+` FROM loomplane_release_reports
		WHERE org_id = $1 AND to_environment_id = $2 AND status IN ('planning', 'reviewing')`, orgID, toEnvironmentID)
}

// ForPlan implements Store.
func (s *PgStore) ForPlan(ctx context.Context, orgID, planID string) (*ReleaseReport, error) {
	if planID == "" {
		return nil, ErrNotFound
	}
	return s.one(ctx, `SELECT `+reportColumns+` FROM loomplane_release_reports
		WHERE org_id = $1 AND plan_id = $2
		ORDER BY created_at DESC, id DESC LIMIT 1`, orgID, planID)
}

// Unsettled implements Store. It is the one read across organizations: the
// settle sweep reads every workspace's unfinished reports. It claims the
// least recently swept and stamps swept_at, so reports that stay unsettled
// for a long time can't starve newer ones, and two sweepers at once skip
// each other's rows. swept_at isn't in the event, so a sweep emits nothing.
func (s *PgStore) Unsettled(ctx context.Context, limit int) ([]*ReleaseReport, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	return s.many(ctx, `UPDATE loomplane_release_reports SET swept_at = $2
		WHERE id IN (SELECT id FROM loomplane_release_reports
			WHERE `+unsettled+`
			ORDER BY swept_at NULLS FIRST, updated_at, id LIMIT $1
			FOR UPDATE SKIP LOCKED)
		RETURNING `+reportColumns, limit, stored(time.Now()))
}

// changed runs an update of one report and tells "changed nothing because
// the report isn't there" (ErrNotFound) from "changed nothing because the
// condition didn't hold" (false).
func (s *PgStore) changed(ctx context.Context, orgID, id, q string, args ...any) (bool, error) {
	res, err := s.db.Exec(ctx, q, args...)
	if err != nil {
		return false, fmt.Errorf("update release report: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	return false, s.exists(ctx, orgID, id)
}

func (s *PgStore) exists(ctx context.Context, orgID, id string) error {
	var one int
	err := s.db.QueryRow(ctx, `SELECT 1 FROM loomplane_release_reports WHERE org_id = $1 AND id = $2`, orgID, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// mustChange is changed for updates that must apply: when the report is
// there but its condition no longer holds, it has moved on.
func (s *PgStore) mustChange(ctx context.Context, orgID, id, q string, args ...any) error {
	ok, err := s.changed(ctx, orgID, id, q, args...)
	if err == nil && !ok {
		return ErrMovedOn
	}
	return err
}

// SetPlan implements Store.
func (s *PgStore) SetPlan(ctx context.Context, orgID, id, planID string, reused bool) (bool, error) {
	if planID == "" {
		return false, errors.New("set plan: no plan")
	}
	return s.changed(ctx, orgID, id, `UPDATE loomplane_release_reports
		SET plan_id = $3, reused_plan = $4, updated_at = $5
		WHERE org_id = $1 AND id = $2 AND plan_id = '' AND status = 'planning'`,
		orgID, id, planID, reused, stored(time.Now()))
}

// SetEvidence implements Store.
func (s *PgStore) SetEvidence(ctx context.Context, orgID, id, planRevision string, ev *ReadinessEvidence, gates []GateCheck) error {
	evidence, err := evidenceParam(ev)
	if err != nil {
		return err
	}
	g, err := gatesParam(gates)
	if err != nil {
		return err
	}
	return s.mustChange(ctx, orgID, id, `UPDATE loomplane_release_reports
		SET plan_revision = $3, evidence = $4, gates = $5, preview = true, updated_at = $6
		WHERE org_id = $1 AND id = $2 AND status = 'planning'`,
		orgID, id, planRevision, evidence, g, stored(time.Now()))
}

// SetTask implements Store. The state follows the status to reviewing
// unless something already moved it on.
func (s *PgStore) SetTask(ctx context.Context, orgID, id, taskID, runID string) (bool, error) {
	if taskID == "" {
		return false, errors.New("set task: no task")
	}
	return s.changed(ctx, orgID, id, `UPDATE loomplane_release_reports
		SET task_id = $3, run_id = $4, status = 'reviewing',
			state = CASE WHEN state = 'planning' THEN 'reviewing' ELSE state END, updated_at = $5
		WHERE org_id = $1 AND id = $2 AND task_id = '' AND status = 'planning'`,
		orgID, id, taskID, runID, stored(time.Now()))
}

// RecordEvaluation implements Store.
func (s *PgStore) RecordEvaluation(ctx context.Context, orgID, id string, gates []GateCheck, decision string, ev *ReadinessEvidence, at time.Time) error {
	if !knownDecision(decision) {
		return fmt.Errorf("unknown release decision %q", decision)
	}
	evidence, err := evidenceParam(ev)
	if err != nil {
		return err
	}
	g, err := gatesParam(gates)
	if err != nil {
		return err
	}
	return s.mustChange(ctx, orgID, id, `UPDATE loomplane_release_reports
		SET gates = $3, decision = $4, evidence = COALESCE($5::jsonb, evidence), preview = false,
			evaluated_at = $6, updated_at = $7
		WHERE org_id = $1 AND id = $2 AND status = 'reviewing'`,
		orgID, id, g, decision, evidence, stored(at), stored(time.Now()))
}

// AddConcern implements Store. The append and the cap are one statement,
// so two concerns at once never pass MaxConcerns.
func (s *PgStore) AddConcern(ctx context.Context, orgID, id string, c Concern) error {
	if c.Kind != ConcernRisk && c.Kind != ConcernAsk {
		return fmt.Errorf("unknown concern kind %q", c.Kind)
	}
	if c.Text == "" {
		return errors.New("a concern needs text")
	}
	if utf8.RuneCountInString(c.Text) > MaxConcernLength {
		return fmt.Errorf("a concern is at most %d characters", MaxConcernLength)
	}
	if c.At.IsZero() {
		c.At = time.Now()
	}
	c.At = c.At.UTC()
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	ok, err := s.changed(ctx, orgID, id, `UPDATE loomplane_release_reports
		SET concerns = concerns || jsonb_build_array($3::jsonb), updated_at = $4
		WHERE org_id = $1 AND id = $2 AND jsonb_array_length(concerns) < $5`,
		orgID, id, raw, stored(time.Now()), MaxConcerns)
	if err != nil {
		return err
	}
	if !ok {
		return ErrTooManyConcerns
	}
	return nil
}

// Settle implements Store. It writes only while the report is still in the
// state the caller read and isn't settled, so a late writer can't undo a
// cancel and a finished report never moves again. It keeps the first time
// the report finished.
func (s *PgStore) Settle(ctx context.Context, orgID, id, from, status, state, reason, errText string, at time.Time) error {
	if status != StatusDone && status != StatusFailed && status != StatusCancelled {
		return fmt.Errorf("a release report settles as done, failed or cancelled, not %q", status)
	}
	if !knownState(state) {
		return fmt.Errorf("unknown release report state %q", state)
	}
	at = stored(at)
	return s.mustChange(ctx, orgID, id, `UPDATE loomplane_release_reports
		SET status = $4, state = $5, state_reason = $6, error = $7, updated_at = $8, finished_at = COALESCE(finished_at, $8)
		WHERE org_id = $1 AND id = $2 AND state = $3 AND `+unsettled+`
			AND NOT ($4 = 'cancelled' AND decision = 'promote')`,
		orgID, id, from, status, state, reason, errText, at)
}

// Finish implements Store. The compare-and-set update and the notice share
// a transaction: the update's row lock holds the report through send, the
// notice and the settle commit together, and a failed send rolls both
// back. notified_state isn't in the event, so it adds nothing there.
func (s *PgStore) Finish(ctx context.Context, orgID, id, from, status, state, reason, errText string, at time.Time, send func(ctx context.Context, tx NoticeTx) error) error {
	if status != StatusDone && status != StatusFailed && status != StatusCancelled {
		return fmt.Errorf("a release report finishes as done, failed or cancelled, not %q", status)
	}
	if !knownState(state) {
		return fmt.Errorf("unknown release report state %q", state)
	}
	at = stored(at)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	res, err := tx.Exec(ctx, `UPDATE loomplane_release_reports
		SET status = $4, state = $5, state_reason = $6, error = $7, updated_at = $8, finished_at = COALESCE(finished_at, $8),
			notified_state = CASE WHEN $9 THEN $5 ELSE notified_state END
		WHERE org_id = $1 AND id = $2 AND state = $3 AND `+unsettled,
		orgID, id, from, status, state, reason, errText, at, send != nil)
	if err != nil {
		return fmt.Errorf("finish release report: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		rollback(tx)
		if err := s.exists(ctx, orgID, id); err != nil {
			return err
		}
		return ErrMovedOn
	}
	if send != nil {
		if err := send(ctx, tx); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("finish release report: %w", err)
	}
	return nil
}

func rollback(tx driver.Tx) { _ = tx.Rollback() } //nolint:errcheck // a committed or rolled back transaction cannot roll back

// evidenceParam is ev as a JSONB parameter, or SQL NULL for no evidence.
func evidenceParam(ev *ReadinessEvidence) (any, error) {
	if ev == nil {
		return nil, nil
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("encode release evidence: %w", err)
	}
	return raw, nil
}

func gatesParam(gates []GateCheck) ([]byte, error) {
	if gates == nil {
		gates = []GateCheck{}
	}
	return json.Marshal(gates)
}

// stored is t as Postgres keeps it: UTC, to the microsecond.
func stored(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

func storedPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := stored(*t)
	return &v
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC()
	return &v
}
