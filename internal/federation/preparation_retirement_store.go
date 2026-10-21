package federation

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/xraph/grove/driver"

	domain "github.com/xraph/loomplane/pkg/federation"
)

type PreparationRetirementStore interface {
	PreparationStore
	BeginPreparationRetirement(context.Context, string, string, int64, string, func(context.Context) error) (*Preparation, error)
	CapturePreparationOwnership(context.Context, string, string, int64, string, string, string, func(context.Context) error) (*Preparation, error)
	CompletePreparationRetirement(context.Context, string, string, int64, string, func(context.Context) error) (*Preparation, error)
}

func (s *PgStore) BeginPreparationRetirement(ctx context.Context, org, id string, expected int64, actor string, guard func(context.Context) error) (*Preparation, error) {
	return s.changePreparationRetirement(ctx, org, id, expected, actor, guard, func(ctx context.Context, tx driver.Tx, d *domain.Destination, r *Preparation) error {
		if r.State == PreparationRetiring || r.State == PreparationRetired {
			return ErrConflict
		}
		installation, err := scanInstallation(tx.QueryRow(ctx, `SELECT `+installationColumns+` FROM loomplane_federation_installations WHERE org_id=$1 AND destination_id=$2`, org, d.ID), org, d.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		name := "loomplane-agent-" + r.ID
		if installation != nil && (installation.Profile.PodName == name || installation.Profile.ServiceAccount == name) {
			if d.Generation == math.MaxInt64 {
				return ErrConflict
			}
			if _, err = tx.Exec(ctx, `DELETE FROM loomplane_federation_installations WHERE org_id=$1 AND destination_id=$2`, org, d.ID); err != nil {
				return err
			}
			installation.Generation = d.Generation + 1
			installation.RecordedBy = actor
			installation.UpdatedAt = time.Now().UTC()
			if stepErr := advanceInstallationDestination(ctx, tx, d, installation, "federation.installation.retired"); stepErr != nil {
				return stepErr
			}
		}
		r.CleanupFrom = r.State
		r.State = PreparationRetiring
		return nil
	})
}

// CapturePreparationOwnership saves identities recovered after an uncertain
// create. A retry can never replace an already observed UID with a new object.
func (s *PgStore) CapturePreparationOwnership(ctx context.Context, org, id string, expected int64, accountUID, podUID, actor string, guard func(context.Context) error) (*Preparation, error) {
	if (accountUID != "" && !domain.ValidIdentitySegment(accountUID)) || (podUID != "" && !domain.ValidIdentitySegment(podUID)) {
		return nil, ErrInvalid
	}
	return s.changePreparationRetirement(ctx, org, id, expected, actor, guard, func(_ context.Context, _ driver.Tx, _ *domain.Destination, r *Preparation) error {
		if r.State != PreparationRetiring || (accountUID == "" && podUID == "") {
			return ErrConflict
		}
		if accountUID != "" {
			if r.AccountUID != "" || r.CleanupFrom == PreparationReserved {
				return ErrConflict
			}
			r.AccountUID = accountUID
		}
		if podUID != "" {
			if r.PodUID != "" || r.AccountUID == "" || (r.CleanupFrom != PreparationPodPending && r.CleanupFrom != PreparationPrepared) {
				return ErrConflict
			}
			r.PodUID = podUID
		}
		return nil
	})
}

// CompletePreparationRetirement is called only after provider cleanup. Retained
// trust and uncertain creation ownership still prevent releasing the reference.
func (s *PgStore) CompletePreparationRetirement(ctx context.Context, org, id string, expected int64, actor string, guard func(context.Context) error) (*Preparation, error) {
	return s.changePreparationRetirement(ctx, org, id, expected, actor, guard, func(ctx context.Context, tx driver.Tx, d *domain.Destination, r *Preparation) error {
		if r.State != PreparationRetiring {
			return ErrConflict
		}
		if (r.CleanupFrom == PreparationAccountPending && r.AccountUID == "") || (r.CleanupFrom == PreparationPodPending && r.PodUID == "") {
			return ErrInUse
		}
		var retained bool
		name := "loomplane-agent-" + r.ID
		err := tx.QueryRow(ctx, `SELECT
   EXISTS(SELECT 1 FROM loomplane_federation_installations WHERE org_id=$1 AND destination_id=$2 AND (profile->>'pod_name'=$3 OR profile->>'service_account'=$3)) OR
   EXISTS(SELECT 1 FROM loomplane_federation_enrollments WHERE org_id=$1 AND destination_id=$2 AND body->'binding'->>'NamespaceUID'=$4 AND body->'binding'->>'ServiceAccount'=$3) OR
   EXISTS(SELECT 1 FROM loomplane_federation_launch_attempts WHERE org_id=$1 AND destination_id=$2 AND id=$5 AND state<>'retired')`, org, d.ID, name, r.NamespaceUID, r.ID).Scan(&retained)
		if err != nil {
			return err
		}
		if retained {
			return ErrInUse
		}
		r.State = PreparationRetired
		return nil
	})
}

// Lock the destination before preparation state, just as installation writes do.
// Enrollment holds this lock in shared mode, so the generation fence commits
// before any new binding can be accepted. Never lock enrollment rows here.
func (s *PgStore) changePreparationRetirement(ctx context.Context, org, id string, expected int64, actor string, guard func(context.Context) error, change func(context.Context, driver.Tx, *domain.Destination, *Preparation) error) (out *Preparation, err error) {
	if expected < 1 || expected == math.MaxInt64 || !domain.ValidIdentitySegment(actor) || guard == nil {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	initial, err := s.GetPreparation(ctx, org, id)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = finishInstallation(tx, err) }()
	d, err := scanDestination(tx.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_federation_destinations WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, initial.DestinationID))
	if err != nil {
		return nil, err
	}
	out, err = scanPreparation(tx.QueryRow(ctx, `SELECT `+preparationColumns+` FROM loomplane_federation_preparations WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, id))
	if err != nil {
		return nil, err
	}
	if out.ID != id || out.DestinationID != d.ID || out.Generation != expected {
		return nil, ErrConflict
	}
	if out.Scope != d.Scope || out.IdentityRevision != d.Identity.Revision() || d.Quarantined {
		return nil, ErrIdentityChanged
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	if stepErr := change(ctx, tx, d, out); stepErr != nil {
		return nil, stepErr
	}
	out.Generation++
	out.RecordedBy = actor
	out.UpdatedAt = time.Now().UTC()
	if _, err = tx.Exec(ctx, `UPDATE loomplane_federation_preparations SET state=$3,cleanup_from=$4,generation=$5,account_uid=$6,pod_uid=$7,recorded_by=$8,updated_at=$9 WHERE org_id=$1 AND id=$2`, org, id, out.State, out.CleanupFrom, out.Generation, out.AccountUID, out.PodUID, actor, out.UpdatedAt); err != nil {
		return nil, storeError(err)
	}
	if eventErr := appendPreparationEvent(ctx, tx, out); eventErr != nil {
		return nil, eventErr
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	return out, tx.Commit()
}
