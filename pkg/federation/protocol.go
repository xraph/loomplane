package federation

import "time"

// Runtime limits apply at both ends of the separate agent protocol.
const (
	RuntimeEnrollmentPath     = "/runtime/v1/enroll/"
	RuntimeObservationPath    = "/runtime/v1/observations"
	RuntimeWatchPath          = "/runtime/v1/watch"
	RuntimeAcknowledgmentPath = "/runtime/v1/acknowledgments"
	MaxAcknowledgmentBytes    = 16 << 10
	MaxObservationBytes       = 2 << 20
	MaxSnapshotBytes          = 16 << 20
	MaxSnapshotRecords        = 10000
	MaxPolicyLease            = 5 * time.Minute
	MaxClockSkew              = 5 * time.Second
	RuntimeStreamLifetime     = 30 * time.Second
)
