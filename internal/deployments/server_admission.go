package deployments

import (
	"context"
	"errors"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/planning"
)

// Direct execution holds the same daemon lock as pool enrollment through its
// external effects, including connections that alias an adopted daemon. A
// pooled action takes it too when it asks for GPUs: its pool reservation
// counts them, but the runtime picks the devices by reading the daemon's
// container labels, and two applies must not both read a device as free.
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
// host-admission lock through its effects. Pooled CPU actions don't: their
// pool reservation already admits them.
func holdsServerAdmission(a planning.Action) bool {
	if a.Provider != "server" || a.Op == planning.Delete || a.Op == planning.Keep {
		return false
	}
	return !poolAction(a) || gpuAction(a)
}

// gpuAction reports whether a service action carries a GPU placement, as a
// reviewed runtime request or as a pool's GPU reservation.
func gpuAction(a planning.Action) bool {
	if a.Service == nil {
		return false
	}
	if a.Service.GPU != nil && a.Service.GPU.Count > 0 {
		return true
	}
	return a.Service.CloudPlacement != nil && a.Service.CloudPlacement.GPUs > 0
}
