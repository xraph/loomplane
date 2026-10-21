package recovery

import (
	"context"
	"time"
)

// RetentionPolicy authorizes recurring selection for one captured source history.
// Backup schedules do not create or enable these policies.
type RetentionPolicy struct {
	ID               string         `json:"id" forge:"id"`
	OrgID            string         `json:"org_id"`
	ProjectID        string         `json:"project_id"`
	AnchorID         string         `json:"anchor_id"`
	SourceKey        string         `json:"-"`
	Revision         int64          `json:"revision"`
	Policy           RetentionInput `json:"policy"`
	Enabled          bool           `json:"enabled"`
	CreatedBy        string         `json:"created_by"`
	AuthorizedBy     string         `json:"authorized_by"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	NextAt           time.Time      `json:"next_at"`
	LastError        string         `json:"last_error,omitempty"`
	LastRetirementID string         `json:"last_retirement_id,omitempty"`
}

type RetentionPolicyStore interface {
	PutRetentionPolicy(context.Context, *RetentionPolicy, int64, RetirementGuard) (*RetentionPolicy, error)
	GetRetentionPolicy(context.Context, string, string) (*RetentionPolicy, error)
	FindRetentionPolicy(context.Context, string, string) (*RetentionPolicy, error)
	ListRetentionPolicies(context.Context, string, string) ([]RetentionPolicy, error)
	DueRetentionPolicies(context.Context) ([]RetentionPolicy, error)
	AdvanceRetentionPolicy(context.Context, *RetentionPolicy, string, error) error
}
