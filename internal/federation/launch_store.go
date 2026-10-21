package federation

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/xraph/grove/driver"
	"k8s.io/apimachinery/pkg/util/validation"

	domain "github.com/xraph/loomplane/pkg/federation"
)

const (
	LaunchPending   = "pending"
	LaunchDelivered = "delivered"
)

// LaunchIntent records the exact public configuration before the provider write.
// The digest includes the immutable payload and its pinned pod owner, not server
// metadata that Kubernetes assigns after creation.
type LaunchIntent struct {
	ID            string `json:"id"`
	EnrollmentID  string `json:"enrollment_id"`
	Namespace     string `json:"namespace"`
	PodUID        string `json:"pod_uid"`
	ConfigMapName string `json:"config_map_name"`
	Digest        string `json:"digest"`
}
type LaunchAttempt struct {
	domain.Scope
	LaunchIntent
	DestinationID         string    `json:"destination_id"`
	DestinationGeneration int64     `json:"destination_generation"`
	IdentityRevision      string    `json:"identity_revision"`
	State                 string    `json:"state"`
	Generation            int64     `json:"generation"`
	ConfigMapUID          string    `json:"config_map_uid"`
	RecordedBy            string    `json:"recorded_by"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}
type LaunchAttemptStore interface {
	ReserveLaunch(context.Context, domain.Destination, LaunchIntent, string, func(context.Context) error) (*LaunchAttempt, error)
	GetLaunch(context.Context, string, string) (*LaunchAttempt, error)
	ConfirmLaunch(context.Context, string, string, int64, string, string, func(context.Context) error) (*LaunchAttempt, error)
}

func validLaunchIntent(in LaunchIntent) bool {
	for _, v := range []string{in.ID, in.EnrollmentID} {
		id, err := uuid.Parse(v)
		if err != nil || id == uuid.Nil || id.String() != v {
			return false
		}
	}
	digest, err := hex.DecodeString(in.Digest)
	return err == nil && len(digest) == 32 && strings.ToLower(in.Digest) == in.Digest && domain.ValidIdentitySegment(in.PodUID) && len(validation.IsDNS1123Label(in.Namespace)) == 0 && in.ConfigMapName == "loomplane-agent-"+in.ID
}

const launchColumns = `org_id,project_id,environment_id,destination_id,destination_generation,identity_revision,intent,state,generation,config_map_uid,recorded_by,created_at,updated_at`

func scanLaunch(row rowScanner) (*LaunchAttempt, error) {
	var out LaunchAttempt
	var raw []byte
	if err := row.Scan(&out.OrgID, &out.ProjectID, &out.EnvironmentID, &out.DestinationID, &out.DestinationGeneration, &out.IdentityRevision, &raw, &out.State, &out.Generation, &out.ConfigMapUID, &out.RecordedBy, &out.CreatedAt, &out.UpdatedAt); err != nil {
		return nil, storeError(err)
	}
	if err := json.Unmarshal(raw, &out.LaunchIntent); err != nil {
		return nil, err
	}
	if !validLaunchIntent(out.LaunchIntent) {
		return nil, ErrInvalid
	}
	return &out, nil
}
func (s *PgStore) GetLaunch(ctx context.Context, org, id string) (*LaunchAttempt, error) {
	r, err := scanLaunch(s.db.QueryRow(ctx, `SELECT `+launchColumns+` FROM loomplane_federation_launch_attempts WHERE org_id=$1 AND id=$2`, org, id))
	if err != nil {
		return nil, err
	}
	if r.OrgID != org || r.ID != id {
		return nil, ErrInvalid
	}
	return r, nil
}

// ReserveLaunch is insert-only. A pending row survives all uncertain provider
// outcomes and retains the destination until exact delivery evidence is recorded.
func (s *PgStore) ReserveLaunch(ctx context.Context, d domain.Destination, in LaunchIntent, actor string, guard func(context.Context) error) (out *LaunchAttempt, err error) {
	if !validLaunchIntent(in) || !domain.ValidIdentitySegment(actor) || guard == nil || d.Quarantined || d.Identity.Provider != "kubernetes" {
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
	out = &LaunchAttempt{Scope: d.Scope, LaunchIntent: in, DestinationID: d.ID, DestinationGeneration: d.Generation, IdentityRevision: d.Identity.Revision(), State: LaunchPending, Generation: 1, RecordedBy: actor, CreatedAt: now, UpdatedAt: now}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	if _, writeErr := tx.Exec(ctx, `INSERT INTO loomplane_federation_launch_attempts(org_id,project_id,environment_id,id,destination_id,destination_generation,identity_revision,enrollment_id,intent,state,generation,recorded_by,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',1,$10,$11,$11)`, d.OrgID, d.ProjectID, d.EnvironmentID, in.ID, d.ID, d.Generation, out.IdentityRevision, in.EnrollmentID, raw, actor, now); writeErr != nil {
		return nil, storeError(writeErr)
	}
	if eventErr := appendLaunchEvent(ctx, tx, out, "federation.launch.reserved"); eventErr != nil {
		return nil, eventErr
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	return out, tx.Commit()
}

// ConfirmLaunch records observed evidence, never issues authority. Existing
// installation and enrollment records retain their own lifecycle references.
func (s *PgStore) ConfirmLaunch(ctx context.Context, org, id string, expected int64, uid, actor string, guard func(context.Context) error) (out *LaunchAttempt, err error) {
	if expected != 1 || !domain.ValidIdentitySegment(uid) || !domain.ValidIdentitySegment(actor) || guard == nil {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = finishInstallation(tx, err) }()
	out, err = scanLaunch(tx.QueryRow(ctx, `SELECT `+launchColumns+` FROM loomplane_federation_launch_attempts WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, id))
	if err != nil {
		return nil, err
	}
	if out.ID != id || out.State != LaunchPending || out.Generation != expected {
		return nil, ErrConflict
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	out.State = LaunchDelivered
	out.Generation++
	out.ConfigMapUID = uid
	out.RecordedBy = actor
	out.UpdatedAt = time.Now().UTC()
	if _, writeErr := tx.Exec(ctx, `UPDATE loomplane_federation_launch_attempts SET state='delivered',generation=$3,config_map_uid=$4,recorded_by=$5,updated_at=$6 WHERE org_id=$1 AND id=$2`, org, id, out.Generation, uid, actor, out.UpdatedAt); writeErr != nil {
		return nil, storeError(writeErr)
	}
	if eventErr := appendLaunchEvent(ctx, tx, out, "federation.launch.delivered"); eventErr != nil {
		return nil, eventErr
	}
	if checkErr := guard(ctx); checkErr != nil {
		return nil, checkErr
	}
	return out, tx.Commit()
}
func appendLaunchEvent(ctx context.Context, tx driver.Tx, r *LaunchAttempt, name string) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT loomplane_append_event($1,$2,$3,$4,$5)`, r.OrgID, r.ProjectID, name, r.ID, raw)
	return err
}
