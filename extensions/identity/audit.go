package identity

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xraph/authsome/bridge"
	"github.com/xraph/authsome/bridge/chronicleadapter"
	"github.com/xraph/chronicle"
	"github.com/xraph/chronicle/audit"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/platformadmin"
	"github.com/xraph/loomplane/internal/telemetry"
)

// authAuditCategory files authsome's own events in Chronicle.
const authAuditCategory = "auth"

// categorized is the Chronicle emitter authsome writes through.
//
// authsome leaves the category empty on most of its events (sign-in,
// organization changes, invitations), and Chronicle refuses an event
// without one. Every such event was logged as "audit record failed" and
// never stored, so the audit trail had nothing from authsome at all. This
// starts each event in a default category. authsome's adapter sets the
// event's own category afterwards, so an event that names one keeps it.
type categorized struct {
	chronicle.Emitter
	category string
}

func (c categorized) Record(ctx context.Context, event *audit.Event) error {
	if event.Category == "" {
		event.Category = c.category
	}
	return c.Emitter.Record(ctx, event)
}

func (c categorized) Info(ctx context.Context, action, resource, resourceID string) *chronicle.EventBuilder {
	return c.Emitter.Info(ctx, action, resource, resourceID).Category(c.category)
}

func (c categorized) Warning(ctx context.Context, action, resource, resourceID string) *chronicle.EventBuilder {
	return c.Emitter.Warning(ctx, action, resource, resourceID).Category(c.category)
}

func (c categorized) Critical(ctx context.Context, action, resource, resourceID string) *chronicle.EventBuilder {
	return c.Emitter.Critical(ctx, action, resource, resourceID).Category(c.category)
}

// staffStream is the bridge authsome records through. An event with no
// organization whose actor holds a platform role goes to the staff stream,
// platformadmin.AuditTenant. Everything else keeps the stream it had: an
// organization's events stay in that organization's, and a customer's or
// an anonymous event with no organization stays at the app level.
//
// A role that can't be read counts as not staff, so the staff stream never
// takes a customer's event on a guess. isStaff is staffRoles', which caches
// each answer briefly and counts the failures.
type staffStream struct {
	bridge.Chronicle
	isStaff func(ctx context.Context, userID string) (bool, error)
}

// authsomeChronicle is the Chronicle bridge authsome records through:
// categorized, and routed to the staff stream by the actor's platform
// roles, cached by staffRoles.
func (e *Extension) authsomeChronicle() bridge.Chronicle {
	return staffStream{
		Chronicle: chronicleadapter.New(categorized{Emitter: e.audit, category: authAuditCategory}),
		isStaff:   newStaffRoles(e.readStaff, e.Logger(), e.telemetry).isStaff,
	}
}

// readStaff reads whether a user holds a platform role now, from Warden.
// Unlike IsPlatformAdmin it returns the failure, for staffRoles to count.
func (e *Extension) readStaff(ctx context.Context, userID string) (bool, error) {
	roles, err := e.platformRoles.ListUserRolesForApp(ctx, e.cfg.Identity.AppID, userID)
	if err != nil {
		return false, err
	}
	return platformadmin.Holds(roles, e.cfg.Identity.AppID), nil
}

func (s staffStream) Record(ctx context.Context, event *bridge.AuditEvent) error {
	if event.OrgID != "" || event.ActorID == "" {
		return s.Chronicle.Record(ctx, event)
	}
	if staff, err := s.isStaff(ctx, event.ActorID); err != nil || !staff {
		return s.Chronicle.Record(ctx, event)
	}
	routed := *event
	routed.OrgID = platformadmin.AuditTenant
	return s.Chronicle.Record(ctx, &routed)
}

// staffRoles caches staffStream's answer per user. Every authsome event
// with an actor and no organization asks it, a sign-in or a session
// refresh included, so reading Warden each time would put a role read on
// the path of each of them. A role granted or revoked shows in the stream
// within staffRolesTTL.
//
// A failed read isn't cached, so the next event asks again. Each failure
// sends that event to the app level, where it would have gone without
// the staff stream. That is counted (loomplane_audit_staff_check_failures_total)
// and warned about at most once a minute, so a Warden outage that moves
// staff events out of the platform stream doesn't pass unnoticed.
const (
	staffRolesTTL       = 30 * time.Second
	staffRolesMax       = 4096
	staffRolesWarnEvery = time.Minute
)

type staffRoles struct {
	read      func(ctx context.Context, userID string) (bool, error)
	log       forge.Logger
	telemetry *telemetry.Recorder
	now       func() time.Time

	// failures counts every failed read since start.
	failures atomic.Uint64

	mu      sync.Mutex
	entries map[string]staffEntry
	// unwarned counts failures since the last warning, and warned is when
	// that went out.
	unwarned uint64
	warned   time.Time
}

type staffEntry struct {
	staff bool
	until time.Time
}

func newStaffRoles(read func(ctx context.Context, userID string) (bool, error), log forge.Logger, rec *telemetry.Recorder) *staffRoles {
	if log == nil {
		log = forge.NewNoopLogger()
	}
	return &staffRoles{read: read, log: log, telemetry: rec, now: time.Now, entries: map[string]staffEntry{}}
}

// isStaff is staffStream's isStaff: the cached answer while it is fresh,
// otherwise a read of Warden.
func (s *staffRoles) isStaff(ctx context.Context, userID string) (bool, error) {
	s.mu.Lock()
	e, ok := s.entries[userID]
	now := s.now()
	s.mu.Unlock()
	if ok && now.Before(e.until) {
		return e.staff, nil
	}
	staff, err := s.read(ctx, userID)
	if err != nil {
		s.failed(userID, err)
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, held := s.entries[userID]; !held && len(s.entries) >= staffRolesMax {
		s.evictLocked(now)
	}
	s.entries[userID] = staffEntry{staff: staff, until: now.Add(staffRolesTTL)}
	return staff, nil
}

// evictLocked makes room for one more user: every expired answer goes,
// and when none had expired, one answer chosen by map order. s.mu is held.
func (s *staffRoles) evictLocked(now time.Time) {
	for id, e := range s.entries {
		if !now.Before(e.until) {
			delete(s.entries, id)
		}
	}
	for id := range s.entries {
		if len(s.entries) < staffRolesMax {
			return
		}
		delete(s.entries, id)
	}
}

// failed counts a read that failed, and warns when a minute has passed
// since the last warning.
func (s *staffRoles) failed(userID string, err error) {
	s.failures.Add(1)
	s.telemetry.AuditStaffCheckFailed()
	s.mu.Lock()
	s.unwarned++
	now := s.now()
	if !s.warned.IsZero() && now.Sub(s.warned) < staffRolesWarnEvery {
		s.mu.Unlock()
		return
	}
	events := s.unwarned
	s.unwarned, s.warned = 0, now
	s.mu.Unlock()
	s.log.Warn("couldn't read platform roles, so authsome events went to the app level instead of the platform stream",
		forge.F("events", events), forge.F("user_id", userID), forge.F("error", err.Error()))
}
