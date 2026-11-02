package deployments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	cpauth "github.com/xraph/ctrlplane/auth"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	fc "github.com/xraph/loomplane/internal/firecracker"
)

func firecrackerDigest(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// IssueFirecracker shares a resource counter across create, stop and remove.
// A takeover gets a new operation identity; an old attempt cannot renew it.
func (s *PgStore) IssueFirecracker(ctx context.Context, scope fc.Scope, operation, digest string) (fc.Authority, error) {
	a, ok := cloudinfra.WorkloadAttemptFrom(ctx)
	manual, manualOK := ctx.Value(firecrackerManualKey{}).(firecrackerManual)
	if (!ok && !manualOK) || (ok && (a.Org != scope.Org || a.Project != scope.Project || a.Environment != scope.Environment)) || !scope.Valid() || operation == "" || len(digest) != 64 {
		return fc.Authority{}, fc.ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fc.Authority{}, err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // Rollback is a no-op after the committed transaction.
	if lockErr := cloudprofiles.LockOrganization(ctx, tx, scope.Org); lockErr != nil {
		return fc.Authority{}, lockErr
	}
	if ok {
		if guardErr := cloudinfra.GuardWorkloadWrite(ctx, tx); guardErr != nil {
			return fc.Authority{}, guardErr
		}
		// Locking the deployment also serializes cancellation against new authority.
		var status string
		if queryErr := tx.QueryRow(ctx, `SELECT status FROM loomplane_deployments WHERE org_id=$1 AND id=$2 AND plan_id=$3 FOR UPDATE`, a.Org, a.Deployment, a.Plan).Scan(&status); queryErr != nil {
			return fc.Authority{}, queryErr
		}
		if status != "running" {
			return fc.Authority{}, fc.ErrConflict
		}
	} else {
		claims := cpauth.ClaimsFrom(ctx)
		if claims == nil || claims.TenantID != scope.Org || claims.SubjectID == "" {
			return fc.Authority{}, fc.ErrConflict
		}
		var state string
		var updated time.Time
		err = tx.QueryRow(ctx, `SELECT state,updated_at FROM cp_instances WHERE tenant_id=$1 AND id=$2 AND labels->>'loomplane.connection'=$3 AND labels->>'loomplane.project'=$4 AND labels->>'loomplane.environment'=$5 FOR UPDATE`, scope.Org, scope.Resource, scope.Connection, scope.Project, scope.Environment).Scan(&state, &updated)
		if err != nil {
			return fc.Authority{}, err
		}
		if state != manual.State || !updated.Equal(manual.Updated) || (state != "starting" && state != "stopping") {
			return fc.Authority{}, fc.ErrConflict
		}
		var busy bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_workload_attempts WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND active AND NOT settled)`, scope.Org, scope.Project, scope.Environment).Scan(&busy)
		if err != nil {
			return fc.Authority{}, err
		}
		if busy {
			return fc.Authority{}, fc.ErrConflict
		}
		a.Deployment = manual.State + "/" + manual.Updated.UTC().Format(time.RFC3339Nano)
	}

	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "firecracker/"+scope.Key()); err != nil {
		return fc.Authority{}, err
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return fc.Authority{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_firecracker_resources(resource_key,scope,generation) VALUES($1,$2,0) ON CONFLICT DO NOTHING`, scope.Key(), raw); err != nil {
		return fc.Authority{}, err
	}
	var saved []byte
	var current int64
	if queryErr := tx.QueryRow(ctx, `SELECT scope,generation FROM loomplane_firecracker_resources WHERE resource_key=$1 FOR UPDATE`, scope.Key()).Scan(&saved, &current); queryErr != nil {
		return fc.Authority{}, queryErr
	}
	var recorded fc.Scope
	if json.Unmarshal(saved, &recorded) != nil || recorded != scope {
		return fc.Authority{}, fc.ErrConflict
	}
	op := firecrackerDigest(fmt.Sprintf("%s/%s/%d/%s", scope.Key(), a.Deployment, a.Generation, operation))
	var generation int64
	var previous string
	err = tx.QueryRow(ctx, `SELECT generation,digest FROM loomplane_firecracker_operations WHERE id=$1`, op).Scan(&generation, &previous)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fc.Authority{}, err
	}
	if err == nil && (generation != current || previous != digest) {
		return fc.Authority{}, fc.ErrConflict
	}
	expires := time.Now().UTC().Add(4 * time.Minute)
	if errors.Is(err, pgx.ErrNoRows) {
		generation = current + 1
		if _, err = tx.Exec(ctx, `UPDATE loomplane_firecracker_resources SET generation=$2 WHERE resource_key=$1`, scope.Key(), generation); err != nil {
			return fc.Authority{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO loomplane_firecracker_operations(id,resource_key,operation,generation,digest,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, op, scope.Key(), operation, generation, digest, expires)
	} else {
		_, err = tx.Exec(ctx, `UPDATE loomplane_firecracker_operations SET expires_at=$2 WHERE id=$1`, op, expires)
	}
	if err != nil {
		return fc.Authority{}, err
	}
	if err := tx.Commit(); err != nil {
		return fc.Authority{}, err
	}
	return fc.Authority{Generation: generation, Operation: op, Expires: expires}, nil
}
