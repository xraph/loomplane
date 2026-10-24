package recovery

import "time"

const NativeReviewKeyLabel = "loomplane/native recovery review v1"

type NativeCopyReviewInput struct {
	RestoreTime *time.Time `json:"restore_time,omitempty"`
	RequestID   string     `json:"request_id"`
	CloudID     string     `json:"cloud_id"`
	TargetID    string     `json:"target_id"`
	TTLHours    int        `json:"ttl_hours"`
}

// NativeCopyCapacity describes this temporary copy, without implying the source
// profile's backup and point-in-time recovery policy applies to the copy.
type NativeCopyCapacity struct {
	IOPS            int32  `json:"iops,omitempty"`
	ThroughputMBPS  int32  `json:"throughput_mbps,omitempty"`
	MaxStorageGB    int32  `json:"max_storage_gb,omitempty"`
	ProfileID       string `json:"profile_id"`
	ProfileRevision string `json:"profile_revision"`
	ComputeClass    string `json:"compute_class"`
	StorageGB       int32  `json:"storage_gb"`
	StorageType     string `json:"storage_type"`
	Availability    string `json:"availability"`
}

type NativeCopyReview struct {
	RestoreTime *time.Time          `json:"restore_time,omitempty"`
	RequestID   string              `json:"request_id"`
	ReviewToken string              `json:"review_token"`
	SourceID    string              `json:"source_id"`
	PointID     string              `json:"point_id"`
	Target      NativeRestoreTarget `json:"target"`
	Capacity    NativeCopyCapacity  `json:"capacity"`
	TTLHours    int                 `json:"ttl_hours"`
	ValidUntil  time.Time           `json:"valid_until"`
	Notice      string              `json:"notice"`
}

type NativeCopyCreateInput struct {
	RequestID          string `json:"request_id"`
	ReviewToken        string `json:"review_token"`
	AcknowledgeCharges bool   `json:"acknowledge_charges"`
}

type NativeCopiesPage struct {
	Copies     []NativeRestore `json:"copies"`
	NextBefore string          `json:"next_before,omitempty"`
}
