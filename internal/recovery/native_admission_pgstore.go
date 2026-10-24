package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
)

type nativeTargetReader interface {
	QueryRow(context.Context, string, ...any) driver.Row
}

func nativeSourceDigest(source planning.Binding) (string, error) {
	raw, err := json.Marshal(source)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// ReviewNativeTarget derives placement from a named target granted to this
// project. A project recovery permission never grants arbitrary account access.
func (s *PgStore) ReviewNativeTarget(ctx context.Context, org, project, cloud, target string) (NativeRestoreTarget, error) {
	return readNativeTarget(ctx, s.db, org, project, cloud, target, false)
}

func readNativeTarget(ctx context.Context, db nativeTargetReader, org, project, cloud, target string, lock bool) (NativeRestoreTarget, error) {
	if org == "" || project == "" || cloud == "" || target == "" {
		return NativeRestoreTarget{}, ErrInvalid
	}
	query := `SELECT p.revision,t.revision,g.revision,t.provider,t.runtime,coalesce(t.pool_id,''),c.id,c.cloud,c.account,c.status,c.details,t.region
 FROM loomplane_cloud_profiles p
 JOIN loomplane_cloud_project_grants g ON g.org_id=p.org_id AND g.cloud_id=p.id
 JOIN loomplane_cloud_targets t ON t.org_id=p.org_id AND t.cloud_id=p.id
 JOIN loomplane_cloud_connections c ON c.org_id=t.org_id AND c.id=t.connection_id
 WHERE p.org_id=$1 AND g.project_id=$2 AND p.id=$3 AND t.id=$4 AND NOT g.revoked AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids))`
	if lock {
		query += ` FOR SHARE OF p,g,t,c`
	}
	out := NativeRestoreTarget{CloudID: cloud, TargetID: target}
	var runtime, pool, cloudKind, account, status string
	var raw []byte
	if err := db.QueryRow(ctx, query, org, project, cloud, target).Scan(&out.CloudRevision, &out.TargetRevision, &out.GrantRevision, &out.Provider, &runtime, &pool, &out.ConnectionID, &cloudKind, &account, &status, &raw, &out.Region); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return NativeRestoreTarget{}, ErrNotFound
		}
		return NativeRestoreTarget{}, err
	}
	var details map[string]string
	if err := json.Unmarshal(raw, &details); err != nil {
		return NativeRestoreTarget{}, err
	}

	out.AccountIdentity = connections.AccountIdentity(cloudKind, account, details)
	if runtime != "managed" || pool != "" || status != connections.StatusReady || out.AccountIdentity == "" || strings.TrimSpace(out.Region) == "" || !nativeTargetPinned(out) {
		return NativeRestoreTarget{}, ErrInvalid
	}
	switch out.Provider {
	case "cloud-sql":
		out.NativeProject = details["project_id"]
		if cloudKind != connections.GCP || strings.TrimSpace(out.NativeProject) == "" {
			return NativeRestoreTarget{}, ErrInvalid
		}
	case "rds":
		route, routeErr := manageddb.RDSRecoverySettings(details)
		if cloudKind != connections.AWS || details["region"] != out.Region || routeErr != nil || !route.Valid() {
			return NativeRestoreTarget{}, ErrInvalid
		}
		out.NativeAccount, out.RDSNetwork = account, route
	default:
		return NativeRestoreTarget{}, ErrInvalid
	}

	return out, nil
}

func nativeTargetPinned(target NativeRestoreTarget) bool {
	return target.CloudID != "" && target.TargetID != "" && target.CloudRevision > 0 && target.TargetRevision > 0 && target.GrantRevision > 0
}

func nativeTargetHasReview(target NativeRestoreTarget) bool {
	return target.CloudID != "" || target.TargetID != "" || target.CloudRevision != 0 || target.TargetRevision != 0 || target.GrantRevision != 0
}

// CheckNativeTarget fences fresh copy operations after Cloud configuration or
// project grant changes. Retained cleanup does not require a placement grant.
func (s *PgStore) CheckNativeTarget(ctx context.Context, r *NativeRestore) error {
	if r == nil || !nativeTargetPinned(r.Target) {
		return ErrInvalid
	}
	current, err := s.ReviewNativeTarget(ctx, r.OrgID, r.ProjectID, r.Target.CloudID, r.Target.TargetID)
	if err != nil {
		return err
	}
	if current != r.Target {
		return ErrConflict
	}
	return nil
}

// CreateReviewedNativeRestore atomically rechecks target authority and source
// inputs before recording the admitted copy. The source digest is server signed.
func (s *PgStore) CreateReviewedNativeRestore(ctx context.Context, r *NativeRestore, sourceDigest string) (*NativeRestore, error) {
	digest, err := hex.DecodeString(sourceDigest)
	if r == nil || !nativeTargetPinned(r.Target) || err != nil || len(digest) != sha256.Size {
		return nil, ErrInvalid
	}
	return s.createNativeRestore(ctx, r, sourceDigest)
}
