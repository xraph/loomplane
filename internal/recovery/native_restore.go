package recovery

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// NativeRestoreTarget is the checked destination retained before a native copy.
type NativeRestoreTarget struct {
	Provider        string `json:"provider"`
	ConnectionID    string `json:"connection_id"`
	AccountIdentity string `json:"account_identity"`
	NativeProject   string `json:"native_project,omitempty"`
	Region          string `json:"region"`
}

// NativeRestore retains the selected provider backup independently of Trove.
// Source, selected metadata and checkpoints are internal worker inputs.
type NativeRestore struct {
	ID            string              `json:"id" forge:"id"`
	OrgID         string              `json:"org_id"`
	ProjectID     string              `json:"project_id"`
	EnvironmentID string              `json:"environment_id"`
	SourceID      string              `json:"source_id"`
	PointID       string              `json:"point_id"`
	Target        NativeRestoreTarget `json:"target"`
	Status        string              `json:"status"`
	ExternalID    string              `json:"external_id,omitempty"`
	PhysicalID    string              `json:"physical_id,omitempty"`
	Error         string              `json:"error,omitempty"`
	CreatedBy     string              `json:"created_by"`
	CreatedAt     time.Time           `json:"created_at"`
	ExpiresAt     time.Time           `json:"expires_at"`
	VerifiedAt    *time.Time          `json:"verified_at,omitempty"`
	DurationMS    int64               `json:"duration_ms"`
	Source        planning.Binding    `json:"-"`
	Point         NativePoint         `json:"-"`
	RequestID     string              `json:"-"`
	Checkpoint    json.RawMessage     `json:"-"`
	Token         string              `json:"-"`
}

// NativeRestoreResult requires an actual SQL probe, not native availability.
type NativeRestoreResult struct {
	ExternalID  string
	PhysicalID  string
	SQLVerified bool
}

type NativeRestoreStore interface {
	CreateNativeRestore(context.Context, *NativeRestore) (*NativeRestore, error)
	GetNativeRestore(context.Context, string, string) (*NativeRestore, error)
	ClaimNativeRestore(context.Context, string, string, string) (*NativeRestore, error)
	SaveNativeCheckpoint(context.Context, *NativeRestore, json.RawMessage) error
	FinishNativeRestore(context.Context, *NativeRestore, NativeRestoreResult, time.Duration, error) error
	ClaimNativeCleanup(context.Context, string, string, string) (*NativeRestore, error)
	FinishNativeCleanup(context.Context, *NativeRestore, error) error
}

// NativeCopyWorkStore keeps abandoned jobs discoverable and fences every write.
type NativeCopyWorkStore interface {
	NativeRestoreStore
	CheckNativeLease(context.Context, *NativeRestore, string) error
	PendingNativeRestores(context.Context, NativeCopyPage) ([]NativeRestore, error)
}

// NativeCopyPage bounds each pending scan and retains a stable upper cutoff.
type NativeCopyPage struct {
	Through      time.Time
	AfterCreated time.Time
	AfterID      string
}

const nativeCopyPageSize = 100

// NativeCopyControl must guard provider writes and operation polls. Selected
// rechecks the original backup before the first native copy request.
type NativeCopyControl struct {
	Check    func(context.Context) error
	Selected func(context.Context) (*NativePoint, error)
	Save     func(context.Context, json.RawMessage) error
}

type NativeCopyBackend interface {
	RestoreNative(context.Context, security.Principal, NativeRestore, NativeCopyControl) (NativeRestoreResult, error)
}
