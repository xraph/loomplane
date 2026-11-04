package deployments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"

	"github.com/xraph/ctrlplane"
	cpauth "github.com/xraph/ctrlplane/auth"
	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/connections"
	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/platformcloud"
)

// platformProvider is the provider, cloud and runtime of a Loomplane Cloud
// service.
const platformProvider = platformcloud.RuntimePlatform

// PlatformHosts is what PlatformTarget needs of Loomplane Cloud's control
// plane. *platformcloud.Service is one. Its errors are safe to show the
// workspace: they never name a host.
type PlatformHosts interface {
	// Session reserves room for req and opens the host it is placed on,
	// with the scope the instance resource runs under there.
	Session(ctx context.Context, orgID, projectID, envID, resource string, req platformcloud.Request) (*fc.Client, fc.Scope, *platformcloud.Placement, error)
	// Attach opens the host the instance resource of service name runs on,
	// reserving nothing. A placement with no host gives a nil client.
	Attach(ctx context.Context, orgID, projectID, envID, resource, name string) (*fc.Client, fc.Scope, *platformcloud.Placement, error)
	// Open opens the host of a placement, whatever its state.
	Open(ctx context.Context, p *platformcloud.Placement) (*fc.Client, fc.Scope, error)
	// Leftovers are the released placements of service name whose VM may
	// still be on their host.
	Leftovers(ctx context.Context, orgID, envID, name string) ([]platformcloud.Placement, error)
	Running(ctx context.Context, orgID, placementID, instance string, generation int64) error
	Stopped(ctx context.Context, orgID, placementID string) error
	Removed(ctx context.Context, orgID, placementID string) error
}

var _ PlatformHosts = (*platformcloud.Service)(nil)

// PlatformTarget runs services on Loomplane Cloud: on Firecracker hosts the
// platform owns, through the same Ctrl Plane lifecycle and Firecracker
// backend an organization's own Firecracker connection uses. Each region is
// one Ctrl Plane provider, "platform.<region>", shared by every
// organization; the instance's labels name its organization, project and
// environment, and the platform connection. It never reads an
// organization's connections.
//
// Loomplane Cloud doesn't build from source or route releases yet: a
// service runs a digest-pinned image, and is reached on its private
// address until its generated hostname is published.
type PlatformTarget struct {
	Ctrl     CtrlPlane
	Platform PlatformHosts
	// Credentials reads the organization's connections. Loomplane Cloud
	// reads only registry connections through it, to pull a workspace's
	// own image with its own credential, passed to the host per request.
	Credentials Credentials
	Authority   fc.Issuer
	Secrets     ResourceSecrets
	RouteGuard  RuntimeRemovalGuard
	// Bindings lists what runs in an environment.
	Bindings func(ctx context.Context, orgID, environmentID string) ([]*planning.Binding, error)

	mu      sync.Mutex
	regions map[string]*OwnedTarget
}

var _ Executor = (*PlatformTarget)(nil)
var _ Locator = (*PlatformTarget)(nil)

// region is the owned target that runs a region's services.
func (t *PlatformTarget) region(region string) (*OwnedTarget, error) {
	if !validPlatformRegion(region) {
		return nil, errors.New("the Loomplane Cloud region is missing or invalid; plan again")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.regions[region]; e != nil {
		return e, nil
	}
	e := &OwnedTarget{Cloud: platformProvider, Ctrl: t.Ctrl, Credentials: registryOnly{t.Credentials}, Secrets: t.Secrets, RouteGuard: t.RouteGuard, FirecrackerAuthority: t.Authority, Bindings: t.Bindings,
		providerKey: platformProvider + "." + region}
	e.Backend = func(string, string) owned.Backend { return t.newBackend(e, region) }
	if t.regions == nil {
		t.regions = map[string]*OwnedTarget{}
	}
	t.regions[region] = e
	return e, nil
}

func validPlatformRegion(region string) bool {
	if region == "" || len(region) > 63 {
		return false
	}
	for _, r := range region {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// backend is the registered backend of a region's provider.
func (t *PlatformTarget) backend(region string) (owned.Backend, error) {
	e, err := t.region(region)
	if err != nil {
		return nil, err
	}
	return e.registeredBackend(e.Register("", fc.PlatformConnection))
}

// platformConnection stands in for the connection a Loomplane Cloud service
// has none of, so the owned lifecycle labels the instance with the platform
// connection.
func platformConnection(orgID string) *connections.Connection {
	return &connections.Connection{ID: fc.PlatformConnection, OrgID: orgID, Cloud: platformProvider, Status: connections.StatusReady}
}

func (t *PlatformTarget) Apply(ctx context.Context, job *Job) (*Outcome, error) {
	if job.Deployment == nil || job.Action.Service == nil {
		return nil, ErrNoExecutor
	}
	a := job.Action
	cfg := *a.Service
	pin := cfg.CloudPlacement
	switch {
	case a.Resource != nil:
		return nil, platformRefusalError(fmt.Sprintf("Loomplane Cloud runs services, not %s.", a.Name))
	case a.Provider != platformProvider || a.ConnectionID != "" || cfg.Target != "" || pin == nil || pin.Platform == nil || pin.ConnectionID != "" || pin.Platform.Region != a.Region:
		return nil, fmt.Errorf("%s isn't a reviewed Loomplane Cloud placement; plan again", a.Name)
	case len(a.Routes) > 0:
		return nil, platformRefusalError(fmt.Sprintf("Loomplane Cloud doesn't run routed releases yet, so %s wasn't deployed.", a.Name))
	case cfg.BuildTarget == nil || !pinnedImage(cfg.BuildTarget.Image):
		return nil, platformRefusalError(fmt.Sprintf("Loomplane Cloud runs digest-pinned images only for now. Give %s an image pinned with @sha256.", a.Name))
	}
	e, err := t.region(a.Region)
	if err != nil {
		return nil, err
	}
	cfg.Target = fc.PlatformConnection
	run := *job
	run.Action.Service = &cfg
	run.Action.ConnectionID = fc.PlatformConnection
	run.Connection, run.Credential = platformConnection(job.Deployment.OrgID), nil
	// A workspace's own image pulls through its registry connection; that
	// is the only connection a platform deployment reads.
	run.BuildCredentials = registryOnly{job.BuildCredentials}
	return e.Apply(ctx, &run)
}

func (t *PlatformTarget) Remove(ctx context.Context, job *Job, b *planning.Binding) error {
	if b == nil || b.Provider != platformProvider || b.ConnectionID != "" {
		return errors.New("the binding isn't a Loomplane Cloud workload")
	}
	e, err := t.region(b.Region)
	if err != nil {
		return err
	}
	bound := *b
	bound.ConnectionID = fc.PlatformConnection
	run := *job
	run.Connection, run.Credential = platformConnection(b.OrgID), nil
	if removeErr := e.Remove(ctx, &run, &bound); removeErr != nil {
		return removeErr
	}
	if b.ExternalID == "" {
		return nil
	}
	// Ctrl Plane removes the VM through the provider when it deletes the
	// instance. One whose instance was already gone may still have its
	// placement and VM, so they go here too; for anything else this finds
	// nothing.
	iid, err := id.Parse(b.ExternalID)
	if err != nil {
		return err
	}
	backend, err := t.backend(b.Region)
	if err != nil {
		return err
	}
	labels := map[string]string{owned.LabelOrg: b.OrgID, owned.LabelProject: b.ProjectID, owned.LabelEnvironment: b.EnvironmentID, owned.LabelConnection: fc.PlatformConnection, owned.LabelService: b.Name}
	if job.Deployment != nil {
		labels[owned.LabelRevision] = job.Deployment.ID + "/" + job.Action.ID
	}
	return backend.Remove(ctx, iid, labels)
}

func (t *PlatformTarget) Observe(ctx context.Context, _ *connections.Connection, _ []byte, b *planning.Binding) (*Observation, error) {
	if b == nil || b.Provider != platformProvider || b.ConnectionID != "" {
		return nil, errors.New("the binding isn't a Loomplane Cloud workload")
	}
	e, err := t.region(b.Region)
	if err != nil {
		return nil, err
	}
	bound := *b
	bound.ConnectionID = fc.PlatformConnection
	return e.Observe(ctx, platformConnection(b.OrgID), nil, &bound)
}

// pinnedImage reports whether image names its content by a sha256 digest.
func pinnedImage(image string) bool {
	name, digest, ok := strings.Cut(image, "@")
	return ok && name != "" && imageDigest.MatchString(digest)
}

// registryOnly reads an organization's registry connections and nothing
// else: any other connection, or one of another organization, is refused.
type registryOnly struct{ inner Credentials }

func (r registryOnly) Credential(ctx context.Context, orgID, connectionID string) (*connections.Connection, []byte, error) {
	if r.inner == nil {
		return nil, nil, ErrNoCredential
	}
	c, raw, err := r.inner.Credential(ctx, orgID, connectionID)
	if err != nil {
		return nil, nil, err
	}
	if c == nil || c.Cloud != connections.Registry || c.OrgID != orgID || c.ID != connectionID {
		return nil, nil, platformRefusalError("Loomplane Cloud reads only the workspace's own registry connections.")
	}
	return c, raw, nil
}

func (t *PlatformTarget) Locate(b *planning.Binding) string {
	return "the Loomplane Cloud workload " + b.ExternalID
}

// platformScope reads whose a Loomplane Cloud instance is from its labels,
// refusing one that isn't on the platform connection or that the caller
// acts for another organization on.
func platformScope(ctx context.Context, labels map[string]string) (org, project, env, name string, err error) {
	org, project, env, name = labels[owned.LabelOrg], labels[owned.LabelProject], labels[owned.LabelEnvironment], labels[owned.LabelService]
	if labels[owned.LabelConnection] != fc.PlatformConnection || org == "" || project == "" || env == "" || name == "" {
		return "", "", "", "", errors.New("the service is not a Loomplane Cloud workload")
	}
	if claims := cpauth.ClaimsFrom(ctx); claims != nil && claims.TenantID != org {
		return "", "", "", "", ctrlplane.ErrForbidden
	}
	return org, project, env, name, nil
}

// newBackend is the Firecracker backend of a region: it reserves a
// placement to apply, opens the placement's host for everything else, and
// keeps the placement's state as the VM's changes.
func (t *PlatformTarget) newBackend(e *OwnedTarget, region string) *firecrackerBackend {
	b := &firecrackerBackend{target: e, unpublished: true, redact: platformRedact}
	b.session = func(ctx context.Context, iid id.ID, labels map[string]string) (*fc.Client, fc.Scope, error) {
		org, project, env, name, err := platformScope(ctx, labels)
		if err != nil {
			return nil, fc.Scope{}, err
		}
		c, scope, _, err := t.Platform.Attach(ctx, org, project, env, iid.String(), name)
		if errors.Is(err, platformcloud.ErrNotFound) {
			// No placement holds it: nothing runs it anywhere.
			return nil, fc.Scope{}, nil
		}
		return c, scope, err
	}
	b.place = func(ctx context.Context, iid id.ID, labels map[string]string, spec fc.Spec) (*fc.Client, fc.Scope, func(context.Context, fc.Record) error, error) {
		org, project, env, name, err := platformScope(ctx, labels)
		if err != nil {
			return nil, fc.Scope{}, nil, err
		}
		req := platformcloud.Request{OrgID: org, ProjectID: project, EnvironmentID: env, Resource: name, Region: region, Kind: "service",
			Architecture: labels[owned.LabelArchitecture], VCPUs: spec.VCPUs, MemoryMiB: spec.MemoryMiB, DiskMiB: spec.DiskMiB}
		c, scope, p, err := t.Platform.Session(ctx, org, project, env, iid.String(), req)
		if err != nil {
			return nil, fc.Scope{}, nil, err
		}
		applied := func(ctx context.Context, r fc.Record) error {
			if runErr := t.Platform.Running(ctx, p.OrgID, p.ID, iid.String(), r.Generation); runErr != nil {
				return runErr
			}
			t.sweep(ctx, b, labels, p)
			return nil
		}
		return c, scope, applied, nil
	}
	b.effect = func(ctx context.Context, iid id.ID, labels map[string]string, action string, act func(*fc.Client, fc.Scope) error) error {
		org, project, env, name, err := platformScope(ctx, labels)
		if err != nil {
			return err
		}
		c, scope, p, err := t.Platform.Attach(ctx, org, project, env, iid.String(), name)
		switch {
		case errors.Is(err, platformcloud.ErrNotFound):
			p = nil
		case err != nil:
			return err
		}
		if c != nil {
			err = act(c, scope)
			c.Close()
			if err != nil {
				return err
			}
		}
		if action == "stop" {
			if p == nil {
				return nil
			}
			// Someone stopped it, so a restore leaves it stopped.
			return t.Platform.Stopped(ctx, p.OrgID, p.ID)
		}
		if p != nil {
			if removedErr := t.Platform.Removed(ctx, p.OrgID, p.ID); removedErr != nil {
				return removedErr
			}
		}
		// What a move left of this instance on another host goes too.
		leftovers, err := t.Platform.Leftovers(ctx, org, env, name)
		if err != nil {
			return err
		}
		var errs []error
		for i := range leftovers {
			if leftovers[i].InstanceID == iid.String() {
				errs = append(errs, t.removeLeftover(ctx, &leftovers[i], act))
			}
		}
		return errors.Join(errs...)
	}
	return b
}

// removeLeftover removes the VM a released placement may have left on its
// host, then records it gone.
func (t *PlatformTarget) removeLeftover(ctx context.Context, p *platformcloud.Placement, act func(*fc.Client, fc.Scope) error) error {
	c, scope, err := t.Platform.Open(ctx, p)
	if err != nil {
		return err
	}
	if c != nil {
		err = act(c, scope)
		c.Close()
		if err != nil {
			return err
		}
	}
	return t.Platform.Removed(ctx, p.OrgID, p.ID)
}

// sweep removes, once a service is up on its placement live, the VMs it
// left on other hosts when Loomplane Cloud moved it, so their room isn't
// stranded. A released placement that names the instance live now holds on
// the same host is the same VM, and is only forgotten. A VM that can't be
// removed now keeps its released placement's instance, which is what the
// cleanup job looks for, and the deploy still succeeds.
func (t *PlatformTarget) sweep(ctx context.Context, b *firecrackerBackend, labels map[string]string, live *platformcloud.Placement) {
	leftovers, err := t.Platform.Leftovers(ctx, live.OrgID, live.EnvironmentID, live.Resource)
	if err != nil {
		slog.WarnContext(ctx, "list what a platform move left behind", "org", live.OrgID, "environment", live.EnvironmentID, "placement", live.ID, "error", err.Error())
		return
	}
	for i := range leftovers {
		l := &leftovers[i]
		if l.ID == live.ID {
			continue
		}
		var leftErr error
		if l.HostID != nil && live.HostID != nil && *l.HostID == *live.HostID && l.InstanceID == live.InstanceID {
			leftErr = t.Platform.Removed(ctx, l.OrgID, l.ID)
		} else {
			leftErr = t.removeLeftover(ctx, l, func(c *fc.Client, scope fc.Scope) error { return b.mutateOn(ctx, c, scope, labels, "remove") })
		}
		if leftErr != nil {
			slog.WarnContext(ctx, "a platform VM a move left behind is still on its old host; the platform cleanup removes it",
				"org", live.OrgID, "environment", live.EnvironmentID, "placement", l.ID, "error", platformRedact(ctx, "move cleanup", leftErr).Error())
		}
	}
}

// platformRefusalError is a refusal whose text is shown to people as written.
type platformRefusalError string

func (m platformRefusalError) Error() string { return string(m) }

// errPlatformHost stands in for a failure reaching a platform host, whose
// own text names the host's address.
var errPlatformHost error = platformRefusalError("Loomplane Cloud couldn't reach the host running this right now.")

// platformRedact keeps what a workspace reads of a failure on a platform
// host free of the host's address: a transport failure becomes
// errPlatformHost. The original goes to the platform owner's log.
func platformRedact(ctx context.Context, op string, err error) error {
	var urlErr *url.Error
	var netErr net.Error
	if errors.As(err, &urlErr) || errors.As(err, &netErr) {
		slog.ErrorContext(ctx, "platform host "+op+" failed", "error", err.Error())
		return errPlatformHost
	}
	return err
}
