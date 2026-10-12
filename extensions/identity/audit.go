package identity

import (
	"context"

	"github.com/xraph/chronicle"
	"github.com/xraph/chronicle/audit"
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
