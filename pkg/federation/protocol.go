package federation

import "time"

// Runtime limits apply at both ends of the separate agent protocol.
const (
	RuntimeEnrollmentPath  = "/runtime/v1/enroll/"
	RuntimeObservationPath = "/runtime/v1/observations"
	RuntimeWatchPath       = "/runtime/v1/watch"
	MaxObservationBytes    = 2 << 20
	MaxSnapshotBytes       = 16 << 20
	RuntimeStreamLifetime  = 30 * time.Second
)
