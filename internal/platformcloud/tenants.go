package platformcloud

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/instance"
	"github.com/xraph/loomplane/internal/metering"
)

const (
	// ProjectionName is what a workspace calls the platform Cloud.
	ProjectionName = cloudprofiles.PlatformCloudName
	// ProjectionFallbackName names the projection in an organization that
	// already has a Cloud of its own called ProjectionName.
	ProjectionFallbackName = cloudprofiles.PlatformCloudFallbackName
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
	store     *PgStore
	settings  SettingsReader
	orgs      OrgLister
	orgExists OrgExists
	secrets   Secrets
	dns       DNSWriter
	// ownerNotifier hears about expiring host certificates; nil only logs.
	ownerNotifier OwnerNotifier
	// gate and meter are set once the edition's billing has started, while
	// deploy workers may already read them.
	gate      atomic.Pointer[metering.Gate]
	meter     atomic.Pointer[metering.Meter]
	usage     func(ctx context.Context, hostID string) ([]fc.UsageSample, error)
	instances InstanceControl
	// inspect reads a VM on a host; nil asks the host. Tests set it.
	inspect func(ctx context.Context, hostID string, scope fc.Scope) (fc.Record, error)
	now     func() time.Time
	// usageTimeout bounds one host's usage read; zero means defaultUsageTimeout.
	usageTimeout time.Duration
}

// WithGate sets what decides whether an organization may start platform
// work and returns the service. Nil lets everything run.
func (s *Service) WithGate(g metering.Gate) *Service {
	if g == nil {
		s.gate.Store(nil)
	} else {
		s.gate.Store(&g)
	}
	return s
}

func (s *Service) canRun() metering.Gate {
	if g := s.gate.Load(); g != nil {
		return *g
	}
	return metering.AllowAll
}

// NewService returns the control plane over store. orgs lists every
// organization for attach mode all_workspaces.
func NewService(store *PgStore, settings SettingsReader, orgs OrgLister) *Service {
	return &Service{store: store, settings: settings, orgs: orgs, now: time.Now}
}

// EnableTenant lets the organization use the platform Cloud and makes sure
// it sees one platform-owned Loomplane Cloud with a target for every active
// region. The tenant and the projection change in one transaction, so a
// failure leaves neither. Nil limits keep what the tenant has, or the
// instance defaults for a new one. Enabling clears a suspension the platform
// owner set by disabling. Calling it again changes nothing.
func (s *Service) EnableTenant(ctx context.Context, orgID string, limits *Limits) (*Tenant, error) {
	if orgID == "" {
		return nil, errors.New("platformcloud: organization is required")
	}
	regions, err := s.activeRegions(ctx)
	if err != nil {
		return nil, err
	}
	var out *Tenant
	err = s.store.WithPlatformWrite(ctx, func(tx driverTx) error {
		if lockErr := cloudprofiles.LockOrganization(ctx, tx, orgID); lockErr != nil {
			return lockErr
		}
		t, readErr := lockTenantTx(ctx, tx, orgID)
		switch {
		case errors.Is(readErr, ErrNotFound):
			t = &Tenant{OrgID: orgID, Status: tenantEnabled, Limits: limits, SuspendReasons: []SuspendReason{}}
			created, insertErr := insertTenantTx(ctx, tx, t)
			if insertErr != nil {
				return insertErr
			}
			if !created {
				// The organization lock makes this unreachable short of a
				// writer that skips it.
				return ErrConflict
			}
		case readErr != nil:
			return readErr
		default:
			next := *t
			next.Status = tenantEnabled
			if limits != nil {
				next.Limits = limits
			}
			next.SuspendReasons = slices.DeleteFunc(slices.Clone(t.SuspendReasons), func(r SuspendReason) bool { return r == ReasonPlatformOwner })
			if len(next.SuspendReasons) == 0 {
				next.SuspendedAt = nil
			}
			if !sameTenant(t, &next) {
				if updateErr := updateTenantTx(ctx, tx, &next); updateErr != nil {
					return updateErr
				}
			}
			t = &next
		}
		out = t
		return s.ensureProjectionTx(ctx, tx, orgID, regions)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DisableTenant stops new platform work for the organization: the tenant
// turns disabled and carries the platform owner's suspend reason. An
// organization with no tenant yet gets a disabled one, so the owner can
// keep it out before attach mode all_workspaces reaches it. The projection
// stays while environments still use it.
func (s *Service) DisableTenant(ctx context.Context, orgID string) error {
	if orgID == "" {
		return errors.New("platformcloud: organization is required")
	}
	added := false
	var reasons []SuspendReason
	var revision int64
	err := s.store.withTx(ctx, func(tx driverTx) error {
		// The same lock EnableTenant and SyncProjections take, so a disable
		// can't interleave with a first enable of the organization.
		if err := cloudprofiles.LockOrganization(ctx, tx, orgID); err != nil {
			return err
		}
		t, err := lockTenantTx(ctx, tx, orgID)
		if errors.Is(err, ErrNotFound) {
			// Nothing of this organization's runs yet, so nothing to stop.
			at := s.now().UTC()
			created, insertErr := insertTenantTx(ctx, tx, &Tenant{OrgID: orgID, Status: tenantDisabled, SuspendReasons: []SuspendReason{ReasonPlatformOwner}, SuspendedAt: &at})
			if insertErr == nil && !created {
				insertErr = ErrConflict
			}
			return insertErr
		}
		if err != nil {
			return err
		}
		next := *t
		next.Status = tenantDisabled
		if !slices.Contains(t.SuspendReasons, ReasonPlatformOwner) {
			next.SuspendReasons = append(slices.Clone(t.SuspendReasons), ReasonPlatformOwner)
			added = true
			if len(t.SuspendReasons) == 0 {
				at := s.now().UTC()
				next.SuspendedAt = &at
			}
		}
		if sameTenant(t, &next) {
			return nil
		}
		if checkErr := updateTenantTx(ctx, tx, &next); checkErr != nil {
			return checkErr
		}
		reasons, revision = next.SuspendReasons, next.Revision
		return nil
	})
	if err != nil {
		return err
	}
	if added {
		s.notifyOrg(ctx, orgID, NoticeSuspended, fmt.Sprintf("%s:%d", orgID, revision), "Loomplane Cloud work is stopped", suspendMessage(reasons))
	}
	// Every step is safe to repeat, so a failure here is for the caller to
	// retry by disabling again.
	return s.stopWork(ctx, orgID)
}

// SyncProjections brings every projection up to date. In attach mode
// all_workspaces it first enables each organization that has no tenant row.
// It only ever creates rows: an organization with a row, enabled or not,
// keeps it, so one the platform owner disabled stays disabled. Then every
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
			if attachErr := s.attachNew(ctx, org, regions); attachErr != nil {
				errs = append(errs, fmt.Errorf("enable %s: %w", org, attachErr))
			}
		}
	}
	for _, t := range tenants {
		if t.Status != tenantEnabled {
			continue
		}
		if syncErr := s.refreshProjection(ctx, t.OrgID, regions); syncErr != nil {
			errs = append(errs, fmt.Errorf("project into %s: %w", t.OrgID, syncErr))
		}
	}
	return errors.Join(errs...)
}

// attachNew enables an organization that had no tenant row, together with
// its projection. If a row appeared since the caller looked, it is left
// as it is.
func (s *Service) attachNew(ctx context.Context, orgID string, regions []Region) error {
	return s.store.WithPlatformWrite(ctx, func(tx driverTx) error {
		if err := cloudprofiles.LockOrganization(ctx, tx, orgID); err != nil {
			return err
		}
		created, err := insertTenantTx(ctx, tx, &Tenant{OrgID: orgID, Status: tenantEnabled, SuspendReasons: []SuspendReason{}})
		if err != nil || !created {
			return err
		}
		return s.ensureProjectionTx(ctx, tx, orgID, regions)
	})
}

// refreshProjection adds region targets for a tenant that is still enabled
// when its row is read under lock.
func (s *Service) refreshProjection(ctx context.Context, orgID string, regions []Region) error {
	return s.store.WithPlatformWrite(ctx, func(tx driverTx) error {
		if err := cloudprofiles.LockOrganization(ctx, tx, orgID); err != nil {
			return err
		}
		t, err := lockTenantTx(ctx, tx, orgID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if t.Status != tenantEnabled {
			return nil
		}
		return s.ensureProjectionTx(ctx, tx, orgID, regions)
	})
}

func (s *Service) activeRegions(ctx context.Context) ([]Region, error) {
	all, err := s.store.ListRegions(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(r Region) bool { return r.Status != "active" }), nil
}

// ensureProjectionTx makes sure the organization has its one platform-owned
// Cloud and a target for each of regions, inside tx, which must run with
// the platform write setting and hold the organization lock. It only adds:
// a closed region's target stays until nothing uses it. The rows carry no
// project and no grant, so no project reaches them until the organization
// grants one. An organization that already named a Cloud of its own
// "Loomplane Cloud" sees the projection under the fallback name.
func (s *Service) ensureProjectionTx(ctx context.Context, tx driverTx, orgID string, regions []Region) error {
	now := s.now().UTC()
	var cloudID string
	err := tx.QueryRow(ctx, `SELECT id FROM loomplane_cloud_profiles WHERE org_id=$1 AND platform_owned ORDER BY created_at,id LIMIT 1`, orgID).Scan(&cloudID)
	if errors.Is(err, pgx.ErrNoRows) {
		name := ProjectionName
		var taken bool
		if takenErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cloud_profiles WHERE org_id=$1 AND name=$2)`, orgID, ProjectionName).Scan(&taken); takenErr != nil {
			return takenErr
		}
		if taken {
			name = ProjectionFallbackName
		}
		id, idErr := typeid.Generate("cloud")
		if idErr != nil {
			return fmt.Errorf("generate cloud id: %w", idErr)
		}
		cloudID = id.String()
		_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_profiles(id,org_id,project_id,name,revision,created_at,updated_at,default_provider,default_connection_id,default_region,platform_owned)
VALUES($1,$2,'',$3,1,$4,$4,NULL,NULL,'',true)`, cloudID, orgID, name, now)
		if err != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" {
				return fmt.Errorf("%w: the organization already has Clouds named %q and %q", ErrConflict, ProjectionName, ProjectionFallbackName)
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
}

// projectedRegions is the set of regions the projection already targets.
func projectedRegions(ctx context.Context, tx driverTx, orgID, cloudID string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT region FROM loomplane_cloud_targets WHERE org_id=$1 AND cloud_id=$2`, orgID, cloudID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
