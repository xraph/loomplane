package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/authsome/rbac"
	"github.com/xraph/forge"
	"github.com/xraph/warden"
	wardenid "github.com/xraph/warden/id"
	"github.com/xraph/warden/permission"
	"github.com/xraph/warden/role"
)

// consoleWardenReads are the grants Warden's dashboard contract checks
// before each of its reads (warden extension/contract/authz.go), as
// (resource, action). The platform roles get these and no write: every
// "manage" grant stays off, and the console gate blocks Warden's commands
// anyway. playground.explain and playground.batchCheck need "check": they
// ask the engine a question as a dry run and change nothing.
var consoleWardenReads = []struct{ resource, action string }{
	{"warden:config", "read"},
	{"warden:overview", "read"},
	{"warden:check_log", "read_audit"},
	{"warden:role", "read"},
	{"warden:permission", "read"},
	{"warden:assignment", "read"},
	{"warden:relation", "read"},
	{"warden:resourcetype", "read"},
	{"warden:policy", "read"},
	{"warden:authz", "check"},
}

// consoleReaderRoles are the platform roles the console admits.
var consoleReaderRoles = []string{rbac.PlatformOwnerSlug, rbac.PlatformAdminSlug}

// platformNamespace is the Warden namespace authsome's bootstrap puts the
// platform roles in (its platform.warden).
const platformNamespace = "platform"

// grantConsoleReads gives the platform roles Warden's read grants, so the
// console's Warden pages answer them. It runs on every start, after
// authsome's bootstrap has applied the platform roles, under the startup
// lock, and adds only what's missing. The console is a convenience, so
// nothing here stops Loomplane starting: a role it can't find or grant is
// logged and skipped.
func (e *Extension) grantConsoleReads(ctx context.Context) {
	tenant := e.cfg.Identity.AppID
	for _, slug := range consoleReaderRoles {
		r, err := e.warden.Store().GetRoleBySlug(ctx, tenant, platformNamespace, slug)
		if err == nil {
			err = e.grantRoleConsoleReads(ctx, r)
		}
		if err != nil {
			e.Logger().Warn("the console's Warden pages won't answer this role", forge.F("role", slug), forge.F("error", err.Error()))
		}
	}
	e.warden.InvalidateTenant(ctx, tenant)
}

func (e *Extension) grantRoleConsoleReads(ctx context.Context, r *role.Role) error {
	store := e.warden.Store()
	held, err := store.ListRolePermissions(ctx, r.TenantID, r.ID)
	if err != nil {
		return fmt.Errorf("list permissions of %s: %w", r.Slug, err)
	}
	has := map[string]bool{}
	for _, p := range held {
		has[p.Resource+":"+p.Action] = true
	}
	now := time.Now().UTC()
	for _, g := range consoleWardenReads {
		name := g.resource + ":" + g.action
		if has[name] {
			continue
		}
		// The permission lives beside the role, in its namespace.
		if _, err := store.GetPermissionByName(ctx, r.TenantID, r.NamespacePath, name); errors.Is(err, warden.ErrNotFound) {
			err = store.CreatePermission(ctx, &permission.Permission{
				ID: wardenid.NewPermissionID(), TenantID: r.TenantID, NamespacePath: r.NamespacePath,
				Name: name, Resource: g.resource, Action: g.action,
				IsSystem: true, CreatedBy: "loomplane", CreatedAt: now, UpdatedAt: now,
			})
			if err != nil && !errors.Is(err, warden.ErrAlreadyExists) {
				return fmt.Errorf("create permission %s: %w", name, err)
			}
		} else if err != nil {
			return fmt.Errorf("look up permission %s: %w", name, err)
		}
		if err := store.AttachPermission(ctx, r.TenantID, r.ID, permission.Ref{NamespacePath: r.NamespacePath, Name: name}); err != nil {
			return fmt.Errorf("grant %s to %s: %w", name, r.Slug, err)
		}
	}
	return nil
}
