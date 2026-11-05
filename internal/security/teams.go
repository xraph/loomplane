package security

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/xraph/warden"
	"github.com/xraph/warden/assignment"
	wardenid "github.com/xraph/warden/id"
	"go.jetify.com/typeid/v2"
)

// Teams reach Warden as ordinary per-person assignments. Warden can't give
// a role to a group, so Sync copies each team grant onto every member and
// keeps the highest role per namespace: the member role at the root, a
// workspace grant at the root too, a project grant at the project's own
// namespace (ProjectNamespace). Sync is the only writer of a person's
// assignments; service accounts keep assign.

// ScopedRole is one role a person holds through a team.
type ScopedRole struct {
	Role      string // RoleViewer, RoleDeveloper or RoleAdmin
	ProjectID string // "" is the whole workspace
	TeamID    string // the team it comes from, for the assignment's metadata
}

// TeamGrants lists the roles a person holds through their teams. Only
// current workspace members get any: the teams service joins authsome's
// membership on every read. Sync calls it while holding the person's lock,
// so it must never sync anyone itself.
type TeamGrants func(ctx context.Context, orgID, userID string) ([]ScopedRole, error)

// LockFunc runs fn while holding the lock called name. pglock.For fits.
// It must not sync anyone besides running fn.
type LockFunc func(ctx context.Context, name string, fn func(context.Context) error) error

// TryLockFunc runs fn while holding the lock called name and reports
// whether it ran. When someone else holds the lock it returns false at once
// instead of waiting. pglock.TryFor fits.
type TryLockFunc func(ctx context.Context, name string, fn func(context.Context) error) (bool, error)

// SyncGrantedBy is the GrantedBy of every assignment Sync writes, except the
// root row Assign makes, which names whoever changed the member role.
const SyncGrantedBy = "loomplane:sync"

const syncLockPrefix = "loomplane-authz-sync:"

// maxLockedSyncs bounds how many syncs in this process hold or wait for
// the sync lock at once. pglock.For holds a pooled connection while it
// waits and while the sync runs, and the sync needs a second one for
// Warden's store. Unbounded, a burst (a dashboard's parallel requests all
// repairing one person) takes every connection and then waits forever for
// one more. Four leaves the rest of the pool free.
const maxLockedSyncs = 4

// SetTeamGrants sets where Sync reads team grants from. Call it once while
// extensions register; nil means no team grants.
func (a *Authorizer) SetTeamGrants(g TeamGrants) { a.teamGrants = g }

// SetSyncLock sets the lock Sync takes per person. nil runs unlocked, which
// only unit tests do. Set the try-lock on the same locks too
// (SetSyncLocks sets both), or TrySync refuses to run.
func (a *Authorizer) SetSyncLock(l LockFunc) {
	a.syncLock = l
	a.ensureSyncSlots()
}

// SetSyncLocks sets the lock Sync takes per person and TrySync's version
// of the same lock, which never waits. Wire both through it: TrySync
// refuses to run with only the first.
func (a *Authorizer) SetSyncLocks(lock LockFunc, try TryLockFunc) {
	a.SetSyncLock(lock)
	a.SetSyncTryLock(try)
}

// SetSyncTryLock sets the lock TrySync takes per person: the same lock as
// SetSyncLock's, taken without waiting.
func (a *Authorizer) SetSyncTryLock(l TryLockFunc) {
	a.syncTryLock = l
	a.ensureSyncSlots()
}

func (a *Authorizer) ensureSyncSlots() {
	if a.syncSlots == nil {
		a.syncSlots = make(chan struct{}, maxLockedSyncs)
	}
}

// ProjectNamespace is the Warden namespace a project's grants live in:
// "proj_01h..." becomes "proj-01h...". Anything that isn't a valid project
// typeid has none, so checks on it stay at the root.
func ProjectNamespace(projectID string) (string, bool) {
	tid, err := typeid.Parse(projectID)
	if err != nil || tid.Prefix() != "proj" || !tid.HasSuffix() {
		return "", false
	}
	// A typeid suffix is 26 lowercase base32 characters, so the segment
	// fits Warden's ^[a-z][a-z0-9-]{0,62}$.
	return "proj-" + tid.Suffix(), true
}

// ProjectPermissions are the permissions a check can answer for one
// project: every one Loomplane checks against a project ID, except the
// owner-only BackupRetire. GET /projects/:id/permissions answers for these.
func ProjectPermissions() []Permission {
	return []Permission{
		ProjectRead, ProjectUpdate, ProjectDelete, EnvironmentRead, EnvironmentManage, AnalysisRun,
		VariableRead, VariableManage, DeploymentRead, DeploymentCreate, DeploymentApprove,
		InfrastructureRead, InfrastructureModify, BackupRead, BackupManage, RestoreManage,
		CloudAccountRead, CloudAccountManage, SecretManage, AIRead, AIEdit, AIManage,
	}
}

// RoleRank orders the catalog roles: owner 4, admin 3, developer 2, viewer
// 1, anything else 0. The roles nest (TestCatalog_rolesNest), so the higher
// rank always holds everything the lower one does.
func RoleRank(slug string) int {
	switch slug {
	case RoleOwner:
		return 4
	case RoleAdmin:
		return 3
	case RoleDeveloper:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// Sync rewrites the person's whole set of assignments in the organization
// from their member role and their teams' grants. A person who isn't a
// member loses every row. Only people are synced: an ID holding ":" (the
// automation's "loomplane:<org>", the agent's "agent:<org>") is refused
// before any lookup.
//
// Sync waits for the person's lock. Never call it (or Assign, or Require,
// whose repair syncs) inside a database transaction or while holding a
// pooled connection, and never from inside a sync: from a LockFunc's fn,
// a TeamGrants func or a Leaver. The lock holds one connection and the
// sync needs another, so a caller that already holds one can starve the
// pool, and a nested sync can wait on itself.
func (a *Authorizer) Sync(ctx context.Context, orgID, userID string) error {
	return a.sync(ctx, orgID, userID, "", SyncGrantedBy)
}

// TrySync is Sync for background work that can come back later, such as
// the teams queue job: when another sync of the person holds their lock,
// or every sync slot is taken, it returns false at once and changes
// nothing. The rules on Sync hold for it too.
func (a *Authorizer) TrySync(ctx context.Context, orgID, userID string) (bool, error) {
	if err := syncable(orgID, userID); err != nil {
		return false, err
	}
	fn := func(ctx context.Context) error { return a.syncLocked(ctx, orgID, userID, "", SyncGrantedBy) }
	if a.syncLock == nil && a.syncTryLock == nil {
		return true, fn(ctx)
	}
	if a.syncTryLock == nil {
		return false, errors.New("sync roles: no try-lock set, so the queue can't sync without waiting")
	}
	select {
	case a.syncSlots <- struct{}{}:
		defer func() { <-a.syncSlots }()
	default:
		return false, nil
	}
	return a.syncTryLock(ctx, syncLockPrefix+orgID+":"+userID, fn)
}

// syncable refuses what Sync never runs for: no organization, or a subject
// that isn't a person.
func syncable(orgID, userID string) error {
	if userID == "" || strings.Contains(userID, ":") {
		return fmt.Errorf("%w: only a person holds member and team roles", ErrForbidden)
	}
	if orgID == "" {
		return errors.New("sync roles: no organization")
	}
	return nil
}

// sync is Sync and Assign. memberRole is the root role to use, or "" to
// look it up through MembershipFunc; rootGrantedBy is the GrantedBy of a
// new root row.
func (a *Authorizer) sync(ctx context.Context, orgID, userID, memberRole, rootGrantedBy string) error {
	if err := syncable(orgID, userID); err != nil {
		return err
	}
	return a.withPersonLock(ctx, orgID, userID, func(ctx context.Context) error {
		return a.syncLocked(ctx, orgID, userID, memberRole, rootGrantedBy)
	})
}

// syncLocked is the sync itself, run while holding the person's lock.
func (a *Authorizer) syncLocked(ctx context.Context, orgID, userID, memberRole, rootGrantedBy string) error {
	if memberRole == "" {
		if a.members == nil {
			return errors.New("sync roles: no membership source")
		}
		found := false
		var err error
		if memberRole, found, err = a.members(ctx, orgID, userID); err != nil {
			return fmt.Errorf("look up membership: %w", err)
		}
		if !found {
			return a.Revoke(ctx, orgID, userID)
		}
		memberRole = RoleForMember(memberRole)
	}
	if err := a.EnsureTenant(ctx, orgID); err != nil {
		return err
	}
	want, err := a.wantedRoles(ctx, orgID, userID, memberRole)
	if err != nil {
		return err
	}
	return a.writeRoles(ctx, orgID, userID, want, rootGrantedBy)
}

// withPersonLock runs fn holding the person's sync lock, when one is set,
// so two syncs of one person never interleave their deletes and creates.
// It first takes one of maxLockedSyncs slots. fn must not sync anyone.
func (a *Authorizer) withPersonLock(ctx context.Context, orgID, userID string, fn func(context.Context) error) error {
	if a.syncLock == nil {
		return fn(ctx)
	}
	select {
	case a.syncSlots <- struct{}{}:
		defer func() { <-a.syncSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return a.syncLock(ctx, syncLockPrefix+orgID+":"+userID, fn)
}

// wantedRole is the one role a person should hold at a namespace, and
// where it comes from: "member" and team IDs.
type wantedRole struct {
	role    string
	sources []string
}

// wantedRoles is the highest role per namespace from the member role and
// the person's team grants. A project entry at or below the root role is
// dropped: the root row already reaches the project.
func (a *Authorizer) wantedRoles(ctx context.Context, orgID, userID, memberRole string) (map[string]*wantedRole, error) {
	var grants []ScopedRole
	if a.teamGrants != nil {
		var err error
		grants, err = a.teamGrants(ctx, orgID, userID)
		if err != nil {
			return nil, fmt.Errorf("list team grants of %s: %w", userID, err)
		}
	}
	return reduceRoles(ctx, orgID, userID, memberRole, grants), nil
}

func reduceRoles(ctx context.Context, orgID, userID, memberRole string, grants []ScopedRole) map[string]*wantedRole {
	want := map[string]*wantedRole{rootNamespace: {role: memberRole, sources: []string{"member"}}}
	for _, g := range grants {
		// A bad row is skipped, never fatal: failing the whole sync would
		// also block a demotion of the member role. Skipping only takes
		// access away. A team never carries the owner's or the automation's
		// role (the teams table enforces it too), and a project grant needs
		// a project.
		ns := rootNamespace
		bad := g.Role != RoleViewer && g.Role != RoleDeveloper && g.Role != RoleAdmin
		if !bad && g.ProjectID != "" {
			var ok bool
			ns, ok = ProjectNamespace(g.ProjectID)
			bad = !ok
		}
		if bad {
			slog.ErrorContext(ctx, "skip a team grant that isn't a team role on the workspace or a project",
				"org", orgID, "user", userID, "team", g.TeamID)
			continue
		}
		cur := want[ns]
		switch {
		case cur == nil || RoleRank(g.Role) > RoleRank(cur.role):
			want[ns] = &wantedRole{role: g.Role, sources: []string{g.TeamID}}
		case RoleRank(g.Role) == RoleRank(cur.role):
			cur.sources = append(cur.sources, g.TeamID)
		}
	}
	rootRank := RoleRank(want[rootNamespace].role)
	for ns, w := range want {
		if ns != rootNamespace && RoleRank(w.role) <= rootRank {
			delete(want, ns)
		}
	}
	return want
}

// writeRoles makes the person's rows match want. It deletes every row
// that doesn't match before it creates any, so a failure midway leaves the
// person with less than before, never more; the next sync (the queue, or
// Require's repair) finishes the job.
func (a *Authorizer) writeRoles(ctx context.Context, orgID, userID string, want map[string]*wantedRole, rootGrantedBy string) error {
	roleIDs := map[string]wardenid.RoleID{}
	for _, w := range want {
		if _, done := roleIDs[w.role]; done {
			continue
		}
		r, err := a.store.GetRoleBySlug(ctx, orgID, rootNamespace, w.role)
		if err != nil {
			return fmt.Errorf("look up role %s in %s: %w", w.role, orgID, err)
		}
		roleIDs[w.role] = r.ID
	}
	have, err := a.store.ListAssignments(ctx, &assignment.ListFilter{
		TenantID: orgID, SubjectKind: string(warden.SubjectUser), SubjectID: userID,
	})
	if err != nil {
		return fmt.Errorf("list roles of %s in %s: %w", userID, orgID, err)
	}

	changed := false
	defer func() {
		if changed {
			a.engine.InvalidateSubject(ctx, orgID, warden.SubjectUser, userID)
		}
	}()

	kept := map[string]bool{}
	for _, row := range have {
		w := want[row.NamespacePath]
		exact := w != nil && !kept[row.NamespacePath] && row.RoleID == roleIDs[w.role] &&
			row.ResourceType == "" && row.ResourceID == "" && row.ExpiresAt == nil
		if exact {
			kept[row.NamespacePath] = true
			continue
		}
		if err := a.store.DeleteAssignment(ctx, orgID, row.ID); err != nil && !errors.Is(err, warden.ErrNotFound) {
			return fmt.Errorf("remove a role of %s in %s: %w", userID, orgID, err)
		}
		changed = true
	}
	for ns, w := range want {
		if kept[ns] {
			continue
		}
		grantedBy := SyncGrantedBy
		if ns == rootNamespace {
			grantedBy = rootGrantedBy
		}
		err := a.store.CreateAssignment(ctx, &assignment.Assignment{
			ID: wardenid.NewAssignmentID(), TenantID: orgID, NamespacePath: ns,
			RoleID: roleIDs[w.role], SubjectKind: string(warden.SubjectUser), SubjectID: userID,
			GrantedBy: grantedBy, Metadata: map[string]any{"sources": w.sources}, CreatedAt: a.now().UTC(),
		})
		if err != nil && !errors.Is(err, warden.ErrAlreadyExists) {
			return fmt.Errorf("assign %s to %s at %q in %s: %w", w.role, userID, ns, orgID, err)
		}
		changed = true
	}
	return nil
}

// Leaver takes a person who is leaving the workspace out of every team.
// It runs inside authsome's removal, before the revoke, and must never sync
// anyone: the hooks revoke the person themselves.
type Leaver func(ctx context.Context, orgID, userID string) error

// SetLeaver sets what OnBeforeMemberRemove runs before it revokes the
// member's roles. nil skips it.
func (s *Sync) SetLeaver(l Leaver) { s.leaver = l }

// Requeuer queues a person for a later sync. The teams service's Requeue
// fits. Like a Leaver, it must never sync anyone itself.
type Requeuer func(ctx context.Context, orgID, userID string) error

// SetRequeuer sets what OnAfterMemberRemove runs when its revoke fails.
// nil skips it.
func (s *Sync) SetRequeuer(r Requeuer) { s.requeue = r }
