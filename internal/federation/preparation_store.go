package federation

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/agentinstall"
	domain "github.com/xraph/loomplane/pkg/federation"
)

const (
	PreparationReserved       = "reserved"
	PreparationAccountPending = "account_pending"
	PreparationAccountCreated = "account_created"
	PreparationPodPending     = "pod_pending"
	PreparationPrepared       = "prepared"
)

type PreparationIntent struct {
	ID           string `json:"id"`
	Namespace    string `json:"namespace"`
	NamespaceUID string `json:"namespace_uid"`
	Image        string `json:"image"`
}
type Preparation struct {
	domain.Scope
	PreparationIntent
	DestinationID         string    `json:"destination_id"`
	DestinationGeneration int64     `json:"destination_generation"`
	IdentityRevision      string    `json:"identity_revision"`
	State                 string    `json:"state"`
	Generation            int64     `json:"generation"`
	AccountUID            string    `json:"account_uid"`
	PodUID                string    `json:"pod_uid"`
	RecordedBy            string    `json:"recorded_by"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}
type PreparationStore interface {
	ReservePreparation(context.Context, domain.Destination, PreparationIntent, string, func(context.Context) error) (*Preparation, error)
	GetPreparation(context.Context, string, string) (*Preparation, error)
	CurrentPreparation(context.Context, string, string) (*Preparation, error)
	AdvancePreparation(context.Context, string, string, int64, string, string, string, func(context.Context) error) (*Preparation, error)
}

func validPreparationIntent(in PreparationIntent) bool {
	_, err := agentinstall.NewKubernetesPlan(in.Namespace, in.ID, in.Image, false)
	return err == nil && domain.ValidIdentitySegment(in.NamespaceUID)
}

const preparationColumns = `org_id,project_id,environment_id,destination_id,destination_generation,identity_revision,intent,state,generation,account_uid,pod_uid,recorded_by,created_at,updated_at`

func scanPreparation(row rowScanner) (*Preparation, error) {
	var r Preparation
	var raw []byte
	if err := row.Scan(&r.OrgID, &r.ProjectID, &r.EnvironmentID, &r.DestinationID, &r.DestinationGeneration, &r.IdentityRevision, &raw, &r.State, &r.Generation, &r.AccountUID, &r.PodUID, &r.RecordedBy, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, storeError(err)
	}
	if err := json.Unmarshal(raw, &r.PreparationIntent); err != nil {
		return nil, err
	}
	if !validPreparationIntent(r.PreparationIntent) {
		return nil, ErrInvalid
	}
	return &r, nil
}
func (s *PgStore) GetPreparation(ctx context.Context, org, id string) (*Preparation, error) {
	r, err := scanPreparation(s.db.QueryRow(ctx, `SELECT `+preparationColumns+` FROM loomplane_federation_preparations WHERE org_id=$1 AND id=$2`, org, id))
	if err != nil {
		return nil, err
	}
	if r.OrgID != org || r.ID != id {
		return nil, ErrInvalid
	}
	return r, nil
}
func (s *PgStore) CurrentPreparation(ctx context.Context, org, destination string) (*Preparation, error) {
	return scanPreparation(s.db.QueryRow(ctx, `SELECT `+preparationColumns+` FROM loomplane_federation_preparations WHERE org_id=$1 AND destination_id=$2`, org, destination))
}
func (s *PgStore) ReservePreparation(ctx context.Context, d domain.Destination, in PreparationIntent, actor string, guard func(context.Context) error) (out *Preparation, err error) {
	if !validPreparationIntent(in) || in.NamespaceUID != d.Identity.NamespaceUID || d.Identity.Provider != "kubernetes" || d.Quarantined || !domain.ValidIdentitySegment(actor) || guard == nil {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = finishInstallation(tx, err) }()
	current, err := scanDestination(tx.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_federation_destinations WHERE org_id=$1 AND id=$2 FOR SHARE`, d.OrgID, d.ID))
	if err != nil {
		return nil, err
	}
	if current.Scope != d.Scope || current.Generation != d.Generation || current.Identity != d.Identity || current.ConnectionID != d.ConnectionID || current.Quarantined {
		return nil, ErrConflict
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	now := time.Now().UTC()
	out = &Preparation{Scope: d.Scope, PreparationIntent: in, DestinationID: d.ID, DestinationGeneration: d.Generation, IdentityRevision: d.Identity.Revision(), State: PreparationReserved, Generation: 1, RecordedBy: actor, CreatedAt: now, UpdatedAt: now}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	if _, writeErr := tx.Exec(ctx, `INSERT INTO loomplane_federation_preparations(org_id,project_id,environment_id,id,destination_id,destination_generation,identity_revision,intent,state,generation,recorded_by,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'reserved',1,$9,$10,$10)`, d.OrgID, d.ProjectID, d.EnvironmentID, in.ID, d.ID, d.Generation, out.IdentityRevision, raw, actor, now); writeErr != nil {
		return nil, storeError(writeErr)
	}
	if eventErr := appendPreparationEvent(ctx, tx, out); eventErr != nil {
		return nil, eventErr
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	return out, tx.Commit()
}
func preparationTransition(from, to, uid string) bool {
	switch from {
	case PreparationReserved:
		return to == PreparationAccountPending && uid == ""
	case PreparationAccountPending:
		return to == PreparationAccountCreated && domain.ValidIdentitySegment(uid)
	case PreparationAccountCreated:
		return to == PreparationPodPending && uid == ""
	case PreparationPodPending:
		return to == PreparationPrepared && domain.ValidIdentitySegment(uid)
	default:
		return false
	}
}
func (s *PgStore) AdvancePreparation(ctx context.Context, org, id string, expected int64, target, uid, actor string, guard func(context.Context) error) (out *Preparation, err error) {
	if expected < 1 || expected > 4 || !domain.ValidIdentitySegment(actor) || guard == nil {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = finishInstallation(tx, err) }()
	out, err = scanPreparation(tx.QueryRow(ctx, `SELECT `+preparationColumns+` FROM loomplane_federation_preparations WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, id))
	if err != nil {
		return nil, err
	}
	if out.ID != id || out.Generation != expected || !preparationTransition(out.State, target, uid) {
		return nil, ErrConflict
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	out.State = target
	out.Generation++
	out.RecordedBy = actor
	out.UpdatedAt = time.Now().UTC()
	if target == PreparationAccountCreated {
		out.AccountUID = uid
	}
	if target == PreparationPrepared {
		out.PodUID = uid
	}
	if _, writeErr := tx.Exec(ctx, `UPDATE loomplane_federation_preparations SET state=$3,generation=$4,account_uid=$5,pod_uid=$6,recorded_by=$7,updated_at=$8 WHERE org_id=$1 AND id=$2`, org, id, target, out.Generation, out.AccountUID, out.PodUID, actor, out.UpdatedAt); writeErr != nil {
		return nil, storeError(writeErr)
	}
	if eventErr := appendPreparationEvent(ctx, tx, out); eventErr != nil {
		return nil, eventErr
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	return out, tx.Commit()
}
func appendPreparationEvent(ctx context.Context, tx driver.Tx, r *Preparation) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT loomplane_append_event($1,$2,$3,$4,$5)`, r.OrgID, r.ProjectID, "federation.preparation."+r.State, r.ID, raw)
	return err
}
