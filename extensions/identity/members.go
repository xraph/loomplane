package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/xraph/authsome/account"
	authid "github.com/xraph/authsome/id"
	"github.com/xraph/authsome/organization"
	authstore "github.com/xraph/authsome/store"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/security"
)

// invitationLifetime is how long an invitation link works. authsome's own
// invitation endpoint sets no expiry at all, so its links never lapse.
const invitationLifetime = 7 * 24 * time.Hour

// OrgMember is a person in the organization, with the profile authsome's
// member record leaves out.
//
// The Org prefix matters. The dashboard keeps one normalized cache for
// Loomplane's API and authsome's, keyed by type name and ID, and authsome
// already has a Member and an Invitation with these same IDs whose role
// field means something else ("member" where Loomplane says "developer").
// Sharing the name would let one overwrite the other.
type OrgMember struct {
	// ID is authsome's member ID, which authsome's role change and removal
	// endpoints take.
	ID     string `json:"id" forge:"id"`
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	// Role is the Loomplane role: owner, admin, developer or viewer.
	Role string `json:"role"`
	// MemberRole is the same role in authsome's terms, for its endpoints.
	MemberRole string    `json:"member_role"`
	JoinedAt   time.Time `json:"joined_at"`
}

// OrgMemberList is the body of GET /api/v1/members.
type OrgMemberList struct {
	Members []OrgMember `json:"members"`
}

// ForgeEnvelope tells the generated client the list wraps OrgMembers.
func (OrgMemberList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "members"}
}

// OrgInvitation is an invitation that hasn't been accepted yet.
type OrgInvitation struct {
	ID        string    `json:"id" forge:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	InvitedBy string    `json:"invited_by"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// OrgInvitationList is the body of GET /api/v1/invitations.
type OrgInvitationList struct {
	Invitations []OrgInvitation `json:"invitations"`
}

// ForgeEnvelope tells the generated client the list wraps OrgInvitations.
func (OrgInvitationList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "invitations"}
}

// CreateInvitationInput is the body of POST /api/v1/invitations.
type CreateInvitationInput struct {
	Email string `json:"email"`
	// Role is the Loomplane role to give; developer when empty.
	Role string `json:"role,omitempty"`
}

// CreatedInvitation is the new invitation and the path that accepts it.
// The path carries the invitation's secret, and this is the only time
// Loomplane hands it out: Loomplane sends no email yet, so whoever invites
// passes the link on.
type CreatedInvitation struct {
	Invitation OrgInvitation `json:"invitation"`
	AcceptPath string        `json:"accept_path"`
}

func (e *Extension) memberRoutes(v1 forge.Router) error {
	if err := v1.GET("/members", e.listMembers,
		forge.WithName("members.list"),
		forge.WithSummary("The organization's members, with their names and emails"),
		forge.WithTags("Members"),
		forge.WithResponseSchema(http.StatusOK, "Members", OrgMemberList{}),
	); err != nil {
		return err
	}
	if err := v1.GET("/invitations", e.listInvitations,
		forge.WithName("invitations.list"),
		forge.WithSummary("Invitations nobody has accepted yet"),
		forge.WithTags("Members"),
		forge.WithResponseSchema(http.StatusOK, "Pending invitations", OrgInvitationList{}),
	); err != nil {
		return err
	}
	return v1.POST("/invitations", e.createInvitation,
		forge.WithName("invitations.create"),
		forge.WithSummary("Invite someone by email and get the link that lets them join"),
		forge.WithTags("Members"),
		forge.WithRequestSchema(CreateInvitationInput{}),
		forge.WithResponseSchema(http.StatusCreated, "The invitation and its link", CreatedInvitation{}),
		// The response wraps the invitation with its link, so it isn't the
		// entity itself; the pending list goes stale instead.
		forge.WithoutEntity(),
		forge.WithInvalidates("OrgInvitation[]"),
	)
}

func (e *Extension) listMembers(c forge.Context) error {
	ctx, p, err := e.require(c, security.MemberRead)
	if err != nil {
		return err
	}
	orgID, err := authid.ParseOrgID(p.OrgID)
	if err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the session's organization isn't valid")
	}
	members, err := e.orgs.ListMembers(ctx, orgID)
	if err != nil {
		return fmt.Errorf("list members: %w", err)
	}
	out := OrgMemberList{Members: make([]OrgMember, 0, len(members))}
	for _, m := range members {
		email, name := e.profile(ctx, m.UserID)
		out.Members = append(out.Members, OrgMember{
			ID: m.ID.String(), UserID: m.UserID.String(), Email: email, Name: name,
			Role: security.RoleForMember(string(m.Role)), MemberRole: string(m.Role), JoinedAt: m.CreatedAt.UTC(),
		})
	}
	sort.SliceStable(out.Members, func(i, j int) bool {
		a, b := out.Members[i], out.Members[j]
		if ra, rb := security.MayGrant(a.MemberRole, b.MemberRole), security.MayGrant(b.MemberRole, a.MemberRole); ra != rb {
			return ra // higher roles first
		}
		return strings.ToLower(a.Email) < strings.ToLower(b.Email)
	})
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) listInvitations(c forge.Context) error {
	ctx, p, err := e.require(c, security.MemberManage)
	if err != nil {
		return err
	}
	orgID, err := authid.ParseOrgID(p.OrgID)
	if err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the session's organization isn't valid")
	}
	invs, err := e.orgs.ListInvitations(ctx, orgID)
	if err != nil {
		return fmt.Errorf("list invitations: %w", err)
	}
	now := time.Now()
	out := OrgInvitationList{Invitations: make([]OrgInvitation, 0, len(invs))}
	for _, inv := range invs {
		if inv.Status != organization.InvitationPending || (!inv.ExpiresAt.IsZero() && now.After(inv.ExpiresAt)) {
			continue
		}
		out.Invitations = append(out.Invitations, e.invitation(ctx, inv))
	}
	sort.SliceStable(out.Invitations, func(i, j int) bool {
		return out.Invitations[i].CreatedAt.After(out.Invitations[j].CreatedAt)
	})
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) createInvitation(c forge.Context) error {
	ctx, p, err := e.require(c, security.MemberManage)
	if err != nil {
		return err
	}
	var in CreateInvitationInput
	if err = c.BindJSON(&in); err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the body must be a JSON invitation")
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(in.Email))
	if err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "that isn't an email address")
	}
	role := in.Role
	if role == "" {
		role = security.RoleDeveloper
	}
	memberRole, err := security.MemberRoleFor(role)
	if err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	orgID, err := authid.ParseOrgID(p.OrgID)
	if err != nil {
		return forge.NewHTTPError(http.StatusBadRequest, "the session's organization isn't valid")
	}
	userID, err := authid.ParseUserID(p.UserID)
	if err != nil {
		return forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	inviter, err := e.store.GetMemberByUserAndOrg(ctx, userID, orgID)
	if err != nil {
		return forge.NewHTTPError(http.StatusForbidden, "you aren't a member of this organization")
	}
	if !security.MayGrant(string(inviter.Role), memberRole) {
		return forge.NewHTTPError(http.StatusForbidden, "you can't invite someone to a role above your own")
	}
	token, err := account.GenerateVerificationToken()
	if err != nil {
		return fmt.Errorf("invitation token: %w", err)
	}
	now := time.Now().UTC()
	inv := &organization.Invitation{
		ID: authid.NewInvitationID(), OrgID: orgID, Email: addr.Address,
		Role: organization.MemberRole(memberRole), InviterID: userID,
		Status: organization.InvitationPending, Token: token,
		ExpiresAt: now.Add(invitationLifetime), CreatedAt: now,
	}
	if err := e.orgs.CreateInvitation(ctx, inv); err != nil {
		if errors.Is(err, authstore.ErrConflict) {
			return forge.NewHTTPError(http.StatusConflict, "that email already has a pending invitation")
		}
		return fmt.Errorf("create invitation: %w", err)
	}
	if err := e.audit.Info(ctx, "member.invite", "invitation", inv.ID.String()).
		Category("members").AppID(e.cfg.Identity.AppID).TenantID(p.OrgID).UserID(p.UserID).
		Meta("role", role).Record(); err != nil {
		e.Logger().Error("record audit event", forge.F("action", "member.invite"), forge.F("error", err.Error()))
	}
	return c.JSON(http.StatusCreated, CreatedInvitation{
		Invitation: e.invitation(ctx, inv),
		AcceptPath: "/invitations/accept?token=" + url.QueryEscape(token),
	})
}

// require reads the principal and checks the permission.
func (e *Extension) require(c forge.Context, perm security.Permission) (context.Context, security.Principal, error) {
	ctx := c.Request().Context()
	p, ok := security.PrincipalFrom(ctx)
	if !ok {
		return ctx, p, forge.NewHTTPError(http.StatusUnauthorized, security.ErrUnauthenticated.Error())
	}
	if err := e.authz.Require(ctx, p, perm, ""); err != nil {
		if status := security.StatusFor(err); status != http.StatusInternalServerError {
			return ctx, p, forge.NewHTTPError(status, err.Error())
		}
		return ctx, p, err
	}
	return ctx, p, nil
}

func (e *Extension) invitation(ctx context.Context, inv *organization.Invitation) OrgInvitation {
	invitedBy, _ := e.profile(ctx, inv.InviterID)
	return OrgInvitation{
		ID: inv.ID.String(), Email: inv.Email, Role: security.RoleForMember(string(inv.Role)),
		InvitedBy: invitedBy, CreatedAt: inv.CreatedAt.UTC(), ExpiresAt: inv.ExpiresAt.UTC(),
	}
}

// profile is a user's email and display name, or blanks when the account
// can't be read: a missing profile shouldn't hide the membership.
func (e *Extension) profile(ctx context.Context, userID authid.UserID) (email, name string) {
	u, err := e.store.GetUser(ctx, userID)
	if err != nil {
		if !errors.Is(err, authstore.ErrNotFound) {
			e.Logger().Warn("read member profile", forge.F("user_id", userID.String()), forge.F("error", err.Error()))
		}
		return "", ""
	}
	return u.Email, strings.TrimSpace(u.Name())
}
