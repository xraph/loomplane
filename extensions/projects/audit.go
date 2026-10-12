package projects

import (
	"context"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/projects"
)

// auditor records project changes in Chronicle, in the organization's
// stream: tenant = organization, app = Loomplane's authsome app.
type auditor struct {
	chronicle *chronicle.Chronicle
	appID     string
	log       forge.Logger
}

// Record implements projects.Auditor. A failed record is logged, never
// returned: the change it describes has already happened.
func (a auditor) Record(ctx context.Context, e projects.AuditEvent) {
	var b *chronicle.EventBuilder
	switch e.Severity {
	case "warning":
		b = a.chronicle.Warning(ctx, e.Action, "project", e.ProjectID)
	case "critical":
		b = a.chronicle.Critical(ctx, e.Action, "project", e.ProjectID)
	default:
		b = a.chronicle.Info(ctx, e.Action, "project", e.ProjectID)
	}
	b = b.Category("projects").AppID(a.appID).TenantID(e.Principal.OrgID).UserID(e.Principal.UserID).Outcome("success")
	for k, v := range e.Attributes {
		b = b.Meta(k, v)
	}
	if err := b.Record(); err != nil {
		a.log.Error("record audit event", forge.F("action", e.Action), forge.F("project_id", e.ProjectID), forge.F("error", err.Error()))
	}
}
