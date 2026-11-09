package repositories

import "time"

// SetClock makes c read the time from now, so a test can move it.
func SetClock(c *Connections, now func() time.Time) { c.now = now }

// WaitGitLabBackground waits for the revokes c started in the background.
func WaitGitLabBackground(c *GitLabConnections) { c.background.Wait() }
