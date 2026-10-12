package security

import (
	"context"

	"github.com/xraph/authsome/organization"
	"github.com/xraph/authsome/plugin"
)

// Sync is an authsome plugin that mirrors organization membership into
// Warden: a new organization gets the role catalog and its creator the
// owner role, and members get, change and lose their role as authsome
// adds, changes and removes them.
//
// authsome logs a hook's error instead of failing the change, so a failed
// sync leaves the member without a role. Authorizer.Require repairs that on
// the member's next request.
type Sync struct {
	authz *Authorizer
}

// NewSync returns the plugin.
func NewSync(authz *Authorizer) *Sync { return &Sync{authz: authz} }

var (
	_ plugin.Plugin                = (*Sync)(nil)
	_ plugin.AfterOrgCreate        = (*Sync)(nil)
	_ plugin.AfterMemberAdd        = (*Sync)(nil)
	_ plugin.AfterMemberRoleChange = (*Sync)(nil)
	_ plugin.BeforeMemberRemove    = (*Sync)(nil)
)

// Name implements plugin.Plugin.
func (s *Sync) Name() string { return "loomplane-warden-sync" }

// OnAfterOrgCreate seeds the new organization's roles and makes its creator
// the owner. authsome adds the creator as a member without firing
// AfterMemberAdd, so this is the creator's only hook.
func (s *Sync) OnAfterOrgCreate(ctx context.Context, o *organization.Organization) error {
	orgID := o.ID.String()
	if err := s.authz.EnsureTenant(ctx, orgID); err != nil {
		return err
	}
	return s.authz.Assign(ctx, orgID, o.CreatedBy.String(), RoleOwner, "authsome:org.create")
}

// OnAfterMemberAdd gives a new member their role.
func (s *Sync) OnAfterMemberAdd(ctx context.Context, m *organization.Member) error {
	return s.authz.Assign(ctx, m.OrgID.String(), m.UserID.String(), RoleForMember(string(m.Role)), "authsome:member.add")
}

// OnAfterMemberRoleChange follows a role change.
func (s *Sync) OnAfterMemberRoleChange(ctx context.Context, m *organization.Member) error {
	return s.authz.Assign(ctx, m.OrgID.String(), m.UserID.String(), RoleForMember(string(m.Role)), "authsome:member.role_change")
}

// OnBeforeMemberRemove takes the member's role away. It's the "before" hook
// because the "after" one only carries the member's ID, not who left which
// organization.
func (s *Sync) OnBeforeMemberRemove(ctx context.Context, m *organization.Member) error {
	return s.authz.Revoke(ctx, m.OrgID.String(), m.UserID.String())
}
