package recovery

import (
	"context"
	"time"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// NativeRetirement permanently excludes the original point from new restores.
// Failures retain that barrier because an accepted provider delete may be late.
type NativeRetirement struct {
	ID               string           `json:"id" forge:"id"`
	OrgID            string           `json:"org_id"`
	ProjectID        string           `json:"project_id"`
	SourceID         string           `json:"source_id"`
	PointID          string           `json:"point_id"`
	WitnessID        string           `json:"witness_id"`
	Status           string           `json:"status"`
	Policy           RetentionInput   `json:"policy"`
	CreatedBy        string           `json:"created_by"`
	CreatedAt        time.Time        `json:"created_at"`
	CompletedAt      *time.Time       `json:"completed_at,omitempty"`
	Error            string           `json:"error,omitempty"`
	PolicyID         string           `json:"policy_id,omitempty"`
	PolicyRevision   int64            `json:"policy_revision,omitempty"`
	ExpectedPolicyAt time.Time        `json:"-"`
	RequestID        string           `json:"-"`
	Token            string           `json:"-"`
	Attempted        bool             `json:"-"`
	Source           planning.Binding `json:"-"`
	Point            NativePoint      `json:"-"`
	Witness          NativePoint      `json:"-"`
}

type NativeRetirementPage struct {
	Retirements []NativeRetirement `json:"retirements"`
	NextBefore  string             `json:"next_before,omitempty"`
}

type NativeRetirementInput struct {
	RequestID           string         `json:"request_id"`
	Policy              RetentionInput `json:"policy"`
	AcknowledgeDeletion bool           `json:"acknowledge_deletion"`
}

type NativeDeletionControl struct {
	Rejected func() error
	Check    func(context.Context) error
	Before   func() error
}

type NativeDeletionBackend interface {
	DeleteNativePoint(context.Context, security.Principal, NativeRetirement, NativeDeletionControl) (bool, error)
}

type NativeRetirementStore interface {
	ListNativeRetirements(context.Context, string, string, NativePageInput) (*NativeRetirementPage, error)
	NativeRetirementHistory(context.Context, string, string, RetirementHistoryInput) (*RetirementHistory, error)
	NativePointRetired(context.Context, string, string, string) (bool, error)
	NativeRetirementByRequest(context.Context, string, string, string) (*NativeRetirement, error)
	NativeRetentionSnapshot(context.Context, string, string) (*NativeSource, []NativeRetentionEntry, error)
	AdmitNativeRetirement(context.Context, *NativeRetirement, RetirementGuard) (*NativeRetirement, error)
	GetNativeRetirement(context.Context, string, string) (*NativeRetirement, error)
	ClaimNativeRetirement(context.Context, string, string, string) (*NativeRetirement, error)
	CheckNativeRetirement(context.Context, *NativeRetirement) error
	SaveNativeRetirementIntent(context.Context, *NativeRetirement, bool) error
	FinishNativeRetirement(context.Context, *NativeRetirement, error) error
	RetryNativeRetirement(context.Context, string, string, string, RetirementGuard) (*NativeRetirement, error)
	PendingNativeRetirements(context.Context, NativeCopyPage) ([]NativeRetirement, error)
}
