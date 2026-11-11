package fleet

import "time"

// RolloutTarget pins an explicit campaign member before any update is admitted.
type RolloutTarget struct {
	InstanceID         string `json:"instance_id"`
	ExpectedRevision   int64  `json:"expected_revision"`
	ExpectedGeneration int64  `json:"expected_generation"`
}

// RolloutHealthPolicy requires fresh observations and uninterrupted healthy dwell.
type RolloutHealthPolicy struct {
	MaxAgeSeconds int `json:"max_age_seconds"`
	DwellSeconds  int `json:"dwell_seconds"`
}

// RolloutInput freezes the target release, ordered membership and wave policy.
type RolloutInput struct {
	ReleaseID      string              `json:"release_id"`
	IdempotencyKey string              `json:"idempotency_key"`
	Members        []RolloutTarget     `json:"members"`
	WaveSizes      []int               `json:"wave_sizes"`
	MaxConcurrent  int                 `json:"max_concurrent"`
	MaxUnavailable int                 `json:"max_unavailable"`
	Health         RolloutHealthPolicy `json:"health"`
}

// RolloutMember retains successful work and the receipt for its current attempt.
type RolloutMember struct {
	LaunchRevision int64 `json:"launch_revision,omitempty"`
	RolloutTarget
	Wave                int        `json:"wave"`
	Status              string     `json:"status"`
	Attempt             int        `json:"attempt"`
	IdempotencyKey      string     `json:"idempotency_key"`
	OperationID         string     `json:"operation_id,omitempty"`
	OperationGeneration int64      `json:"operation_generation,omitempty"`
	HealthySince        *time.Time `json:"healthy_since,omitempty"`
	Reason              string     `json:"reason,omitempty"`
}

// RolloutWave records completion independently of workflow worker lifetime.
type RolloutWave struct {
	Number      int        `json:"number"`
	Status      string     `json:"status"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// Rollout is the durable campaign snapshot returned to operators.
type Rollout struct {
	ResumePending bool            `json:"resume_pending"`
	ID            string          `json:"id"`
	FleetID       string          `json:"fleet_id"`
	Revision      int64           `json:"revision"`
	Input         RolloutInput    `json:"input"`
	Members       []RolloutMember `json:"members"`
	Waves         []RolloutWave   `json:"waves"`
	Status        string          `json:"status"`
	Reason        string          `json:"reason,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// ResumeRolloutInput retries failed members only when their operation still owns
// the current generation. Successful members remain complete.
type ResumeRolloutInput struct {
	ExpectedRevision int64  `json:"expected_revision"`
	RetryFailures    bool   `json:"retry_failures"`
	IdempotencyKey   string `json:"idempotency_key"`
}

// RolloutMemberPin binds an update to a saved campaign launch intent. It grants
// no authority; ordinary action admission still applies.
type RolloutMemberPin struct {
	RolloutID        string `json:"rollout_id"`
	DispatchRevision int64  `json:"dispatch_revision"`
	InstanceID       string `json:"instance_id"`
	Attempt          int    `json:"attempt"`
}

// PauseRolloutInput stops new campaign admissions at the saved revision.
type PauseRolloutInput struct {
	ExpectedRevision int64  `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key"`
}
