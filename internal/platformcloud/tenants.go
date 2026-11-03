package platformcloud

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/instance"
)

const (
	// ProjectionName is what a workspace calls the platform Cloud.
	ProjectionName = "Loomplane Cloud"
	// RuntimePlatform is the runtime of every target in the projection.
	RuntimePlatform = "platform"
	// projectionProvider is the provider the projection's targets name.
	projectionProvider = "platform"

	tenantEnabled  = "enabled"
	tenantDisabled = "disabled"

	attachAllWorkspaces = "all_workspaces"
)

// OrgLister returns the ID of every organization on the instance.
type OrgLister func(ctx context.Context) ([]string, error)

// SettingsReader reads the instance's platform Cloud settings.
type SettingsReader interface {
	PlatformCloud(ctx context.Context) (instance.PlatformCloudSettings, error)
}

// Service is the platform Cloud control plane: which organizations may use
// it and the read-only Cloud each of them sees.
type Service struct {
	store    *PgStore
	settings SettingsReader
	orgs     OrgLister
	now      func() time.Time
}

// NewService returns the control plane over store. orgs lists every
// organization for attach mode all_workspaces.
func NewService(store *PgStore, settings SettingsReader, orgs OrgLister) *Service {
	return &Service{store: store, settings: settings, orgs: orgs, now: time.Now}
}

// EnableTenant lets the organization use the platform Cloud and makes sure
// it sees one platform-owned Loomplane Cloud with a target for every active
// region. Nil limits keep what the tenant has, or the instance defaults for
// a new one. Enabling clears a suspension the platform owner set by
// disabling. Calling it again changes nothing.
func (s *Service) EnableTenant(ctx context.Context, orgID string, limits *Limits) (*Tenant, error) {
	if orgID == "" {
		return nil, errors.New("platformcloud: organization is required")
	}
	t, err := s.store.GetTenant(ctx, orgID)
	switch {
	case errors.Is(err, ErrNotFound):
		t = &Tenant{OrgID: orgID, Status: tenantEnabled, Limits: limits, SuspendReasons: []SuspendReason{}}
		if upsertErr := s.store.UpsertTenant(ctx, t); upsertErr != nil {
			return nil, upsertErr
		}
	case err != nil:
		return nil, err
	default:
		next := *t
		next.Status = tenantEnabled
		if limits != nil {
			next.Limits = limits
		}
		next.SuspendReasons = slices.DeleteFunc(slices.Clone(t.SuspendReasons), func(r SuspendReason) bool { return r == ReasonPlatformOwner })
		if !sameTenant(t, &next) {
			if upsertErr := s.store.UpsertTenant(ctx, &next); upsertErr != nil {
				return nil, upsertErr
			}
		}
		t = &next
	}
	regions, err := s.activeRegions(ctx)
	if err != nil {
		return nil, err
	}
	if projectErr := s.ensureProjection(ctx, orgID, regions); projectErr != nil {
		return nil, projectErr
	}
	return t, nil
}

// DisableTenant stops new platform work for the organization: the tenant
// turns disabled and carries the platform owner's suspend reason. The
// projection stays while environments still use it.
func (s *Service) DisableTenant(ctx context.Context, orgID string) error {
	t, err := s.store.GetTenant(ctx, orgID)
	if err != nil {
		return err
	}
	next := *t
	next.Status = tenantDisabled
	if !slices.Contains(t.SuspendReasons, ReasonPlatformOwner) {
		next.SuspendReasons = append(slices.Clone(t.SuspendReasons), ReasonPlatformOwner)
	}
	if sameTenant(t, &next) {
		return nil
	}
	return s.store.UpsertTenant(ctx, &next)
}

// SyncProjections brings every projection up to date. In attach mode
// all_workspaces it first enables each organization that has no tenant row,
// so an organization the platform owner disabled stays disabled. Then every
// enabled tenant gets a target for each active region it lacks. One
// organization's failure doesn't stop the rest.
func (s *Service) SyncProjections(ctx context.Context) error {
	cfg, err := s.settings.PlatformCloud(ctx)
	if err != nil {
		return err
	}
	tenants, err := s.store.ListTenants(ctx)
	if err != nil {
		return err
	}
	regions, err := s.activeRegions(ctx)
	if err != nil {
		return err
	}
	var errs []error
	if cfg.Attach == attachAllWorkspaces {
		if s.orgs == nil {
			return errors.New("platformcloud: attach mode all_workspaces needs an organization list")
		}
		orgs, listErr := s.orgs(ctx)
		if listErr != nil {
			return listErr
		}
		known := make(map[string]bool, len(tenants))
		for _, t := range tenants {
			known[t.OrgID] = true
		}
		for _, org := range orgs {
			if known[org] || org == "" {
				continue
			}
			known[org] = true
			if _, enableErr := s.EnableTenant(ctx, org, nil); enableErr != nil {
				errs = append(errs, fmt.Errorf("enable %s: %w", org, enableErr))
			}
		}
	}
	for _, t := range tenants {
		if t.Status != tenantEnabled {
			continue
		}
		if syncErr := s.ensureProjection(ctx, t.OrgID, regions); syncErr != nil {
			errs = append(errs, fmt.Errorf("project into %s: %w", t.OrgID, syncErr))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) activeRegions(ctx context.Context) ([]Region, error) {
	all, err := s.store.ListRegions(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(r Region) bool { return r.Status != "active" }), nil
}

// ensureProjection makes sure the organization has its one platform-owned
// Cloud and a target for each of regions. It only adds: a closed region's
// target stays until nothing uses it. The rows carry no project and no
// grant, so no project reaches them until the organization grants one.
func (s *Service) ensureProjection(ctx context.Context, orgID string, regions []Region) error {
	return s.store.WithPlatformWrite(ctx, func(tx driverTx) error {
		// Serializes with the organization's Cloud changes, and with another
		// projection run for it, so two runs can't both create the Cloud.
		if err := cloudprofiles.LockOrganization(ctx, tx, orgID); err != nil {
			return err
		}
		now := s.now().UTC()
		var cloudID string
		err := tx.QueryRow(ctx, `SELECT id FROM loomplane_cloud_profiles WHERE org_id=$1 AND platform_owned ORDER BY created_at,id LIMIT 1`, orgID).Scan(&cloudID)
		if errors.Is(err, pgx.ErrNoRows) {
			id, idErr := typeid.Generate("cloud")
			if idErr != nil {
				return fmt.Errorf("generate cloud id: %w", idErr)
			}
			cloudID = id.String()
			_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_profiles(id,org_id,project_id,name,revision,created_at,updated_at,default_provider,default_connection_id,default_region,platform_owned)
VALUES($1,$2,'',$3,1,$4,$4,NULL,NULL,'',true)`, cloudID, orgID, ProjectionName, now)
			if err != nil {
				if errors.Is(storeError(err), ErrConflict) {
					return fmt.Errorf("%w: the organization already has a Cloud named %q", ErrConflict, ProjectionName)
				}
				return err
			}
		} else if err != nil {
			return err
		}
		have, err := projectedRegions(ctx, tx, orgID, cloudID)
		if err != nil {
			return err
		}
		for _, r := range regions {
			if have[r.Name] {
				continue
			}
			id, idErr := typeid.Generate("target")
			if idErr != nil {
				return fmt.Errorf("generate target id: %w", idErr)
			}
			_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_targets(id,org_id,project_id,cloud_id,name,display_name,provider,connection_id,region,revision,created_at,updated_at,runtime,pool_id,tags)
VALUES($1,$2,'',$3,$4,$5,$6,NULL,$4,1,$7,$7,$8,NULL,'{}')`, id.String(), orgID, cloudID, r.Name, r.DisplayName, projectionProvider, now, RuntimePlatform)
			if err != nil {
				return storeError(err)
			}
		}
		return nil
	})
}

// projectedRegions is the set of regions the projection already targets.
func projectedRegions(ctx context.Context, tx driverTx, orgID, cloudID string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT region FROM loomplane_cloud_targets WHERE org_id=$1 AND cloud_id=$2`, orgID, cloudID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	have := map[string]bool{}
	for rows.Next() {
		var region string
		if scanErr := rows.Scan(&region); scanErr != nil {
			return nil, scanErr
		}
		have[region] = true
	}
	return have, rows.Err()
}

// sameTenant reports whether writing b over a would change nothing.
func sameTenant(a, b *Tenant) bool {
	if a.Status != b.Status || !slices.Equal(a.SuspendReasons, b.SuspendReasons) {
		return false
	}
	if a.Limits == nil || b.Limits == nil {
		return a.Limits == b.Limits
	}
	return *a.Limits == *b.Limits
}
