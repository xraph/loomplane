package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrNotFound = errors.New("realtime collection not found")

// Collection separates the provider's resume token from visible log status.
type Collection struct {
	ID           string    `json:"id" forge:"id"`
	OrgID        string    `json:"org_id"`
	ProjectID    string    `json:"project_id"`
	DeploymentID string    `json:"deployment_id"`
	ActionID     string    `json:"action_id"`
	Attempt      string    `json:"attempt"`
	OperationID  string    `json:"operation_id"`
	Cursor       string    `json:"-"`
	State        string    `json:"state"`
	Message      string    `json:"message"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func collectionID(ref LogInput) string {
	return ref.DeploymentID + "/" + ref.ActionID + "/" + ref.Attempt + "/" + ref.OperationID
}

func (s *PgStore) Checkpoint(ctx context.Context, ref LogInput) (*Collection, error) {
	out := &Collection{ID: collectionID(ref), OrgID: ref.OrgID, DeploymentID: ref.DeploymentID, ActionID: ref.ActionID, Attempt: ref.Attempt, OperationID: ref.OperationID}
	err := s.db.QueryRow(ctx, `SELECT project_id,cursor,state,message,updated_at FROM loomplane_log_checkpoints WHERE org_id=$1 AND deployment_id=$2 AND action_id=$3 AND attempt=$4 AND operation_id=$5`, ref.OrgID, ref.DeploymentID, ref.ActionID, ref.Attempt, ref.OperationID).Scan(&out.ProjectID, &out.Cursor, &out.State, &out.Message, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return out, err
}

func (s *PgStore) SaveCheckpoint(ctx context.Context, ref LogInput, position, state, message string, redactor *Redactor) (err error) {
	if !validOrg(ref.OrgID) || ref.ProjectID == "" || ref.DeploymentID == "" || ref.ActionID == "" || ref.Attempt == "" || len(position) > 4096 {
		return ErrInvalid
	}
	switch state {
	case "collecting", "complete", "unavailable", "error", "incomplete":
	default:
		return ErrInvalid
	}
	message, _ = redactor.Line(message, MaxLineBytes)
	out := &Collection{ID: collectionID(ref), OrgID: ref.OrgID, ProjectID: ref.ProjectID, DeploymentID: ref.DeploymentID, ActionID: ref.ActionID, Attempt: ref.Attempt, OperationID: ref.OperationID, State: state, Message: message, UpdatedAt: time.Now().UTC()}
	payload, err := json.Marshal(out)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = rollback(tx, err) }()
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_log_checkpoints(org_id,project_id,deployment_id,action_id,attempt,operation_id,cursor,state,message,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(org_id,deployment_id,action_id,attempt,operation_id)
		DO UPDATE SET cursor=EXCLUDED.cursor,state=EXCLUDED.state,message=EXCLUDED.message,updated_at=EXCLUDED.updated_at`, ref.OrgID, ref.ProjectID, ref.DeploymentID, ref.ActionID, ref.Attempt, ref.OperationID, position, state, message, out.UpdatedAt); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT loomplane_append_event($1,$2,'build.collection',$3,$4)`, ref.OrgID, ref.ProjectID, out.ID, payload); err != nil {
		return err
	}
	return tx.Commit()
}
