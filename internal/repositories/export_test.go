package repositories

import "time"

// SetClock makes c read the time from now, so a test can move it.
func SetClock(c *Connections, now func() time.Time) { c.now = now }
