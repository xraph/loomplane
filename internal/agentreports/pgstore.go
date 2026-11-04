package agentreports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/notifications"
)

// PgStore is the Postgres Store, over loomplane_agent_reports
// (database/migrations 20261220000600).
type PgStore struct {
	db *pgdriver.PgDB
}

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

const reportColumns = `id, org_id, project_id, environment_id, environment, signal, component, fingerprint, status, severity,
	title, facts, evidence, findings, repository, occurrences, first_seen_at, last_seen_at, resolved_at, closed_by, resolution,
	taken_by, taken_at, investigation, investigation_note, task_id, run_id, investigation_started_at,
	issue_number, issue_url, issue_state, issue_error, revision, opened_by, opened_by_task, created_at, updated_at,
	occurrence_keys, pending_notice, last_notice_at, issue_comments, notified_revision, issue_revision, notice_seq,
	issue_comment_at`

func scanRecord(row driver.Row) (*record, error) {
	var r record
	var facts, evidence, findings, keys, comments []byte
	if err := row.Scan(&r.ID, &r.OrgID, &r.ProjectID, &r.EnvironmentID, &r.Environment, &r.Signal, &r.Component, &r.Fingerprint,
		&r.Status, &r.Severity, &r.Title, &facts, &evidence, &findings, &r.Repository, &r.Occurrences, &r.FirstSeenAt,
		&r.LastSeenAt, &r.ResolvedAt, &r.ClosedBy, &r.Resolution, &r.TakenBy, &r.TakenAt, &r.Investigation,
		&r.InvestigationNote, &r.TaskID, &r.RunID, &r.InvestigationStartedAt, &r.Issue.Number, &r.Issue.URL, &r.Issue.State,
		&r.Issue.Error, &r.Revision, &r.OpenedBy, &r.OpenedByTask, &r.CreatedAt, &r.UpdatedAt, &keys, &r.PendingNotice,
		&r.LastNoticeAt, &comments, &r.NotifiedRevision, &r.IssueRevision, &r.NoticeSeq, &r.IssueCommentAt); err != nil {
		return nil, err
	}
	r.Facts, r.OccurrenceKeys, r.IssueComments = []Fact{}, []string{}, []string{}
	for _, part := range []struct {
		raw []byte
		to  any
	}{{facts, &r.Facts}, {evidence, &r.Evidence}, {keys, &r.OccurrenceKeys}, {comments, &r.IssueComments}} {
		if len(part.raw) > 0 {
			if err := json.Unmarshal(part.raw, part.to); err != nil {
				return nil, fmt.Errorf("read agent report %s: %w", r.ID, err)
			}
		}
	}
	if len(findings) > 0 && string(findings) != "null" {
		r.Findings = &Findings{}
		if err := json.Unmarshal(findings, r.Findings); err != nil {
			return nil, fmt.Errorf("read agent report %s findings: %w", r.ID, err)
		}
		r.Findings.WrittenAt = r.Findings.WrittenAt.UTC()
	}
	r.FirstSeenAt, r.LastSeenAt, r.CreatedAt, r.UpdatedAt = r.FirstSeenAt.UTC(), r.LastSeenAt.UTC(), r.CreatedAt.UTC(), r.UpdatedAt.UTC()
	for _, t := range []**time.Time{&r.ResolvedAt, &r.TakenAt, &r.InvestigationStartedAt, &r.LastNoticeAt, &r.IssueCommentAt} {
		if *t != nil {
			at := (*t).UTC()
			*t = &at
		}
	}
	return &r, nil
}

func (s *PgStore) query(ctx context.Context, q string, args ...any) ([]*Report, error) {
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Report{}
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, &r.Report)
	}
	return out, rows.Err()
}

func rollback(tx driver.Tx) { _ = tx.Rollback() } //nolint:errcheck // a committed transaction cannot roll back

// Get implements Store.
func (s *PgStore) Get(ctx context.Context, orgID, id string) (*Report, error) {
	r, err := scanRecord(s.db.QueryRow(ctx, `SELECT `+reportColumns+` FROM loomplane_agent_reports WHERE org_id = $1 AND id = $2`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r.Report, nil
}

// List implements Store.
func (s *PgStore) List(ctx context.Context, orgID string, projectIDs []string, f ListFilter) ([]*Report, error) {
	return s.query(ctx, `SELECT `+reportColumns+` FROM loomplane_agent_reports
		WHERE org_id = $1 AND project_id = ANY($2) AND ($3 = 'all' OR status = $3)
		ORDER BY last_seen_at DESC, id DESC LIMIT $4`, orgID, projectIDs, f.Status, f.Limit)
}

// Open implements Store.
func (s *PgStore) Open(ctx context.Context, orgID, projectID, environmentID string) ([]*Report, error) {
	return s.query(ctx, `SELECT `+reportColumns+` FROM loomplane_agent_reports
		WHERE org_id = $1 AND status = 'open' AND ($2 = '' OR project_id = $2) AND ($3 = '' OR environment_id = $3)
		ORDER BY first_seen_at, id`, orgID, projectID, environmentID)
}

// Investigating implements Store.
func (s *PgStore) Investigating(ctx context.Context, orgID string) ([]*Report, error) {
	return s.query(ctx, `SELECT `+reportColumns+` FROM loomplane_agent_reports
		WHERE org_id = $1 AND investigation IN ('queued', 'running')
		ORDER BY first_seen_at, id`, orgID)
}

// Observe implements Store. One transaction holds a lock on the
// fingerprint while it decides between a repeat, a reopen and a new
// report, so two writers never open the same incident twice; the partial
// unique index on open reports is the backstop.
func (s *PgStore) Observe(ctx context.Context, sig Signal, reopenAfter, now time.Time) (*Observation, error) {
	fp := Fingerprint(sig.ProjectID, sig.EnvironmentID, sig.Kind, sig.Component)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || '/' || $2, 0))`, sig.OrgID, fp); err != nil {
		return nil, fmt.Errorf("lock agent report fingerprint: %w", err)
	}

	open, err := scanRecord(tx.QueryRow(ctx, `SELECT `+reportColumns+` FROM loomplane_agent_reports
		WHERE org_id = $1 AND fingerprint = $2 AND status = 'open' FOR UPDATE`, sig.OrgID, fp))
	switch {
	case err == nil:
		change, written := open.repeat(sig, now)
		if written {
			if serr := save(ctx, tx, open); serr != nil {
				return nil, serr
			}
		}
		return &Observation{Report: &open.Report, Change: change}, tx.Commit()
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}

	latest, err := scanRecord(tx.QueryRow(ctx, `SELECT `+reportColumns+` FROM loomplane_agent_reports
		WHERE org_id = $1 AND fingerprint = $2 AND status IN ('resolved', 'closed') AND resolved_at >= $3
		ORDER BY resolved_at DESC, id DESC LIMIT 1 FOR UPDATE`, sig.OrgID, fp, reopenAfter.UTC()))
	switch {
	case err == nil && latest.counted(sig):
		return &Observation{Report: &latest.Report, Change: ChangeUnchanged}, tx.Commit()
	case err == nil:
		latest.reopen(sig, now)
		if serr := save(ctx, tx, latest); serr != nil {
			return nil, serr
		}
		return &Observation{Report: &latest.Report, Change: ChangeReopened}, tx.Commit()
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}
	r := newRecord(id, sig, now)
	if err := insert(ctx, tx, r); err != nil {
		return nil, err
	}
	return &Observation{Report: &r.Report, Change: ChangeOpened}, tx.Commit()
}

func insert(ctx context.Context, tx driver.Tx, r *record) error {
	facts, evidence, keys, comments, err := marshalRecord(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_agent_reports (id, org_id, project_id, environment_id, environment, signal,
			component, fingerprint, status, severity, title, facts, evidence, repository, occurrences, occurrence_keys,
			first_seen_at, last_seen_at, investigation, revision, pending_notice, issue_comments, issue_state, opened_by,
			opened_by_task, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24,
			$25, $26, $27)`,
		r.ID, r.OrgID, r.ProjectID, r.EnvironmentID, r.Environment, r.Signal, r.Component, r.Fingerprint, r.Status, r.Severity,
		r.Title, facts, evidence, r.Repository, r.Occurrences, keys, r.FirstSeenAt.UTC(), r.LastSeenAt.UTC(), r.Investigation,
		r.Revision, r.PendingNotice, comments, r.Issue.State, r.OpenedBy, r.OpenedByTask, r.CreatedAt.UTC(), r.UpdatedAt.UTC())
	if err != nil {
		return fmt.Errorf("create agent report: %w", err)
	}
	return nil
}

// save writes back what a repeat or a reopen changes.
func save(ctx context.Context, tx driver.Tx, r *record) error {
	facts, evidence, keys, comments, err := marshalRecord(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE loomplane_agent_reports
		SET status = $3, facts = $4, evidence = $5, occurrences = $6, occurrence_keys = $7, last_seen_at = $8,
			resolved_at = $9, closed_by = $10, resolution = $11, taken_by = $12, taken_at = $13, investigation = $14,
			investigation_note = $15, revision = $16, pending_notice = $17, issue_comments = $18, updated_at = $19
		WHERE org_id = $1 AND id = $2`,
		r.OrgID, r.ID, r.Status, facts, evidence, r.Occurrences, keys, r.LastSeenAt.UTC(), r.ResolvedAt, r.ClosedBy,
		r.Resolution, r.TakenBy, r.TakenAt, r.Investigation, r.InvestigationNote, r.Revision, r.PendingNotice, comments,
		r.UpdatedAt.UTC())
	if err != nil {
		return fmt.Errorf("update agent report: %w", err)
	}
	return nil
}

func marshalRecord(r *record) (facts, evidence, keys, comments []byte, err error) {
	if facts, err = json.Marshal(cloneFacts(r.Facts)); err != nil {
		return
	}
	if evidence, err = json.Marshal(r.Evidence); err != nil {
		return
	}
	if keys, err = json.Marshal(nonNil(r.OccurrenceKeys)); err != nil {
		return
	}
	comments, err = json.Marshal(nonNil(r.IssueComments))
	return
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Clear implements Store.
func (s *PgStore) Clear(ctx context.Context, c Clearance) ([]*Report, error) {
	components := c.Components
	return s.query(ctx, `UPDATE loomplane_agent_reports
		SET status = 'resolved', resolution = $7, resolved_at = $8, revision = revision + 1,
			issue_comments = issue_comments || '["resolved"]'::jsonb, updated_at = $8
		WHERE org_id = $1 AND project_id = $2 AND environment_id = $3 AND status = 'open' AND signal = ANY($4)
			AND ($5 OR component = ANY($6))
			AND signal <> 'manual' AND opened_by = '' AND opened_by_task = ''
		RETURNING `+reportColumns,
		c.OrgID, c.ProjectID, c.EnvironmentID, c.Signals, components == nil, nonNil(components), c.Reason, c.At.UTC())
}

// Close implements Store.
func (s *PgStore) Close(ctx context.Context, orgID, id, by, reason string, at time.Time) (*Report, error) {
	r, err := scanRecord(s.db.QueryRow(ctx, `UPDATE loomplane_agent_reports
		SET status = 'closed', closed_by = $3, resolution = $4, resolved_at = $5, revision = revision + 1,
			issue_comments = issue_comments || '["closed"]'::jsonb, updated_at = $5
		WHERE org_id = $1 AND id = $2 AND status = 'open'
		RETURNING `+reportColumns, orgID, id, by, reason, at.UTC()))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, gerr := s.Get(ctx, orgID, id); gerr != nil {
			return nil, gerr
		}
		return nil, ErrNotOpen
	}
	if err != nil {
		return nil, fmt.Errorf("close agent report: %w", err)
	}
	return &r.Report, nil
}

// SetInvestigation implements Store. Queued records the task and run as
// given; running keeps them unless given; done and failed bump the
// revision, and done queues a findings comment on an issue that exists.
func (s *PgStore) SetInvestigation(ctx context.Context, orgID, id string, from []string, inv Investigation, at time.Time) (bool, error) {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_agent_reports
		SET investigation = $3::text, investigation_note = $4,
			task_id = CASE WHEN $3::text = 'queued' THEN $5::text WHEN $3::text = 'running' AND $5::text <> '' THEN $5::text ELSE task_id END,
			run_id = CASE WHEN $3::text = 'queued' THEN $6::text WHEN $3::text = 'running' AND $6::text <> '' THEN $6::text ELSE run_id END,
			investigation_started_at = CASE WHEN $3::text IN ('queued', 'running') THEN $7 ELSE investigation_started_at END,
			revision = CASE WHEN $3::text IN ('done', 'failed') THEN revision + 1 ELSE revision END,
			issue_comments = CASE WHEN $3::text = 'done' AND issue_number > 0 THEN issue_comments || '["findings"]'::jsonb ELSE issue_comments END,
			updated_at = $7
		WHERE org_id = $1 AND id = $2 AND investigation = ANY($8)`,
		orgID, id, inv.Status, inv.Note, inv.TaskID, inv.RunID, at.UTC(), from)
	if err != nil {
		return false, fmt.Errorf("move agent report investigation: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// WriteFindings implements Store.
func (s *PgStore) WriteFindings(ctx context.Context, orgID, id, taskID string, f Findings) (*Report, error) {
	findings, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	r, err := scanRecord(s.db.QueryRow(ctx, `UPDATE loomplane_agent_reports
		SET findings = $3, revision = revision + 1, updated_at = $4
		WHERE org_id = $1 AND id = $2 AND $5::text <> '' AND $5::text IN (task_id, opened_by_task)
		RETURNING `+reportColumns, orgID, id, findings, f.WrittenAt.UTC(), taskID))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, gerr := s.Get(ctx, orgID, id); gerr != nil {
			return nil, gerr
		}
		return nil, ErrNotYourReport
	}
	if err != nil {
		return nil, fmt.Errorf("write agent report findings: %w", err)
	}
	return &r.Report, nil
}

// Documents implements Store.
// A full page goes on through every report updated at the same moment as
// its last one (a Clear stamps a whole batch alike), so a caller that
// reads on from the last UpdatedAt never skips one.
func (s *PgStore) Documents(ctx context.Context, orgID string, after time.Time, limit int) ([]*Report, error) {
	page, err := s.query(ctx, `SELECT `+reportColumns+` FROM loomplane_agent_reports
		WHERE org_id = $1 AND (findings IS NOT NULL OR status <> 'open') AND updated_at > $2
		ORDER BY updated_at, id LIMIT $3`, orgID, after.UTC(), limit)
	if err != nil || len(page) < limit {
		return page, err
	}
	last := page[len(page)-1]
	rest, err := s.query(ctx, `SELECT `+reportColumns+` FROM loomplane_agent_reports
		WHERE org_id = $1 AND (findings IS NOT NULL OR status <> 'open') AND updated_at = $2 AND id > $3
		ORDER BY id`, orgID, last.UpdatedAt.UTC(), last.ID)
	if err != nil {
		return nil, err
	}
	return append(page, rest...), nil
}

// Pending implements Store.
func (s *PgStore) Pending(ctx context.Context, orgID string) ([]*Report, error) {
	return s.query(ctx, `SELECT `+reportColumns+` FROM loomplane_agent_reports
		WHERE org_id = $1 AND (revision > notified_revision OR revision > issue_revision
			OR (issue_state = 'waiting_for_permission' AND status = 'open'))
		ORDER BY first_seen_at, id`, orgID)
}

// QueueDelivery implements Store.
func (s *PgStore) QueueDelivery(ctx context.Context, orgID, id string, now time.Time) (bool, error) {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_agent_reports SET delivery_queued_at = $3
		WHERE org_id = $1 AND id = $2 AND (delivery_queued_at IS NULL OR delivery_queued_at < $4)`,
		orgID, id, now.UTC(), now.UTC().Add(-DeliveryRequeue))
	if err != nil {
		return false, fmt.Errorf("queue agent report delivery: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ClaimDelivery implements Store. The lease runs on the database's
// clock, so workers on different machines agree on when it ends.
func (s *PgStore) ClaimDelivery(ctx context.Context, orgID, id, owner string, lease time.Duration) (*Delivery, error) {
	r, err := scanRecord(s.db.QueryRow(ctx, `UPDATE loomplane_agent_reports
		SET delivery_owner = $3, delivery_until = now() + ($4::float8 * interval '1 second')
		WHERE org_id = $1 AND id = $2 AND (delivery_until IS NULL OR delivery_until < now() OR delivery_owner = $3)
		RETURNING `+reportColumns, orgID, id, owner, lease.Seconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, gerr := s.Get(ctx, orgID, id); gerr != nil {
			return nil, gerr
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim agent report delivery: %w", err)
	}
	return r.delivery(), nil
}

// SaveNotice implements Store: the notice is queued in the transaction
// that records it, so it's queued once.
func (s *PgStore) SaveNotice(ctx context.Context, orgID, id, owner string, revision int, pendingNotice string,
	send func(ctx context.Context, q notifications.Execer, seq int) error, at time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer rollback(tx)
	var current, seq int
	var pending, holder string
	err = tx.QueryRow(ctx, `SELECT revision, pending_notice, notice_seq, delivery_owner FROM loomplane_agent_reports
		WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, id).Scan(&current, &pending, &seq, &holder)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("read agent report notice: %w", err)
	}
	if current != revision || pending != pendingNotice || holder != owner {
		return false, nil
	}
	if pending == "" {
		_, err = tx.Exec(ctx, `UPDATE loomplane_agent_reports SET notified_revision = GREATEST(notified_revision, $3)
			WHERE org_id = $1 AND id = $2`, orgID, id, revision)
	} else {
		if serr := send(ctx, tx, seq+1); serr != nil {
			return false, serr
		}
		_, err = tx.Exec(ctx, `UPDATE loomplane_agent_reports
			SET notice_seq = $3, last_notice_at = $4, pending_notice = '', notified_revision = GREATEST(notified_revision, $5)
			WHERE org_id = $1 AND id = $2`, orgID, id, seq+1, at.UTC(), revision)
	}
	if err != nil {
		return false, fmt.Errorf("record agent report notice: %w", err)
	}
	return true, tx.Commit()
}

// SaveIssue implements Store. It drops only the comments the step
// handled, so one queued meanwhile waits for the next delivery.
func (s *PgStore) SaveIssue(ctx context.Context, orgID, id, owner string, revision int, in IssueSave) error {
	_, err := s.db.Exec(ctx, `UPDATE loomplane_agent_reports
		SET issue_number = $4, issue_url = $5, issue_state = $6, issue_error = $7,
			issue_comments = COALESCE((SELECT jsonb_agg(c.e ORDER BY c.i) FROM jsonb_array_elements(issue_comments) WITH ORDINALITY AS c(e, i)
				WHERE c.i > $8), '[]'::jsonb),
			issue_comment_at = COALESCE($9::timestamptz, issue_comment_at),
			issue_revision = CASE WHEN $10 THEN GREATEST(issue_revision, $11) ELSE issue_revision END
		WHERE org_id = $1 AND id = $2 AND delivery_owner = $3`,
		orgID, id, owner, in.Number, in.URL, in.State, in.Error, in.Handled, utcOrNil(in.CommentAt), in.Done, revision)
	if err != nil {
		return fmt.Errorf("record agent report issue: %w", err)
	}
	return nil
}

// ReleaseDelivery implements Store.
func (s *PgStore) ReleaseDelivery(ctx context.Context, orgID, id, owner string, done bool) error {
	_, err := s.db.Exec(ctx, `UPDATE loomplane_agent_reports
		SET delivery_owner = '', delivery_until = NULL, delivery_queued_at = CASE WHEN $4 THEN NULL ELSE delivery_queued_at END
		WHERE org_id = $1 AND id = $2 AND delivery_owner = $3`, orgID, id, owner, done)
	if err != nil {
		return fmt.Errorf("release agent report delivery: %w", err)
	}
	return nil
}

func utcOrNil(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
