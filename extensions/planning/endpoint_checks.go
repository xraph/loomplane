package planning

import (
	"context"
	"sync"
	"time"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/security"
)

// Endpoint checks through the API are limited to checksPerMinute for each
// person in each organization: a check dials wherever its DSN says, from
// the target's network or Loomplane's. Plans and releases check the
// endpoints they connect without a limit.
const checksPerMinute = 10

// checkLimiter is a token bucket for each principal, in memory.
type checkLimiter struct {
	burst float64
	every time.Duration // one token comes back each every
	now   func() time.Time

	mu      sync.Mutex
	buckets map[checkKey]*checkBucket
}

type checkKey struct{ org, user string }

type checkBucket struct {
	tokens  float64
	at      time.Time
	refused bool // the last request was refused
}

// newCheckLimiter allows n checks per window, coming back evenly.
func newCheckLimiter(n int, window time.Duration) *checkLimiter {
	return &checkLimiter{burst: float64(n), every: window / time.Duration(n), now: time.Now, buckets: map[checkKey]*checkBucket{}}
}

// maxIdleBuckets is how many buckets are kept before full ones go.
const maxIdleBuckets = 4096

// allow takes a check from p's bucket. first reports a refusal that
// follows an allowed check, so a run of refusals is audited once.
func (l *checkLimiter) allow(p security.Principal) (ok, first bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	key := checkKey{p.OrgID, p.UserID}
	b, found := l.buckets[key]
	if !found {
		if len(l.buckets) >= maxIdleBuckets {
			l.prune(now)
		}
		b = &checkBucket{tokens: l.burst, at: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+float64(now.Sub(b.at))/float64(l.every))
	b.at = now
	if b.tokens < 1 {
		first = !b.refused
		b.refused = true
		return false, first
	}
	b.tokens--
	b.refused = false
	return true, false
}

// prune forgets the buckets that have filled up again.
func (l *checkLimiter) prune(now time.Time) {
	for key, b := range l.buckets {
		if b.tokens+float64(now.Sub(b.at))/float64(l.every) >= l.burst {
			delete(l.buckets, key)
		}
	}
}

// checks is the limiter the check route uses.
func (e *Extension) checks() *checkLimiter {
	e.checkLimitOnce.Do(func() {
		if e.checkLimit == nil {
			e.checkLimit = newCheckLimiter(checksPerMinute, time.Minute)
		}
	})
	return e.checkLimit
}

// endpointAuditor records endpoint changes and checks in the audit trail.
type endpointAuditor struct {
	chronicle *chronicle.Chronicle
	appID     string
	log       forge.Logger
}

var _ catalogconnect.Auditor = endpointAuditor{}

// Record implements catalogconnect.Auditor. A failed record is logged,
// never returned.
func (a endpointAuditor) Record(ctx context.Context, e catalogconnect.AuditEvent) {
	resource, id := "catalog_endpoint", e.EndpointID
	if e.Action == catalogconnect.AuditCheck {
		// A check of a DSN that isn't stored is about the target it
		// dialed from.
		resource, id = "cloud_connection", e.Target
	}
	var b *chronicle.EventBuilder
	if e.Action == catalogconnect.AuditCheck {
		b = a.chronicle.Info(ctx, e.Action, resource, id)
	} else {
		// Like a cloud connection's, an endpoint's DSN is a credential.
		b = a.chronicle.Warning(ctx, e.Action, resource, id)
	}
	b = b.Category("catalog_endpoints").AppID(a.appID).TenantID(e.Principal.OrgID).UserID(e.Principal.UserID).Outcome(e.Outcome).
		Meta("engine", e.Engine).Meta("address", e.Address)
	if e.Target != "" {
		b = b.Meta("target", e.Target)
	}
	if e.EndpointID != "" {
		b = b.Meta("endpoint_id", e.EndpointID)
	}
	if err := b.Record(); err != nil {
		a.log.Error("record audit event", forge.F("action", e.Action), forge.F("error", err.Error()))
	}
}
