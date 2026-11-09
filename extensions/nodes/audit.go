package nodes

import (
	"context"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/security"
)

// auditor records node events in Chronicle, in the organization's stream.
type auditor struct {
	chronicle *chronicle.Chronicle
	appID     string
	log       forge.Logger
}

func (a auditor) Record(ctx context.Context, action string, p security.Principal, n nodes.Node, meta map[string]string) {
	b := a.chronicle.Warning(ctx, action, "node", n.ID).
		Category("nodes").AppID(a.appID).TenantID(n.OrgID).UserID(p.UserID).Outcome("success").
		Meta("name", n.Name).Meta("role", n.Role).Meta("status", string(n.Status)).Meta("key_fingerprint", n.KeyFingerprint)
	for name, v := range meta {
		b = b.Meta(name, v)
	}
	if err := b.Record(); err != nil {
		a.log.Error("record audit event", forge.F("action", action), forge.F("error", err.Error()))
	}
}
