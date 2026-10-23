package recovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/connections"
)

const nativeRestoreColumns = `id,org_id,project_id,environment_id,source_id,point_id,request_id,source,point,target,checkpoint,status,token,external_id,physical_id,error,created_by,created_at,expires_at,verified_at,duration_ms`

func scanNativeRestore(row pgx.Row) (*NativeRestore, error) {
	var r NativeRestore
	var source, point, target []byte
	if err := row.Scan(&r.ID, &r.OrgID, &r.ProjectID, &r.EnvironmentID, &r.SourceID, &r.PointID, &r.RequestID, &source, &point, &target, &r.Checkpoint, &r.Status, &r.Token, &r.ExternalID, &r.PhysicalID, &r.Error, &r.CreatedBy, &r.CreatedAt, &r.ExpiresAt, &r.VerifiedAt, &r.DurationMS); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	for _, item := range []struct {
		raw []byte
		out any
	}{{source, &r.Source}, {point, &r.Point}, {target, &r.Target}} {
		if err := json.Unmarshal(item.raw, item.out); err != nil {
			return nil, err
		}
	}
	return &r, nil
}

func sameNativeRestoreRequest(a, b *NativeRestore) bool {
	return a.OrgID == b.OrgID && a.ProjectID == b.ProjectID && a.EnvironmentID == b.EnvironmentID && a.SourceID == b.SourceID && a.PointID == b.PointID && a.Target == b.Target && sameNativePointIdentity(a.Point, b.Point)
}

func (s *PgStore) CreateNativeRestore(ctx context.Context, r *NativeRestore) (*NativeRestore, error) {
	if r == nil || r.ID == "" || r.OrgID == "" || r.ProjectID == "" || r.EnvironmentID == "" || r.SourceID == "" || r.PointID == "" || r.RequestID == "" || r.CreatedBy == "" || r.CreatedAt.IsZero() || !r.ExpiresAt.After(r.CreatedAt) || r.ExpiresAt.Sub(r.CreatedAt) > 24*time.Hour || r.Point.ID != r.PointID || r.Point.SourceID != r.SourceID {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // A no-op after commit.
	existing, err := scanNativeRestore(tx.QueryRow(ctx, `SELECT `+nativeRestoreColumns+` FROM loomplane_native_restores WHERE org_id=$1 AND project_id=$2 AND request_id=$3`, r.OrgID, r.ProjectID, r.RequestID))
	if err == nil {
		if !sameNativeRestoreRequest(existing, r) {
			return nil, ErrConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	source, err := scanNativeSource(tx.QueryRow(ctx, `SELECT `+nativeSourceColumns+` FROM loomplane_native_sources WHERE org_id=$1 AND id=$2 AND project_id=$3 AND environment_id=$4 FOR SHARE`, r.OrgID, r.SourceID, r.ProjectID, r.EnvironmentID))
	if err != nil {
		return nil, err
	}
	point, err := scanNativePoint(tx.QueryRow(ctx, `SELECT id,source_id,body,present,last_seen_at FROM loomplane_native_points WHERE org_id=$1 AND source_id=$2 AND id=$3 FOR SHARE`, r.OrgID, r.SourceID, r.PointID))
	if err != nil {
		return nil, err
	}
	if !sameNativePointIdentity(*point, r.Point) || source.Provider != r.Target.Provider {
		return nil, ErrConflict
	}
	var cloud, account, status string
	var detailsRaw []byte
	if readErr := tx.QueryRow(ctx, `SELECT cloud,account,status,details FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2 FOR SHARE`, r.OrgID, r.Target.ConnectionID).Scan(&cloud, &account, &status, &detailsRaw); readErr != nil {
		if errors.Is(readErr, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, readErr
	}
	var details map[string]string
	if decodeErr := json.Unmarshal(detailsRaw, &details); decodeErr != nil {
		return nil, decodeErr
	}
	expectedCloud := connections.AWS
	if source.Provider == "cloud-sql" {
		expectedCloud = connections.GCP
	}
	if status != connections.StatusReady || cloud != expectedCloud || r.Target.Region == "" || r.Target.AccountIdentity == "" || connections.AccountIdentity(cloud, account, details) != r.Target.AccountIdentity || cloud == connections.GCP && r.Target.NativeProject != details["project_id"] || cloud == connections.AWS && r.Target.Region != details["region"] {
		return nil, ErrConflict
	}
	sourceRaw, err := json.Marshal(source.Binding)
	if err != nil {
		return nil, err
	}
	pointRaw, err := json.Marshal(r.Point)
	if err != nil {
		return nil, err
	}
	targetRaw, err := json.Marshal(r.Target)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_native_restores(id,org_id,project_id,environment_id,source_id,point_id,request_id,source,point,target,status,created_by,created_at,expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'queued',$11,$12,$13) ON CONFLICT(org_id,project_id,request_id) DO NOTHING`, r.ID, r.OrgID, r.ProjectID, r.EnvironmentID, r.SourceID, r.PointID, r.RequestID, sourceRaw, pointRaw, targetRaw, r.CreatedBy, r.CreatedAt, r.ExpiresAt); err != nil {
		return nil, err
	}
	existing, err = scanNativeRestore(tx.QueryRow(ctx, `SELECT `+nativeRestoreColumns+` FROM loomplane_native_restores WHERE org_id=$1 AND project_id=$2 AND request_id=$3`, r.OrgID, r.ProjectID, r.RequestID))
	if err != nil {
		return nil, err
	}
	if !sameNativeRestoreRequest(existing, r) {
		return nil, ErrConflict
	}
	return existing, tx.Commit()
}

func (s *PgStore) GetNativeRestore(ctx context.Context, org, id string) (*NativeRestore, error) {
	return scanNativeRestore(s.db.QueryRow(ctx, `SELECT `+nativeRestoreColumns+` FROM loomplane_native_restores WHERE org_id=$1 AND id=$2`, org, id))
}

func (s *PgStore) ClaimNativeRestore(ctx context.Context, org, id, token string) (*NativeRestore, error) {
	if token == "" {
		return nil, ErrInvalid
	}
	r, err := scanNativeRestore(s.db.QueryRow(ctx, `UPDATE loomplane_native_restores SET status='running',token=$3,error='',lease_until=now()+interval '50 minutes'
 WHERE org_id=$1 AND id=$2 AND token<>$3 AND expires_at>now() AND (status='queued' OR status='running' AND lease_until<now()) RETURNING `+nativeRestoreColumns, org, id, token))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrConflict
	}
	return r, err
}

func nativeRestoreWrite(result driver.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

func (s *PgStore) SaveNativeCheckpoint(ctx context.Context, r *NativeRestore, state json.RawMessage) error {
	trimmed := bytes.TrimSpace(state)
	if len(trimmed) == 0 || len(trimmed) > 64<<10 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return ErrInvalid
	}
	return nativeRestoreWrite(s.db.Exec(ctx, `UPDATE loomplane_native_restores SET checkpoint=$4 WHERE org_id=$1 AND id=$2 AND token=$3 AND status IN ('running','deleting') AND lease_until>now()`, r.OrgID, r.ID, r.Token, state))
}

func (s *PgStore) FinishNativeRestore(ctx context.Context, r *NativeRestore, result NativeRestoreResult, duration time.Duration, cause error) error {
	status, message := "succeeded", ""
	var verified *time.Time
	if cause == nil {
		if !result.SQLVerified || result.ExternalID == "" || result.PhysicalID == "" || duration < 0 {
			return ErrInvalid
		}
		now := time.Now().UTC()
		verified = &now
	} else {
		status, message = "failed", "Native restore did not finish. Check the saved operation and remove or reconcile the temporary copy."
	}
	return nativeRestoreWrite(s.db.Exec(ctx, `UPDATE loomplane_native_restores SET status=$4,error=$5,external_id=$6,physical_id=$7,verified_at=$8,duration_ms=$9,lease_until=NULL
 WHERE org_id=$1 AND id=$2 AND token=$3 AND status='running' AND lease_until>now()`, r.OrgID, r.ID, r.Token, status, message, result.ExternalID, result.PhysicalID, verified, max(duration.Milliseconds(), 0)))
}

func (s *PgStore) ClaimNativeCleanup(ctx context.Context, org, id, token string) (*NativeRestore, error) {
	if token == "" {
		return nil, ErrInvalid
	}
	r, err := scanNativeRestore(s.db.QueryRow(ctx, `UPDATE loomplane_native_restores SET status='deleting',token=$3,lease_until=now()+interval '10 minutes'
 WHERE org_id=$1 AND id=$2 AND token<>$3 AND (status IN ('succeeded','failed','cleanup_failed') OR status='deleting' AND lease_until<now() OR status='running' AND expires_at<=now() AND lease_until<now() OR status='queued' AND expires_at<=now()) RETURNING `+nativeRestoreColumns, org, id, token))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrConflict
	}
	return r, err
}

func (s *PgStore) FinishNativeCleanup(ctx context.Context, r *NativeRestore, cause error) error {
	status, message := "deleted", ""
	if cause != nil {
		status, message = "cleanup_failed", "The native temporary copy could not be removed. Check the original recovery account and retry cleanup."
	}
	return nativeRestoreWrite(s.db.Exec(ctx, `UPDATE loomplane_native_restores SET status=$4,error=$5,lease_until=CASE WHEN $4='cleanup_failed' THEN now()+interval '1 hour' ELSE NULL END
 WHERE org_id=$1 AND id=$2 AND token=$3 AND status='deleting' AND lease_until>now()`, r.OrgID, r.ID, r.Token, status, message))
}
