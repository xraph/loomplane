package cloudflare

import "time"

// SetRegistrarPollInterval shortens the wait between update-status polls.
func SetRegistrarPollInterval(c *Client, d time.Duration) { c.pollInterval = d }
