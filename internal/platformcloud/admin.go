package platformcloud

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// This file is what the platform owner's console and a workspace's own view
// read and write through the service. The platform owner's routes reach every
// method here; a workspace reaches only Workspace.

// WithOrgs sets what lists every organization on the instance and returns
// the service. Attach mode all_workspaces and the cleanup of tenants whose
// organization is gone need it.
func (s *Service) WithOrgs(orgs OrgLister) *Service {
	s.orgs = orgs
	return s
}

const (
	regionActive = "active"
	regionClosed = "closed"
)

var regionName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Regions lists every region, active or closed.
func (s *Service) Regions(ctx context.Context) ([]Region, error) {
	return s.store.ListRegions(ctx)
}

// CreateRegion adds a region. Its name is what a workspace's targets carry,
// so it is lowercase letters, digits and hyphens. A new region is active
// unless it says closed.
func (s *Service) CreateRegion(ctx context.Context, name, displayName, status string) (*Region, error) {
	if !regionName.MatchString(name) {
		return nil, errors.New("platformcloud: a region name is lowercase letters, digits and hyphens, up to 63 characters")
	}
	if status == "" {
		status = regionActive
	}
	if status != regionActive && status != regionClosed {
		return nil, errors.New("platformcloud: a region is active or closed")
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = name
	}
	r := &Region{Name: name, DisplayName: displayName, Status: status}
	if err := s.store.CreateRegion(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

// RegionUpdate changes a region's display name or status. A nil field keeps
// what the region has. A zero Revision updates whatever is stored.
type RegionUpdate struct {
	DisplayName *string `json:"display_name,omitempty"`
	Status      *string `json:"status,omitempty"`
	Revision    int64   `json:"revision,omitempty"`
}

// UpdateRegion changes the region named by its ID or its name. Its name
// never changes: the targets workspaces already have carry it.
func (s *Service) UpdateRegion(ctx context.Context, region string, in RegionUpdate) (*Region, error) {
	all, err := s.store.ListRegions(ctx)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(all, func(r Region) bool { return r.ID == region || r.Name == region })
	if i < 0 {
		return nil, fmt.Errorf("%w: region", ErrNotFound)
	}
	r := all[i]
	if in.Revision != 0 && in.Revision != r.Revision {
		return nil, ErrConflict
	}
	if in.DisplayName != nil {
		name := strings.TrimSpace(*in.DisplayName)
		if name == "" {
			return nil, errors.New("platformcloud: a region needs a display name")
		}
		r.DisplayName = name
	}
	if in.Status != nil {
		if *in.Status != regionActive && *in.Status != regionClosed {
			return nil, errors.New("platformcloud: a region is active or closed")
		}
		r.Status = *in.Status
	}
	if err = s.store.UpdateRegion(ctx, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// HostView is a host with what its live placements take from it.
type HostView struct {
	Host
	Use Allotment `json:"use"`
	// Placements counts the host's live placements, builds included.
	Placements int `json:"placements"`
}

// Hosts lists every host, retired ones included, with what each one's
// placements use.
func (s *Service) Hosts(ctx context.Context) ([]HostView, error) {
	hosts, err := s.store.ListHosts(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]HostView, 0, len(hosts))
	for _, h := range hosts {
		use, useErr := s.store.HostUse(ctx, h.ID)
		if useErr != nil {
			return nil, useErr
		}
		out = append(out, HostView{Host: h, Use: use, Placements: use.VMs})
	}
	return out, nil
}

// HostPlacements lists the host's placements that are not released.
func (s *Service) HostPlacements(ctx context.Context, hostID string) ([]Placement, error) {
	if _, err := s.store.GetHost(ctx, hostID); err != nil {
		return nil, err
	}
	return s.store.PlacementsOnHost(ctx, hostID)
}

// TenantView is one organization's standing with the platform Cloud. Tenant
// is nil for an organization that has never been enabled or disabled.
type TenantView struct {
	OrgID  string  `json:"org_id"`
	Tenant *Tenant `json:"tenant"`
	// Limits is what applies: the tenant's own, or the instance defaults.
	Limits Limits `json:"limits"`
	Use    Use    `json:"use"`
	// CleanupBlocked is set for a deleted organization's tenant whose
	// cleanup is waiting on something still referencing it.
	CleanupBlocked *CleanupBlock `json:"cleanup_blocked,omitempty"`
}

// Tenants lists every tenant and, when the service can list organizations,
// every organization without one too, in organization ID order.
func (s *Service) Tenants(ctx context.Context) ([]TenantView, error) {
	defaults, err := s.defaultLimits(ctx)
	if err != nil {
		return nil, err
	}
	tenants, err := s.store.ListTenants(ctx)
	if err != nil {
		return nil, err
	}
	byOrg := make(map[string]*Tenant, len(tenants))
	for i := range tenants {
		byOrg[tenants[i].OrgID] = &tenants[i]
	}
	orgs := make([]string, 0, len(tenants))
	for _, t := range tenants {
		orgs = append(orgs, t.OrgID)
	}
	if s.orgs != nil {
		all, listErr := s.orgs(ctx)
		if listErr != nil {
			return nil, listErr
		}
		for _, org := range all {
			if org != "" && byOrg[org] == nil {
				orgs = append(orgs, org)
			}
		}
	}
	slices.Sort(orgs)
	orgs = slices.Compact(orgs)
	blocked, err := s.cleanupBlocks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]TenantView, 0, len(orgs))
	for _, org := range orgs {
		v := TenantView{OrgID: org, Tenant: byOrg[org], Limits: limitsOf(byOrg[org], defaults)}
		if b, ok := blocked[org]; ok {
			v.CleanupBlocked = &b
		}
		if v.Tenant != nil {
			if v.Use, err = s.store.TenantUse(ctx, org); err != nil {
				return nil, err
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// EffectsPending reports whether err says a suspension, restore or disable
// was recorded but some VM couldn't be stopped or started yet. The jobs
// finish it.
func EffectsPending(err error) bool { return errors.Is(err, errEffects) }

// Tenant is the organization's tenant row.
func (s *Service) Tenant(ctx context.Context, orgID string) (*Tenant, error) {
	return s.store.GetTenant(ctx, orgID)
}

func validLimits(l *Limits) error {
	if l == nil {
		return nil
	}
	if l.VCPUs < 0 || l.MemoryMiB < 0 || l.VMs < 0 || l.ConcurrentBuilds < 0 {
		return errors.New("platformcloud: limits can't be negative")
	}
	return nil
}

// SetTenant enables or disables the organization and, when limits is set,
// replaces its limits. Enabling clears the platform owner's suspension;
// disabling adds it and stops the organization's work.
func (s *Service) SetTenant(ctx context.Context, orgID, status string, limits *Limits) (*Tenant, error) {
	if err := validLimits(limits); err != nil {
		return nil, err
	}
	switch status {
	case tenantEnabled:
		return s.EnableTenant(ctx, orgID, limits)
	case tenantDisabled:
	default:
		return nil, errors.New("platformcloud: a tenant is enabled or disabled")
	}
	// The reason and the tenant are recorded even when stopping the work
	// fails, so the limits still change and the error says to try again.
	disableErr := s.DisableTenant(ctx, orgID)
	t, err := s.store.GetTenant(ctx, orgID)
	if err != nil {
		return nil, errors.Join(disableErr, err)
	}
	if limits != nil && (t.Limits == nil || *t.Limits != *limits) {
		t.Limits = limits
		if err = s.store.UpsertTenant(ctx, t); err != nil {
			return nil, errors.Join(disableErr, err)
		}
	}
	return t, disableErr
}

// SecretsSet reports whether the registry and DNS credentials are stored.
// Neither is ever read back out.
func (s *Service) SecretsSet(ctx context.Context) (registry, dns bool) {
	vault, err := s.secretStore()
	if err != nil {
		return false, false
	}
	return secretStored(ctx, vault, registrySecret), secretStored(ctx, vault, dnsSecret)
}

// SecretChecker is a Secrets that can tell whether a key is stored without
// decrypting it. The Vault-backed store is one.
type SecretChecker interface {
	Exists(ctx context.Context, key string) (bool, error)
}

// secretStored reports whether key is stored, without reading the value
// when the store can say so.
func secretStored(ctx context.Context, vault Secrets, key string) bool {
	if c, ok := vault.(SecretChecker); ok {
		on, err := c.Exists(ctx, key)
		return err == nil && on
	}
	_, err := vault.Reveal(ctx, key)
	return err == nil
}

// PlacementOfInstance is the placement that runs instanceID: the live one,
// or the newest released one when none is live.
func (s *Service) PlacementOfInstance(ctx context.Context, instanceID string) (*Placement, error) {
	if instanceID == "" {
		return nil, ErrNotFound
	}
	return scanPlacement(s.store.db.QueryRow(ctx, `SELECT `+placementColumns+` FROM loomplane_platform_placements
WHERE instance_id=$1 ORDER BY (state<>'released') DESC, created_at DESC, id DESC LIMIT 1`, instanceID))
}

// RegionName is the name of the region with ID regionID.
func (s *Service) RegionName(ctx context.Context, regionID string) (string, error) {
	var name string
	err := s.store.db.QueryRow(ctx, `SELECT name FROM loomplane_platform_regions WHERE id=$1`, regionID).Scan(&name)
	return name, storeError(err)
}

// HasRoomElsewhere reports whether another host in the placement's region
// could take it now, as a move off its host would need. It reserves nothing,
// so a move can still find the room gone; it is the check that keeps a move
// from stopping a service with nowhere to go.
func (s *Service) HasRoomElsewhere(ctx context.Context, p Placement) (bool, error) {
	region, err := s.RegionName(ctx, p.RegionID)
	if err != nil {
		return false, err
	}
	r, err := regionByName(ctx, s.store.db, region)
	if err != nil {
		return false, err
	}
	req := Request{OrgID: p.OrgID, ProjectID: p.ProjectID, EnvironmentID: p.EnvironmentID, Resource: p.Resource, Region: region, Kind: p.Kind,
		VCPUs: p.VCPUs, MemoryMiB: p.MemoryMiB, DiskMiB: p.DiskMiB}
	if p.HostID != nil {
		if h, hostErr := s.store.GetHost(ctx, *p.HostID); hostErr == nil {
			req.Architecture = h.Evidence.Architecture
		}
	}
	hosts, err := s.candidates(ctx, s.store.db, req, r, &p)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(hosts, func(h Host) bool { return p.HostID == nil || h.ID != *p.HostID }), nil
}

// ActiveBuilds lists the organization's build placements that are reserved
// or running.
func (s *Service) ActiveBuilds(ctx context.Context, orgID string) ([]Placement, error) {
	return queryPlacements(ctx, s.store.db, `org_id=$1 AND kind='build' AND state IN ('reserved','running')`, orgID)
}

// WorkspaceRegion is a region as a workspace sees it.
type WorkspaceRegion struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
}

// WorkspacePlacement is one of a workspace's services as the workspace sees
// it: where it runs is the region, never the host.
type WorkspacePlacement struct {
	Project         string `json:"project"`
	Environment     string `json:"environment"`
	Service         string `json:"service"`
	Region          string `json:"region"`
	State           string `json:"state"`
	WaitingCapacity bool   `json:"waiting_capacity"`
}

// WorkspaceView is everything a workspace may read about its own use of
// the platform Cloud. It names no host, endpoint or host evidence.
type WorkspaceView struct {
	Enabled        bool                 `json:"enabled"`
	SuspendReasons []SuspendReason      `json:"suspend_reasons"`
	Limits         Limits               `json:"limits"`
	Use            Use                  `json:"use"`
	Regions        []WorkspaceRegion    `json:"regions"`
	Placements     []WorkspacePlacement `json:"placements"`
}

// Workspace is the organization's own view of the platform Cloud.
func (s *Service) Workspace(ctx context.Context, orgID string) (*WorkspaceView, error) {
	if orgID == "" {
		return nil, errors.New("platformcloud: organization is required")
	}
	if err := checkTenant(ctx, orgID); err != nil {
		return nil, err
	}
	defaults, err := s.defaultLimits(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.store.GetTenant(ctx, orgID)
	switch {
	case errors.Is(err, ErrNotFound):
		t = nil
	case err != nil:
		return nil, err
	}
	out := &WorkspaceView{SuspendReasons: []SuspendReason{}, Limits: limitsOf(t, defaults), Regions: []WorkspaceRegion{}, Placements: []WorkspacePlacement{}}
	if t != nil {
		out.Enabled = t.Status == tenantEnabled
		out.SuspendReasons = append(out.SuspendReasons, t.SuspendReasons...)
		if out.Use, err = s.store.TenantUse(ctx, orgID); err != nil {
			return nil, err
		}
	}
	regions, err := s.store.ListRegions(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(regions))
	for _, r := range regions {
		names[r.ID] = r.Name
		out.Regions = append(out.Regions, WorkspaceRegion{Name: r.Name, DisplayName: r.DisplayName, Status: r.Status})
	}
	placements, err := s.store.ListPlacements(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for _, p := range placements {
		if p.Kind != kindService {
			continue
		}
		out.Placements = append(out.Placements, WorkspacePlacement{Project: p.ProjectID, Environment: p.EnvironmentID, Service: p.Resource,
			Region: names[p.RegionID], State: p.State, WaitingCapacity: p.WaitingCapacity})
	}
	return out, nil
}
