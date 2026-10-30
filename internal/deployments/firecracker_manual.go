package deployments

import (
	"time"
)

type firecrackerManualKey struct{}
type firecrackerManual struct {
	State   string
	Updated time.Time
}
