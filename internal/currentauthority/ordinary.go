package currentauthority

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	authid "github.com/xraph/authsome/id"
	keyid "github.com/xraph/keysmith/id"
	"github.com/xraph/keysmith/key"
	"github.com/xraph/warden"
	"github.com/xraph/warden/assignment"
	"github.com/xraph/warden/role"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/security"
)

// ordinaryProfile captures the qualified Authorizer and startup policy.
// Its values remain fixed after publication by the owned installer.
type ordinaryProfile struct {
	authorizer    *security.Authorizer
	appID         string
	requireEmail  bool
	migrationRole uint32
}

type optionalInstant struct {
	value   time.Time
	present bool
}

func instant(t *time.Time) optionalInstant {
	if t == nil {
		return optionalInstant{}
	}
	return optionalInstant{value: *t, present: true}
}

type ordinaryFacts struct {
	user, app, env, org, orgApp, member, memberRole        string
	verified, banned, deleted                              bool
	banUntil                                               optionalInstant
	keyID, keyApp, keyOrg, keyCreator, keyState, keyAccess string
	keyRevoked, keyPolicy, keyKind, keyScopes              bool
	keyUntil                                               optionalInstant
}

// MaxOrdinaryRequirements bounds the number of permission checks in one attempt.
const MaxOrdinaryRequirements = 16

// OrdinaryRequirement names a permission and the exact resource to check.
type OrdinaryRequirement struct {
	Permission security.Permission
	ResourceID string
}

type ordinaryRequirementProof struct {
	permission          security.Permission
	resource, namespace string
}

func compareOrdinaryRequirements(left, right ordinaryRequirementProof) int {
	return cmp.Or(
		cmp.Compare(left.permission.Resource, right.permission.Resource),
		cmp.Compare(left.permission.Action, right.permission.Action),
		cmp.Compare(left.resource, right.resource),
	)
}

type ordinaryProof struct {
	requirements [MaxOrdinaryRequirements]ordinaryRequirementProof
	count        int
	facts        ordinaryFacts
	requireEmail bool
}

func (p *ordinaryProof) validRequirements() bool {
	if p.count < 1 || p.count > MaxOrdinaryRequirements {
		return false
	}
	for i, req := range p.requirements[:p.count] {
		ns, _ := security.ProjectNamespace(req.resource)
		if req.permission.Resource == "" || req.permission.Action == "" || req.namespace != ns {
			return false
		}
		if i > 0 && compareOrdinaryRequirements(p.requirements[i-1], req) >= 0 {
			return false
		}
	}
	for _, req := range p.requirements[p.count:] {
		if req != (ordinaryRequirementProof{}) {
			return false
		}
	}
	return true
}

func (f ordinaryFacts) validate(b binding, requiredEmail bool, at time.Time) error {
	if at.IsZero() || f.user != b.user || f.app != b.app || f.org != b.org || f.orgApp != b.app || f.member == "" || f.memberRole == "" || f.deleted || (requiredEmail && !f.verified) {
		return security.ErrForbidden
	}
	if _, err := authid.ParseUserID(f.user); err != nil {
		return security.ErrForbidden
	}
	if _, err := authid.ParseAppID(f.app); err != nil {
		return security.ErrForbidden
	}
	if _, err := authid.ParseEnvironmentID(f.env); err != nil {
		return security.ErrForbidden
	}
	if _, err := authid.ParseOrgID(f.org); err != nil {
		return security.ErrForbidden
	}
	if _, err := authid.ParseMemberID(f.member); err != nil {
		return security.ErrForbidden
	}
	if f.banned && (!f.banUntil.present || f.banUntil.value.After(at)) {
		return security.ErrForbidden
	}
	if b.key == "" {
		if f.keyID != "" {
			return ErrBinding
		}
		return nil
	}
	if f.keyID != b.key || f.keyRevoked || f.keyPolicy || f.keyKind || f.keyScopes || !f.keyUntil.present {
		return security.ErrForbidden
	}
	kid, err := keyid.ParseKeyID(f.keyID)
	if err != nil {
		return security.ErrForbidden
	}
	expiry := f.keyUntil.value
	record := &key.Key{ID: kid, AppID: f.keyApp, TenantID: f.keyOrg, CreatedBy: f.keyCreator, State: key.State(f.keyState), ExpiresAt: &expiry, Metadata: map[string]any{"access": f.keyAccess}}
	return apikeys.ValidateSavedKeyRecordAt(actor(b), b.app, record, at)
}

func actor(b binding) security.Principal {
	p := security.Principal{Kind: b.kind, UserID: b.user, OrgID: b.org, KeyID: b.key}
	if b.kind == security.KindFleet {
		p.Fleet = &security.FleetScope{ProjectID: b.project, FleetID: b.fleet}
	}
	return p
}

// EvaluateOrdinary checks the bound person and exact resource after domain guards.
// It requires the private ordinary profile qualified by the owned installer.
func (a *Attempt) EvaluateOrdinary(ctx context.Context, permission security.Permission, resourceID string) (*Decision, error) {
	return a.EvaluateOrdinaryAll(ctx, []OrdinaryRequirement{{Permission: permission, ResourceID: resourceID}})
}

// EvaluateOrdinaryAll checks every requirement in one authority interval after
// domain guards. Supply the complete set for the operation you will stage.
func (a *Attempt) EvaluateOrdinaryAll(ctx context.Context, requirements []OrdinaryRequirement) (*Decision, error) {
	if a.binding.kind != security.KindUser {
		return nil, a.fail(ErrBinding)
	}
	if a.profile == nil || a.profile.mode != installedProfile || a.profile.ordinary == nil || a.profile.ordinary.authorizer == nil {
		return nil, a.fail(ErrUnavailable)
	}
	if a.binding.app != a.profile.ordinary.appID {
		return nil, a.fail(ErrBinding)
	}
	count := len(requirements)
	if count < 1 || count > MaxOrdinaryRequirements {
		return nil, a.fail(ErrDecision)
	}
	ordinary := a.profile.ordinary
	proof := &ordinaryProof{count: count, requireEmail: ordinary.requireEmail}
	checks := proof.requirements[:count]
	for i, req := range requirements {
		ns, _ := security.ProjectNamespace(req.ResourceID)
		checks[i] = ordinaryRequirementProof{permission: req.Permission, resource: req.ResourceID, namespace: ns}
	}
	slices.SortFunc(checks, compareOrdinaryRequirements)
	if !proof.validRequirements() {
		return nil, a.fail(ErrDecision)
	}
	return a.evaluate(ctx, func(ctx context.Context) error {
		facts, grants, err := a.readOrdinaryFacts(ctx)
		if err != nil {
			return err
		}
		proof.facts = facts
		desired, err := security.CurrentManagedRoles(facts.memberRole, grants)
		if err != nil {
			return err
		}
		catalog := make([]*role.Role, 0, 4)
		router := NewStoreRouter(a.owner, nil)
		for _, slug := range []string{security.RoleOwner, security.RoleAdmin, security.RoleDeveloper, security.RoleViewer} {
			r, readErr := router.GetRoleBySlug(ctx, a.binding.org, "", slug)
			if readErr != nil {
				return fmt.Errorf("%w: current catalog: %w", ErrUnavailable, readErr)
			}
			if r == nil || r.Slug != slug {
				return ErrUnavailable
			}
			catalog = append(catalog, r)
		}
		rows, err := a.allAssignments(ctx)
		if err != nil {
			return err
		}
		if verifyErr := security.VerifyCurrentManagedRoles(a.binding.org, a.binding.user, desired, catalog, rows); verifyErr != nil {
			return verifyErr
		}
		person := actor(a.binding)
		// Warden uses the context namespace when a request has an empty
		// namespace. Start at root so opaque resource IDs stay at root.
		checkCtx := warden.WithNamespace(ctx, "")
		for _, req := range checks {
			if ctxErr := checkCtx.Err(); ctxErr != nil {
				return ctxErr
			}
			if _, checkErr := ordinary.authorizer.CheckUnrepaired(checkCtx, person, req.permission, req.resource, facts.memberRole); checkErr != nil {
				return checkErr
			}
		}
		return nil
	}, proof, nil)
}

func (a *Attempt) allAssignments(ctx context.Context) ([]*assignment.Assignment, error) {
	const pageSize = 256
	router := NewStoreRouter(a.owner, nil)
	var all []*assignment.Assignment
	var previous *assignment.Assignment
	seen := make(map[string]bool)
	for offset := 0; ; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := router.ListAssignments(ctx, &assignment.ListFilter{TenantID: a.binding.org, SubjectKind: string(warden.SubjectUser), SubjectID: a.binding.user, Limit: pageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		if len(page) > pageSize {
			return nil, ErrUnavailable
		}
		for _, row := range page {
			if row == nil || row.ID.String() == "" || seen[row.ID.String()] || row.TenantID != a.binding.org || row.SubjectKind != string(warden.SubjectUser) || row.SubjectID != a.binding.user {
				return nil, ErrUnavailable
			}
			if previous != nil && (row.CreatedAt.Before(previous.CreatedAt) || (row.CreatedAt.Equal(previous.CreatedAt) && row.ID.String() <= previous.ID.String())) {
				return nil, ErrUnavailable
			}
			seen[row.ID.String()] = true
			previous = row
		}
		all = append(all, page...)
		if len(page) < pageSize {
			return all, nil
		}
		if offset > int(^uint(0)>>1)-len(page) {
			return nil, ErrUnavailable
		}
		offset += len(page)
	}
}

func (d *Decision) validateOrdinary(a *Attempt) error {
	if a.profile == nil || a.profile.mode != installedProfile || a.profile.ordinary == nil || a.binding.kind != security.KindUser || d.fleet != nil {
		return ErrDecision
	}
	if a.binding.app != a.profile.ordinary.appID || d.ordinary == nil || d.kind != ordinaryDecision {
		return ErrDecision
	}
	p := d.ordinary
	if p.requireEmail != a.profile.ordinary.requireEmail || !p.validRequirements() {
		return ErrDecision
	}
	return p.facts.validate(a.binding, p.requireEmail, d.at)
}
