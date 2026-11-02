package realtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xraph/grove/driver"
)

// Snapshot is one repeatable-read view and its atomic replay boundary.
// Partial tells a large workspace to recover older records through REST.
type Snapshot struct {
	Cursor  string  `json:"cursor"`
	Records []Event `json:"records"`
	Partial bool    `json:"partial"`
}

func (s *PgStore) Snapshot(ctx context.Context, org string, projects, deployments []string) (out *Snapshot, err error) {
	if !validOrg(org) {
		return nil, ErrInvalid
	}
	if projects == nil {
		projects = []string{}
	}
	if deployments == nil {
		deployments = []string{}
	}
	tx, err := s.db.BeginTx(ctx, &driver.TxOptions{IsolationLevel: driver.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { err = rollback(tx, err) }()
	var high int64
	if readErr := tx.QueryRow(ctx, `SELECT coalesce((SELECT next_seq FROM loomplane_event_cursors WHERE org_id=$1),0)`, org).Scan(&high); readErr != nil {
		return nil, readErr
	}
	out = &Snapshot{Cursor: cursor(org, high), Records: []Event{}}
	bytes := 0
	for _, kind := range []struct {
		table, event, project, expression, filter string
		limit                                     int
	}{
		{"loomplane_projects", "project.updated", "id", `(to_jsonb(r)-'repository_url') || jsonb_build_object('repositories',coalesce((SELECT jsonb_agg(repository ORDER BY alias) FROM (SELECT DISTINCT ON(repository) repository,alias FROM loomplane_project_sources WHERE project_id=r.id AND removed_at IS NULL ORDER BY repository,alias) repos),'[]'))`, "", 256},
		{"loomplane_environments", "environment.updated", "project_id", `to_jsonb(r)`, "", 512},
		{"loomplane_project_sources", "source.updated", "project_id", `to_jsonb(r)`, ` AND r.removed_at IS NULL`, 512},
		{"loomplane_analyses", "analysis.updated", "project_id", `to_jsonb(r)-'graph'-'attempts' || jsonb_build_object('source',coalesce((SELECT alias FROM loomplane_project_sources WHERE id=r.source_id),''))`, "", 128},
		{"loomplane_manifests", "manifest.updated", "project_id", `to_jsonb(r)-'body' || jsonb_build_object('yaml',r.body)`, "", 128},
		{"loomplane_plans", "plan.updated", "project_id", `to_jsonb(r)-'result' || jsonb_build_object('actions','[]'::jsonb,'problems','[]'::jsonb,'warnings','[]'::jsonb,'connections','{}'::jsonb,'summary','{}'::jsonb) || coalesce(nullif(r.result,'null'::jsonb),'{}') || jsonb_build_object('sources',coalesce((SELECT jsonb_agg(to_jsonb(ps)-'plan_id' ORDER BY alias) FROM loomplane_plan_sources ps WHERE plan_id=r.id),'[]'))`, "", 128},
		{"loomplane_bindings", "binding.updated", "project_id", `to_jsonb(r)-'replaced'`, "", 512},
		{"loomplane_deployments", "deployment.updated", "project_id", `to_jsonb(r)`, ` AND (cardinality($3::text[])=0 OR r.id=ANY($3))`, 128},
		{"loomplane_cloud_connections", "connection.updated", "", `to_jsonb(r)`, "", 256},
		{"loomplane_canary_verdicts", "canary_verdict.updated", "project_id", `to_jsonb(r)`, ` AND r.deployment_id=ANY($3)`, 256},
		{"loomplane_release_watches", "release_watch.updated", "project_id", `to_jsonb(r)`, "", 128},
		{"loomplane_log_checkpoints", "build.collection", "project_id", `to_jsonb(r)-'cursor' || jsonb_build_object('id',r.deployment_id||'/'||r.action_id||'/'||r.attempt||'/'||r.operation_id)`, ` AND r.deployment_id=ANY($3)`, 256},
	} {
		query := fmt.Sprintf(`SELECT %s FROM %s r WHERE r.org_id=$1`, kind.expression, kind.table)
		if kind.project != "" {
			query += fmt.Sprintf(` AND (cardinality($2::text[])=0 OR r.%s=ANY($2))`, kind.project)
		} else {
			query += ` AND ($2::text[] IS NULL OR TRUE)`
		}
		query += kind.filter
		// All parameters stay typed even on a table with no deployment filter.
		query += ` AND ($3::text[] IS NULL OR TRUE) ORDER BY coalesce(to_jsonb(r)->>'updated_at',to_jsonb(r)->>'created_at',to_jsonb(r)->>'started_at',to_jsonb(r)->>'decided_at') DESC NULLS LAST LIMIT $4`
		rows, queryErr := tx.Query(ctx, query, org, projects, deployments, kind.limit+1)
		if queryErr != nil {
			return nil, queryErr
		}
		count := 0
		for rows.Next() {
			var data json.RawMessage
			if scanErr := rows.Scan(&data); scanErr != nil {
				rows.Close()
				return nil, scanErr
			}
			count++
			if count > kind.limit {
				out.Partial = true
				continue
			}
			var identity struct {
				ID        string `json:"id"`
				ProjectID string `json:"project_id"`
			}
			if decodeErr := json.Unmarshal(data, &identity); decodeErr != nil {
				rows.Close()
				return nil, decodeErr
			}
			if kind.table == "loomplane_projects" {
				identity.ProjectID = identity.ID
			}
			event := Event{ID: out.Cursor, OrgID: org, ProjectID: identity.ProjectID, Type: kind.event, EntityID: identity.ID, Data: data}
			if hydrateErr := hydrateEvent(&event); hydrateErr != nil {
				rows.Close()
				return nil, hydrateErr
			}
			bytes += len(event.Data) + 256
			if bytes > 4<<20 {
				out.Partial = true
				continue
			}
			out.Records = append(out.Records, event)
		}
		rows.Close()
		if queryErr := rows.Err(); queryErr != nil {
			return nil, queryErr
		}
	}
	err = tx.Commit()
	return out, err
}
