package platformcloud

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/instance"
	"github.com/xraph/loomplane/internal/platformcloud"
	"github.com/xraph/loomplane/internal/security"
)

// control is what the routes ask of the platform Cloud's control plane.
// *platformcloud.Service is one.
type control interface {
	Regions(ctx context.Context) ([]platformcloud.Region, error)
	CreateRegion(ctx context.Context, name, displayName, status string) (*platformcloud.Region, error)
	UpdateRegion(ctx context.Context, region string, in platformcloud.RegionUpdate) (*platformcloud.Region, error)
	Hosts(ctx context.Context) ([]platformcloud.HostView, error)
	HostPlacements(ctx context.Context, hostID string) ([]platformcloud.Placement, error)
	AddHost(ctx context.Context, actor string, in platformcloud.HostInput) (*platformcloud.NewHost, error)
	CheckHost(ctx context.Context, actor, hostID string) (*platformcloud.Host, error)
	DrainHost(ctx context.Context, actor, hostID string) (*platformcloud.Host, error)
	RetireHost(ctx context.Context, actor, hostID string) error
	SetAllotment(ctx context.Context, actor, hostID string, a platformcloud.Allotment) (*platformcloud.Host, error)
	Tenants(ctx context.Context) ([]platformcloud.TenantView, error)
	Tenant(ctx context.Context, orgID string) (*platformcloud.Tenant, error)
	SetTenant(ctx context.Context, orgID, status string, limits *platformcloud.Limits) (*platformcloud.Tenant, error)
	EnableTenant(ctx context.Context, orgID string, limits *platformcloud.Limits) (*platformcloud.Tenant, error)
	Suspend(ctx context.Context, orgID string, reason platformcloud.SuspendReason) error
	SetRegistrySecret(ctx context.Context, actor, username, password, token string) error
	SetDNSSecret(ctx context.Context, actor, accountID, teamID, token string) error
	SecretsSet(ctx context.Context) (registry, dns bool)
	Workspace(ctx context.Context, orgID string) (*platformcloud.WorkspaceView, error)
}

var _ control = (*platformcloud.Service)(nil)

// settingsStore reads and writes the instance's platform Cloud settings.
type settingsStore interface {
	PlatformCloud(ctx context.Context) (instance.PlatformCloudSettings, error)
	SetPlatformCloud(ctx context.Context, v instance.PlatformCloudSettings) error
}

// mover moves a running service off its draining host through the normal
// deploy path.
type mover interface {
	Move(ctx context.Context, instanceID string) error
}

// auditor records what the platform owner changed, once the change is
// made, with how it went: success, pending (recorded, its effects still
// being carried out by the jobs), no_change, or failure.
type auditor interface {
	Record(ctx context.Context, action, userID, resource, resourceID, outcome string, meta map[string]string)
}

const (
	outcomeSuccess  = "success"
	outcomePending  = "pending"
	outcomeNoChange = "no_change"
	outcomeFailure  = "failure"
)

// authorizer checks a workspace member's permission.
type authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// routes serves the platform owner's Platform Cloud console and a
// workspace's own view of it.
type routes struct {
	svc        control
	settings   settingsStore
	mover      mover
	audit      auditor
	authz      authorizer
	isPlatform identity.PlatformUser
	// orgNames maps organization IDs to their names.
	orgNames func(ctx context.Context) (map[string]string, error)
}

const platformBase = "/api/v1/platform/cloud"

// platformUserKey carries the platform user the gate found to the handlers.
type platformUserKey struct{}

func platformUserID(c forge.Context) string {
	id, ok := c.Request().Context().Value(platformUserKey{}).(string)
	if !ok {
		return ""
	}
	return id
}

var errNotFound = forge.NewHTTPError(http.StatusNotFound, "not found")

// platformOnly is the platform routes' middleware: the guard, then the gate
// that lets only a platform user through. Anyone else gets the 404 an
// unknown route would, an anonymous caller and a refused API key included,
// so the routes' existence isn't given away. An API key never passes: the
// platform user comes from the session alone, and a principal that came
// with a key is turned away too.
func (r *routes) platformOnly(guard identity.Guard) []forge.Middleware {
	hide := func(next forge.Handler) forge.Handler {
		return func(c forge.Context) error {
			err := next(c)
			var status interface{ StatusCode() int }
			if errors.As(err, &status) && (status.StatusCode() == http.StatusUnauthorized || status.StatusCode() == http.StatusForbidden) {
				return errNotFound
			}
			return err
		}
	}
	gate := func(next forge.Handler) forge.Handler {
		return func(c forge.Context) error {
			ctx := c.Request().Context()
			if p, ok := security.PrincipalFrom(ctx); ok && p.KeyID != "" {
				return errNotFound
			}
			userID, ok := r.isPlatform(ctx)
			if !ok || userID == "" {
				return errNotFound
			}
			c.WithContext(context.WithValue(ctx, platformUserKey{}, userID))
			return next(c)
		}
	}
	return slices.Concat([]forge.Middleware{hide}, guard, []forge.Middleware{gate})
}

// SettingsView is the platform Cloud settings. The registry and DNS
// credentials are never returned, only whether they are stored.
type SettingsView struct {
	Attach                  string                       `json:"attach"`
	DefaultLimits           platformcloud.Limits         `json:"default_limits"`
	Apex                    string                       `json:"apex"`
	Registry                instance.RegistrySettings    `json:"registry"`
	DNS                     instance.PlatformDNSSettings `json:"dns"`
	RegistryCredentialSaved bool                         `json:"registry_credential_saved"`
	DNSCredentialSaved      bool                         `json:"dns_credential_saved"`
}

// SettingsInput replaces the platform Cloud settings. The credential fields
// are write-only: set them to replace the stored credential, leave them out
// to keep it.
type SettingsInput struct {
	Attach           string                       `json:"attach"`
	DefaultLimits    platformcloud.Limits         `json:"default_limits"`
	Apex             string                       `json:"apex"`
	Registry         instance.RegistrySettings    `json:"registry"`
	DNS              instance.PlatformDNSSettings `json:"dns"`
	RegistryUsername string                       `json:"registry_username,omitempty"`
	RegistryPassword string                       `json:"registry_password,omitempty"`
	DNSAccountID     string                       `json:"dns_account_id,omitempty"`
	DNSTeamID        string                       `json:"dns_team_id,omitempty"`
	DNSToken         string                       `json:"dns_token,omitempty"`
}

// RegionList is every region.
type RegionList struct {
	Regions []platformcloud.Region `json:"regions"`
}

// RegionInput adds a region.
type RegionInput struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status,omitempty"`
}

// HostList is every host with what its placements use.
type HostList struct {
	Hosts []platformcloud.HostView `json:"hosts"`
}

// HostInput registers a host.
type HostInput struct {
	RegionID  string                  `json:"region_id"`
	Endpoint  string                  `json:"endpoint"`
	HostID    string                  `json:"host_id"`
	ServerCA  string                  `json:"server_ca"`
	Allotment platformcloud.Allotment `json:"allotment"`
}

// NewHost is a registered host with the client certificate its agent must
// trust. The certificate is shown here once and never again.
type NewHost struct {
	Host              platformcloud.Host `json:"host"`
	ClientCertificate string             `json:"client_certificate"`
	ClientFingerprint string             `json:"client_fingerprint"`
}

// MoveAll is a host that is draining and what became of its services:
// how many moved to another host, which were skipped because no other host
// had room (they run where they were), and which failed to start elsewhere
// (started again where they were).
type MoveAll struct {
	Host    platformcloud.Host `json:"host"`
	Moved   int                `json:"moved"`
	Skipped []string           `json:"skipped"`
	Failed  []string           `json:"failed"`
}

// TenantRow is one organization's standing with the platform Cloud.
type TenantRow struct {
	platformcloud.TenantView
	OrgName string `json:"org_name"`
}

// TenantList is every organization's standing with the platform Cloud.
type TenantList struct {
	Tenants []TenantRow `json:"tenants"`
}

// TenantInput enables or disables an organization and sets its limits.
// Leaving limits out keeps them.
type TenantInput struct {
	Status string                `json:"status"`
	Limits *platformcloud.Limits `json:"limits,omitempty"`
}

func (r *routes) mount(router forge.Router, guard identity.Guard) error {
	g := router.Group(platformBase,
		forge.WithGroupMiddleware(r.platformOnly(guard)...),
		forge.WithGroupTags("Platform Cloud"))
	notPlatform := forge.WithResponseSchema(http.StatusNotFound, "Not a platform user, or no such thing", nil)
	type route struct {
		method, path string
		handler      forge.Handler
		opts         []forge.RouteOption
	}
	all := []route{
		{http.MethodGet, "/settings", r.getSettings, []forge.RouteOption{
			forge.WithName("platformCloudSettings.get"),
			forge.WithSummary("The platform Cloud settings. The registry and DNS credentials are never returned"),
			forge.WithResponseSchema(http.StatusOK, "Settings", SettingsView{}),
		}},
		{http.MethodPut, "/settings", r.setSettings, []forge.RouteOption{
			forge.WithName("platformCloudSettings.set"),
			forge.WithSummary("Replace the platform Cloud settings, and the registry or DNS credential when given"),
			forge.WithRequestSchema(SettingsInput{}),
			forge.WithResponseSchema(http.StatusOK, "Settings", SettingsView{}),
			forge.WithResponseSchema(http.StatusUnprocessableEntity, "A setting or credential is invalid", nil),
		}},
		{http.MethodGet, "/regions", r.listRegions, []forge.RouteOption{
			forge.WithName("platformCloudRegions.list"),
			forge.WithSummary("Every platform Cloud region"),
			forge.WithResponseSchema(http.StatusOK, "Regions", RegionList{}),
		}},
		{http.MethodPost, "/regions", r.createRegion, []forge.RouteOption{
			forge.WithName("platformCloudRegions.create"),
			forge.WithSummary("Add a region"),
			forge.WithRequestSchema(RegionInput{}),
			forge.WithResponseSchema(http.StatusCreated, "The region", platformcloud.Region{}),
			forge.WithResponseSchema(http.StatusConflict, "A region has the name", nil),
		}},
		{http.MethodPatch, "/regions/:region", r.updateRegion, []forge.RouteOption{
			forge.WithName("platformCloudRegions.update"),
			forge.WithSummary("Rename a region for display, or open or close it"),
			forge.WithRequestSchema(platformcloud.RegionUpdate{}),
			forge.WithResponseSchema(http.StatusOK, "The region", platformcloud.Region{}),
			forge.WithResponseSchema(http.StatusConflict, "The region changed since it was read", nil),
		}},
		{http.MethodGet, "/hosts", r.listHosts, []forge.RouteOption{
			forge.WithName("platformCloudHosts.list"),
			forge.WithSummary("Every host, with its evidence, allotment and what its placements use"),
			forge.WithResponseSchema(http.StatusOK, "Hosts", HostList{}),
		}},
		{http.MethodPost, "/hosts", r.createHost, []forge.RouteOption{
			forge.WithName("platformCloudHosts.create"),
			forge.WithSummary("Register a host. The client certificate its agent must trust is returned this once"),
			forge.WithRequestSchema(HostInput{}),
			forge.WithResponseSchema(http.StatusCreated, "The host and its client certificate", NewHost{}),
		}},
		{http.MethodPost, "/hosts/:host/check", r.checkHost, []forge.RouteOption{
			forge.WithName("platformCloudHosts.check"),
			forge.WithSummary("Check a host now and record its evidence"),
			forge.WithResponseSchema(http.StatusOK, "The host", platformcloud.Host{}),
		}},
		{http.MethodPost, "/hosts/:host/drain", r.drainHost, []forge.RouteOption{
			forge.WithName("platformCloudHosts.drain"),
			forge.WithSummary("Stop new placements on a host. What runs there keeps running"),
			forge.WithResponseSchema(http.StatusOK, "The host", platformcloud.Host{}),
		}},
		{http.MethodPost, "/hosts/:host/move-all", r.moveAll, []forge.RouteOption{
			forge.WithName("platformCloudHosts.moveAll"),
			forge.WithSummary("Drain a host and move its services to other hosts one at a time. A service with nowhere to go stays where it is"),
			forge.WithResponseSchema(http.StatusOK, "The host and what moved, was skipped for capacity or failed", MoveAll{}),
		}},
		{http.MethodPost, "/hosts/:host/retire", r.retireHost, []forge.RouteOption{
			forge.WithName("platformCloudHosts.retire"),
			forge.WithSummary("Take a drained, empty host out of service for good"),
			forge.WithNoContentResponse(),
			forge.WithResponseSchema(http.StatusConflict, "The host isn't drained or still has placements", nil),
		}},
		{http.MethodPut, "/hosts/:host/allotment", r.setAllotment, []forge.RouteOption{
			forge.WithName("platformCloudHosts.setAllotment"),
			forge.WithSummary("Change how much of a host the platform may use. The host is checked again before it takes placements"),
			forge.WithRequestSchema(platformcloud.Allotment{}),
			forge.WithResponseSchema(http.StatusOK, "The host", platformcloud.Host{}),
		}},
		{http.MethodGet, "/tenants", r.listTenants, []forge.RouteOption{
			forge.WithName("platformCloudTenants.list"),
			forge.WithSummary("Every organization's standing with the platform Cloud and its use against its limits"),
			forge.WithResponseSchema(http.StatusOK, "Tenants", TenantList{}),
		}},
		{http.MethodPut, "/tenants/:org", r.setTenant, []forge.RouteOption{
			forge.WithName("platformCloudTenants.set"),
			forge.WithSummary("Enable or disable an organization and set its limits"),
			forge.WithRequestSchema(TenantInput{}),
			forge.WithResponseSchema(http.StatusOK, "The tenant", platformcloud.Tenant{}),
			forge.WithResponseSchema(http.StatusAccepted, "The tenant is disabled; some of its work is still being stopped", platformcloud.Tenant{}),
		}},
		{http.MethodPost, "/tenants/:org/suspend", r.suspendTenant, []forge.RouteOption{
			forge.WithName("platformCloudTenants.suspend"),
			forge.WithSummary("Stop an organization's platform work as the platform owner"),
			forge.WithResponseSchema(http.StatusOK, "The tenant", platformcloud.Tenant{}),
			forge.WithResponseSchema(http.StatusAccepted, "The tenant is suspended; some of its work is still being stopped", platformcloud.Tenant{}),
			forge.WithResponseSchema(http.StatusConflict, "The organization isn't a tenant", nil),
		}},
		{http.MethodPost, "/tenants/:org/restore", r.restoreTenant, []forge.RouteOption{
			forge.WithName("platformCloudTenants.restore"),
			forge.WithSummary("Lift the platform owner's suspension. The work starts again within a minute, once nothing else stops it"),
			forge.WithResponseSchema(http.StatusOK, "The tenant", platformcloud.Tenant{}),
			forge.WithResponseSchema(http.StatusConflict, "The organization is disabled, or isn't a tenant", nil),
		}},
	}
	for _, rt := range all {
		opts := append(slices.Clone(rt.opts), notPlatform)
		var err error
		switch rt.method {
		case http.MethodGet:
			err = g.GET(rt.path, rt.handler, opts...)
		case http.MethodPost:
			err = g.POST(rt.path, rt.handler, opts...)
		case http.MethodPut:
			err = g.PUT(rt.path, rt.handler, opts...)
		case http.MethodPatch:
			err = g.PATCH(rt.path, rt.handler, opts...)
		}
		if err != nil {
			return err
		}
	}
	return r.mountWorkspace(router, guard)
}

// fail maps a control plane error to its response. The platform owner may
// read the control plane's own refusals; anything else is logged and is a
// 500.
func fail(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, platformcloud.ErrNotFound):
		return forge.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, platformcloud.ErrConflict), errors.Is(err, platformcloud.ErrNotEnabled), errors.Is(err, platformcloud.ErrSuspended):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case strings.HasPrefix(err.Error(), "platformcloud: "):
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}
	slog.ErrorContext(ctx, "platform cloud request failed", "error", err)
	return forge.NewHTTPError(http.StatusInternalServerError, "the platform Cloud request failed")
}

func badBody(example string) error {
	return forge.NewHTTPError(http.StatusBadRequest, "the body must be JSON: "+example)
}

func (r *routes) settingsView(ctx context.Context) (*SettingsView, error) {
	cfg, err := r.settings.PlatformCloud(ctx)
	if err != nil {
		return nil, err
	}
	reg, dns := r.svc.SecretsSet(ctx)
	return &SettingsView{Attach: cfg.Attach, DefaultLimits: cfg.DefaultLimits, Apex: cfg.Apex, Registry: cfg.Registry, DNS: cfg.DNS,
		RegistryCredentialSaved: reg, DNSCredentialSaved: dns}, nil
}

func (r *routes) getSettings(c forge.Context) error {
	ctx := c.Request().Context()
	v, err := r.settingsView(ctx)
	if err != nil {
		return fail(ctx, err)
	}
	return c.JSON(http.StatusOK, v)
}

func unprocessable(msg string) error { return forge.NewHTTPError(http.StatusUnprocessableEntity, msg) }

func (r *routes) setSettings(c forge.Context) error {
	ctx := c.Request().Context()
	var in SettingsInput
	if err := c.BindJSON(&in); err != nil {
		return badBody(`{"attach": "manual", "default_limits": {...}, "apex": "...", "registry": {...}, "dns": {...}}`)
	}
	switch in.Attach {
	case "manual", "all_workspaces":
	default:
		return unprocessable(`attach is "manual" or "all_workspaces"`)
	}
	switch in.DNS.Provider {
	case "", "cloudflare", "digitalocean", "vercel":
	default:
		return unprocessable(`the DNS provider is "cloudflare", "digitalocean" or "vercel"`)
	}
	l := in.DefaultLimits
	if l.VCPUs < 0 || l.MemoryMiB < 0 || l.VMs < 0 || l.ConcurrentBuilds < 0 {
		return unprocessable("default limits can't be negative")
	}
	registrySecret := in.RegistryUsername != "" || in.RegistryPassword != ""
	if registrySecret && (in.RegistryUsername == "" || in.RegistryPassword == "") {
		return unprocessable("the registry credential needs a username and a password")
	}
	dnsSecret := in.DNSToken != "" || in.DNSAccountID != "" || in.DNSTeamID != ""
	if dnsSecret && (in.DNSToken == "" || in.DNSAccountID == "") {
		return unprocessable("the DNS credential needs an account and a token")
	}
	cfg := instance.PlatformCloudSettings{Attach: in.Attach, DefaultLimits: in.DefaultLimits, Apex: strings.ToLower(strings.TrimSpace(in.Apex)),
		Registry: in.Registry, DNS: instance.PlatformDNSSettings{Provider: in.DNS.Provider, Zone: strings.ToLower(strings.TrimSpace(in.DNS.Zone))}}
	if err := r.settings.SetPlatformCloud(ctx, cfg); err != nil {
		return fail(ctx, err)
	}
	actor := platformUserID(c)
	meta := map[string]string{"attach": cfg.Attach, "apex": cfg.Apex, "dns_provider": cfg.DNS.Provider, "registry": cfg.Registry.Endpoint,
		"registry_credential": "kept", "dns_credential": "kept"}
	var secretErr error
	if registrySecret {
		if secretErr = r.svc.SetRegistrySecret(ctx, actor, in.RegistryUsername, in.RegistryPassword, ""); secretErr != nil {
			meta["registry_credential"] = "refused"
		} else {
			meta["registry_credential"] = "replaced"
		}
	}
	if dnsSecret && secretErr == nil {
		if secretErr = r.svc.SetDNSSecret(ctx, actor, in.DNSAccountID, in.DNSTeamID, in.DNSToken); secretErr != nil {
			meta["dns_credential"] = "refused"
		} else {
			meta["dns_credential"] = "replaced"
		}
	}
	outcome := outcomeSuccess
	if secretErr != nil {
		outcome = outcomeFailure
	}
	r.audit.Record(ctx, "platform_cloud.settings.update", actor, "platform_cloud_settings", "platform_cloud", outcome, meta)
	if secretErr != nil {
		return fail(ctx, secretErr)
	}
	v, err := r.settingsView(ctx)
	if err != nil {
		return fail(ctx, err)
	}
	return c.JSON(http.StatusOK, v)
}

func (r *routes) listRegions(c forge.Context) error {
	ctx := c.Request().Context()
	regions, err := r.svc.Regions(ctx)
	if err != nil {
		return fail(ctx, err)
	}
	return c.JSON(http.StatusOK, RegionList{Regions: regions})
}

func (r *routes) createRegion(c forge.Context) error {
	ctx := c.Request().Context()
	var in RegionInput
	if err := c.BindJSON(&in); err != nil {
		return badBody(`{"name": "eu-west", "display_name": "EU West"}`)
	}
	region, err := r.svc.CreateRegion(ctx, in.Name, in.DisplayName, in.Status)
	if err != nil {
		return fail(ctx, err)
	}
	r.audit.Record(ctx, "platform_cloud.region.create", platformUserID(c), "platform_cloud_region", region.ID, outcomeSuccess,
		map[string]string{"name": region.Name, "status": region.Status})
	return c.JSON(http.StatusCreated, region)
}

func (r *routes) updateRegion(c forge.Context) error {
	ctx := c.Request().Context()
	var in platformcloud.RegionUpdate
	if err := c.BindJSON(&in); err != nil {
		return badBody(`{"display_name": "EU West", "status": "closed", "revision": 2}`)
	}
	region, err := r.svc.UpdateRegion(ctx, c.Param("region"), in)
	if err != nil {
		return fail(ctx, err)
	}
	r.audit.Record(ctx, "platform_cloud.region.update", platformUserID(c), "platform_cloud_region", region.ID, outcomeSuccess,
		map[string]string{"name": region.Name, "status": region.Status})
	return c.JSON(http.StatusOK, region)
}

func (r *routes) listHosts(c forge.Context) error {
	ctx := c.Request().Context()
	hosts, err := r.svc.Hosts(ctx)
	if err != nil {
		return fail(ctx, err)
	}
	return c.JSON(http.StatusOK, HostList{Hosts: hosts})
}

func (r *routes) createHost(c forge.Context) error {
	ctx := c.Request().Context()
	var in HostInput
	if err := c.BindJSON(&in); err != nil {
		return badBody(`{"region_id": "...", "endpoint": "https://...", "host_id": "...", "server_ca": "-----BEGIN CERTIFICATE-----...", "allotment": {...}}`)
	}
	actor := platformUserID(c)
	nh, err := r.svc.AddHost(ctx, actor, platformcloud.HostInput{RegionID: in.RegionID, Endpoint: in.Endpoint, HostID: in.HostID, ServerCA: in.ServerCA, Allotment: in.Allotment})
	if err != nil {
		return fail(ctx, err)
	}
	r.audit.Record(ctx, "platform_cloud.host.create", actor, "platform_cloud_host", nh.Host.ID, outcomeSuccess,
		map[string]string{"region": nh.Host.RegionID, "host_id": nh.Host.HostID, "client_fingerprint": nh.ClientFingerprint})
	return c.JSON(http.StatusCreated, NewHost{Host: nh.Host, ClientCertificate: nh.ClientCertificate, ClientFingerprint: nh.ClientFingerprint})
}

func (r *routes) hostAction(c forge.Context, verb string, act func(ctx context.Context, actor, hostID string) (*platformcloud.Host, error)) error {
	ctx := c.Request().Context()
	actor := platformUserID(c)
	h, err := act(ctx, actor, c.Param("host"))
	if err != nil {
		return fail(ctx, err)
	}
	r.audit.Record(ctx, "platform_cloud.host."+verb, actor, "platform_cloud_host", h.ID, outcomeSuccess, map[string]string{"status": h.Status})
	return c.JSON(http.StatusOK, h)
}

func (r *routes) checkHost(c forge.Context) error { return r.hostAction(c, "check", r.svc.CheckHost) }
func (r *routes) drainHost(c forge.Context) error { return r.hostAction(c, "drain", r.svc.DrainHost) }

func (r *routes) setAllotment(c forge.Context) error {
	var a platformcloud.Allotment
	if err := c.BindJSON(&a); err != nil {
		return badBody(`{"vcpus": 32, "memory_mib": 65536, "disk_mib": 512000, "vms": 40}`)
	}
	return r.hostAction(c, "allotment_update", func(ctx context.Context, actor, hostID string) (*platformcloud.Host, error) {
		return r.svc.SetAllotment(ctx, actor, hostID, a)
	})
}

func (r *routes) retireHost(c forge.Context) error {
	ctx := c.Request().Context()
	actor, hostID := platformUserID(c), c.Param("host")
	if err := r.svc.RetireHost(ctx, actor, hostID); err != nil {
		return fail(ctx, err)
	}
	r.audit.Record(ctx, "platform_cloud.host.retire", actor, "platform_cloud_host", hostID, outcomeSuccess, nil)
	return c.NoContent(http.StatusNoContent)
}

// moveAllTimeout bounds one service's redeploy in a move.
const moveAllTimeout = 30 * time.Minute

// moveAll drains the host, then moves its services off it one at a time,
// each through the normal deploy path, waiting for each to finish. A service
// with no room on another host is left running where it is and counted as
// skipped; one whose new host fails is started again where it was and
// counted as failed. The answer says how many moved, were skipped and
// failed. Run it again once there is room: the host stays draining.
func (r *routes) moveAll(c forge.Context) error {
	ctx := c.Request().Context()
	actor, hostID := platformUserID(c), c.Param("host")
	h, err := r.svc.DrainHost(ctx, actor, hostID)
	if err != nil {
		return fail(ctx, err)
	}
	placements, err := r.svc.HostPlacements(ctx, hostID)
	if err != nil {
		return fail(ctx, err)
	}
	out := MoveAll{Host: *h, Skipped: []string{}, Failed: []string{}}
	for _, p := range placements {
		if p.Kind != "service" || p.InstanceID == "" || p.State != "running" {
			continue
		}
		if r.mover == nil {
			out.Skipped = append(out.Skipped, p.Resource)
			continue
		}
		// A move that has stopped the service must finish whatever the
		// request does, so it runs on a context of its own.
		one, cancel := context.WithTimeout(context.WithoutCancel(ctx), moveAllTimeout)
		moveErr := r.mover.Move(one, p.InstanceID)
		cancel()
		switch {
		case moveErr == nil:
			out.Moved++
		case errors.Is(moveErr, platformcloud.ErrNoCapacity):
			out.Skipped = append(out.Skipped, p.Resource)
		default:
			slog.ErrorContext(ctx, "platform cloud move: a service could not be moved", "host", hostID, "placement", p.ID, "error", moveErr)
			out.Failed = append(out.Failed, p.Resource)
		}
	}
	outcome := outcomeSuccess
	if len(out.Failed) > 0 || len(out.Skipped) > 0 {
		outcome = outcomeFailure
	}
	// The moves ran whatever the request did; so does their record.
	r.audit.Record(context.WithoutCancel(ctx), "platform_cloud.host.move_all", actor, "platform_cloud_host", h.ID, outcome,
		map[string]string{"moved": strconv.Itoa(out.Moved), "skipped": strconv.Itoa(len(out.Skipped)), "failed": strconv.Itoa(len(out.Failed))})
	return c.JSON(http.StatusOK, out)
}

func (r *routes) listTenants(c forge.Context) error {
	ctx := c.Request().Context()
	tenants, err := r.svc.Tenants(ctx)
	if err != nil {
		return fail(ctx, err)
	}
	names := map[string]string{}
	if r.orgNames != nil {
		if names, err = r.orgNames(ctx); err != nil {
			return fail(ctx, err)
		}
	}
	out := TenantList{Tenants: make([]TenantRow, 0, len(tenants))}
	for _, t := range tenants {
		out.Tenants = append(out.Tenants, TenantRow{TenantView: t, OrgName: names[t.OrgID]})
	}
	return c.JSON(http.StatusOK, out)
}

func (r *routes) setTenant(c forge.Context) error {
	ctx := c.Request().Context()
	var in TenantInput
	if err := c.BindJSON(&in); err != nil {
		return badBody(`{"status": "enabled", "limits": {"vcpus": 4, "memory_mib": 8192, "vms": 10, "concurrent_builds": 1}}`)
	}
	org := c.Param("org")
	t, err := r.svc.SetTenant(ctx, org, in.Status, in.Limits)
	if err != nil && t == nil {
		return fail(ctx, err)
	}
	meta := map[string]string{"org": org, "status": in.Status}
	if in.Limits != nil {
		meta["limits"] = "replaced"
	}
	outcome := outcomeSuccess
	if err != nil {
		outcome = outcomePending
	}
	r.audit.Record(ctx, "platform_cloud.tenant.update", platformUserID(c), "platform_cloud_tenant", org, outcome, meta)
	if platformcloud.EffectsPending(err) {
		// Recorded, but the work didn't all stop yet; the jobs finish it.
		return c.JSON(http.StatusAccepted, t)
	}
	if err != nil {
		return fail(ctx, err)
	}
	return c.JSON(http.StatusOK, t)
}

func (r *routes) suspendTenant(c forge.Context) error {
	ctx := c.Request().Context()
	org := c.Param("org")
	err := r.svc.Suspend(ctx, org, platformcloud.ReasonPlatformOwner)
	if errors.Is(err, platformcloud.ErrNotEnabled) {
		return forge.NewHTTPError(http.StatusConflict, "the organization isn't a platform Cloud tenant")
	}
	meta := map[string]string{"org": org, "reason": string(platformcloud.ReasonPlatformOwner)}
	outcome := outcomeSuccess
	switch {
	case platformcloud.EffectsPending(err):
		outcome = outcomePending
	case err != nil:
		outcome = outcomeFailure
		meta["error"] = err.Error()
	}
	r.audit.Record(ctx, "platform_cloud.tenant.suspend", platformUserID(c), "platform_cloud_tenant", org, outcome, meta)
	t, readErr := r.svc.Tenant(ctx, org)
	if readErr != nil {
		return fail(ctx, errors.Join(err, readErr))
	}
	if platformcloud.EffectsPending(err) {
		// The reason is recorded; what still runs is stopped by the jobs.
		return c.JSON(http.StatusAccepted, t)
	}
	if err != nil {
		return fail(ctx, err)
	}
	return c.JSON(http.StatusOK, t)
}

// restoreTenant lifts the platform owner's suspension of an enabled tenant
// by enabling it again, which keeps its limits. A disabled tenant is
// enabled through PUT /tenants/:org instead, so a restore never undoes a
// disable by accident. The restore job starts the work.
func (r *routes) restoreTenant(c forge.Context) error {
	ctx := c.Request().Context()
	org := c.Param("org")
	t, err := r.svc.Tenant(ctx, org)
	if errors.Is(err, platformcloud.ErrNotFound) {
		return forge.NewHTTPError(http.StatusConflict, "the organization isn't a platform Cloud tenant")
	}
	if err != nil {
		return fail(ctx, err)
	}
	if t.Status != "enabled" {
		return forge.NewHTTPError(http.StatusConflict, "the tenant is disabled; enable it to restore its work")
	}
	outcome := outcomeNoChange
	if slices.Contains(t.SuspendReasons, platformcloud.ReasonPlatformOwner) {
		outcome = outcomeSuccess
	}
	if t, err = r.svc.EnableTenant(ctx, org, nil); err != nil {
		r.audit.Record(ctx, "platform_cloud.tenant.restore", platformUserID(c), "platform_cloud_tenant", org, outcomeFailure,
			map[string]string{"org": org, "error": err.Error()})
		return fail(ctx, err)
	}
	r.audit.Record(ctx, "platform_cloud.tenant.restore", platformUserID(c), "platform_cloud_tenant", org, outcome,
		map[string]string{"org": org, "reason": string(platformcloud.ReasonPlatformOwner)})
	return c.JSON(http.StatusOK, t)
}
