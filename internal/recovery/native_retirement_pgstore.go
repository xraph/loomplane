package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
)

const nativeRetirementColumns = `id,org_id,project_id,source_id,point_id,witness_id,status,policy,created_by,created_at,completed_at,error,request_id,token,attempted,source,point,witness,policy_id,policy_revision`

type nativeRetentionReader interface {
	QueryRow(context.Context, string, ...any) driver.Row
	Query(context.Context, string, ...any) (driver.Rows, error)
}

func scanNativeRetirement(row pgx.Row) (*NativeRetirement, error) {
	var r NativeRetirement
	var policy, source, point, witness []byte
	if err := row.Scan(&r.ID, &r.OrgID, &r.ProjectID, &r.SourceID, &r.PointID, &r.WitnessID, &r.Status, &policy, &r.CreatedBy, &r.CreatedAt, &r.CompletedAt, &r.Error, &r.RequestID, &r.Token, &r.Attempted, &source, &point, &witness, &r.PolicyID, &r.PolicyRevision); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	for _, v := range []struct {
		raw []byte
		out any
	}{{policy, &r.Policy}, {source, &r.Source}, {point, &r.Point}, {witness, &r.Witness}} {
		if decodeErr := json.Unmarshal(v.raw, v.out); decodeErr != nil {
			return nil, decodeErr
		}
	}
	if r.Source.OrgID != r.OrgID || r.Source.ProjectID != r.ProjectID || r.Source.PhysicalID == "" || r.Point.SourceID != r.SourceID || r.Point.ID != r.PointID || r.Witness.SourceID != r.SourceID || r.Witness.ID != r.WitnessID {
		return nil, ErrInvalid
	}
	return &r, nil
}

func readNativeRetention(ctx context.Context, db nativeRetentionReader, org, sourceID string) (*NativeSource, []NativeRetentionEntry, error) {
	source, err := scanNativeSource(db.QueryRow(ctx, `SELECT `+nativeSourceColumns+` FROM loomplane_native_sources WHERE org_id=$1 AND id=$2 FOR SHARE`, org, sourceID))
	if err != nil {
		return nil, nil, err
	}
	rows, err := db.Query(ctx, `SELECT p.id,p.source_id,p.body,p.present,p.last_seen_at,
 EXISTS(SELECT 1 FROM loomplane_native_restores r WHERE r.org_id=p.org_id AND r.source_id=p.source_id AND r.point_id=p.id AND r.status<>'deleted'),
 EXISTS(SELECT 1 FROM loomplane_native_retirements d WHERE d.org_id=p.org_id AND d.source_id=p.source_id AND d.point_id=p.id),
 EXISTS(SELECT 1 FROM loomplane_native_retirements d WHERE d.org_id=p.org_id AND d.source_id=p.source_id AND d.witness_id=p.id AND d.status<>'succeeded'),
 (SELECT max(r.verified_at) FROM loomplane_native_restores r WHERE r.org_id=p.org_id AND r.source_id=p.source_id AND r.point_id=p.id AND r.verified_at IS NOT NULL AND r.status IN ('succeeded','deleted') AND r.physical_id<>'' AND r.external_id<>'' AND r.source->>'physical_id'=$3 AND r.point->>'provider_id'=p.provider_id AND r.point->>'created_at'=p.body->>'created_at')
 FROM loomplane_native_points p WHERE p.org_id=$1 AND p.source_id=$2 AND (
 p.present OR EXISTS(SELECT 1 FROM loomplane_native_retirements d WHERE d.org_id=p.org_id AND d.source_id=p.source_id AND d.status<>'succeeded' AND (d.point_id=p.id OR d.witness_id=p.id))
 OR EXISTS(SELECT 1 FROM loomplane_native_restores r WHERE r.org_id=p.org_id AND r.source_id=p.source_id AND r.point_id=p.id AND r.status<>'deleted'))
 ORDER BY p.creation_identity DESC,p.id DESC LIMIT 1001`, org, sourceID, source.PhysicalID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	entries := []NativeRetentionEntry{}
	for rows.Next() {
		var e NativeRetentionEntry
		var raw []byte
		var id, source string
		var present bool
		var seen time.Time
		if scanErr := rows.Scan(&id, &source, &raw, &present, &seen, &e.InUse, &e.Retired, &e.WitnessProtected, &e.VerifiedAt); scanErr != nil {
			return nil, nil, scanErr
		}
		if decodeErr := json.Unmarshal(raw, &e.Point); decodeErr != nil {
			return nil, nil, decodeErr
		}
		e.Point.ID, e.Point.SourceID, e.Point.Present, e.Point.LastSeenAt = id, source, present, seen.UTC()
		entries = append(entries, e)
	}
	return source, entries, rows.Err()
}

func nativeRetentionComplete(source *NativeSource, now time.Time) bool {
	return source != nil && source.State == "ready" && source.Complete && source.ObservedAt != nil && !source.ObservedAt.After(now) && source.ObservedAt.After(now.Add(-5*time.Minute))
}

func (s *PgStore) NativeRetentionSnapshot(ctx context.Context, org, sourceID string) (*NativeSource, []NativeRetentionEntry, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // A no-op after commit.
	source, entries, err := readNativeRetention(ctx, tx, org, sourceID)
	if err != nil {
		return nil, nil, err
	}
	return source, entries, tx.Commit()
}

func (s *PgStore) GetNativeRetirement(ctx context.Context, org, id string) (*NativeRetirement, error) {
	return scanNativeRetirement(s.db.QueryRow(ctx, `SELECT `+nativeRetirementColumns+` FROM loomplane_native_retirements WHERE org_id=$1 AND id=$2`, org, id))
}

func appendNativeRetirementEvent(ctx context.Context, db driver.Tx, r *NativeRetirement, kind string) error {
	_, err := db.Exec(ctx, `INSERT INTO loomplane_native_retirement_events(org_id,retirement_id,kind,status,actor,error) VALUES($1,$2,$3,$4,$5,$6)`, r.OrgID, r.ID, kind, r.Status, r.CreatedBy, r.Error)
	return err
}

func (s *PgStore) AdmitNativeRetirement(ctx context.Context, in *NativeRetirement, guard RetirementGuard) (*NativeRetirement, error) {
	if in == nil || guard == nil || in.ID == "" || in.OrgID == "" || in.ProjectID == "" || in.SourceID == "" || in.PointID == "" || in.CreatedBy == "" || !requestKey.MatchString(in.RequestID) {
		return nil, ErrInvalid
	}
	if checkErr := in.Policy.Validate(); checkErr != nil {
		return nil, checkErr
	}
	if authorityErr := guard(ctx); authorityErr != nil {
		return nil, authorityErr
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // A no-op after commit.
	source, err := scanNativeSource(tx.QueryRow(ctx, `SELECT `+nativeSourceColumns+` FROM loomplane_native_sources WHERE org_id=$1 AND id=$2 AND project_id=$3 FOR UPDATE`, in.OrgID, in.SourceID, in.ProjectID))
	if err != nil {
		return nil, err
	}
	existing, err := scanNativeRetirement(tx.QueryRow(ctx, `SELECT `+nativeRetirementColumns+` FROM loomplane_native_retirements WHERE org_id=$1 AND source_id=$2 AND request_id=$3`, in.OrgID, in.SourceID, in.RequestID))
	if err == nil {
		if existing.PointID != in.PointID || existing.CreatedBy != in.CreatedBy || existing.Policy != in.Policy || existing.PolicyID != in.PolicyID || existing.PolicyRevision != in.PolicyRevision {
			return nil, ErrConflict
		}
		if authorityErr := guard(ctx); authorityErr != nil {
			return nil, authorityErr
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if checkErr := checkNativeRetentionGrant(ctx, tx, in); checkErr != nil {
		return nil, checkErr
	}
	var busy bool
	if scanErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_native_retirements WHERE org_id=$1 AND source_id=$2 AND status<>'succeeded')`, in.OrgID, in.SourceID).Scan(&busy); scanErr != nil {
		return nil, scanErr
	}
	if busy {
		return nil, ErrConflict
	}
	_, entries, err := readNativeRetention(ctx, tx, in.OrgID, in.SourceID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	preview, err := previewNativeRetention(now, source.Provider, in.Policy, entries, nativeRetentionComplete(source, now))
	if err != nil {
		return nil, err
	}
	eligible := false
	for _, p := range preview.Points {
		if p.BackupID == in.PointID && p.Eligible {
			eligible = true
		}
	}
	if !eligible || preview.VerifiedBackupID == "" {
		return nil, ErrConflict
	}
	r := *in
	r.Status, r.CreatedAt, r.Source, r.WitnessID = "queued", now, source.Binding, preview.VerifiedBackupID
	for _, e := range entries {
		if e.Point.ID == r.PointID {
			r.Point = e.Point
		}
		if e.Point.ID == r.WitnessID {
			r.Witness = e.Point
		}
	}
	raw := make([][]byte, 4)
	for i, v := range []any{r.Policy, r.Source, r.Point, r.Witness} {
		encoded, encodeErr := json.Marshal(v)
		if encodeErr != nil {
			return nil, encodeErr
		}
		raw[i] = encoded
	}
	if authorityErr := guard(ctx); authorityErr != nil {
		return nil, authorityErr
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_native_retirements(id,org_id,project_id,source_id,point_id,witness_id,request_id,created_by,created_at,status,policy,source,point,witness,policy_id,policy_revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'queued',$10,$11,$12,$13,$14,$15)`, r.ID, r.OrgID, r.ProjectID, r.SourceID, r.PointID, r.WitnessID, r.RequestID, r.CreatedBy, r.CreatedAt, raw[0], raw[1], raw[2], raw[3], r.PolicyID, r.PolicyRevision)
	if err != nil {
		return nil, err
	}
	if eventErr := appendNativeRetirementEvent(ctx, tx, &r, "admitted"); eventErr != nil {
		return nil, eventErr
	}
	return &r, tx.Commit()
}

func (s *PgStore) nativeRetirementTransition(ctx context.Context, kind, query string, args ...any) (*NativeRetirement, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // A no-op after commit.
	r, err := scanNativeRetirement(tx.QueryRow(ctx, query+` RETURNING `+nativeRetirementColumns, args...))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	if eventErr := appendNativeRetirementEvent(ctx, tx, r, kind); eventErr != nil {
		return nil, eventErr
	}
	return r, tx.Commit()
}

func (s *PgStore) ClaimNativeRetirement(ctx context.Context, org, id, token string) (*NativeRetirement, error) {
	if token == "" {
		return nil, ErrInvalid
	}
	return s.nativeRetirementTransition(ctx, "claimed", `UPDATE loomplane_native_retirements SET status='running',token=$3,lease_until=now()+interval '10 minutes',error='' WHERE org_id=$1 AND id=$2 AND token<>$3 AND (status='queued' OR status='running' AND lease_until<now())`, org, id, token)
}

func (s *PgStore) CheckNativeRetirement(ctx context.Context, r *NativeRetirement) error {
	if r == nil || r.Token == "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // A no-op after commit.
	source, entries, err := readNativeRetention(ctx, tx, r.OrgID, r.SourceID)
	if err != nil {
		return err
	}
	current, err := scanNativeRetirement(tx.QueryRow(ctx, `SELECT `+nativeRetirementColumns+` FROM loomplane_native_retirements WHERE org_id=$1 AND id=$2 AND token=$3 AND status='running' AND lease_until>now() FOR SHARE`, r.OrgID, r.ID, r.Token))
	if errors.Is(err, ErrNotFound) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	captured, err := nativeSourceDigest(r.Source)
	if err != nil {
		return err
	}
	observed, err := nativeSourceDigest(source.Binding)
	if err != nil {
		return err
	}
	if captured != observed || current.SourceID != r.SourceID || current.PointID != r.PointID || current.WitnessID != r.WitnessID || current.CreatedBy != r.CreatedBy {
		return ErrConflict
	}
	now := time.Now().UTC()
	if !nativeRetentionComplete(source, now) {
		return ErrNativeUnavailable
	}
	witnessOK := false
	var candidate *NativeRetentionEntry
	kept := 0
	for i := range entries {
		e := &entries[i]
		if e.Point.ID == r.PointID {
			candidate = e
			if e.InUse || !sameNativePointIdentity(e.Point, r.Point) {
				return ErrConflict
			}
			e.Retired = false
		} else if usableNativeRetentionPoint(now, source.Provider, e.Point) && !e.Retired {
			kept++
		}
		if e.Point.ID == r.WitnessID && sameNativePointIdentity(e.Point, r.Witness) && usableNativeRetentionPoint(now, source.Provider, e.Point) && !e.Retired && e.VerifiedAt != nil && !e.VerifiedAt.Before(e.Point.CreatedAt) && !e.VerifiedAt.After(now) && r.Point.CreatedAt.Before(e.Point.CreatedAt) {
			witnessOK = true
		}
	}
	if candidate == nil || !witnessOK || kept < r.Policy.KeepLast {
		return ErrConflict
	}
	if !current.Attempted && candidate.Point.Present {
		if !usableNativeRetentionPoint(now, source.Provider, candidate.Point) {
			return ErrNativePending
		}
		preview, err := previewNativeRetention(now, source.Provider, r.Policy, entries, true)
		if err != nil {
			return err
		}
		eligible := false
		for _, p := range preview.Points {
			if p.BackupID == r.PointID && p.Eligible {
				eligible = true
			}
		}
		if !eligible {
			return ErrConflict
		}
	}
	return tx.Commit()
}

func (s *PgStore) SaveNativeRetirementIntent(ctx context.Context, r *NativeRetirement, attempted bool) error {
	if r == nil {
		return ErrInvalid
	}
	_, err := s.nativeRetirementTransition(ctx, "delete_intent", `UPDATE loomplane_native_retirements SET attempted=$4 WHERE org_id=$1 AND id=$2 AND token=$3 AND status='running' AND lease_until>now()`, r.OrgID, r.ID, r.Token, attempted)
	return err
}

func (s *PgStore) FinishNativeRetirement(ctx context.Context, r *NativeRetirement, cause error) error {
	if r == nil {
		return ErrInvalid
	}
	status, message := "succeeded", ""
	if errors.Is(cause, ErrNativePending) {
		status, message = "queued", "Deletion is waiting for reconciliation. The point stays blocked from new restores."
	} else if cause != nil {
		status, message = "failed", "Native backup deletion needs attention. The point remains excluded from new restores."
	}
	_, err := s.nativeRetirementTransition(ctx, "finished", `UPDATE loomplane_native_retirements SET status=$4,error=$5,completed_at=CASE WHEN $4='succeeded' THEN now() ELSE NULL END,lease_until=NULL WHERE org_id=$1 AND id=$2 AND token=$3 AND status='running' AND lease_until>now() AND ($4<>'succeeded' OR attempted)`, r.OrgID, r.ID, r.Token, status, message)
	return err
}

func (s *PgStore) RetryNativeRetirement(ctx context.Context, org, id, actor string, guard RetirementGuard) (*NativeRetirement, error) {
	if actor == "" || guard == nil {
		return nil, ErrInvalid
	}
	if authorityErr := guard(ctx); authorityErr != nil {
		return nil, authorityErr
	}
	return s.nativeRetirementTransition(ctx, "retried", `UPDATE loomplane_native_retirements SET status='queued',error='' WHERE org_id=$1 AND id=$2 AND created_by=$3 AND status='failed'`, org, id, actor)
}

func (s *PgStore) PendingNativeRetirements(ctx context.Context, page NativeCopyPage) ([]NativeRetirement, error) {
	if page.Through.IsZero() {
		page.Through = time.Now().UTC()
	}
	rows, err := s.db.Query(ctx, `SELECT `+nativeRetirementColumns+` FROM loomplane_native_retirements WHERE (status='queued' OR status='running' AND lease_until<now()) AND created_at<=$1 AND (created_at,id)>($2,$3) ORDER BY created_at,id LIMIT 100`, page.Through, page.AfterCreated, page.AfterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NativeRetirement{}
	for rows.Next() {
		r, err := scanNativeRetirement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *PgStore) NativeRetirementByRequest(ctx context.Context, org, source, request string) (*NativeRetirement, error) {
	return scanNativeRetirement(s.db.QueryRow(ctx, `SELECT `+nativeRetirementColumns+` FROM loomplane_native_retirements WHERE org_id=$1 AND source_id=$2 AND request_id=$3`, org, source, request))
}
