package buildrunner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/platformcloud"
	"github.com/xraph/loomplane/internal/security"
)

// Global runners are a platform pool's runners: the platform owner runs
// them, and every workspace the platform owner enabled for Loomplane Cloud
// builds on them when its own project and organization name no default
// pool. Each build runs in its own microVM, under its own workspace's
// scope, and is metered as Loomplane Cloud build minutes. A platform pool
// has no connection: plans name it by its own ID.

// PlatformAdmins says who runs the installation.
type PlatformAdmins interface {
	IsPlatformAdmin(ctx context.Context, userID string) (bool, error)
}

// PlatformWorkspaces says whether a workspace may use platform capacity.
// The platform Cloud service satisfies it.
type PlatformWorkspaces interface {
	Workspace(ctx context.Context, orgID string) (*platformcloud.WorkspaceView, error)
}

// globalSource is how a plan names a build on global runners.
const globalSource = "Loomplane's global runners, billed as Loomplane Cloud build minutes,"

// isPoolID reports whether builder names a platform pool by its ID, which
// only platform pools are named by.
func isPoolID(builder string) bool { return strings.HasPrefix(builder, "rpool_") }

func (s *Service) requirePlatformAdmin(ctx context.Context, p security.Principal) error {
	// Instance-wide authority is session-only, like the platform console.
	if s.Admins == nil || p.UserID == "" || p.KeyID != "" {
		return security.ErrForbidden
	}
	ok, err := s.Admins.IsPlatformAdmin(ctx, p.UserID)
	if err != nil {
		return err
	}
	if !ok {
		return security.ErrForbidden
	}
	return nil
}

// CreatePlatformPool creates a pool of global runners. Only a platform
// admin, signed in, may.
func (s *Service) CreatePlatformPool(ctx context.Context, p security.Principal, in PoolInput) (*Pool, error) {
	store, err := s.st()
	if err != nil {
		return nil, err
	}
	if xerr := s.requirePlatformAdmin(ctx, p); xerr != nil {
		return nil, xerr
	}
	in.Name = strings.TrimSpace(in.Name)
	if !poolName.MatchString(in.Name) {
		return nil, fmt.Errorf("%w: a pool's name is a lowercase DNS label of at most 63 characters", ErrInvalid)
	}
	if in.Mode == "" {
		in.Mode = ModeSelfHosted
	}
	if in.Mode != ModeSelfHosted {
		return nil, fmt.Errorf("%w: global runners are the platform owner's own machines", ErrInvalid)
	}
	if in.Settings.Registry != "" {
		return nil, fmt.Errorf("%w: global builds push to each workspace's own registry", ErrInvalid)
	}
	if xerr := validSettings(in.Mode, in.Settings); xerr != nil {
		return nil, xerr
	}
	if xerr := s.oneDefault(ctx, store, "", "", ScopePlatform, "", in.Settings); xerr != nil {
		return nil, xerr
	}
	id, err := typeid.Generate("rpool")
	if err != nil {
		return nil, err
	}
	now := s.now()
	pool := &Pool{ID: id.String(), Scope: ScopePlatform, Name: in.Name, Mode: in.Mode, Settings: in.Settings, Revision: 1, CreatedBy: p.UserID, CreatedAt: now, UpdatedAt: now}
	if err := store.CreatePool(ctx, pool); err != nil {
		return nil, err
	}
	return pool, nil
}

// ListPlatformPools lists the global runner pools, for a platform admin.
func (s *Service) ListPlatformPools(ctx context.Context, p security.Principal) ([]PoolView, error) {
	store, err := s.st()
	if err != nil {
		return nil, err
	}
	if xerr := s.requirePlatformAdmin(ctx, p); xerr != nil {
		return nil, xerr
	}
	pools, err := store.ListPools(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]PoolView, 0, len(pools))
	for i := range pools {
		v, verr := s.view(ctx, store, &pools[i])
		if verr != nil {
			return nil, verr
		}
		out = append(out, *v)
	}
	return out, nil
}

// PlatformJoinToken mints a token a global runner joins pool id with. The
// runner joins the platform admin's own organization, and serves every
// enabled workspace.
func (s *Service) PlatformJoinToken(ctx context.Context, p security.Principal, id string, lifetime time.Duration) (string, time.Time, error) {
	store, err := s.st()
	if err != nil {
		return "", time.Time{}, err
	}
	if xerr := s.requirePlatformAdmin(ctx, p); xerr != nil {
		return "", time.Time{}, xerr
	}
	pool, err := store.GetPool(ctx, id)
	if err != nil {
		return "", time.Time{}, err
	}
	if pool.Scope != ScopePlatform || s.Tokens == nil {
		return "", time.Time{}, ErrNotFound
	}
	return s.Tokens.MintRunnerJoin(ctx, p, pool.ID, "", lifetime)
}

// globalAllowed reports whether orgID may build on global runners: the
// platform owner enabled it for Loomplane Cloud and hasn't suspended it.
func (s *Service) globalAllowed(ctx context.Context, orgID string) bool {
	if s.Workspaces == nil {
		return false
	}
	w, err := s.Workspaces.Workspace(ctx, orgID)
	return err == nil && w != nil && w.Enabled && len(w.SuspendReasons) == 0
}

// globalDefault is the default platform pool orgID builds on, with the
// connection a plan names it by, or nil.
func (s *Service) globalDefault(ctx context.Context, store Store, orgID string) (*planning.BuildDefault, error) {
	if !s.globalAllowed(ctx, orgID) {
		return nil, nil
	}
	pools, err := store.ListPools(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range pools {
		q := &pools[i]
		if q.Scope != ScopePlatform || !q.Settings.Default {
			continue
		}
		members, merr := store.ListMembers(ctx, q.ID)
		if merr != nil {
			return nil, merr
		}
		details := Details(q, members)
		return &planning.BuildDefault{Builder: q.ID, Source: globalSource,
			Connection: &planning.ConnectionOption{ID: q.ID, Name: "Loomplane global runners", Cloud: "runner", Ready: details["build_isolation"] == "ready", Details: details}}, nil
	}
	return nil, nil
}

// PoolDetails are a platform pool's details, which a build reviews in
// place of a connection's: a platform pool has none. A workspace the
// platform owner hasn't enabled, or has suspended, gets none.
func (s *Service) PoolDetails(ctx context.Context, orgID, builder string) (map[string]string, error) {
	store, err := s.st()
	if err != nil {
		return nil, err
	}
	pool, err := store.GetPool(ctx, builder)
	if err != nil || pool.Scope != ScopePlatform {
		return nil, ErrNotFound
	}
	if !s.globalAllowed(ctx, orgID) {
		return nil, errors.New("this workspace may not build on Loomplane's global runners: Loomplane Cloud isn't enabled for it, or is suspended")
	}
	members, err := store.ListMembers(ctx, pool.ID)
	if err != nil {
		return nil, err
	}
	return Details(pool, members), nil
}

// MeterBuild meters a build on a global runner as Loomplane Cloud build
// minutes, rounded up. A build on a workspace's own runners records
// nothing: its machines are the workspace's.
func (s *Service) MeterBuild(ctx context.Context, orgID, builder, operation string, started, ended time.Time) error {
	if !isPoolID(builder) || s.Meter == nil {
		return nil
	}
	d := ended.Sub(started)
	if d <= 0 {
		return nil
	}
	return s.Meter.Record(ctx, metering.Event{
		ID: "runner-build:" + operation, OrgID: orgID, Feature: platformcloud.FeatureBuildMinutes, ResourceRef: builder,
		Quantity: float64((d + time.Minute - 1) / time.Minute), OccurredAt: ended.UTC(), PlatformOwned: true,
	})
}
