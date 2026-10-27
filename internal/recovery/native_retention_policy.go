package recovery

import (
	"context"
	"time"
)

// NativeRetentionPolicy is an explicit grant to select independent native backups.
// It never changes the provider's automated backup or PITR configuration.
type NativeRetentionPolicy struct {
	ID               string         `json:"id" forge:"id"`
	OrgID            string         `json:"org_id"`
	ProjectID        string         `json:"project_id"`
	SourceID         string         `json:"source_id"`
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

type NativeRetentionPolicyStore interface {
	PutNativeRetentionPolicy(context.Context, *NativeRetentionPolicy, int64, RetirementGuard) (*NativeRetentionPolicy, error)
	NativeRetentionPolicyForSource(context.Context, string, string) (*NativeRetentionPolicy, error)
	DueNativeRetentionPolicies(context.Context) ([]NativeRetentionPolicy, error)
	AdvanceNativeRetentionPolicy(context.Context, *NativeRetentionPolicy, string, error) error
}
