package infrastructure

import (
	"context"

	cpauth "github.com/xraph/ctrlplane/auth"

	"github.com/xraph/loomplane/internal/security"
)

// consoleReads are the Ctrl Plane reads the instance console makes. Ctrl
// Plane keeps each organization's data in that organization's tenant and
// the console has no organization, so it reads as a system admin with no
// tenant. Ctrl Plane's stores take an empty tenant as every tenant for some
// reads (workloads, with their services' env and config files), so only
// these, which answer about Ctrl Plane itself rather than an organization's
// resources, are open. Everything else waits for an organization picker.
var consoleReads = map[string]bool{
	"session.detail": true,
	"system.stats":   true,
	"tenants.list":   true,
	"tenants.detail": true,
	"tenants.quota":  true,
	"providers.list": true,
	"workers.list":   true,
	"workers.detail": true,
	"events.list":    true,
	"config.detail":  true,
	"audit.list":     true,
}

// ConsoleRead reports whether the instance console may read Ctrl Plane's
// intent.
func ConsoleRead(intent string) bool { return consoleReads[intent] }

// consoleRead reports whether req is a console read Ctrl Plane's policy
// should allow: marked by the console gate, by a system admin with no
// tenant, for a request that names none, of an intent the console may read.
func consoleRead(ctx context.Context, req cpauth.AuthzRequest) bool {
	if !security.IsConsoleRead(ctx) {
		return false
	}
	c := cpauth.ClaimsFrom(ctx)
	return c != nil && c.IsSystemAdmin() && c.TenantID == "" && req.TenantID == "" && ConsoleRead(req.Action)
}
