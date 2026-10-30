package security

import (
	"context"
	"errors"
	"sync"

	authid "github.com/xraph/authsome/id"
	"github.com/xraph/authsome/organization"
	"github.com/xraph/authsome/plugin"
)

// Sync is an authsome plugin that mirrors organization membership into
// Warden: a new organization gets the role catalog and its creator the
// owner role, and members get, change and lose their role as authsome
// adds, changes and removes them.
//
// authsome logs an "after" hook's error instead of failing the change, so
// a failed sync leaves the member without a role. Authorizer.Require
// repairs that on the member's next request. The "before remove" hook is
// different: its error aborts the removal (see OnBeforeMemberRemove).
type Sync struct {
	authz  *Authorizer
	leaver Leaver // takes a leaving member out of their teams (teams.go)

	// leaving maps the ID of a member being removed to who they are, from
	// the before-hook to the after-hook, which carries only the ID.
	leaving sync.Map // member ID -> leavingMember
}

// leavingMember is who a member being removed is.
type leavingMember struct{ orgID, userID string }

// NewSync returns the plugin.
func NewSync(authz *Authorizer) *Sync { return &Sync{authz: authz} }

var (
	_ plugin.Plugin                = (*Sync)(nil)
	_ plugin.AfterOrgCreate        = (*Sync)(nil)
	_ plugin.AfterMemberAdd        = (*Sync)(nil)
	_ plugin.AfterMemberRoleChange = (*Sync)(nil)
	_ plugin.BeforeMemberRemove    = (*Sync)(nil)
	_ plugin.AfterMemberRemove     = (*Sync)(nil)
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

// OnBeforeMemberRemove takes the member out of their teams, then takes
// every role away. The after-hook (OnAfterMemberRemove) carries only the
// member's ID, so this hook also remembers who is leaving for it.
//
// authsome aborts the removal when this hook fails: the person stays a
// member, and whatever this hook already revoked comes back on their next
// request through Require's repair. Revoke runs even when the leaver
// failed, so nobody is left holding rights in that case either. It runs
// under the person's sync lock, so a sync that read their teams before the
// leaver ran finishes first and its rows go too. If the lock can't be had,
// it revokes anyway, and the lock's error aborts the removal.
func (s *Sync) OnBeforeMemberRemove(ctx context.Context, m *organization.Member) error {
	orgID, userID := m.OrgID.String(), m.UserID.String()
	var leaveErr error
	if s.leaver != nil {
		leaveErr = s.leaver(ctx, orgID, userID)
	}
	err := errors.Join(leaveErr, s.revokeLocked(ctx, orgID, userID))
	if err == nil {
		s.leaving.Store(m.ID.String(), leavingMember{orgID: orgID, userID: userID})
	}
	return err
}

// OnAfterMemberRemove revokes the member's roles again, under their lock,
// once authsome has deleted the membership. Between the before-hook and
// the delete the person was still a member, so a sync in that window (a
// repair from one of their own requests, or the teams queue) could write
// their member role back. Any sync after the delete finds no membership
// and revokes, and the lock makes one that started in the window finish
// first. authsome only logs this hook's error.
func (s *Sync) OnAfterMemberRemove(ctx context.Context, memberID authid.MemberID) error {
	v, ok := s.leaving.LoadAndDelete(memberID.String())
	if !ok {
		return nil
	}
	who, ok := v.(leavingMember)
	if !ok {
		return nil
	}
	return s.revokeLocked(ctx, who.orgID, who.userID)
}

// revokeLocked revokes the person's roles under their sync lock, and
// without it when the lock can't be had.
func (s *Sync) revokeLocked(ctx context.Context, orgID, userID string) error {
	revoked := false
	err := s.authz.withPersonLock(ctx, orgID, userID, func(ctx context.Context) error {
		revoked = true
		return s.authz.Revoke(ctx, orgID, userID)
	})
	if !revoked {
		err = errors.Join(err, s.authz.Revoke(ctx, orgID, userID))
	}
	return err
}
