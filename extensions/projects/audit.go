package projects

import (
	"context"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// Auditor records project changes in Chronicle, in the organization's
// stream: tenant = organization, app = Loomplane's authsome app. The
// discovery extension records analyses through it too.
type Auditor struct {
	chronicle *chronicle.Chronicle
	appID     string
	log       forge.Logger
}

// NewAuditor returns an Auditor over c.
func NewAuditor(c *chronicle.Chronicle, appID string, log forge.Logger) Auditor {
	return Auditor{chronicle: c, appID: appID, log: log}
}

// Record implements projects.Auditor. A failed record is logged, never
// returned: the change it describes has already happened.
func (a Auditor) Record(ctx context.Context, e projects.AuditEvent) {
	var b *chronicle.EventBuilder
	switch e.Severity {
	case "warning":
		b = a.chronicle.Warning(ctx, e.Action, "project", e.ProjectID)
	case "critical":
		b = a.chronicle.Critical(ctx, e.Action, "project", e.ProjectID)
	default:
		b = a.chronicle.Info(ctx, e.Action, "project", e.ProjectID)
	}
	outcome := e.Outcome
	if outcome == "" {
		outcome = "success"
	}
	b = b.Category("projects").AppID(a.appID).TenantID(e.Principal.OrgID).UserID(e.Principal.UserID).Outcome(outcome)
	for k, v := range e.Attributes {
		b = b.Meta(k, v)
	}
	// Set after the attributes, so an event can't name its own actor.
	if e.Principal.IsSystem() {
		b = b.Meta("actor", "system").Meta("actor_name", security.SystemActorName)
	}
	if err := b.Record(); err != nil {
		a.log.Error("record audit event", forge.F("action", e.Action), forge.F("project_id", e.ProjectID), forge.F("error", err.Error()))
	}
}
