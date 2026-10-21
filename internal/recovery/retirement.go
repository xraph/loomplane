package recovery

import (
	"context"
	"time"

	"github.com/xraph/loomplane/internal/backupfiles"
)

// Retirement permanently excludes its candidate from restore admission. Failed
// or expired workers do not release that barrier because deletion may be late.
type Retirement struct {
	RetentionPolicyID string             `json:"retention_policy_id,omitempty"`
	RetentionRevision int64              `json:"retention_revision,omitempty"`
	ID                string             `json:"id" forge:"id"`
	OrgID             string             `json:"org_id"`
	ProjectID         string             `json:"project_id"`
	BackupID          string             `json:"backup_id"`
	WitnessID         string             `json:"witness_id"`
	Status            string             `json:"status"`
	ExecutionBy       string             `json:"execution_by"`
	AttemptLimit      int                `json:"attempt_limit"`
	Attempt           int                `json:"attempt"`
	Error             string             `json:"error,omitempty"`
	Policy            RetentionInput     `json:"policy"`
	CreatedBy         string             `json:"created_by"`
	CreatedAt         time.Time          `json:"created_at"`
	CompletedAt       *time.Time         `json:"completed_at,omitempty"`
	RequestID         string             `json:"-"`
	SourceKey         string             `json:"-"`
	Token             string             `json:"-"`
	Object            backupfiles.Object `json:"-"`
	WitnessObject     backupfiles.Object `json:"-"`
}

// RetirementGuard is supplied by the management service, never a request body.
type RetirementGuard func(context.Context) error

// RetirementEvent records a committed transition. Imported events describe only
// state observed during migration; they do not reconstruct earlier attempts.
type RetirementEvent struct {
	Cursor        string    `json:"cursor"`
	Kind          string    `json:"kind"`
	Status        string    `json:"status"`
	Attempt       int       `json:"attempt"`
	AttemptLimit  int       `json:"attempt_limit"`
	Actor         string    `json:"actor"`
	PreviousActor string    `json:"previous_actor,omitempty"`
	Error         string    `json:"error,omitempty"`
	PreviousError string    `json:"previous_error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type RetirementHistoryInput struct {
	Before string `json:"before,omitempty" query:"before,omitempty"`
	Limit  int    `json:"limit,omitempty" query:"limit,omitempty"`
}

type RetirementHistory struct {
	Events     []RetirementEvent `json:"events"`
	NextBefore string            `json:"next_before,omitempty"`
}
