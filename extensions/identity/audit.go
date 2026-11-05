package identity

import (
	"context"

	"github.com/xraph/authsome/bridge"
	"github.com/xraph/chronicle"
	"github.com/xraph/chronicle/audit"

	"github.com/xraph/loomplane/internal/platformadmin"
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
// takes a customer's event on a guess.
type staffStream struct {
	bridge.Chronicle
	isStaff func(ctx context.Context, userID string) (bool, error)
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
