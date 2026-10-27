package deployments

import (
	"context"
	"errors"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/planning"
)

// Direct execution holds the same daemon lock as pool enrollment through its
// external effects, including connections that alias an adopted daemon.
// Pooled actions never take it, GPU or not. Their pool reservation admits
// them, and this transaction keeps a share lock on the connection row open
// through the run, while a pooled run takes the organization lock in later
// transactions. Pool saves, cloud sync and plan approval take the
// organization lock first and the connection rows after, so a pooled action
// holding this lock could wait on them while they wait on it. Two GPU
// applies on one daemon are kept apart by the server runtime's own
// gpu-devices lock instead (server.GPULocks).
func (r *Runner) directServerAdmission(ctx context.Context, p *planning.Plan, a planning.Action) (driver.Tx, error) {
	if !holdsServerAdmission(a) {
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

// holdsServerAdmission reports whether a server action holds its daemon's
// host-admission lock through its effects. Only direct creates, replaces and
// updates do.
func holdsServerAdmission(a planning.Action) bool {
	return a.Provider == "server" && !poolAction(a) && a.Op != planning.Delete && a.Op != planning.Keep
}
