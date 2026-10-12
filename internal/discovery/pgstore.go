package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/graph"
)

// PgStore is the Postgres Store, over the grove pgdriver pool. The schema
// is migration group "loomplane" (database/migrations).
type PgStore struct {
	db *pgdriver.PgDB
}

// NewPgStore returns a Store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

const analysisColumns = `id, org_id, project_id, status, repository, ref, commit_sha, error, summary,
	created_by, created_at, started_at, finished_at`

// unfinished lists the statuses of an analysis still running.
const unfinished = `('queued', 'fetching', 'analyzing')`

func scanAnalysis(row pgx.Row) (*Analysis, error) {
	var a Analysis
	var summary []byte
	if err := row.Scan(&a.ID, &a.OrgID, &a.ProjectID, &a.Status, &a.Repository, &a.Ref, &a.CommitSHA, &a.Error, &summary,
		&a.CreatedBy, &a.CreatedAt, &a.StartedAt, &a.FinishedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(summary, &a.Summary); err != nil {
		return nil, fmt.Errorf("decode analysis summary: %w", err)
	}
	a.normalize()
	return &a, nil
}

// CreateAnalysis inserts a.
func (s *PgStore) CreateAnalysis(ctx context.Context, a *Analysis) error {
	summary, err := json.Marshal(a.Summary)
	if err != nil {
		return fmt.Errorf("encode analysis summary: %w", err)
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_analyses (`+analysisColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		a.ID, a.OrgID, a.ProjectID, a.Status, a.Repository, a.Ref, a.CommitSHA, a.Error, summary,
		a.CreatedBy, a.CreatedAt, a.StartedAt, a.FinishedAt)
	if err != nil {
		return fmt.Errorf("insert analysis: %w", err)
	}
	return nil
}

// DeleteAnalysis removes an analysis.
func (s *PgStore) DeleteAnalysis(ctx context.Context, orgID, id string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM loomplane_analyses WHERE org_id = $1 AND id = $2`, orgID, id); err != nil {
		return fmt.Errorf("delete analysis: %w", err)
	}
	return nil
}

// GetAnalysis returns the analysis, or ErrNotFound.
func (s *PgStore) GetAnalysis(ctx context.Context, orgID, id string) (*Analysis, error) {
	a, err := scanAnalysis(s.db.QueryRow(ctx, `SELECT `+analysisColumns+` FROM loomplane_analyses
		WHERE org_id = $1 AND id = $2`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get analysis: %w", err)
	}
	return a, nil
}

// ListAnalyses returns a project's latest analyses, newest first.
func (s *PgStore) ListAnalyses(ctx context.Context, orgID, projectID string, limit int) ([]*Analysis, error) {
	rows, err := s.db.Query(ctx, `SELECT `+analysisColumns+` FROM loomplane_analyses
		WHERE org_id = $1 AND project_id = $2 ORDER BY created_at DESC, id DESC LIMIT $3`, orgID, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list analyses: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []*Analysis{}
	for rows.Next() {
		a, err := scanAnalysis(rows)
		if err != nil {
			return nil, fmt.Errorf("scan analysis: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list analyses: %w", err)
	}
	return out, nil
}

// ActiveAnalysis returns the project's newest running analysis created
// after since, or ErrNotFound.
func (s *PgStore) ActiveAnalysis(ctx context.Context, orgID, projectID string, since time.Time) (*Analysis, error) {
	a, err := scanAnalysis(s.db.QueryRow(ctx, `SELECT `+analysisColumns+` FROM loomplane_analyses
		WHERE org_id = $1 AND project_id = $2 AND status IN `+unfinished+` AND created_at > $3
		ORDER BY created_at DESC LIMIT 1`, orgID, projectID, since))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find running analysis: %w", err)
	}
	return a, nil
}

// BeginAttempt implements Store.
func (s *PgStore) BeginAttempt(ctx context.Context, orgID, id string, now time.Time) (attempt int, ok bool, err error) {
	var attempts int
	err = s.db.QueryRow(ctx, `UPDATE loomplane_analyses
		SET status = 'fetching', attempts = attempts + 1, started_at = COALESCE(started_at, $3)
		WHERE org_id = $1 AND id = $2 AND status IN `+unfinished+`
		RETURNING attempts`, orgID, id, now).Scan(&attempts)
	if err == nil {
		return attempts, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, fmt.Errorf("begin analysis attempt: %w", err)
	}
	if _, err = s.GetAnalysis(ctx, orgID, id); err != nil {
		return 0, false, err
	}
	return 0, false, nil
}

// Progress implements Store. A finished analysis isn't touched.
func (s *PgStore) Progress(ctx context.Context, orgID, id, status, commitSHA string) error {
	if _, err := s.db.Exec(ctx, `UPDATE loomplane_analyses SET status = $3, commit_sha = $4
		WHERE org_id = $1 AND id = $2 AND status IN `+unfinished, orgID, id, status, commitSHA); err != nil {
		return fmt.Errorf("record analysis progress: %w", err)
	}
	return nil
}

// Succeed implements Store.
func (s *PgStore) Succeed(ctx context.Context, orgID, id string, g *graph.Graph, now time.Time) error {
	doc, err := json.Marshal(g)
	if err != nil {
		return fmt.Errorf("encode graph: %w", err)
	}
	summary, err := json.Marshal(g.Summarize())
	if err != nil {
		return fmt.Errorf("encode analysis summary: %w", err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE loomplane_analyses
		SET status = 'succeeded', graph = $3, summary = $4, error = '', finished_at = $5
		WHERE org_id = $1 AND id = $2 AND status IN `+unfinished, orgID, id, doc, summary, now); err != nil {
		return fmt.Errorf("store analysis result: %w", err)
	}
	return nil
}

// Fail implements Store.
func (s *PgStore) Fail(ctx context.Context, orgID, id, reason string, now time.Time) error {
	if _, err := s.db.Exec(ctx, `UPDATE loomplane_analyses SET status = 'failed', error = $3, finished_at = $4
		WHERE org_id = $1 AND id = $2 AND status IN `+unfinished, orgID, id, reason, now); err != nil {
		return fmt.Errorf("record analysis failure: %w", err)
	}
	return nil
}

// Graph implements Store.
func (s *PgStore) Graph(ctx context.Context, orgID, id string) (*graph.Graph, error) {
	var status string
	var doc []byte
	err := s.db.QueryRow(ctx, `SELECT status, graph FROM loomplane_analyses WHERE org_id = $1 AND id = $2`, orgID, id).
		Scan(&status, &doc)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get graph: %w", err)
	}
	if status != StatusSucceeded || doc == nil {
		return nil, ErrNoGraph
	}
	var g graph.Graph
	if err := json.Unmarshal(doc, &g); err != nil {
		return nil, fmt.Errorf("decode graph: %w", err)
	}
	return &g, nil
}
