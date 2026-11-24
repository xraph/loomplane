package buildrunner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/connections"
	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/metering"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/relay"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// Streams opens streams to joined nodes. The nodes hub satisfies it.
type Streams interface {
	Open(ctx context.Context, orgID, nodeID string, hdr nodeproto.StreamHeader) (net.Conn, error)
}

// NodeReader reads joined nodes. The nodes store satisfies it.
type NodeReader interface {
	Get(ctx context.Context, org, id string) (*nodes.Node, error)
}

// Publisher writes a pool's runner connection. The connections service
// satisfies it.
type Publisher interface {
	PublishRunnerPool(ctx context.Context, orgID, id, name, createdBy string, details map[string]string) (*connections.Connection, error)
	DeleteRunnerPool(ctx context.Context, orgID, id string) error
}

// Authorizer says whether a principal may do something.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// JoinTokens mint a runner's join token. The API keys service satisfies it
// through an adapter, so this package doesn't import it.
type JoinTokens interface {
	// MintRunnerJoin mints a token for one runner of poolID. machineID,
	// when set, binds it to the on-demand machine it was started for.
	MintRunnerJoin(ctx context.Context, p security.Principal, poolID, machineID string, lifetime time.Duration) (token string, expires time.Time, err error)
}

// Service keeps runner pools and reaches their runners. Its store is bound
// when its extension registers, which is after other extensions were given
// it, so every method first checks it is bound.
type Service struct {
	mu          sync.RWMutex
	store       Store
	Streams     Streams
	Nodes       NodeReader
	Connections Publisher
	Authz       Authorizer
	Tokens      JoinTokens
	// Provisioner starts and removes on-demand pools' machines, and
	// Revoker revokes their runners when they go. Nil refuses on-demand
	// pools' machines.
	Provisioner Provisioner
	Revoker     Revoker
	// PublicURL is where on-demand runners reach Loomplane.
	PublicURL string
	// Admins, Workspaces and Meter are global runners': who may manage
	// them, which workspaces may build on them, and where their builds are
	// metered. Nil Workspaces means no workspace may.
	Admins     PlatformAdmins
	Workspaces PlatformWorkspaces
	Meter      metering.Meter
	Log        *slog.Logger
	// clock is what time it is. Nil means time.Now. Tests set it.
	clock func() time.Time
	// starting tracks runners started in the background for a waiting
	// build, so tests can wait for them.
	starting sync.WaitGroup
}

// errUnbound is a call before the extension registered.
var errUnbound = errors.New("runner pools aren't available here")

// Bind gives the service its store.
func (s *Service) Bind(store Store) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store = store
}

func (s *Service) st() (Store, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.store == nil {
		return nil, errUnbound
	}
	return s.store, nil
}

func (s *Service) now() time.Time {
	if s.clock != nil {
		return s.clock().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

var poolName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// PoolInput is what creating a pool takes.
type PoolInput struct {
	Name      string       `json:"name"`
	Scope     string       `json:"scope"`
	ProjectID string       `json:"project_id,omitempty"`
	Mode      string       `json:"mode,omitempty"`
	Settings  PoolSettings `json:"settings"`
}

// CreatePool creates an organization or project pool and its connection.
// The connection isn't ready until a runner joins and passes its check.
func (s *Service) CreatePool(ctx context.Context, p security.Principal, in PoolInput) (*Pool, error) {
	store, err := s.st()
	if err != nil {
		return nil, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if !poolName.MatchString(in.Name) {
		return nil, fmt.Errorf("%w: a pool's name is a lowercase DNS label of at most 63 characters", ErrInvalid)
	}
	switch in.Scope {
	case ScopeOrganization:
		in.ProjectID = ""
	case ScopeProject:
		if strings.TrimSpace(in.ProjectID) == "" {
			return nil, fmt.Errorf("%w: a project pool names its project", ErrInvalid)
		}
	default:
		return nil, fmt.Errorf("%w: a pool serves an organization or one project", ErrInvalid)
	}
	// A project pool is the project's: a grant on the project may make one.
	if xerr := s.Authz.Require(ctx, p, security.NodeManage, in.ProjectID); xerr != nil {
		return nil, xerr
	}
	if in.Mode == "" {
		in.Mode = ModeSelfHosted
	}
	if xerr := validSettings(in.Mode, in.Settings); xerr != nil {
		return nil, xerr
	}
	if xerr := s.oneDefault(ctx, store, p.OrgID, "", in.Scope, in.ProjectID, in.Settings); xerr != nil {
		return nil, xerr
	}
	poolID, err := typeid.Generate("rpool")
	if err != nil {
		return nil, err
	}
	connID, err := typeid.Generate("conn")
	if err != nil {
		return nil, err
	}
	now := s.now()
	pool := &Pool{ID: poolID.String(), OrgID: p.OrgID, Scope: in.Scope, ProjectID: in.ProjectID, Name: in.Name, ConnectionID: connID.String(),
		Mode: in.Mode, Settings: in.Settings, Revision: 1, CreatedBy: p.UserID, CreatedAt: now, UpdatedAt: now}
	if err = store.CreatePool(ctx, pool); err != nil {
		if errors.Is(err, ErrConflict) {
			return nil, fmt.Errorf("%w: a pool named %s exists", ErrConflict, in.Name)
		}
		return nil, err
	}
	if _, err = s.Connections.PublishRunnerPool(ctx, p.OrgID, pool.ConnectionID, "Runner pool "+pool.Name, p.UserID, Details(pool, nil)); err != nil {
		if delErr := store.DeletePool(context.WithoutCancel(ctx), pool.ID); delErr != nil {
			s.log().Error("buildrunner: removing a pool whose connection failed", "pool", pool.ID, "error", delErr)
		}
		return nil, err
	}
	return pool, nil
}

// validSettings checks an on-demand pool's settings. A self-hosted pool
// takes none of them but the limits.
func validSettings(mode string, s PoolSettings) error {
	if s.MinWarm < 0 || s.MaxRunners < 0 || s.MaxBuilds < 0 || s.MaxAgeMinutes < 0 || s.IdleTimeoutMinutes < 0 || (s.MaxRunners > 0 && s.MinWarm > s.MaxRunners) {
		return fmt.Errorf("%w: pool limits must be positive, with min_warm at most max_runners", ErrInvalid)
	}
	switch mode {
	case ModeSelfHosted:
		if s.Provider != "" || s.CloudConnectionID != "" || len(s.Sizes) > 0 || s.MinWarm > 0 || s.ToolchainURL != "" || s.Spot {
			return fmt.Errorf("%w: a self-hosted pool's runners are the ones you start; it takes no cloud account, sizes or warm runners", ErrInvalid)
		}
	case ModeOnDemand:
		if (s.Provider != "digitalocean" && s.Provider != "aws") || s.CloudConnectionID == "" || s.Region == "" || len(s.Sizes) == 0 || s.MaxRunners < 1 {
			return fmt.Errorf("%w: an on-demand pool names a DigitalOcean or AWS connection, a region, the sizes it may start and max_runners", ErrInvalid)
		}
		// EC2 runners boot the AMI the pool pins, on instance types with
		// nested virtualization, such as C8i, M8i and R8i.
		if s.Provider == "digitalocean" && s.Spot {
			return fmt.Errorf("%w: DigitalOcean has no spot Droplets; spot is for AWS pools", ErrInvalid)
		}
		if s.Provider == "aws" && !amiPattern.MatchString(s.Image) {
			return fmt.Errorf("%w: an EC2 pool pins its runners' AMI, such as an Ubuntu 24.04 image", ErrInvalid)
		}
		return validToolchain(s)
	default:
		return fmt.Errorf("%w: a pool is self_hosted or on_demand", ErrInvalid)
	}
	return nil
}

// Details are what pool's runner connection carries for planning: the pins
// a plan reviews, and whether a runner showing them has checked in.
func Details(pool *Pool, members []Member) map[string]string {
	d := map[string]string{
		"pool_id": pool.ID, "pool_scope": pool.Scope, "pool_revision": strconv.FormatInt(pool.Revision, 10),
		"host_id": pool.ID, "isolation": "firecracker", "pool_mode": pool.Mode,
	}
	if pool.ProjectID != "" {
		d["project_id"] = pool.ProjectID
	}
	if pool.RuntimeIdentity != "" {
		d["architecture"], d["runtime_identity"] = pool.Architecture, pool.RuntimeIdentity
	}
	for _, m := range members {
		if pool.Takes(m) {
			d["build_isolation"] = "ready"
			// A platform pool shows the platform evidence its runners
			// showed, which the platform policy checks.
			if pool.Scope == ScopePlatform {
				for _, k := range []string{"platform", "smt_active", "denied_ports", "per_org_vms", "per_org_vcpus", "per_org_memory_mib"} {
					d[k] = m.Evidence[k]
				}
			}
			break
		}
	}
	return d
}

// PoolView is a pool with its runners, as people see it.
type PoolView struct {
	Pool
	Runners []RunnerView `json:"runners"`
}

// RunnerView is one runner of a pool.
type RunnerView struct {
	Member
	Name   string `json:"name"`
	Status string `json:"status"`
	// Takes says whether the runner takes the pool's builds.
	Takes bool `json:"takes_builds"`
}

// ListPools lists the organization's pools with their runners.
func (s *Service) ListPools(ctx context.Context, p security.Principal) ([]PoolView, error) {
	store, err := s.st()
	if err != nil {
		return nil, err
	}
	if xerr := s.Authz.Require(ctx, p, security.NodeRead, ""); xerr != nil {
		return nil, xerr
	}
	pools, err := store.ListPools(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	out := make([]PoolView, 0, len(pools))
	for i := range pools {
		v, err := s.view(ctx, store, &pools[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

// GetPool returns one of the organization's pools with its runners.
func (s *Service) GetPool(ctx context.Context, p security.Principal, id string) (*PoolView, error) {
	store, err := s.st()
	if err != nil {
		return nil, err
	}
	pool, err := store.GetPool(ctx, id)
	if err != nil {
		return nil, err
	}
	if pool.OrgID != p.OrgID {
		return nil, ErrNotFound
	}
	if err := s.Authz.Require(ctx, p, security.NodeRead, pool.ProjectID); err != nil {
		return nil, err
	}
	return s.view(ctx, store, pool)
}

func (s *Service) view(ctx context.Context, store Store, pool *Pool) (*PoolView, error) {
	members, err := store.ListMembers(ctx, pool.ID)
	if err != nil {
		return nil, err
	}
	v := &PoolView{Pool: *pool, Runners: []RunnerView{}}
	for _, m := range members {
		r := RunnerView{Member: m, Takes: pool.Takes(m), Status: string(nodes.StatusOffline)}
		if n, nerr := s.Nodes.Get(ctx, m.OrgID, m.NodeID); nerr == nil {
			r.Name, r.Status = n.Name, string(n.Status)
		}
		v.Runners = append(v.Runners, r)
	}
	return v, nil
}

// DeletePool removes a pool and its connection once none of its runners is
// still joined. Revoke or leave them first.
func (s *Service) DeletePool(ctx context.Context, p security.Principal, id string) error {
	store, err := s.st()
	if err != nil {
		return err
	}
	pool, err := store.GetPool(ctx, id)
	if err != nil {
		return err
	}
	if pool.OrgID != p.OrgID {
		return ErrNotFound
	}
	if xerr := s.Authz.Require(ctx, p, security.NodeManage, pool.ProjectID); xerr != nil {
		return xerr
	}
	members, err := store.ListMembers(ctx, pool.ID)
	if err != nil {
		return err
	}
	for _, m := range members {
		n, nerr := s.Nodes.Get(ctx, m.OrgID, m.NodeID)
		if nerr == nil && n.Status != nodes.StatusRevoked {
			return fmt.Errorf("%w: runner %s is still in the pool; revoke it or run runner leave on it first", ErrInUse, n.Name)
		}
		if derr := store.DeleteMember(ctx, m.NodeID); derr != nil {
			return derr
		}
	}
	if pool.ConnectionID != "" {
		if err := s.Connections.DeleteRunnerPool(ctx, pool.OrgID, pool.ConnectionID); err != nil {
			return err
		}
	}
	return store.DeletePool(ctx, pool.ID)
}

// JoinToken mints a single-use token a runner joins pool id with.
func (s *Service) JoinToken(ctx context.Context, p security.Principal, id string, lifetime time.Duration) (token string, expires time.Time, err error) {
	store, err := s.st()
	if err != nil {
		return "", time.Time{}, err
	}
	pool, err := store.GetPool(ctx, id)
	if err != nil {
		return "", time.Time{}, err
	}
	if pool.OrgID != p.OrgID {
		return "", time.Time{}, ErrNotFound
	}
	if s.Tokens == nil {
		return "", time.Time{}, errUnbound
	}
	return s.Tokens.MintRunnerJoin(ctx, p, pool.ID, "", lifetime)
}

// JoinRunner records node nodeID as a runner of pool poolID as it joins,
// and returns the connection its builds run under. The nodes service calls
// it once the join token is spent.
func (s *Service) JoinRunner(ctx context.Context, orgID, poolID, nodeID string) (string, error) {
	store, err := s.st()
	if err != nil {
		return "", err
	}
	pool, err := store.GetPool(ctx, poolID)
	if err != nil {
		return "", err
	}
	if pool.Scope == ScopePlatform {
		// A global runner joins its platform admin's organization, and
		// its builds run under the pool's own ID. Only a platform admin
		// mints its token.
		if err := store.AddMember(ctx, &Member{NodeID: nodeID, PoolID: pool.ID, OrgID: orgID, CreatedAt: s.now()}); err != nil {
			return "", err
		}
		return pool.ID, nil
	}
	if pool.OrgID != orgID || pool.ConnectionID == "" {
		return "", fmt.Errorf("%w: the runner pool isn't in this organization", ErrNotFound)
	}
	if err := store.AddMember(ctx, &Member{NodeID: nodeID, PoolID: pool.ID, OrgID: orgID, CreatedAt: s.now()}); err != nil {
		return "", err
	}
	return pool.ConnectionID, nil
}

// RunnerOnline checks a runner that just connected, or whose inventory
// changed: it asks its Firecracker host for its capabilities over its
// stream, records what it showed, pins the pool's toolchain from the first
// runner that passes, and publishes the pool's connection again. The hub
// runs it off its own goroutine with a deadline.
func (s *Service) RunnerOnline(ctx context.Context, n *nodes.Node) {
	if n == nil || n.Role != "runner" {
		return
	}
	if err := s.checkRunner(ctx, n); err != nil {
		s.log().Warn("buildrunner: checking a runner; its next connect checks again", "node", n.ID, "error", err)
	}
}

func (s *Service) checkRunner(ctx context.Context, n *nodes.Node) error {
	store, err := s.st()
	if err != nil {
		return err
	}
	member, err := store.GetMember(ctx, n.ID)
	if err != nil {
		return err
	}
	pool, err := store.GetPool(ctx, member.PoolID)
	if err != nil {
		return err
	}
	now := s.now()
	member.CheckedAt = &now
	member.Reason = ""
	client, err := s.RunnerClient(ctx, n.OrgID, n.ID)
	if err == nil {
		defer client.Close()
		var caps fc.Capabilities
		if caps, err = client.Check(ctx); err == nil {
			member.Architecture, member.RuntimeIdentity = caps.Architecture, caps.RuntimeIdentity
			member.Evidence = evidence(client.Details, caps)
		}
	}
	if err != nil {
		member.Reason = "the runner didn't pass its KVM and microVM check: " + err.Error()
	}
	if member.Reason == "" && pool.RuntimeIdentity == "" {
		// The first runner to pass pins the pool's toolchain. Plans
		// reviewed against the unpinned pool are stale from here on.
		expected := pool.Revision
		pool.Architecture, pool.RuntimeIdentity, pool.Revision = member.Architecture, member.RuntimeIdentity, pool.Revision+1
		if uerr := store.UpdatePool(ctx, pool, expected); uerr != nil {
			if pool, err = store.GetPool(ctx, member.PoolID); err != nil {
				return err
			}
		}
	}
	if member.Reason == "" && (member.RuntimeIdentity != pool.RuntimeIdentity || member.Architecture != pool.Architecture) {
		member.Reason = "the runner's Firecracker toolchain or architecture differs from the one the pool pins; install the same release and guest image, or repin the pool"
	}
	if err := store.UpdateMember(ctx, member); err != nil {
		return err
	}
	if member.Reason == "" {
		if err := s.linkMachine(ctx, n, pool); err != nil {
			return err
		}
	}
	return s.publish(ctx, store, pool)
}

// evidence is what a runner showed, as the pool records it.
func evidence(details map[string]string, caps fc.Capabilities) map[string]string {
	e := map[string]string{"isolation": "firecracker", "build_isolation": details["build_isolation"], "runtime_identity": caps.RuntimeIdentity,
		"host_id": caps.HostID, "architecture": caps.Architecture, "platform": strconv.FormatBool(caps.Platform), "smt_active": strconv.FormatBool(caps.SMTActive),
		"per_org_vms": strconv.Itoa(caps.Limits.PerOrg.VMs), "per_org_vcpus": strconv.Itoa(caps.Limits.PerOrg.VCPUs), "per_org_memory_mib": strconv.Itoa(caps.Limits.PerOrg.MemoryMiB),
		"vms": strconv.Itoa(caps.Limits.VMs)}
	ports := make([]string, 0, len(caps.DeniedPorts))
	for _, p := range caps.DeniedPorts {
		ports = append(ports, strconv.Itoa(p))
	}
	e["denied_ports"] = strings.Join(ports, ",")
	return e
}

// publish writes pool's connection from its members.
func (s *Service) publish(ctx context.Context, store Store, pool *Pool) error {
	if pool.ConnectionID == "" {
		return nil
	}
	members, err := store.ListMembers(ctx, pool.ID)
	if err != nil {
		return err
	}
	_, err = s.Connections.PublishRunnerPool(ctx, pool.OrgID, pool.ConnectionID, "Runner pool "+pool.Name, pool.CreatedBy, Details(pool, members))
	return err
}

// BuildRunners are the runners of the pool behind connection that may take
// a build now: online and showing the toolchain the pool pins, those that
// built least recently first.
func (s *Service) BuildRunners(ctx context.Context, orgID, connection string) ([]string, error) {
	store, err := s.st()
	if err != nil {
		return nil, err
	}
	var pool *Pool
	if isPoolID(connection) {
		if pool, err = store.GetPool(ctx, connection); err != nil || pool.Scope != ScopePlatform {
			return nil, ErrNotFound
		}
		if !s.globalAllowed(ctx, orgID) {
			return nil, errors.New("this workspace may not build on Loomplane's global runners")
		}
	} else if pool, err = store.PoolByConnection(ctx, orgID, connection); err != nil {
		return nil, err
	}
	runners, err := s.online(ctx, store, pool, true)
	if err != nil || pool.Mode != ModeOnDemand {
		return runners, err
	}
	// An on-demand pool's cold runners take one build each.
	ms, err := s.machines()
	if err != nil {
		return nil, err
	}
	free := runners[:0]
	for _, nodeID := range runners {
		if m, merr := ms.MachineByNode(ctx, nodeID); merr == nil && takesOnDemand(m) {
			free = append(free, nodeID)
		}
	}
	if len(free) == 0 {
		// No runner is free: start one. The build waits and looks again.
		waiting := *pool
		s.starting.Go(func() {
			if derr := s.demand(context.WithoutCancel(ctx), &waiting); derr != nil {
				s.log().Warn("buildrunner: starting a runner for a waiting build", "pool", waiting.ID, "error", derr)
			}
		})
	}
	return free, nil
}

// online lists pool's online runners; build keeps only those that take its
// builds.
func (s *Service) online(ctx context.Context, store Store, pool *Pool, build bool) ([]string, error) {
	members, err := store.ListMembers(ctx, pool.ID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range members {
		if build && !pool.Takes(m) {
			continue
		}
		n, nerr := s.Nodes.Get(ctx, m.OrgID, m.NodeID)
		if nerr != nil || n.Status != nodes.StatusOnline || n.Role != "runner" {
			continue
		}
		out = append(out, m.NodeID)
	}
	return out, nil
}

// RunnerClient reaches a runner's Firecracker host over its node stream.
func (s *Service) RunnerClient(_ context.Context, orgID, nodeID string) (*fc.Client, error) {
	if s.Streams == nil {
		return nil, errUnbound
	}
	return fc.NewStreamClient(nodeID, func(ctx context.Context) (net.Conn, error) {
		return s.Streams.Open(ctx, orgID, nodeID, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointFirecracker})
	})
}

// RecordBuild counts a build a runner finished.
func (s *Service) RecordBuild(ctx context.Context, _, nodeID string) error {
	store, err := s.st()
	if err != nil {
		return err
	}
	return store.RecordBuild(ctx, nodeID, s.now())
}

// DialVia opens TCP to address through an online runner of pool poolID, in
// the pool's own organization, from inside the runner's network. Connection
// checks hold a connection to its own organization's pools. The runner dials only networks its
// owner allowed, and never cloud metadata or loopback. Runners are tried in
// turn; the error names why each refused.
func (s *Service) DialVia(ctx context.Context, poolID, address string) (net.Conn, error) {
	store, err := s.st()
	if err != nil {
		return nil, err
	}
	pool, err := store.GetPool(ctx, poolID)
	if err != nil {
		return nil, fmt.Errorf("the relay pool %s: %w", poolID, err)
	}
	if pool.OrgID == "" {
		return nil, errors.New("a platform pool relays to no one's network")
	}
	orgID := pool.OrgID
	runners, err := s.online(ctx, store, pool, false)
	if err != nil {
		return nil, err
	}
	if len(runners) == 0 {
		return nil, fmt.Errorf("no runner of pool %s is online to relay to %s", pool.Name, address)
	}
	var reasons []string
	for _, nodeID := range runners {
		conn, oerr := s.Streams.Open(ctx, orgID, nodeID, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointRelay, Target: address})
		if oerr == nil {
			return conn, nil
		}
		reasons = append(reasons, oerr.Error())
	}
	return nil, fmt.Errorf("no runner of pool %s relayed to %s: %s", pool.Name, address, strings.Join(reasons, "; "))
}

// PoolOwner is the organization pool belongs to, empty for a platform pool.
func (s *Service) PoolOwner(ctx context.Context, poolID string) (string, error) {
	store, err := s.st()
	if err != nil {
		return "", err
	}
	pool, err := store.GetPool(ctx, poolID)
	if err != nil {
		return "", err
	}
	return pool.OrgID, nil
}

var _ relay.Dialer = (*Service)(nil)

// oneDefault refuses a second default pool for one organization or project.
func (s *Service) oneDefault(ctx context.Context, store Store, orgID, self, scope, projectID string, settings PoolSettings) error {
	if settings.Registry != "" && !strings.HasPrefix(settings.Registry, "conn_") {
		return fmt.Errorf("%w: registry names a registry connection", ErrInvalid)
	}
	if !settings.Default {
		return nil
	}
	pools, err := store.ListPools(ctx, orgID)
	if err != nil {
		return err
	}
	for _, q := range pools {
		if q.ID != self && q.Settings.Default && q.Scope == scope && q.ProjectID == projectID {
			return fmt.Errorf("%w: pool %s is the default already; make it not the default first", ErrConflict, q.Name)
		}
	}
	return nil
}

// UpdateSettings replaces a pool's settings. Its pins and revision stay, so
// plans made against it stay current; a default that moves takes effect on
// the next plan.
func (s *Service) UpdateSettings(ctx context.Context, p security.Principal, id string, settings PoolSettings) (*Pool, error) {
	store, err := s.st()
	if err != nil {
		return nil, err
	}
	pool, err := store.GetPool(ctx, id)
	if err != nil {
		return nil, err
	}
	if pool.OrgID != p.OrgID {
		return nil, ErrNotFound
	}
	if err := s.Authz.Require(ctx, p, security.NodeManage, pool.ProjectID); err != nil {
		return nil, err
	}
	if err := validSettings(pool.Mode, settings); err != nil {
		return nil, err
	}
	if err := s.oneDefault(ctx, store, pool.OrgID, pool.ID, pool.Scope, pool.ProjectID, settings); err != nil {
		return nil, err
	}
	pool.Settings = settings
	if err := store.UpdatePool(ctx, pool, pool.Revision); err != nil {
		return nil, err
	}
	return pool, nil
}

// BuildDefault is the pool a project's services build on when their
// manifest names no builder: the project's default pool, else the
// organization's. Nil when neither has one.
func (s *Service) BuildDefault(ctx context.Context, orgID, projectID string) (*planning.BuildDefault, error) {
	store, err := s.st()
	if err != nil {
		return nil, nil //nolint:nilerr // without runner pools there is no default
	}
	pools, err := store.ListPools(ctx, orgID)
	if err != nil {
		return nil, err
	}
	var org *Pool
	for i := range pools {
		q := &pools[i]
		if !q.Settings.Default || q.ConnectionID == "" {
			continue
		}
		if q.Scope == ScopeProject && q.ProjectID == projectID {
			return &planning.BuildDefault{Builder: q.ConnectionID, Registry: q.Settings.Registry, Source: "the project's runner pool " + q.Name}, nil
		}
		if q.Scope == ScopeOrganization {
			org = q
		}
	}
	if org == nil {
		return s.globalDefault(ctx, store, orgID)
	}
	return &planning.BuildDefault{Builder: org.ConnectionID, Registry: org.Settings.Registry, Source: "the organization's runner pool " + org.Name}, nil
}
