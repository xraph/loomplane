package planning

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/security"
)

type resourceRefresher interface {
	Sync(context.Context, security.Principal, string) (*providerresources.ProviderResourceSync, error)
	RefreshAll(context.Context) error
}
type resourceRefreshConnections interface {
	All(context.Context, string) ([]*connections.Connection, error)
}
type checkedResourceConnection struct {
	principal  security.Principal
	connection connections.Connection
}

type providerResourceRefresh struct {
	resources   resourceRefresher
	connections resourceRefreshConnections
	supports    func(string) bool
	warn        func(error)
	queue       chan checkedResourceConnection
	cancel      context.CancelFunc
	done        chan struct{}
}

func newProviderResourceRefresh(resources resourceRefresher, conns resourceRefreshConnections, supports func(string) bool, warn func(error)) *providerResourceRefresh {
	return &providerResourceRefresh{resources: resources, connections: conns, supports: supports, warn: warn, queue: make(chan checkedResourceConnection, 64)}
}

func (r *providerResourceRefresh) checked(_ context.Context, p security.Principal, c connections.Connection) {
	if p.OrgID == "" || p.UserID == "" || p.OrgID != c.OrgID || p.ReadOnly || p.KeyID != "" || c.Status != connections.StatusReady || c.Generation < 1 || !r.supports(c.Cloud) || connections.AccountIdentity(c.Cloud, c.Account, c.Details) == "" {
		return
	}
	select {
	case r.queue <- checkedResourceConnection{principal: p, connection: c}:
	default:
		r.warn(errors.New("initial resource sync queue is full; sync this account from Resources"))
	}
}

func (r *providerResourceRefresh) runChecked(ctx context.Context, event checkedResourceConnection) error {
	current, err := r.connections.All(ctx, event.principal.OrgID)
	if err != nil {
		return err
	}
	for _, c := range current {
		if c.ID != event.connection.ID {
			continue
		}
		if c.OrgID != event.principal.OrgID || c.Generation != event.connection.Generation || c.Cloud != event.connection.Cloud || c.Status != connections.StatusReady || connections.AccountIdentity(c.Cloud, c.Account, c.Details) != connections.AccountIdentity(event.connection.Cloud, event.connection.Account, event.connection.Details) {
			return nil
		}
		_, err := r.resources.Sync(ctx, event.principal, c.ID)
		return err
	}
	return nil
}

func (r *providerResourceRefresh) start(ctx context.Context) {
	if r.cancel != nil {
		return
	}
	ctx, r.cancel = context.WithCancel(context.WithoutCancel(ctx))
	r.done = make(chan struct{})
	go func() {
		defer close(r.done)
		timer := time.NewTimer(providerResourceRefreshInterval())
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-r.queue:
				if err := r.runChecked(ctx, event); err != nil && ctx.Err() == nil {
					r.warn(err)
				}
			case <-timer.C:
				if err := r.resources.RefreshAll(ctx); err != nil && ctx.Err() == nil {
					r.warn(err)
				}
				timer.Reset(providerResourceRefreshInterval())
			}
		}
	}()
}

func providerResourceRefreshInterval() time.Duration {
	jitter, err := rand.Int(rand.Reader, big.NewInt(int64(30*time.Second)))
	if err != nil {
		return 15 * time.Minute
	}
	return 15*time.Minute + time.Duration(jitter.Int64())
}

func (r *providerResourceRefresh) stop(ctx context.Context) error {
	if r.cancel == nil {
		return nil
	}
	r.cancel()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
