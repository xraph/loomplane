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
// person in each organization, and checksPerUserPerMinute for each person
// across all of them: a check dials wherever its DSN says, from the
// target's network or Loomplane's. Plans and releases check the endpoints
// they connect without a limit.
const (
	checksPerMinute        = 10
	checksPerUserPerMinute = 30
)

// checkLimiter keeps token buckets in memory: one for each principal in
// each organization, and a larger one for each person across them. A
// check takes from both, and only when both have one.
type checkLimiter struct {
	now func() time.Time

	mu      sync.Mutex
	perOrg  *buckets
	perUser *buckets
	refused map[checkKey]bool // the principal's last request was refused
}

type checkKey struct{ org, user string }

// buckets are token buckets of one size, by key.
type buckets struct {
	burst float64
	every time.Duration // one token comes back each every
	by    map[checkKey]*checkBucket
}

type checkBucket struct {
	tokens float64
	at     time.Time
}

// newCheckLimiter allows perOrg checks a minute for each principal in an
// organization, and perUser for each person across organizations, coming
// back evenly.
func newCheckLimiter(perOrg, perUser int) *checkLimiter {
	return &checkLimiter{
		now:     time.Now,
		perOrg:  newBuckets(perOrg, time.Minute),
		perUser: newBuckets(perUser, time.Minute),
		refused: map[checkKey]bool{},
	}
}

func newBuckets(n int, window time.Duration) *buckets {
	return &buckets{burst: float64(n), every: window / time.Duration(n), by: map[checkKey]*checkBucket{}}
}

// maxIdleBuckets is how many buckets are kept before full ones go.
const maxIdleBuckets = 4096

// allow takes a check from p's buckets. first reports a refusal that
// follows an allowed check, so a run of refusals is audited once.
func (l *checkLimiter) allow(p security.Principal) (ok, first bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	key := checkKey{p.OrgID, p.UserID}
	org, user := l.perOrg.get(key, now), l.perUser.get(checkKey{user: p.UserID}, now)
	if org.tokens < 1 || user.tokens < 1 {
		first = !l.refused[key]
		if len(l.refused) >= maxIdleBuckets {
			// Forgetting costs at most one more audited refusal each.
			clear(l.refused)
		}
		l.refused[key] = true
		return false, first
	}
	org.tokens--
	user.tokens--
	delete(l.refused, key)
	return true, false
}

// get is key's bucket, refilled to now.
func (b *buckets) get(key checkKey, now time.Time) *checkBucket {
	bucket, found := b.by[key]
	if !found {
		if len(b.by) >= maxIdleBuckets {
			b.prune(now)
		}
		bucket = &checkBucket{tokens: b.burst, at: now}
		b.by[key] = bucket
	}
	bucket.tokens = min(b.burst, bucket.tokens+float64(now.Sub(bucket.at))/float64(b.every))
	bucket.at = now
	return bucket
}

// prune forgets the buckets that have filled up again.
func (b *buckets) prune(now time.Time) {
	for key, bucket := range b.by {
		if bucket.tokens+float64(now.Sub(bucket.at))/float64(b.every) >= b.burst {
			delete(b.by, key)
		}
	}
}

// checks is the limiter the check route uses.
func (e *Extension) checks() *checkLimiter {
	e.checkLimitOnce.Do(func() {
		if e.checkLimit == nil {
			e.checkLimit = newCheckLimiter(checksPerMinute, checksPerUserPerMinute)
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
