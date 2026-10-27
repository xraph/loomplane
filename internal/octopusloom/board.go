package octopusloom

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/provider"
)

// Where a board row comes from: BoardRoute.Source.
const (
	SourceFarp  = "farp"
	SourceHere  = "here"
	SourceAdmin = "octopus-admin"
)

// Why a service can't be routed: UnroutableService.Reason.
const (
	ReasonNoPort      = "no-port"
	ReasonOtherTarget = "other-target"
)

// What a gateway's FARP discovery is: DiscoverySummary.Mode. On a cluster
// Octopus finds the services itself; on a server Loomplane registers them.
const (
	DiscoveryKubernetes = "kubernetes"
	DiscoveryPush       = "push"
)

// The states of a discovered service: DiscoveredService.State.
const (
	DiscoveryRegistered = "registered"
	DiscoveryWaiting    = "waiting"
	DiscoveryFailed     = "failed"
	DiscoveryDiscovered = "discovered"
)

// Fixed text the board shows.
const (
	msgBoardUnreachable = "Octopus isn't answering on its private port, so FARP routes can't be shown. Your routes are kept and pushed again when it's back."
	noteAdminRoute      = "Added in the Octopus admin. It won't survive a restart, so add it here instead."
	noteOpenAPI         = "From the OpenAPI schema"
	noteAsyncAPI        = "From the AsyncAPI schema"
	noteFarp            = "From the FARP schemas"
)

// Origins of the routes FARP discovery put in Octopus.
const (
	originFarp       = "farp"
	originKubernetes = "kubernetes"
)

// blockedPathPrefixes are the paths a domain route to Octopus never
// forwards: FARP's endpoints and the admin dashboard.
var blockedPathPrefixes = []string{"/__farp", "/_farp", "/admin"}

// Board is a gateway's routes by hostname: the ones added in Loomplane,
// the ones FARP discovered and the ones made in the Octopus admin, with
// the services nothing routes to yet.
type Board struct {
	Gateway    OctopusStatus       `json:"gateway"`
	Discovery  DiscoverySummary    `json:"discovery"`
	Hosts      []HostCard          `json:"hosts"`
	Unrouted   []ServicePort       `json:"unrouted"`   // HTTP port, no route on any host
	Unroutable []UnroutableService `json:"unroutable"` // reason "no-port" | "other-target"
	Counts     SourceCounts        `json:"counts"`
	Live       bool                `json:"live"` // Octopus answered
	LiveError  string              `json:"live_error,omitempty"`
}

// HostCard is one hostname's routes.
type HostCard struct {
	Hostname string `json:"hostname"` // "" is any host
	// Attached is false for a hostname a manual route names that is no
	// longer attached to the gateway.
	Attached      bool                    `json:"attached"`
	DomainRouteID string                  `json:"domain_route_id,omitempty"`
	Status        *cloudinfra.RouteStatus `json:"status,omitempty"`
	// FarpHome is a hostname FARP routes answer on.
	FarpHome bool         `json:"farp_home"`
	Routes   []BoardRoute `json:"routes"`
}

// BoardRoute is one row of a host card.
type BoardRoute struct {
	Key         string   `json:"key"`
	Source      string   `json:"source"`             // "farp" | "here" | "octopus-admin"
	RouteID     string   `json:"route_id,omitempty"` // manual route id
	Path        string   `json:"path"`
	Methods     []string `json:"methods"` // empty is any
	Service     string   `json:"service"`
	Port        int      `json:"port"`
	Note        string   `json:"note"`
	StripPrefix bool     `json:"strip_prefix"`
	// Overrides marks a manual route that hides a FARP route on the same
	// host and path.
	Overrides bool   `json:"overrides"`
	Revision  int64  `json:"revision,omitempty"`
	Sync      string `json:"sync,omitempty"` // manual: "applied" | "pending" | "failed"
	SyncError string `json:"sync_error,omitempty"`
}

// ServicePort is a service's HTTP port.
type ServicePort struct {
	Service string `json:"service"`
	Port    int    `json:"port"`
}

// UnroutableService is a service the gateway can't route to, and why.
type UnroutableService struct {
	Service string `json:"service"`
	Reason  string `json:"reason"` // "no-port" | "other-target"
}

// SourceCounts counts a board's rows: all of them, FARP's and Loomplane's.
type SourceCounts struct {
	All  int `json:"all"`
	Farp int `json:"farp"`
	Here int `json:"here"`
}

// OctopusStatus is a gateway's state: where it runs, the FARP hostnames
// it serves, how far its last sync got, and what attaching a domain to it
// takes. It never carries a credential.
type OctopusStatus struct {
	Resource  string `json:"resource"`
	Version   string `json:"version,omitempty"`    // Octopus's, when it answered
	StartTime string `json:"start_time,omitempty"` // when Octopus last started
	Provider  string `json:"provider"`
	Namespace string `json:"namespace,omitempty"`
	// PrivateURL is the gateway's address inside its environment, empty
	// until its first deploy writes its binding.
	PrivateURL        string         `json:"private_url,omitempty"`
	FarpHostnames     []string       `json:"farp_hostnames"`
	Generation        int64          `json:"generation"`
	AppliedGeneration int64          `json:"applied_generation"`
	Revision          int64          `json:"revision"`
	SyncedAt          time.Time      `json:"synced_at"`
	SyncError         string         `json:"sync_error,omitempty"`
	Attach            AttachDefaults `json:"attach"`
}

// AttachDefaults is what a domain route to the gateway is created with.
type AttachDefaults struct {
	CloudID             string   `json:"cloud_id,omitempty"` // empty off a Cloud
	EnvironmentID       string   `json:"environment_id"`
	Service             string   `json:"service"` // GatewayAction(resource)
	Port                int      `json:"port"`    // 8080
	BlockedPathPrefixes []string `json:"blocked_path_prefixes"`
}

// DiscoverySummary is what the gateway's FARP discovery found.
type DiscoverySummary struct {
	Mode       string              `json:"mode"` // "kubernetes" | "push"
	Namespace  string              `json:"namespace,omitempty"`
	Services   []DiscoveredService `json:"services"`
	LastChange time.Time           `json:"last_change"`
}

// DiscoveredService is one service FARP knows of or should.
type DiscoveredService struct {
	Service   string            `json:"service"`
	OptedIn   bool              `json:"opted_in"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	State     string            `json:"state"` // "registered" | "waiting" | "failed" | "discovered"
	Routes    int               `json:"routes"`
	Error     string            `json:"error,omitempty"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// RouteResult is a saved route and how pushing it went.
type RouteResult struct {
	Route *Route     `json:"route"`
	Sync  SyncResult `json:"sync"`
}

// BoardHostname is an attached hostname and its domain states.
type BoardHostname struct {
	ServiceHostname
	Status *cloudinfra.RouteStatus
}

// LiveState is what Octopus reports.
type LiveState struct {
	Info         SystemInfo
	Routes       []RouteInfo
	Upstreams    []UpstreamInfo
	FarpServices []FarpService
}

// BoardInput is everything a board is built from. Live is nil when
// Octopus isn't answering.
type BoardInput struct {
	Gateway   Gateway
	Bindings  []*planning.Binding
	Routes    []*Route
	Hostnames []BoardHostname
	Live      *LiveState
}

// BuildBoard merges the gateway's manual routes with what Octopus reports,
// by hostname. It is pure: the same input gives the same board. A manual
// route beats a FARP route on the same host and path, which is hidden
// until the manual route goes.
func BuildBoard(in BoardInput) *Board {
	g := in.Gateway
	env := newBoardEnv(in)
	b := &Board{
		Gateway:    octopusStatus(g, in.Bindings, infoOf(in.Live)),
		Discovery:  buildDiscovery(g, in.Live, env),
		Unrouted:   []ServicePort{},
		Unroutable: []UnroutableService{},
		Live:       in.Live != nil,
	}
	if in.Live == nil {
		b.LiveError = msgBoardUnreachable
	}

	cards := newCardSet(in.Hostnames, g.FarpHostnames)
	manual := map[string]bool{} // "host path" of each manual route
	for _, r := range in.Routes {
		if r == nil {
			continue
		}
		c := cards.add(r.Hostname)
		c.Routes = append(c.Routes, manualRow(g, r))
		manual[r.Hostname+" "+r.Path] = true
	}
	overridden := map[string]bool{}
	if in.Live != nil {
		for _, lr := range groupBoardRoutes(in.Live.Routes) {
			source := lr.source()
			if source == "" {
				continue
			}
			for _, host := range slices.Sorted(maps.Keys(lr.hosts)) {
				if source == SourceFarp && manual[host+" "+lr.path] {
					overridden[host+" "+lr.path] = true
					continue
				}
				c := cards.add(host)
				c.Routes = append(c.Routes, env.liveRow(lr, host, source))
			}
		}
	}

	targeted := map[string]bool{}
	for _, c := range cards.ordered() {
		for i := range c.Routes {
			r := &c.Routes[i]
			if r.Source == SourceHere && overridden[c.Hostname+" "+r.Path] {
				r.Overrides = true
			}
			targeted[r.Service] = true
			b.Counts.All++
			switch r.Source {
			case SourceFarp:
				b.Counts.Farp++
			case SourceHere:
				b.Counts.Here++
			}
		}
		slices.SortStableFunc(c.Routes, func(x, y BoardRoute) int {
			return cmp.Or(strings.Compare(x.Path, y.Path), cmp.Compare(sourceRank(x.Source), sourceRank(y.Source)), strings.Compare(x.Key, y.Key))
		})
		b.Hosts = append(b.Hosts, *c)
	}
	if b.Hosts == nil {
		b.Hosts = []HostCard{}
	}

	connection, found := gatewayConnection(in.Bindings, g.Resource)
	for _, name := range slices.Sorted(maps.Keys(env.services)) {
		svc := env.services[name]
		switch {
		case svc.cfg.Port <= 0 || !httpWorkload(svc.cfg.Workload):
			b.Unroutable = append(b.Unroutable, UnroutableService{Service: name, Reason: ReasonNoPort})
		case found && svc.binding.ConnectionID != connection:
			b.Unroutable = append(b.Unroutable, UnroutableService{Service: name, Reason: ReasonOtherTarget})
		case !targeted[name]:
			b.Unrouted = append(b.Unrouted, ServicePort{Service: name, Port: svc.cfg.Port})
		}
	}
	return b
}

func infoOf(live *LiveState) *SystemInfo {
	if live == nil {
		return nil
	}
	return &live.Info
}

// httpWorkload reports whether a workload serves HTTP. An unset workload
// is a service that does.
func httpWorkload(w provider.Workload) bool {
	return w != provider.Worker && w != provider.StaticSite
}

func sourceRank(source string) int {
	switch source {
	case SourceHere:
		return 0
	case SourceFarp:
		return 1
	}
	return 2
}

// manualRow is a manual route's row. Its sync state is the route's own
// error from the last sync, else whether that sync applied the generation
// the route is in.
func manualRow(g Gateway, r *Route) BoardRoute {
	note := r.Note
	if note == "" && r.StripPrefix && r.Prefix() != "/" {
		note = "Strips " + r.Prefix() + " before forwarding"
	}
	row := BoardRoute{
		Key: "here:" + r.ID, Source: SourceHere, RouteID: r.ID, Path: r.Path, Methods: nonNilStrings(r.Methods),
		Service: r.Service, Port: r.Port, Note: note, StripPrefix: r.StripPrefix, Revision: r.Revision,
	}
	switch msg, failed := g.State.RouteErrors[r.ID]; {
	case failed:
		row.Sync, row.SyncError = SyncFailed, msg
	case g.AppliedGeneration >= g.Generation:
		row.Sync = SyncApplied
	default:
		row.Sync = SyncPending
	}
	return row
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return slices.Clone(in)
}

// cardSet keeps a board's cards: the attached hostnames first, exact
// before wildcard, then hostnames routes name that aren't attached, then
// any host.
type cardSet struct {
	cards map[string]*HostCard
}

func newCardSet(attached []BoardHostname, farpHostnames []string) *cardSet {
	s := &cardSet{cards: map[string]*HostCard{}}
	names := make([]string, 0, len(attached))
	for _, h := range attached {
		names = append(names, h.Hostname)
	}
	farp := attachedFarpHostnames(farpHostnames, names)
	for _, h := range attached {
		name := strings.ToLower(h.Hostname)
		if name == "" || s.cards[name] != nil {
			continue
		}
		s.cards[name] = &HostCard{
			Hostname: name, Attached: true, DomainRouteID: h.RouteID, Status: h.Status,
			FarpHome: slices.Contains(farp, name), Routes: []BoardRoute{},
		}
	}
	return s
}

// add is the card for host, a detached one when it isn't attached.
func (s *cardSet) add(host string) *HostCard {
	host = strings.ToLower(host)
	if c := s.cards[host]; c != nil {
		return c
	}
	c := &HostCard{Hostname: host, Routes: []BoardRoute{}}
	s.cards[host] = c
	return c
}

func (s *cardSet) ordered() []*HostCard {
	out := slices.Collect(maps.Values(s.cards))
	rank := func(c *HostCard) int {
		switch {
		case c.Hostname == "":
			return 2
		case c.Attached:
			return 0
		}
		return 1
	}
	slices.SortFunc(out, func(a, b *HostCard) int {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), hostnameOrder(a.Hostname, b.Hostname))
	})
	return out
}

// hostnameOrder sorts exact hostnames before wildcards, then by name.
func hostnameOrder(a, b string) int {
	aw, bw := strings.HasPrefix(a, "*."), strings.HasPrefix(b, "*.")
	if aw != bw {
		if aw {
			return 1
		}
		return -1
	}
	return strings.Compare(a, b)
}

// boardEnv is what rows are resolved against: the environment's services,
// the names Octopus knows them by and the upstreams' ports.
type boardEnv struct {
	services map[string]*serviceTarget
	// names maps a workload name, such as a Kubernetes Service's, to the
	// manifest service it runs.
	names map[string]string
	ports map[string]int
}

func newBoardEnv(in BoardInput) *boardEnv {
	e := &boardEnv{services: map[string]*serviceTarget{}, names: map[string]string{}, ports: map[string]int{}}
	for _, b := range in.Bindings {
		if b == nil || b.Subject != planning.ServiceSubject {
			continue
		}
		var cfg planning.ServiceConfig
		if json.Unmarshal(b.Config, &cfg) != nil {
			continue
		}
		e.services[b.Name] = &serviceTarget{binding: b, cfg: cfg}
		if iid, err := id.Parse(b.ExternalID); err == nil {
			e.names[kubernetes.WorkloadName(iid)] = b.Name
		}
	}
	if in.Live != nil {
		for _, u := range in.Live.Upstreams {
			if len(u.Instances) > 0 {
				e.ports[u.Name] = u.Instances[0].Port
			}
		}
	}
	return e
}

// service is the manifest service an Octopus name stands for: FARP names
// an upstream after the service it registered, and Kubernetes discovery
// after the Service it found.
func (e *boardEnv) service(name string) string {
	if _, ok := e.services[name]; ok {
		return name
	}
	if svc, ok := e.names[name]; ok {
		return svc
	}
	return name
}

func (e *boardEnv) liveRow(lr *boardLiveRoute, host, source string) BoardRoute {
	svc := e.service(lr.upstream)
	row := BoardRoute{
		Key: source + ":" + lr.key + "@" + host, Source: source, Path: lr.path, Methods: lr.methodsOn(host),
		Service: svc, Port: e.ports[lr.upstream], StripPrefix: lr.strip,
	}
	if source == SourceAdmin {
		row.Note = noteAdminRoute
	} else {
		row.Note = e.schemaNote(svc)
	}
	return row
}

// schemaNote credits the one schema a service's FARP metadata names.
func (e *boardEnv) schemaNote(service string) string {
	svc := e.services[service]
	if svc == nil || svc.cfg.Farp == nil {
		return noteFarp
	}
	var schemas []string
	for _, key := range []string{"farp.openapi.path", "farp.asyncapi.path", "farp.graphql.path"} {
		if svc.cfg.Farp.Metadata[key] != "" {
			schemas = append(schemas, key)
		}
	}
	switch {
	case len(schemas) != 1:
		return noteFarp
	case schemas[0] == "farp.openapi.path":
		return noteOpenAPI
	case schemas[0] == "farp.asyncapi.path":
		return noteAsyncAPI
	}
	return noteFarp
}

// boardLiveRoute is a route Octopus reports, its entries grouped by route
// id: the methods it answers on each host, "" for any host.
type boardLiveRoute struct {
	key, path, upstream string
	origins             map[string]bool
	strip               bool
	hosts               map[string]map[string]bool
}

func groupBoardRoutes(infos []RouteInfo) []*boardLiveRoute {
	groups := map[string]*boardLiveRoute{}
	for _, info := range infos {
		key := info.ID
		if info.RouteID != nil && *info.RouteID != "" {
			key = *info.RouteID
		}
		lr := groups[key]
		if lr == nil {
			lr = &boardLiveRoute{key: key, path: info.Path, upstream: info.Upstream, origins: map[string]bool{}, hosts: map[string]map[string]bool{}}
			groups[key] = lr
		}
		host := ""
		if info.Host != nil {
			host = strings.ToLower(*info.Host)
		}
		if lr.hosts[host] == nil {
			lr.hosts[host] = map[string]bool{}
		}
		lr.hosts[host][strings.ToUpper(info.Method)] = true
		lr.origins[info.Origin] = true
		if info.StripPrefix != nil && *info.StripPrefix != "" {
			lr.strip = true
		}
	}
	out := make([]*boardLiveRoute, 0, len(groups))
	for _, key := range slices.Sorted(maps.Keys(groups)) {
		out = append(out, groups[key])
	}
	return out
}

// source is where the route comes from, or "" when it isn't a row of its
// own: Loomplane's "lp-" routes are shown as the manual routes they come
// from, whatever Octopus says their origin is.
func (lr *boardLiveRoute) source() string {
	switch {
	case strings.HasPrefix(lr.key, "lp-"):
		return ""
	case lr.origins[originFarp] || lr.origins[originKubernetes]:
		return SourceFarp
	case lr.origins[originAdmin]:
		return SourceAdmin
	}
	return ""
}

// methodsOn is the methods the route answers on host, empty for every one.
func (lr *boardLiveRoute) methodsOn(host string) []string {
	set := lr.hosts[host]
	out := []string{}
	for _, m := range methodOrder {
		if set[m] {
			out = append(out, m)
		}
	}
	if len(out) == len(methodOrder) {
		return []string{}
	}
	return out
}

// farpRoutes counts each service's FARP routes in what Octopus reports.
func (e *boardEnv) farpRoutes(live *LiveState) map[string]int {
	out := map[string]int{}
	if live == nil {
		return out
	}
	for _, lr := range groupBoardRoutes(live.Routes) {
		if lr.source() == SourceFarp {
			out[e.service(lr.upstream)]++
		}
	}
	return out
}

// gatewayStatus is g's status. info is what Octopus reported, nil when it
// didn't answer.
func octopusStatus(g Gateway, bindings []*planning.Binding, info *SystemInfo) OctopusStatus {
	st := OctopusStatus{
		Resource: g.Resource, StartTime: g.State.StartTime, Provider: g.Provider, Namespace: g.Namespace,
		FarpHostnames: nonNilStrings(g.FarpHostnames), Generation: g.Generation, AppliedGeneration: g.AppliedGeneration,
		Revision: g.Revision, SyncedAt: g.State.SyncedAt, SyncError: g.State.Error,
		Attach: AttachDefaults{
			EnvironmentID: g.EnvironmentID, Service: GatewayAction(g.Resource), Port: recipes.OctopusGatewayPort,
			BlockedPathPrefixes: slices.Clone(blockedPathPrefixes),
		},
	}
	if info != nil {
		st.Version = info.Version
		if info.StartTime != "" {
			st.StartTime = info.StartTime
		}
	}
	if b := gatewayBindingOf(bindings, g.Resource); b != nil {
		if address, err := ServiceAddress(g.Provider, g.Namespace, b); err == nil {
			st.PrivateURL = "http://" + address + ":" + strconv.Itoa(recipes.OctopusGatewayPort)
		}
		var cfg planning.ResourceConfig
		if json.Unmarshal(b.Config, &cfg) == nil && cfg.CloudPlacement != nil {
			st.Attach.CloudID = cfg.CloudPlacement.CloudID
		}
	}
	return st
}

func gatewayBindingOf(bindings []*planning.Binding, resource string) *planning.Binding {
	name := GatewayAction(resource)
	for _, b := range bindings {
		if b != nil && b.Subject == planning.ResourceSubject && b.Name == name {
			return b
		}
	}
	return nil
}

// buildDiscovery is what FARP found for g. On a server it reads the
// registrations the syncer keeps; on a cluster, the services Octopus
// reports.
func buildDiscovery(g Gateway, live *LiveState, env *boardEnv) DiscoverySummary {
	d := DiscoverySummary{Mode: DiscoveryPush, Services: []DiscoveredService{}}
	if g.Provider == ProviderKubernetes {
		d.Mode, d.Namespace = DiscoveryKubernetes, g.Namespace
	}
	routes := env.farpRoutes(live)
	found := map[string]FarpService{}
	if live != nil {
		for _, fs := range live.FarpServices {
			found[env.service(fs.Name)] = fs
		}
	}
	services := map[string]*DiscoveredService{}
	for name, svc := range env.services {
		if svc.cfg.Farp == nil || svc.cfg.Farp.Gateway != g.Resource {
			continue
		}
		services[name] = &DiscoveredService{Service: name, OptedIn: true, Metadata: maps.Clone(svc.cfg.Farp.Metadata), State: DiscoveryWaiting}
	}
	if d.Mode == DiscoveryPush {
		for name, reg := range g.State.Registrations {
			s := services[name]
			if s == nil {
				s = &DiscoveredService{Service: name}
				services[name] = s
			}
			switch {
			case reg.Error != "":
				s.State, s.Error = DiscoveryFailed, reg.Error
			case !reg.RegisteredAt.IsZero():
				s.State = DiscoveryRegistered
			default:
				s.State = DiscoveryWaiting
			}
			s.UpdatedAt = reg.RegisteredAt
		}
	} else {
		for name, fs := range found {
			s := services[name]
			if s == nil {
				s = &DiscoveredService{Service: name}
				services[name] = s
			}
			s.State = DiscoveryDiscovered
			s.UpdatedAt = fs.UpdatedAt
			if s.UpdatedAt.IsZero() {
				s.UpdatedAt = fs.RegisteredAt
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(services)) {
		s := services[name]
		s.Routes = routes[name]
		if s.UpdatedAt.After(d.LastChange) {
			d.LastChange = s.UpdatedAt
		}
		d.Services = append(d.Services, *s)
	}
	return d
}
