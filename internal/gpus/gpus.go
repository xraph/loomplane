// Package gpus answers where GPUs can run and what they cost per hour: on
// the servers and clusters an organization has connected, and on the GPU
// server sizes it could create in a connected provider account. The model
// registry reads it to say where a model fits.
package gpus

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// ErrInvalid is a query that asks for something no target could offer.
var ErrInvalid = errors.New("invalid GPU target query")

// Sources of a GPUTarget.
const (
	SourceConnection = "connection"
	SourceServerSize = "server_size"
)

// GPUTargetQuery filters GET /api/v1/gpu-targets.
type GPUTargetQuery struct {
	MinMemoryGiB int    `query:"min_memory_gib,omitempty"` // per GPU
	Count        int    `query:"count,omitempty"`          // GPUs on one server or node; default 1
	Kind         string `query:"kind,omitempty"`           // only this kind
}

// GPUTarget is one place N GPUs could run: a connected server or cluster,
// or a GPU server size Loomplane can create.
type GPUTarget struct {
	Source string `json:"source"` // "connection" or "server_size"
	// ConnectionID is the server or cluster; for server_size, the provider
	// account to create with.
	ConnectionID string               `json:"connection_id"`
	Name         string               `json:"name"`
	Cloud        string               `json:"cloud"`             // "server", "kubernetes" or "digitalocean"
	Regions      []string             `json:"regions,omitempty"` // server_size only
	SizeID       string               `json:"size_id,omitempty"` // server_size only
	Accelerator  provider.Accelerator `json:"accelerator"`       // Count is per server or node
	// Free is, on a server, how many GPUs of the kind no deployed workload
	// holds. On a cluster it's the most GPUs of the kind one node can
	// allocate (PerNode), not how many are free: Loomplane doesn't count
	// the cluster's other pods. It's 0 when Loomplane can't tell.
	Free             int     `json:"free"`
	Fits             bool    `json:"fits"`
	Code             string  `json:"code,omitempty"` // a GPUUnavailableError code when it doesn't fit
	Message          string  `json:"message,omitempty"`
	HourlyUSD        float64 `json:"hourly_usd"`
	HourlyPriceKnown bool    `json:"hourly_price_known"`
	PriceEstimate    bool    `json:"price_estimate"` // true for reference list prices (clusters)
}

// GPUTargetList is every target, and the provider accounts whose sizes
// couldn't be read.
type GPUTargetList struct {
	Targets     []GPUTarget          `json:"targets"`
	Unreachable []UnreachableAccount `json:"unreachable"`
}

// UnreachableAccount is a provider account whose GPU sizes couldn't be read.
type UnreachableAccount struct {
	ConnectionID string `json:"connection_id"`
	Name         string `json:"name"`
	Message      string `json:"message"`
}

// ConnectionLister lists an organization's stored connections, GPU details
// included. *connections.Service is the real one.
type ConnectionLister interface {
	List(ctx context.Context, p security.Principal) ([]*connections.Connection, error)
}

// GPUBindingReader sums the GPUs deployed services and components hold,
// by connection and kind. *planning.PgStore is the real one.
type GPUBindingReader interface {
	GPUBindings(ctx context.Context, org string) (map[string]map[string]int, error)
}

// ServerOptionsReader reads a provider account's server sizes, regions and
// images. The caller authorizes. *cloudinfra.Service is the real one.
type ServerOptionsReader interface {
	AccountServerOptions(ctx context.Context, org, connectionID string) (provider.ServerOptions, error)
}

// ServerReader lists the Cloud servers that run a connection.
// *cloudinfra.PgStore is the real one.
type ServerReader interface {
	RuntimeServers(ctx context.Context, org string) ([]*cloudinfra.Server, error)
}

// StoredConnections lists an organization's stored connections with no
// principal, for Loomplane's own background work. *connections.PgStore is
// the real one.
type StoredConnections interface {
	ListConnections(ctx context.Context, orgID string) ([]*connections.Connection, error)
}

// Authorizer decides whether a principal may act.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// Deps are what the service reads. Options and Servers may be nil: there
// are then no sizes to create and no server prices. Stored may be nil, and
// then ConnectedTargets has nothing to read.
type Deps struct {
	Connections ConnectionLister
	Stored      StoredConnections
	Bindings    GPUBindingReader
	Options     ServerOptionsReader
	Servers     ServerReader
	Authz       Authorizer
}

// Service lists GPU targets.
type Service struct {
	d Deps
}

// NewService returns the service.
func NewService(d Deps) *Service {
	return &Service{d: d}
}

// optionsTimeout bounds reading one provider account's sizes.
const optionsTimeout = 20 * time.Second

// Targets lists where q's GPUs could run. Listing connections needs
// cloud_account:read and their GPUs infrastructure:read, so it needs both.
// An account whose sizes can't be read is reported, not fatal.
func (s *Service) Targets(ctx context.Context, p security.Principal, q GPUTargetQuery) (*GPUTargetList, error) {
	if err := s.d.Authz.Require(ctx, p, security.InfrastructureRead, ""); err != nil {
		return nil, err
	}
	if err := s.d.Authz.Require(ctx, p, security.CloudAccountRead, ""); err != nil {
		return nil, err
	}
	if q.Count == 0 {
		q.Count = 1
	}
	if q.Count < 1 || q.Count > 8 {
		return nil, fmt.Errorf("%w: count is 1 to 8 GPUs on one server or node", ErrInvalid)
	}
	if q.MinMemoryGiB < 0 {
		return nil, fmt.Errorf("%w: min_memory_gib can't be negative", ErrInvalid)
	}
	conns, err := s.d.Connections.List(ctx, p)
	if err != nil {
		return nil, err
	}
	held, err := s.d.Bindings.GPUBindings(ctx, p.OrgID)
	if err != nil {
		return nil, fmt.Errorf("read the GPUs services hold: %w", err)
	}
	prices := map[string]*cloudinfra.Server{}
	if s.d.Servers != nil {
		servers, serr := s.d.Servers.RuntimeServers(ctx, p.OrgID)
		if serr != nil {
			return nil, fmt.Errorf("read server prices: %w", serr)
		}
		for _, v := range servers {
			if v.Ownership == "managed" && v.RuntimeConnectionID != "" {
				prices[v.RuntimeConnectionID] = v
			}
		}
	}
	out := &GPUTargetList{Targets: []GPUTarget{}, Unreachable: []UnreachableAccount{}}
	var accounts []*connections.Connection
	for _, c := range conns {
		switch c.Cloud {
		case connections.Server:
			out.Targets = append(out.Targets, serverRows(c, q, held[c.ID], prices[c.ID])...)
		case connections.Kubernetes:
			out.Targets = append(out.Targets, clusterRows(c, q)...)
		case connections.DigitalOcean:
			if c.Status == connections.StatusReady && s.d.Options != nil {
				accounts = append(accounts, c)
			}
		}
	}
	sizes, unreachable := s.sizes(ctx, p.OrgID, accounts, q)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	out.Targets = append(out.Targets, sizes...)
	out.Unreachable = append(out.Unreachable, unreachable...)
	slices.SortStableFunc(out.Targets, compareTargets)
	slices.SortFunc(out.Unreachable, func(a, b UnreachableAccount) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ConnectionID, b.ConnectionID))
	})
	return out, nil
}

// ConnectedTargets lists the rows of an organization's connected servers
// and clusters, each with its price per hour where Loomplane knows it,
// for one GPU. It's for Loomplane's own background work, such as adding
// up what a model costs each month: it checks no permission, so its
// caller decides what it may read, and it asks no provider anything, so
// it never lists server sizes.
func (s *Service) ConnectedTargets(ctx context.Context, orgID string) ([]GPUTarget, error) {
	if s.d.Stored == nil {
		return nil, errors.New("gpus: stored connections aren't configured")
	}
	conns, err := s.d.Stored.ListConnections(ctx, orgID)
	if err != nil {
		return nil, err
	}
	held, err := s.d.Bindings.GPUBindings(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("read the GPUs services hold: %w", err)
	}
	prices := map[string]*cloudinfra.Server{}
	if s.d.Servers != nil {
		servers, serr := s.d.Servers.RuntimeServers(ctx, orgID)
		if serr != nil {
			return nil, fmt.Errorf("read server prices: %w", serr)
		}
		for _, v := range servers {
			if v.Ownership == "managed" && v.RuntimeConnectionID != "" {
				prices[v.RuntimeConnectionID] = v
			}
		}
	}
	q := GPUTargetQuery{Count: 1}
	out := []GPUTarget{}
	for _, c := range conns {
		switch c.Cloud {
		case connections.Server:
			out = append(out, serverRows(c, q, held[c.ID], prices[c.ID])...)
		case connections.Kubernetes:
			out = append(out, clusterRows(c, q)...)
		}
	}
	slices.SortStableFunc(out, compareTargets)
	return out, nil
}

// compareTargets puts fitting rows first, then the cheapest, with unknown
// prices last, then by name.
func compareTargets(a, b GPUTarget) int {
	if a.Fits != b.Fits {
		if a.Fits {
			return -1
		}
		return 1
	}
	if a.HourlyPriceKnown != b.HourlyPriceKnown {
		if a.HourlyPriceKnown {
			return -1
		}
		return 1
	}
	return cmp.Or(
		cmp.Compare(a.HourlyUSD, b.HourlyUSD),
		cmp.Compare(a.Name, b.Name),
		cmp.Compare(a.ConnectionID, b.ConnectionID),
		cmp.Compare(a.SizeID, b.SizeID),
		cmp.Compare(a.Accelerator.Kind, b.Accelerator.Kind),
	)
}

// inventory is a connection's stored GPU inventory. A connection that
// reports no GPUs, or was never asked, has none, and isn't a target. One
// whose inventory was asked for but can't be read is unknown, which never
// fits.
func inventory(c *connections.Connection) (provider.GPUInventory, bool) {
	inv, ok := provider.ParseGPUInventory(c.Details)
	if !ok {
		status, asked := c.Details[provider.GPUDetailStatus]
		if !asked || status == provider.GPUStatusNone {
			return provider.GPUInventory{}, false
		}
		return provider.GPUInventory{Status: provider.GPUStatusUnknown}, true
	}
	if inv.Status == provider.GPUStatusNone && len(inv.Devices) == 0 && len(inv.Nodes) == 0 {
		return provider.GPUInventory{}, false
	}
	return inv, true
}

// connectionRow starts a connection's row.
func connectionRow(c *connections.Connection) GPUTarget {
	return GPUTarget{Source: SourceConnection, ConnectionID: c.ID, Name: c.Name, Cloud: c.Cloud}
}

// unready is the one row of a connection that can't take GPU work: it
// isn't ready, or its GPUs aren't. It says why, and never fits.
func unready(c *connections.Connection, inv provider.GPUInventory, accel provider.Accelerator) GPUTarget {
	row := connectionRow(c)
	row.Accelerator = accel
	row.Code = provider.GPUUnavailable
	switch {
	case c.Status != connections.StatusReady && c.Message != "":
		row.Message = "This connection isn't ready: " + c.Message
	case c.Status != connections.StatusReady:
		row.Message = "This connection isn't ready. Check it again before running GPU work on it."
	case inv.Message != "":
		row.Message = inv.Message
	default:
		row.Message = provider.GPUStatusMessage(inv.Status)
	}
	return row
}

// accelerator describes kind as one server or node carries it.
func accelerator(kind string, count, memoryGiB int) provider.Accelerator {
	a := provider.Accelerator{Kind: kind, Vendor: provider.VendorNVIDIA, Count: count, MemoryGiB: memoryGiB}
	if k, ok := provider.LookupAcceleratorKind(kind); ok {
		a.Vendor, a.MemoryGiB = k.Vendor, k.MemoryGiB
	} else if strings.HasPrefix(kind, provider.VendorAMD+"-") {
		a.Vendor = provider.VendorAMD
	}
	return a
}

// serverKinds counts a server's devices by kind, with the most memory a
// device of the kind reports, in kind order.
func serverKinds(devices []provider.GPUDevice) []provider.Accelerator {
	byKind := map[string]*provider.Accelerator{}
	for _, d := range devices {
		if d.Kind == "" {
			continue
		}
		a, ok := byKind[d.Kind]
		if !ok {
			a = &provider.Accelerator{Kind: d.Kind}
			byKind[d.Kind] = a
		}
		a.Count++
		a.MemoryGiB = max(a.MemoryGiB, d.MemoryMiB/1024)
	}
	out := make([]provider.Accelerator, 0, len(byKind))
	for _, a := range byKind {
		out = append(out, accelerator(a.Kind, a.Count, a.MemoryGiB))
	}
	slices.SortFunc(out, func(a, b provider.Accelerator) int { return cmp.Compare(a.Kind, b.Kind) })
	return out
}

// wanted reports whether a row of kind belongs in the list. A row whose
// kind Loomplane doesn't know is kept, so its reason shows.
func wanted(q GPUTargetQuery, kind string) bool {
	return q.Kind == "" || kind == "" || kind == q.Kind
}

// serverRows is a server's rows: one per GPU kind it has, or one saying
// why it can't take GPU work. held is what deployed services already hold
// there by kind; priced is the Cloud server that runs it, if any.
func serverRows(c *connections.Connection, q GPUTargetQuery, held map[string]int, priced *cloudinfra.Server) []GPUTarget {
	inv, ok := inventory(c)
	if !ok {
		return nil
	}
	price := func(row *GPUTarget) {
		if priced != nil && priced.HourlyPriceKnown {
			row.HourlyUSD, row.HourlyPriceKnown = priced.HourlyUSD, true
		}
	}
	kinds := serverKinds(inv.Devices)
	if c.Status != connections.StatusReady || inv.Status != provider.GPUStatusReady || len(kinds) == 0 {
		var first provider.Accelerator
		if len(kinds) > 0 {
			first = kinds[0]
		}
		if !wanted(q, first.Kind) {
			return nil
		}
		row := unready(c, inv, first)
		price(&row)
		return []GPUTarget{row}
	}
	var out []GPUTarget
	for _, a := range kinds {
		if !wanted(q, a.Kind) {
			continue
		}
		row := connectionRow(c)
		row.Accelerator = a
		row.Free = max(a.Count-held[a.Kind], 0)
		price(&row)
		fit(&row, inv, q, func() (string, string) {
			if row.Free >= q.Count {
				return "", ""
			}
			return provider.GPUCountUnavailable, fmt.Sprintf("%s has %d %s GPUs and %d are given to other services, so %d are free.",
				c.Name, a.Count, title(a.Kind), held[a.Kind], row.Free)
		})
		out = append(out, row)
	}
	return out
}

// clusterRows is a cluster's rows: one per GPU kind its labelled nodes
// have, or one saying why it can't take GPU work. A node group's price is
// a reference list price, so it's an estimate.
func clusterRows(c *connections.Connection, q GPUTargetQuery) []GPUTarget {
	inv, ok := inventory(c)
	if !ok {
		return nil
	}
	best := map[string]provider.GPUNodeGroup{}
	for _, g := range inv.Nodes {
		if g.Kind == "" {
			continue
		}
		cur, seen := best[g.Kind]
		labelled, curLabelled := len(g.NodeSelector) > 0, len(cur.NodeSelector) > 0
		if !seen || labelled && !curLabelled || labelled == curLabelled && g.PerNode > cur.PerNode {
			best[g.Kind] = g
		}
	}
	kinds := make([]string, 0, len(best))
	for k := range best {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	if c.Status != connections.StatusReady || inv.Status != provider.GPUStatusReady || len(kinds) == 0 {
		var first provider.Accelerator
		if len(kinds) > 0 {
			g := best[kinds[0]]
			first = accelerator(g.Kind, g.PerNode, g.MemoryGiB)
		}
		if !wanted(q, first.Kind) {
			return nil
		}
		return []GPUTarget{unready(c, inv, first)}
	}
	var out []GPUTarget
	for _, kind := range kinds {
		if !wanted(q, kind) {
			continue
		}
		g := best[kind]
		row := connectionRow(c)
		row.Accelerator = accelerator(kind, g.PerNode, g.MemoryGiB)
		// A cluster row's Free is per node, not what's free right now.
		row.Free = g.PerNode
		if g.HourlyPriceKnown {
			row.HourlyUSD, row.HourlyPriceKnown, row.PriceEstimate = g.HourlyUSD, true, true
		}
		fit(&row, inv, q, func() (string, string) { return "", "" })
		out = append(out, row)
	}
	return out
}

// fit settles whether row takes q: the inventory places it, each GPU has
// the memory asked for, and enough of them are free. The first reason it
// doesn't is the row's code and message.
func fit(row *GPUTarget, inv provider.GPUInventory, q GPUTargetQuery, free func() (string, string)) {
	_, err := inv.Place(provider.GPURequest{Kind: row.Accelerator.Kind, Count: q.Count})
	var unavailable *provider.GPUUnavailableError
	switch {
	case errors.As(err, &unavailable):
		row.Code, row.Message = unavailable.Code, unavailable.Message
	case err != nil:
		row.Code, row.Message = provider.GPUUnavailable, err.Error()
	case q.MinMemoryGiB > 0 && row.Accelerator.MemoryGiB == 0:
		row.Code = provider.GPUKindUnavailable
		row.Message = fmt.Sprintf("Loomplane doesn't know how much memory a %s GPU has, and you asked for at least %d GiB.", title(row.Accelerator.Kind), q.MinMemoryGiB)
	case row.Accelerator.MemoryGiB < q.MinMemoryGiB:
		row.Code = provider.GPUKindUnavailable
		row.Message = fmt.Sprintf("%s has %d GiB per GPU and you asked for at least %d.", title(row.Accelerator.Kind), row.Accelerator.MemoryGiB, q.MinMemoryGiB)
	default:
		row.Code, row.Message = free()
	}
	row.Fits = row.Code == ""
}

// title is a kind's name for people.
func title(kind string) string {
	if k, ok := provider.LookupAcceleratorKind(kind); ok {
		return k.Title
	}
	return kind
}

// sizes reads each account's GPU server sizes, side by side, and keeps the
// ones q fits on.
func (s *Service) sizes(ctx context.Context, org string, accounts []*connections.Connection, q GPUTargetQuery) ([]GPUTarget, []UnreachableAccount) {
	type result struct {
		options provider.ServerOptions
		err     error
	}
	results := make([]result, len(accounts))
	var wg sync.WaitGroup
	for i, c := range accounts {
		wg.Go(func() {
			actx, cancel := context.WithTimeout(ctx, optionsTimeout)
			defer cancel()
			options, err := s.d.Options.AccountServerOptions(actx, org, c.ID)
			results[i] = result{options, err}
		})
	}
	wg.Wait()
	var rows []GPUTarget
	var unreachable []UnreachableAccount
	for i, c := range accounts {
		if err := results[i].err; err != nil {
			unreachable = append(unreachable, UnreachableAccount{ConnectionID: c.ID, Name: c.Name, Message: reason(err)})
			continue
		}
		rows = append(rows, sizeRows(c, results[i].options, q)...)
	}
	return rows, unreachable
}

// sizeRows is the account's NVIDIA sizes with enough GPUs and memory, in
// the regions that offer an image with the driver.
func sizeRows(c *connections.Connection, options provider.ServerOptions, q GPUTargetQuery) []GPUTarget {
	var out []GPUTarget
	for _, size := range options.Sizes {
		a := size.Accelerator
		if a == nil || a.Vendor != provider.VendorNVIDIA || a.Count < q.Count || a.MemoryGiB < q.MinMemoryGiB || !wanted(q, a.Kind) || a.Kind == "" {
			continue
		}
		regions := gpuRegions(options, size)
		if len(regions) == 0 {
			continue
		}
		row := GPUTarget{
			Source: SourceServerSize, ConnectionID: c.ID, Name: size.ID, Cloud: c.Cloud, Regions: regions, SizeID: size.ID,
			Accelerator: *a, Free: a.Count, Fits: true,
		}
		if size.HourlyPriceKnown {
			row.HourlyUSD, row.HourlyPriceKnown = size.HourlyUSD, true
		}
		out = append(out, row)
	}
	return out
}

// gpuRegions is where size is sold beside a bootstrap image that ships its
// vendor's driver.
func gpuRegions(options provider.ServerOptions, size provider.ServerSize) []string {
	var out []string
	for _, region := range size.Regions {
		if slices.ContainsFunc(options.Images, func(v provider.ServerImage) bool {
			return v.Bootstrap && slices.Contains(v.Accelerators, size.Accelerator.Vendor) && slices.Contains(v.Regions, region)
		}) {
			out = append(out, region)
		}
	}
	return out
}

// reason is an account's failure in one line.
func reason(err error) string {
	line, _, _ := strings.Cut(strings.TrimSpace(err.Error()), "\n")
	const most = 240
	if r := []rune(line); len(r) > most {
		line = string(r[:most]) + "..."
	}
	return "Loomplane couldn't read this account's GPU sizes: " + line
}
