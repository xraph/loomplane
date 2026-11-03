// Package nodes is the Forge extension that serves machines joined to an
// organization: the nodes API, the join route, the WebSocket every node holds
// open, and the hop other replicas reach its sessions through.
package nodes

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	groveext "github.com/xraph/grove/extension"

	cloudinfraext "github.com/xraph/loomplane/extensions/cloudinfra"
	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/buildinfo"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// Name is the extension's name, for other extensions' Dependencies.
const Name = "loomplane-nodes"

// Extension serves nodes.
type Extension struct {
	*forge.BaseExtension
	cfg    config.Config
	hub    *nodes.Hub
	hubErr error
	svc    *nodes.Service
	minter JoinTokenMinter
	// drains drains the Cloud server a node was adopted as.
	drains ServerDrains

	guard identity.Guard

	// sums are the sha256 of each loomctl archive of this release, verified
	// against its signature; node.sh carries them. fetchSums is replaced in
	// tests.
	sums      atomic.Pointer[map[string]string]
	fetchSums func(ctx context.Context, version string) (map[string]string, error)

	// run ends when the extension stops, and with it every session.
	mu     sync.Mutex
	run    context.Context
	cancel context.CancelFunc
	swept  sync.WaitGroup
}

// New returns the extension and its hub. replica names this process in logs
// and in the sessions it holds (the platform passes the name it already
// gives Dispatch). A setting the hub cannot run with, such as an unreadable
// nodes.hop_ca_file, fails Register, never the platform's construction.
func New(cfg config.Config, replica string) *Extension {
	e := &Extension{
		BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Machines that join an organization"),
		cfg:           cfg,
	}
	hc, err := hubConfig(cfg, replica)
	e.hub, e.hubErr = nodes.NewHub(hc), err
	return e
}

// Hub is the hub that holds this replica's node sessions. Later extensions
// open streams to nodes through it.
func (e *Extension) Hub() *nodes.Hub { return e.hub }

// Dependencies implements forge.Extension. Cloud infrastructure registers
// first, so a node joined with a Cloud can be adopted into it.
func (e *Extension) Dependencies() []string {
	return []string{groveext.ExtensionName, identity.Name, cloudinfraext.Name}
}

// Register builds the service, binds the hub and mounts the routes. A hub
// that cannot be bound, for an empty audience or secret say, stops the
// platform from starting.
func (e *Extension) Register(fapp forge.App) error {
	if err := e.BaseExtension.Register(fapp); err != nil {
		return err
	}
	if e.hubErr != nil {
		return fmt.Errorf("nodes: %w", e.hubErr)
	}
	c := fapp.Container()
	db, err := forge.InjectType[*grove.DB](c)
	if err != nil {
		return fmt.Errorf("nodes needs the grove extension: %w", err)
	}
	authz, err := forge.InjectType[*security.Authorizer](c)
	if err != nil {
		return fmt.Errorf("nodes needs the identity extension: %w", err)
	}
	audit, err := forge.InjectType[*chronicle.Chronicle](c)
	if err != nil {
		return fmt.Errorf("nodes needs the audit trail: %w", err)
	}
	keys, err := forge.InjectType[*apikeys.Service](c)
	if err != nil {
		return fmt.Errorf("nodes needs the identity extension's API keys: %w", err)
	}
	e.guard, err = forge.InjectType[identity.Guard](c)
	if err != nil {
		return fmt.Errorf("nodes needs the identity extension: %w", err)
	}
	infra, err := forge.InjectType[*cloudinfra.Service](c)
	if err != nil {
		return fmt.Errorf("nodes needs cloud infrastructure: %w", err)
	}
	store := nodes.NewPgStore(pgdriver.Unwrap(db))
	e.minter = keys
	e.svc = &nodes.Service{
		Store: store, Tokens: keys, Authz: authz, Version: buildinfo.Version,
		Audit: auditor{chronicle: audit, appID: e.cfg.Identity.AppID, log: e.Logger()},
		// A join token scoped to a Cloud is the approval to adopt the node
		// into it; the first connect does the adopting.
		Adopt: infra.RecordNodeAdoption,
	}
	// An adopted node's address check settles its ingress through the
	// service, which sends the mode the way an update does.
	infra.NodeIngress = e.svc
	// A node whose address changes is followed from the node as stored.
	infra.NodeRecords = store
	e.adoptOnConnect(infra)
	e.followAddressChanges(infra)
	e.serveNodeServers(infra, store)
	// Bind makes the hub the service's notifier and refuses an empty
	// audience or secret.
	if err = e.hub.Bind(store, e.svc); err != nil {
		return fmt.Errorf("nodes: %w", err)
	}
	if err = forge.ProvideValue(c, e.svc); err != nil {
		return fmt.Errorf("provide the nodes service: %w", err)
	}
	if err = forge.ProvideValue(c, e.hub); err != nil {
		return fmt.Errorf("provide the nodes hub: %w", err)
	}
	if routeErr := e.routes(fapp.Router(), e.guard...); routeErr != nil {
		return routeErr
	}
	return e.mountNodeEndpoints(fapp.Router())
}

// adoptOnConnect adopts a node waiting for its Cloud when it comes online,
// and again whenever its inventory changes, since Docker may only start
// answering after the first connect. The hub runs each hook off its own
// goroutines with a deadline and recovers a panic.
func (e *Extension) adoptOnConnect(infra *cloudinfra.Service) {
	adopt := func(ctx context.Context, n *nodes.Node) {
		if err := infra.AdoptOnline(ctx, n); err != nil {
			e.Logger().Warn("Adopt a node into its Cloud; the next connect tries again", forge.F("node", n.ID), forge.F("error", err.Error()))
		}
	}
	e.hub.OnOnline(adopt)
	e.hub.OnInventory(func(ctx context.Context, n *nodes.Node, _, _ nodeproto.Inventory) { adopt(ctx, n) })
}

// followAddressChanges follows a node whose public addresses change to its
// new address, and a node that comes back online, which may have moved
// while away. The follow outlasts the hook's deadline, so it gets its own.
// What a follow leaves unfinished, cloud infrastructure's reconcile picks up.
func (e *Extension) followAddressChanges(infra *cloudinfra.Service) {
	follow := func(ctx context.Context, n *nodes.Node) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cloudinfra.NodeAddressFollowWait)
		defer cancel()
		if err := infra.FollowNodeAddress(ctx, n); err != nil {
			e.Logger().Warn("Follow a node to its new address; the reconcile tries again", forge.F("node", n.ID), forge.F("error", err.Error()))
		}
	}
	e.hub.OnOnline(follow)
	e.hub.OnInventory(func(ctx context.Context, n *nodes.Node, old, cur nodeproto.Inventory) {
		if !slices.Equal(old.PublicAddresses, cur.PublicAddresses) {
			follow(ctx, n)
		}
	})
}

// Start runs the hub's sweep, which marks silent nodes offline.
func (e *Extension) Start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.run, e.cancel = ctx, cancel
	e.mu.Unlock()
	e.swept.Add(1)
	go func() {
		defer e.swept.Done()
		e.hub.Sweep(ctx)
	}()
	e.swept.Add(1)
	go func() {
		defer e.swept.Done()
		e.keepSums(ctx)
	}()
	e.MarkStarted()
	return nil
}

// Stop ends the sweep and closes the node sessions this replica holds: their
// contexts end with the extension's.
func (e *Extension) Stop(ctx context.Context) error {
	e.mu.Lock()
	cancel := e.cancel
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() { e.swept.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	e.MarkStopped()
	return nil
}
