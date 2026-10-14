package owned

import (
	"context"
	"errors"

	"github.com/xraph/grove/drivers/pgdriver"
)

var ErrReserved = errors.New("the host port is reserved by another deployed service on this server")

type Reservation struct {
	Scope, Owner, OrgID, ProjectID, EnvironmentID, ConnectionID string
	Port                                                        int
}

type PortReservations interface {
	Reserve(context.Context, Reservation) error
	Release(context.Context, Reservation) error
}

type Reservations struct{ db *pgdriver.PgDB }

func NewReservations(db *pgdriver.PgDB) *Reservations { return &Reservations{db: db} }

func (s *Reservations) Reserve(ctx context.Context, r Reservation) error {
	if r.Scope == "" || r.Owner == "" || r.OrgID == "" || r.ProjectID == "" || r.EnvironmentID == "" || r.ConnectionID == "" || r.Port < 1 || r.Port > 65535 {
		return errors.New("invalid host port reservation")
	}
	result, err := s.db.Exec(ctx, `INSERT INTO loomplane_host_ports(scope,port,owner,org_id,project_id,environment_id,connection_id)
	 VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(scope,port) DO UPDATE SET owner=EXCLUDED.owner
	 WHERE loomplane_host_ports.owner=EXCLUDED.owner AND loomplane_host_ports.org_id=EXCLUDED.org_id
	 AND loomplane_host_ports.project_id=EXCLUDED.project_id AND loomplane_host_ports.environment_id=EXCLUDED.environment_id`, r.Scope, r.Port, r.Owner, r.OrgID, r.ProjectID, r.EnvironmentID, r.ConnectionID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrReserved
	}
	return nil
}

func (s *Reservations) Release(ctx context.Context, r Reservation) error {
	_, err := s.db.Exec(ctx, `DELETE FROM loomplane_host_ports WHERE scope=$1 AND port=$2 AND owner=$3 AND org_id=$4`, r.Scope, r.Port, r.Owner, r.OrgID)
	return err
}
