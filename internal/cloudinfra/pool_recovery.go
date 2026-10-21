package cloudinfra

import (
	"context"
	"fmt"

	"github.com/xraph/loomplane/internal/security"
)

type PoolCleanup interface {
	CleanupPoolReservation(context.Context, string, string, string, []byte, func([]byte) error) error
}

// ReservationRecovery retains the accepted reservation's cleanup authority.
// It contains identifiers and state only, never runtime configuration or secrets.
type ReservationRecovery struct {
	PlanID          string `json:"plan_id"`
	Service         string `json:"service"`
	ServerID        string `json:"server_id"`
	Phase           string `json:"phase"`
	Started         bool   `json:"started"`
	Active          bool   `json:"active"`
	Revision        int64  `json:"revision"`
	Review          string `json:"review"`
	StorageRetained bool   `json:"storage_retained"`
}
type RecoveryInput struct {
	PlanID  string `json:"plan_id"`
	Service string `json:"service"`
	Review  string `json:"review,omitempty"`
}

func (s *Service) PreviewReservationRecovery(ctx context.Context, p security.Principal, cloud string, in RecoveryInput) (*ReservationRecovery, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	st, ok := s.Store.(*PgStore)
	if !ok {
		return nil, ErrInvalid
	}
	return st.reservationRecovery(ctx, p.OrgID, cloud, in)
}
func (s *PgStore) reservationRecovery(ctx context.Context, org, cloud string, in RecoveryInput) (*ReservationRecovery, error) {
	v := &ReservationRecovery{PlanID: in.PlanID, Service: in.Service}
	var binding string
	err := s.db.QueryRow(ctx, `SELECT r.server_id,r.binding_id,x.phase,x.started,x.active,x.revision,x.storage_retained FROM loomplane_capacity_reservations r JOIN loomplane_server_pools p ON p.id=r.pool_id AND p.org_id=r.org_id JOIN loomplane_pool_execution x ON x.org_id=r.org_id AND x.plan_id=r.plan_id AND x.service=r.service WHERE r.org_id=$1 AND p.cloud_id=$2 AND r.plan_id=$3 AND r.service=$4`, org, cloud, in.PlanID, in.Service).Scan(&v.ServerID, &binding, &v.Phase, &v.Started, &v.Active, &v.Revision, &v.StorageRetained)
	if err != nil {
		return nil, storeError(err)
	}
	if binding != "" {
		return nil, fmt.Errorf("%w: use the serving workload's reviewed delete or move", ErrConflict)
	}
	v.Review = digest([]any{org, cloud, v})
	return v, nil
}

// RecoverReservation permanently fences new attempts before any cleanup. A
// failed cleanup remains blocked and can be reviewed and retried without grants.
func (s *Service) RecoverReservation(ctx context.Context, p security.Principal, cloud string, in RecoveryInput) (*ReservationRecovery, error) {
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	st, ok := s.Store.(*PgStore)
	if !ok {
		return nil, ErrInvalid
	}
	lock, err := st.lockPoolExecution(ctx, p.OrgID, in.PlanID, in.Service)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	v, err := st.reservationRecovery(ctx, p.OrgID, cloud, in)
	if err != nil {
		return nil, err
	}
	if in.Review == "" || v.Review != in.Review {
		return nil, ErrConflict
	}

	if v.Started && s.PoolCleanup == nil {
		return nil, fmt.Errorf("%w: runtime cleanup unavailable", ErrConflict)
	}
	// Conditional update also fences an attempt entering after this review.
	var revision, generation int64
	err = st.db.QueryRow(ctx, `UPDATE loomplane_pool_execution SET phase='cleanup',active=false,generation=generation+1,revision=revision+1 WHERE org_id=$1 AND plan_id=$2 AND service=$3 AND revision=$4 RETURNING revision,generation`, p.OrgID, in.PlanID, in.Service, v.Revision).Scan(&revision, &generation)
	if err != nil {
		return nil, ErrConflict
	}
	if v.Started {
		var checkpoint []byte
		err = st.db.QueryRow(ctx, `SELECT checkpoint FROM loomplane_pool_execution WHERE org_id=$1 AND plan_id=$2 AND service=$3`, p.OrgID, in.PlanID, in.Service).Scan(&checkpoint)
		if err == nil {
			err = s.PoolCleanup.CleanupPoolReservation(WithExecutionScope(ctx, ExecutionScope{p.OrgID, in.PlanID, in.Service, generation}), p.OrgID, in.PlanID, in.Service, checkpoint, func(raw []byte) error {
				e := st.db.QueryRow(ctx, `UPDATE loomplane_pool_execution SET checkpoint=$4 WHERE org_id=$1 AND plan_id=$2 AND service=$3 AND phase='cleanup' AND generation=$5 RETURNING generation`, p.OrgID, in.PlanID, in.Service, raw, generation).Scan(new(int64))
				return e
			})
		}
		if err != nil {
			_, saveErr := st.db.Exec(context.WithoutCancel(ctx), `UPDATE loomplane_pool_execution SET phase='blocked',revision=revision+1 WHERE org_id=$1 AND plan_id=$2 AND service=$3 AND generation=$4`, p.OrgID, in.PlanID, in.Service, generation)
			if saveErr != nil {
				return nil, saveErr
			}
			v.Phase = "blocked"
			v.Review = ""
			return v, fmt.Errorf("%w: exact owned compute cleanup could not be verified; restore the recorded connection or reconcile its checkpoint", ErrConflict)
		}
	}
	// The same transaction validates ownership before releasing capacity. A lost
	// locking session or superseded cleanup can never publish a release.
	var current int64
	if err = lock.tx.QueryRow(ctx, `SELECT generation FROM loomplane_pool_execution WHERE org_id=$1 AND plan_id=$2 AND service=$3 AND generation=$4 AND phase='cleanup' FOR UPDATE`, p.OrgID, in.PlanID, in.Service, generation).Scan(&current); err != nil {
		return nil, ErrConflict
	}
	if _, err = lock.tx.Exec(ctx, `DELETE FROM loomplane_capacity_reservations WHERE org_id=$1 AND plan_id=$2 AND service=$3 AND binding_id=''`, p.OrgID, in.PlanID, in.Service); err != nil {
		return nil, err
	}
	if _, err = lock.tx.Exec(ctx, `UPDATE loomplane_pool_execution SET phase='released',storage_retained=$4,revision=revision+1 WHERE org_id=$1 AND plan_id=$2 AND service=$3`, p.OrgID, in.PlanID, in.Service, v.Started); err != nil {
		return nil, err
	}
	if _, err = lock.tx.Exec(ctx, `UPDATE loomplane_workload_attempts SET active=false,settled=true,generation=generation+1 WHERE org_id=$1 AND plan_id=$2 AND service=$3`, p.OrgID, in.PlanID, in.Service); err != nil {
		return nil, err
	}
	if commitErr := lock.tx.Commit(); commitErr != nil {
		return nil, commitErr
	}
	v.Phase = "released"
	v.StorageRetained = v.Started
	v.Review = ""
	v.Revision = revision + 1
	return v, nil
}
