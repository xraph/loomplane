package cloudprofiles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
)

// SyncStore persists immutable reviews and atomic desired-state transitions.
type SyncStore interface {
	Store
	Resolver
	SnapshotBindings(context.Context, string, string) (json.RawMessage, error)
	AssignmentGeneration(context.Context, string, string, string) (int64, error)
	SavePreview(context.Context, *Preview, *Snapshot) error
	GetPreview(context.Context, string, string, string) (*Preview, *Snapshot, error)
	Accept(context.Context, *Preview, *Snapshot, *Sync, string) (*Sync, error)
	GetSync(context.Context, string, string, string) (*Sync, *Snapshot, error)
	ListSyncs(context.Context, string, string) ([]*Sync, error)
	PendingSyncs(context.Context) ([]*Sync, error)
	SaveSync(context.Context, *Sync, *Snapshot) error
}

// AssignmentGeneration includes unassignment tombstones, preventing ABA replay.
func (s *PgStore) AssignmentGeneration(ctx context.Context, org, project, env string) (int64, error) {
	var revision int64
	err := s.db.QueryRow(ctx, `SELECT coalesce(a.revision,0) FROM loomplane_environments e LEFT JOIN loomplane_cloud_assignments a ON a.org_id=e.org_id AND a.project_id=e.project_id AND a.environment_id=e.id WHERE e.org_id=$1 AND e.project_id=$2 AND e.id=$3`, org, project, env).Scan(&revision)
	return revision, storeError(err)
}

// SavePreview inserts a review once; updates are deliberately unsupported.
func (s *PgStore) SavePreview(ctx context.Context, p *Preview, snapshot *Snapshot) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackSync(tx)
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_previews(id,org_id,project_id,revision,body,snapshot,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, p.ID, p.OrgID, p.ProjectID, p.Revision, body, raw, p.CreatedAt)
	if err != nil {
		return storeError(err)
	}
	for _, id := range snapshot.Connections {
		_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_preview_connections(org_id,preview_id,connection_id) SELECT $1,$2,id FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$3 ON CONFLICT DO NOTHING`, p.OrgID, p.ID, id)
		if err != nil {
			return storeError(err)
		}
	}
	return tx.Commit()
}

// GetPreview returns only a review in the requested project.
func (s *PgStore) GetPreview(ctx context.Context, org, project, id string) (*Preview, *Snapshot, error) {
	var body, raw []byte
	err := s.db.QueryRow(ctx, `SELECT body,snapshot FROM loomplane_cloud_previews WHERE org_id=$1 AND project_id=$2 AND id=$3`, org, project, id).Scan(&body, &raw)
	if err != nil {
		return nil, nil, storeError(err)
	}
	var p Preview
	var snap Snapshot
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, nil, err
	}
	return &p, &snap, nil
}

func rollbackSync(tx driver.Tx) { _ = tx.Rollback() } //nolint:errcheck // a no-op once committed

// Accept changes desired state and records pending work in one transaction.
// A committed sync is itself an outbox entry; recovery scans pending/running rows.
func (s *PgStore) Accept(ctx context.Context, p *Preview, snapshot *Snapshot, sync *Sync, key string) (*Sync, error) {
	if key == "" || len(key) > 200 || len(p.Blockers) > 0 {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollbackSync(tx)
	// Project lock orders overlapping multi-environment changes and idempotent calls.
	var project string
	if err = tx.QueryRow(ctx, `SELECT id FROM loomplane_projects WHERE org_id=$1 AND id=$2 FOR UPDATE`, p.OrgID, p.ProjectID).Scan(&project); err != nil {
		return nil, storeError(err)
	}
	existing, _, e := readSync(tx.QueryRow(ctx, `SELECT body,snapshot,version FROM loomplane_cloud_syncs WHERE org_id=$1 AND project_id=$2 AND (idempotency_key=$3 OR preview_id=$4) ORDER BY (idempotency_key=$3) DESC LIMIT 1`, p.OrgID, p.ProjectID, key, p.ID))
	if e == nil {
		if existing.PreviewID != p.ID {
			return nil, ErrConflict
		}
		return existing, nil
	}
	if !errors.Is(e, ErrNotFound) {
		return nil, e
	}
	var storedRevision string
	if err = tx.QueryRow(ctx, `SELECT revision FROM loomplane_cloud_previews WHERE org_id=$1 AND project_id=$2 AND id=$3 AND created_at>now()-interval '24 hours'`, p.OrgID, p.ProjectID, p.ID).Scan(&storedRevision); err != nil {
		return nil, storeError(err)
	}
	if storedRevision != p.Revision {
		return nil, ErrConflict
	}
	var originalSnapshot *Snapshot
	if p.Reverts != "" {
		// A running cleanup owns the same lock. Review again once it settles.
		var acquired bool
		if lockErr := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, "cloud-sync/"+p.Reverts).Scan(&acquired); lockErr != nil {
			return nil, lockErr
		}
		if !acquired {
			return nil, ErrBusy
		}
		original, retained, readErr := readSync(tx.QueryRow(ctx, `SELECT body,snapshot,version FROM loomplane_cloud_syncs WHERE org_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, p.OrgID, p.ProjectID, p.Reverts))
		if readErr != nil {
			return nil, readErr
		}
		if original.Status != SyncFailed && original.Status != SyncRouting && original.Status != SyncCleanup && original.Status != SyncSucceeded {
			return nil, ErrConflict
		}
		originalSnapshot = retained
	}

	for _, id := range slices.Sorted(maps.Keys(snapshot.ProfileRevisions)) {
		var revision int64
		if err = tx.QueryRow(ctx, `SELECT revision FROM loomplane_cloud_profiles WHERE org_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, p.OrgID, p.ProjectID, id).Scan(&revision); err != nil {
			return nil, storeError(err)
		}
		if revision != snapshot.ProfileRevisions[id] {
			return nil, ErrConflict
		}
	}
	for _, id := range slices.Sorted(maps.Keys(snapshot.TargetRevisions)) {
		var revision int64
		if err = tx.QueryRow(ctx, `SELECT revision FROM loomplane_cloud_targets WHERE org_id=$1 AND project_id=$2 AND id=$3 FOR SHARE`, p.OrgID, p.ProjectID, id).Scan(&revision); err != nil {
			return nil, storeError(err)
		}
		if revision != snapshot.TargetRevisions[id] {
			return nil, ErrConflict
		}
	}
	if p.Input.TargetID != "" {
		var rev int64
		if err = tx.QueryRow(ctx, `SELECT revision FROM loomplane_cloud_profiles WHERE org_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, p.OrgID, p.ProjectID, p.Input.CloudID).Scan(&rev); err != nil {
			return nil, storeError(err)
		}
		var current []byte
		if err = tx.QueryRow(ctx, `SELECT to_jsonb(t) FROM loomplane_cloud_targets t WHERE org_id=$1 AND project_id=$2 AND cloud_id=$3 AND id=$4 FOR UPDATE`, p.OrgID, p.ProjectID, p.Input.CloudID, p.Input.TargetID).Scan(&current); err != nil {
			return nil, storeError(err)
		}
		var target Target
		if decodeErr := json.Unmarshal(current, &target); decodeErr != nil {
			return nil, decodeErr
		}
		if target.Revision != p.Input.Revision || p.Before == nil || target.ConnectionID != p.Before.ConnectionID || target.Provider != p.Before.Provider || target.Region != p.Before.Region {
			return nil, ErrConflict
		}
	}
	if lockErr := validateLockedConnections(ctx, tx, p.OrgID, snapshot); lockErr != nil {
		return nil, lockErr
	}
	envs := slices.Clone(snapshot.Environments)
	slices.SortFunc(envs, func(a, b EnvironmentSnapshot) int {
		if a.EnvironmentID < b.EnvironmentID {
			return -1
		}
		if a.EnvironmentID > b.EnvironmentID {
			return 1
		}
		return 0
	})
	for _, env := range envs {
		var id string
		if err = tx.QueryRow(ctx, `SELECT id FROM loomplane_environments WHERE org_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, p.OrgID, p.ProjectID, env.EnvironmentID).Scan(&id); err != nil {
			return nil, storeError(err)
		}
		var active bool
		if queryErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_environment_sync_guards WHERE org_id=$1 AND environment_id=$2 AND sync_id<>$3) OR EXISTS(SELECT 1 FROM loomplane_deployments WHERE org_id=$1 AND environment_id=$2 AND status='running') OR (NOT EXISTS(SELECT 1 FROM loomplane_environment_sync_guards WHERE org_id=$1 AND environment_id=$2 AND sync_id=$3) AND EXISTS(SELECT 1 FROM loomplane_plans p WHERE p.org_id=$1 AND p.environment_id=$2 AND p.status='approved' AND coalesce(p.result->>'cloud_sync_id','')='' AND NOT EXISTS(SELECT 1 FROM loomplane_deployments d WHERE d.plan_id=p.id)))`, p.OrgID, env.EnvironmentID, p.Reverts).Scan(&active); queryErr != nil {
			return nil, queryErr
		}
		if active {
			return nil, ErrBusy
		}
		var generation int64
		if queryErr := tx.QueryRow(ctx, `SELECT coalesce((SELECT revision FROM loomplane_cloud_assignments WHERE org_id=$1 AND project_id=$2 AND environment_id=$3),0)`, p.OrgID, p.ProjectID, env.EnvironmentID).Scan(&generation); queryErr != nil {
			return nil, queryErr
		}
		if generation != env.AssignmentRevision {
			return nil, ErrConflict
		}
		if _, lockErr := tx.Exec(ctx, `SELECT id FROM loomplane_bindings WHERE org_id=$1 AND environment_id=$2 ORDER BY id FOR UPDATE`, p.OrgID, env.EnvironmentID); lockErr != nil {
			return nil, lockErr
		}
		var equal bool
		if queryErr := tx.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(b)-'observed_at'-'drift_message' ORDER BY b.id),'[]'::jsonb)=$3::jsonb FROM loomplane_bindings b WHERE org_id=$1 AND environment_id=$2`, p.OrgID, env.EnvironmentID, []byte(env.Bindings)).Scan(&equal); queryErr != nil {
			return nil, queryErr
		}
		if !equal {
			return nil, ErrConflict
		}
		if originalSnapshot != nil {
			var original *EnvironmentSnapshot
			for i := range originalSnapshot.Environments {
				if originalSnapshot.Environments[i].EnvironmentID == env.EnvironmentID {
					original = &originalSnapshot.Environments[i]
				}
			}
			if original == nil {
				return nil, ErrConflict
			}
			if compareErr := tx.QueryRow(ctx, CompareCompensationSQL, p.OrgID, p.Reverts, []byte(original.Bindings), env.EnvironmentID).Scan(&equal); compareErr != nil {
				return nil, compareErr
			}
			if !equal {
				return nil, ErrConflict
			}
		}
	}
	if p.Input.TargetID != "" {
		// Check the full assignment set too, including a newly assigned empty environment.
		var ids []string
		rows, qerr := tx.Query(ctx, `SELECT environment_id FROM loomplane_cloud_assignments WHERE org_id=$1 AND project_id=$2 AND cloud_id=$3 ORDER BY environment_id`, p.OrgID, p.ProjectID, p.Input.CloudID)
		if qerr != nil {
			return nil, qerr
		}
		for rows.Next() {
			var id string
			if qerr = rows.Scan(&id); qerr != nil {
				rows.Close()
				return nil, qerr
			}
			ids = append(ids, id)
		}
		qerr = rows.Err()
		rows.Close()
		if qerr != nil {
			return nil, qerr
		}
		reviewed := make([]string, 0, len(envs))
		for _, env := range envs {
			reviewed = append(reviewed, env.EnvironmentID)
		}
		if !slices.Equal(ids, reviewed) {
			return nil, ErrConflict
		}
		_, err = tx.Exec(ctx, `UPDATE loomplane_cloud_targets SET provider=$5,connection_id=$6,region=$7,revision=revision+1,updated_at=$8 WHERE org_id=$1 AND project_id=$2 AND cloud_id=$3 AND id=$4`, p.OrgID, p.ProjectID, p.Input.CloudID, p.Input.TargetID, p.After.Provider, p.After.ConnectionID, p.After.Region, sync.CreatedAt)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_assignments(org_id,project_id,environment_id,cloud_id,revision) VALUES($1,$2,$3,nullif($4,''),1) ON CONFLICT(org_id,project_id,environment_id) DO UPDATE SET cloud_id=EXCLUDED.cloud_id,revision=loomplane_cloud_assignments.revision+1`, p.OrgID, p.ProjectID, p.Input.EnvironmentID, p.Input.CloudID)
	}
	if err != nil {
		return nil, storeError(err)
	}
	if p.Reverts != "" {
		_, err = tx.Exec(ctx, `UPDATE loomplane_cloud_syncs SET status='reverting',body=jsonb_set(body,'{status}','"reverting"'),version=version+1,updated_at=$4 WHERE org_id=$1 AND project_id=$2 AND id=$3`, p.OrgID, p.ProjectID, p.Reverts, sync.CreatedAt)
		if err != nil {
			return nil, err
		}
	}
	body, err := json.Marshal(sync)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_syncs(id,org_id,project_id,preview_id,idempotency_key,status,target_id,cloud_id,body,snapshot,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)`, sync.ID, p.OrgID, p.ProjectID, p.ID, key, sync.Status, sync.TargetID, sync.CloudID, body, raw, sync.CreatedAt)
	if err != nil {
		return nil, storeError(err)
	}
	for _, env := range envs {
		_, err = tx.Exec(ctx, `INSERT INTO loomplane_environment_sync_guards(org_id,project_id,environment_id,sync_id) VALUES($1,$2,$3,$4) ON CONFLICT(org_id,environment_id) DO UPDATE SET sync_id=EXCLUDED.sync_id`, p.OrgID, p.ProjectID, env.EnvironmentID, sync.ID)
		if err != nil {
			return nil, err
		}
	}
	for _, id := range snapshot.Connections {
		if id == "" {
			continue
		}
		_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_sync_connections(org_id,sync_id,connection_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, p.OrgID, sync.ID, id)
		if err != nil {
			return nil, storeError(err)
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM loomplane_cloud_preview_connections WHERE preview_id=$1`, p.ID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	sync.Version = 1
	return sync, nil
}

func readSync(row pgx.Row) (*Sync, *Snapshot, error) {
	var body, raw []byte
	var version int64
	if err := row.Scan(&body, &raw, &version); err != nil {
		return nil, nil, storeError(err)
	}
	var sync Sync
	var snapshot Snapshot
	if err := json.Unmarshal(body, &sync); err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, nil, err
	}
	sync.Version = version
	sync.Actor = snapshot.Actor
	return &sync, &snapshot, nil
}

// GetSync reads a sync and its retained migration inputs.
func (s *PgStore) GetSync(ctx context.Context, org, project, id string) (*Sync, *Snapshot, error) {
	return readSync(s.db.QueryRow(ctx, `SELECT body,snapshot,version FROM loomplane_cloud_syncs WHERE org_id=$1 AND project_id=$2 AND id=$3`, org, project, id))
}

// ListSyncs lists a project's durable operation history.
func (s *PgStore) ListSyncs(ctx context.Context, org, project string) ([]*Sync, error) {
	return s.listSyncRows(ctx, `SELECT body,snapshot,version FROM loomplane_cloud_syncs WHERE org_id=$1 AND project_id=$2 ORDER BY created_at DESC LIMIT 100`, org, project)
}

// PendingSyncs is the durable outbox read on startup and periodically.
func (s *PgStore) PendingSyncs(ctx context.Context) ([]*Sync, error) {
	if _, err := s.db.Exec(ctx, `DELETE FROM loomplane_cloud_preview_connections r USING loomplane_cloud_previews p WHERE r.preview_id=p.id AND p.created_at<now()-interval '24 hours'`); err != nil {
		return nil, err
	}

	return s.listSyncRows(ctx, `SELECT body,snapshot,version FROM loomplane_cloud_syncs WHERE status IN ('pending','running','cleanup_pending') ORDER BY created_at LIMIT 100`)
}
func (s *PgStore) listSyncRows(ctx context.Context, query string, args ...any) ([]*Sync, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Sync{}
	for rows.Next() {
		sync, _, e := readSync(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, sync)
	}
	return out, rows.Err()
}

// SaveSync checkpoints an operation using compare-and-set; terminal success
// releases guards and credential references in the same transaction.
func (s *PgStore) SaveSync(ctx context.Context, sync *Sync, snapshot *Snapshot) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackSync(tx)
	sync.UpdatedAt = time.Now().UTC()
	body, err := json.Marshal(sync)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE loomplane_cloud_syncs SET status=$4,body=$5,snapshot=$6,version=version+1,updated_at=$7 WHERE org_id=$1 AND project_id=$2 AND id=$3 AND version=$8`, sync.OrgID, sync.ProjectID, sync.ID, sync.Status, body, raw, sync.UpdatedAt, sync.Version)
	if err != nil {
		return err
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	if sync.Status == SyncSucceeded {
		for _, query := range []string{`DELETE FROM loomplane_environment_sync_guards WHERE sync_id=$1`, `DELETE FROM loomplane_cloud_sync_connections WHERE sync_id=$1`} {
			if _, err = tx.Exec(ctx, query, sync.ID); err != nil {
				return err
			}
		}
		if sync.Reverts != "" {
			if _, err = tx.Exec(ctx, `UPDATE loomplane_cloud_syncs SET status='reverted',body=jsonb_set(body,'{status}','"reverted"'),version=version+1 WHERE id=$1 AND org_id=$2 AND project_id=$3`, sync.Reverts, sync.OrgID, sync.ProjectID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `DELETE FROM loomplane_cloud_sync_connections WHERE sync_id=$1`, sync.Reverts); err != nil {
				return err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("checkpoint Cloud sync: %w", err)
	}
	sync.Version++
	return nil
}

// SnapshotBindings includes the replacement chain and all applied provider inputs.
func (s *PgStore) SnapshotBindings(ctx context.Context, org, env string) (json.RawMessage, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(b)-'observed_at'-'drift_message' ORDER BY b.id),'[]'::jsonb) FROM loomplane_bindings b WHERE org_id=$1 AND environment_id=$2`, org, env).Scan(&raw)
	return raw, err
}

// WithSyncLock prevents duplicate activities from executing an operation together.
// PostgreSQL releases the transaction advisory lock when the worker disconnects.
func (s *PgStore) WithSyncLock(ctx context.Context, id string, run func() error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackSync(tx)
	var acquired bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, "cloud-sync/"+id).Scan(&acquired); err != nil {
		return err
	}
	if !acquired {
		return ErrBusy
	}
	return run()
}

// PendingReleasePlans includes approvals whose original workflow ended while a
// sync held the environment. Running deployments cover commit/enqueue recovery.
func (s *PgStore) PendingReleasePlans(ctx context.Context) ([][2]string, error) {
	rows, err := s.db.Query(ctx, `SELECT p.org_id,p.id FROM loomplane_plans p LEFT JOIN loomplane_deployments d ON d.plan_id=p.id WHERE p.status='approved' AND coalesce(p.result->>'cloud_sync_id','')='' AND (d.id IS NULL OR d.status='running') ORDER BY p.created_at LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := [][2]string{}
	for rows.Next() {
		var ids [2]string
		if err := rows.Scan(&ids[0], &ids[1]); err != nil {
			return nil, err
		}
		out = append(out, ids)
	}
	return out, rows.Err()
}

// FindAccepted returns the operation for an idempotency key or accepted review.
func (s *PgStore) FindAccepted(ctx context.Context, org, project, key, preview string) (*Sync, error) {
	sync, _, err := readSync(s.db.QueryRow(ctx, `SELECT body,snapshot,version FROM loomplane_cloud_syncs WHERE org_id=$1 AND project_id=$2 AND (idempotency_key=$3 OR preview_id=$4) ORDER BY (idempotency_key=$3) DESC LIMIT 1`, org, project, key, preview))
	if err != nil {
		return nil, err
	}
	if sync.PreviewID != preview {
		return nil, ErrConflict
	}
	return sync, nil
}
