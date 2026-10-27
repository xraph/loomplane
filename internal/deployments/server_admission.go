package deployments

import (
	"context"
	"errors"
	"fmt"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// directServerAdmission admits a direct server action. It returns the
// transaction that holds its daemon until the run ends, and the docker_id it
// admitted, which run checks against the credential it deploys with.
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
// Who waits for direct-apply and who refuses. Anything that takes the
// organization lock before it reaches the daemon mustn't wait for it.
// SavePool and infrastructure operations on a server's own connection
// (adoption, disconnect) try it under that lock and return ErrBusy.
// RetireServer waits for it, but first, before the organization lock, so
// a retirement still can't start while a deployment is admitted and a
// deployment can't be admitted while a retirement is in flight. Connection
// edits don't wait at all, so run refuses if the credential it reads after
// admission names a different daemon.
//
// Run writes step progress in other transactions while admission is open.
// Until the plan has a succeeded deployment, each write re-syncs the plan's
// connection uses, and loomplane_retiring_server_use runs for every inserted
// use and takes FOR SHARE on the connection row (migration 20261030000300).
// Nothing in the hold transaction conflicts with that. Retirement still
// serializes with connection uses: its FOR UPDATE conflicts with the
// trigger's FOR SHARE, so it either sees the new use and refuses, or
// commits first and the trigger sees the retiring server and refuses the
// use.
//
// Pooled actions take neither lock, GPU or not. Their pool reservation
// admits them, and the server runtime's gpu-devices lock keeps two GPU
// applies on one daemon apart (server.GPULocks).
func (r *Runner) directServerAdmission(ctx context.Context, p *planning.Plan, a planning.Action) (driver.Tx, string, error) {
	if !holdsServerAdmission(a) {
		return nil, "", nil
	}
	st, ok := r.store.(*PgStore)
	if !ok {
		return nil, "", nil
	}
	connectionID := a.ConnectionID
	if connectionID == "" {
		connectionID = p.Connections["server"]
	}
	const identityQuery = `SELECT coalesce(details->>'docker_id','') FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2`
	var identity string
	if err := st.db.QueryRow(ctx, identityQuery, p.OrgID, connectionID).Scan(&identity); err != nil {
		return nil, "", err
	}
	if identity == "" {
		return nil, "", errors.New("server identity must be verified before deployment")
	}
	// Waiting here holds nothing else, so it can be as long as the direct
	// apply ahead of it takes.
	hold, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	if _, err = hold.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('direct-apply/'||$1,0))`, identity); err != nil {
		return nil, "", errors.Join(err, hold.Rollback())
	}
	if err = admitDirectServer(ctx, st, p.OrgID, connectionID, identity); err != nil {
		return nil, "", errors.Join(err, hold.Rollback())
	}
	return hold, identity, nil
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

// admittedDaemonKey carries the docker_id a direct server action was
// admitted on.
type admittedDaemonKey struct{}

// checkAdmittedDaemon refuses a credential that no longer reaches the daemon
// the action was admitted on. The connection row isn't locked through the
// run, so it can be re-verified against another daemon after admission.
func checkAdmittedDaemon(ctx context.Context, a planning.Action, conn *connections.Connection) error {
	admitted, ok := ctx.Value(admittedDaemonKey{}).(string)
	if !ok || conn == nil || conn.Details["docker_id"] == admitted {
		return nil
	}
	return fmt.Errorf("the connection for %s now reaches a different server than the one it was admitted on; deploy again", a.Name)
}
