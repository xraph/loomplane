package nodes

import "time"

// How fast a node may send control messages. An honest node sends a
// heartbeat every 15 seconds, an inventory when its machine changes and a
// handful of replies around each connect, so it stays far inside this. A
// node past it is closed before the message that broke it is handled.
const (
	controlBurst     = 64
	controlPerSecond = 2
)

// bucket is a token bucket on the hub's clock. Only a session's read loop
// touches it, so it has no lock.
type bucket struct {
	tokens float64
	last   time.Time
}

func newBucket(now time.Time) bucket { return bucket{tokens: controlBurst, last: now} }

// take spends one token, refilling for the time since the last call first.
// It reports false, and spends nothing, when the bucket is empty.
func (b *bucket) take(now time.Time) bool {
	if d := now.Sub(b.last); d > 0 {
		b.tokens = min(controlBurst, b.tokens+d.Seconds()*controlPerSecond)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
