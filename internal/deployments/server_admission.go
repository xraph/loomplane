package deployments

import (
	"context"
	"errors"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/planning"
)

// Direct execution holds the same daemon lock as pool enrollment through its
// external effects, including connections that alias an adopted daemon.
//
// Locking protocol. The admission transaction stays open until Run returns
// and holds two locks:
//
//   - FOR SHARE on the target connection row. RetireServer and infrastructure
//     operations take FOR UPDATE on that row and connection edits update it,
//     so none of them can run while a deployment is admitted, and a
//     deployment isn't admitted while one of them is in flight.
//   - The host-admission advisory lock on the daemon identity. It serializes
//     direct deployments with pool enrollment and plan acceptance on the same
//     daemon, whichever connection reaches it.
//
// Run writes step progress in other transactions while admission is open.
// Until the plan has a succeeded deployment, each write re-syncs the plan's
// connection uses, and loomplane_retiring_server_use runs for every inserted
// use. That trigger takes FOR SHARE as well (migration 20261030000300). It
// used to take FOR UPDATE, which waited on this transaction's own share lock
// and hung every first deployment of a plan with a direct server action.
// Share locks don't conflict, and Postgres grants a share request next to an
// existing share lock without queueing it behind a waiting FOR UPDATE, so a
// retirement waiting for this admission can't stall the progress writes
// either. Retirement still serializes with connection uses: its FOR UPDATE
// conflicts with the trigger's FOR SHARE, so it either sees the new use and
// refuses, or commits first and the trigger sees the retiring server and
// refuses the use. Keep the row lock here: retirement doesn't take the
// advisory lock, so the row lock is what keeps it out of an admitted
// deployment.
func (r *Runner) directServerAdmission(ctx context.Context, p *planning.Plan, a planning.Action) (driver.Tx, error) {
	if a.Provider != "server" || poolAction(a) || a.Op == planning.Delete || a.Op == planning.Keep {
		return nil, nil
	}
	st, ok := r.store.(*PgStore)
	if !ok {
		return nil, nil
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	connectionID := a.ConnectionID
	if connectionID == "" {
		connectionID = p.Connections["server"]
	}
	var identity string
	err = tx.QueryRow(ctx, `SELECT coalesce(details->>'docker_id','') FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2 FOR SHARE`, p.OrgID, connectionID).Scan(&identity)
	if err == nil && identity == "" {
		err = errors.New("server identity must be verified before deployment")
	}
	if err == nil {
		_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('host-admission/'||$1,0))`, identity)
	}
	if err != nil {
		err = errors.Join(err, tx.Rollback())
		return nil, err
	}
	return tx, nil
}
