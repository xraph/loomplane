package recovery

import (
	"context"
	"time"

	"github.com/xraph/loomplane/internal/backupfiles"
)

// Retirement permanently excludes its candidate from restore admission. Failed
// or expired workers do not release that barrier because deletion may be late.
type Retirement struct {
	ID            string             `json:"id" forge:"id"`
	OrgID         string             `json:"org_id"`
	ProjectID     string             `json:"project_id"`
	BackupID      string             `json:"backup_id"`
	WitnessID     string             `json:"witness_id"`
	Status        string             `json:"status"`
	Attempt       int                `json:"attempt"`
	Error         string             `json:"error,omitempty"`
	Policy        RetentionInput     `json:"policy"`
	CreatedBy     string             `json:"created_by"`
	CreatedAt     time.Time          `json:"created_at"`
	CompletedAt   *time.Time         `json:"completed_at,omitempty"`
	RequestID     string             `json:"-"`
	SourceKey     string             `json:"-"`
	Token         string             `json:"-"`
	Object        backupfiles.Object `json:"-"`
	WitnessObject backupfiles.Object `json:"-"`
}

// RetirementGuard is supplied by the management service, never a request body.
type RetirementGuard func(context.Context) error
