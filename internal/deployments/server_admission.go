package deployments

import (
	"context"
	"errors"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/planning"
)

// directServerAdmission admits a direct server action and returns the
// transaction that holds its daemon until the run ends.
//
// That transaction holds one lock, direct-apply/<docker_id>, and nothing
// else. It reads and locks no rows, so the run can take the organization
// lock inside its effects (MarkWorkloadEffects, PutBinding) without a cycle.
// Pool saves, cloud sync and plan approval take the organization lock first
// and the daemon's connection rows and host-admission lock after it. If the
// run held either of those, one of them could wait on the run while the run
// waits on its organization lock, and Postgres would never see the cycle
// because one edge of it is this goroutine. Two direct applies on one
// daemon still queue on direct-apply. A pool save that would enroll the
// daemon tries the lock and refuses while it's held, rather than wait for
// it under the organization lock.
//
// The admission check runs in a short transaction of its own once the run
// holds the daemon. It takes the connection row and host-admission, like
// any approval or pool save, and so waits for whichever of those is in
// flight to commit before the run checks pool membership.
//
// Pooled actions take neither lock, GPU or not. Their pool reservation
// admits them, and the server runtime's gpu-devices lock keeps two GPU
// applies on one daemon apart (server.GPULocks).
func (r *Runner) directServerAdmission(ctx context.Context, p *planning.Plan, a planning.Action) (driver.Tx, error) {
	if !holdsServerAdmission(a) {
		return nil, nil
	}
	st, ok := r.store.(*PgStore)
	if !ok {
		return nil, nil
	}
	connectionID := a.ConnectionID
	if connectionID == "" {
		connectionID = p.Connections["server"]
	}
	const identityQuery = `SELECT coalesce(details->>'docker_id','') FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2`
	var identity string
	if err := st.db.QueryRow(ctx, identityQuery, p.OrgID, connectionID).Scan(&identity); err != nil {
		return nil, err
	}
	if identity == "" {
		return nil, errors.New("server identity must be verified before deployment")
	}
	// Waiting here holds nothing else, so it can be as long as the direct
	// apply ahead of it takes.
	hold, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err = hold.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('direct-apply/'||$1,0))`, identity); err != nil {
		return nil, errors.Join(err, hold.Rollback())
	}
	if err = admitDirectServer(ctx, st, p.OrgID, connectionID, identity); err != nil {
		return nil, errors.Join(err, hold.Rollback())
	}
	return hold, nil
}

func admitDirectServer(ctx context.Context, st *PgStore, org, connectionID, identity string) error {
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // a no-op once committed
	if boundErr := cloudprofiles.BoundLockWaits(ctx, tx); boundErr != nil {
		return boundErr
	}
	var current string
	err = tx.QueryRow(ctx, `SELECT coalesce(details->>'docker_id','') FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2 FOR SHARE`, org, connectionID).Scan(&current)
	if err != nil {
		return cloudprofiles.LockWaitError(err, "the server connection")
	}
	if current != identity {
		return errors.New("server identity changed during admission; deploy again")
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('host-admission/'||$1,0))`, identity); err != nil {
		return cloudprofiles.LockWaitError(err, "the server's admission lock")
	}
	return tx.Commit()
}

// holdsServerAdmission reports whether a server action holds its daemon's
// direct-apply lock through its effects. Only direct creates, replaces and
// updates do.
func holdsServerAdmission(a planning.Action) bool {
	return a.Provider == "server" && !poolAction(a) && a.Op != planning.Delete && a.Op != planning.Keep
}
