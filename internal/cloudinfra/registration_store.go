package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

var _ RegistrationStore = (*PgStore)(nil)

// registrationRow is the stored body: the registration and the principal
// who approved it, which the API never shows.
type registrationRow struct {
	*Registration
	Principal security.Principal `json:"principal"`
}

func encodeRegistration(reg *Registration) ([]byte, error) {
	return json.Marshal(registrationRow{Registration: reg, Principal: reg.Principal})
}

func decodeRegistration(raw []byte) (*Registration, error) {
	row := registrationRow{Registration: &Registration{}}
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, err
	}
	row.Registration.Principal = row.Principal
	return row.Registration, nil
}

func (s *PgStore) CreateRegistration(ctx context.Context, reg *Registration) error {
	raw, err := encodeRegistration(reg)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_domain_registrations(id,org_id,connection_id,name,status,revision,body) VALUES($1,$2,$3,$4,$5,$6,$7)`, reg.ID, reg.OrgID, reg.ConnectionID, reg.Name, reg.Status, reg.Revision, raw)
	return storeError(err)
}

func (s *PgStore) Registration(ctx context.Context, org, id string) (*Registration, error) {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT body FROM loomplane_domain_registrations WHERE org_id=$1 AND id=$2`, org, id).Scan(&raw); err != nil {
		return nil, storeError(err)
	}
	return decodeRegistration(raw)
}

func (s *PgStore) registrations(ctx context.Context, query string, args ...any) ([]*Registration, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Registration{}
	for rows.Next() {
		var raw []byte
		if scanErr := rows.Scan(&raw); scanErr != nil {
			return nil, scanErr
		}
		reg, decodeErr := decodeRegistration(raw)
		if decodeErr != nil {
			return nil, decodeErr
		}
		out = append(out, reg)
	}
	return out, rows.Err()
}

// Registrations lists the organization's registrations, newest first.
func (s *PgStore) Registrations(ctx context.Context, org string) ([]*Registration, error) {
	return s.registrations(ctx, `SELECT body FROM loomplane_domain_registrations WHERE org_id=$1 ORDER BY created_at DESC,id`, org)
}

func (s *PgStore) PendingRegistrations(ctx context.Context) ([]*Registration, error) {
	return s.registrations(ctx, `SELECT body FROM loomplane_domain_registrations WHERE status IN ('preview','approved','registering','zone_pending') ORDER BY created_at,id`)
}

func (s *PgStore) SucceededRegistrations(ctx context.Context) ([]*Registration, error) {
	return s.registrations(ctx, `SELECT body FROM loomplane_domain_registrations WHERE status='succeeded' ORDER BY created_at,id`)
}

func (s *PgStore) UnnotifiedRegistrations(ctx context.Context, since time.Time) ([]*Registration, error) {
	return s.registrations(ctx, `SELECT body FROM loomplane_domain_registrations WHERE status IN ('succeeded','failed','action_required','blocked','price_changed','unconfirmed')
 AND COALESCE(body->>'result_notified_for','')<>status AND (body->>'updated_at')::timestamptz>=$1 ORDER BY created_at,id`, since)
}

// SaveRegistration writes reg if nobody saved it since it was read, and
// moves it to the next revision.
func (s *PgStore) SaveRegistration(ctx context.Context, reg *Registration) error {
	prior := reg.Revision
	reg.Revision++
	raw, err := encodeRegistration(reg)
	if err == nil {
		err = storeError(s.db.QueryRow(ctx, `UPDATE loomplane_domain_registrations SET status=$3,revision=$4,body=$5 WHERE org_id=$1 AND id=$2 AND revision=$6 RETURNING id`, reg.OrgID, reg.ID, reg.Status, reg.Revision, raw, prior).Scan(new(string)))
	}
	if err != nil {
		reg.Revision = prior
		if errors.Is(err, ErrNotFound) {
			return ErrConflict
		}
	}
	return err
}
