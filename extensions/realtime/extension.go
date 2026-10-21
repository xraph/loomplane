package realtime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/config"
	store "github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/security"
)

const Name = "loomplane-realtime"

type Extension struct {
	*forge.BaseExtension
	cfg   config.Config
	store *store.PgStore
	bus   *store.Bus
	authz interface {
		Require(context.Context, security.Principal, security.Permission, string) error
	}
	verify identity.RevalidateStream
	guard  identity.Guard
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func New(cfg config.Config) *Extension {
	return &Extension{BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Live deployment state and output"), cfg: cfg}
}
func (e *Extension) Dependencies() []string { return []string{identity.Name} }

func (e *Extension) Register(app forge.App) error {
	if err := e.BaseExtension.Register(app); err != nil {
		return err
	}
	c := app.Container()
	db, err := forge.InjectType[*grove.DB](c)
	if err != nil {
		return err
	}
	e.store = store.NewPgStore(pgdriver.Unwrap(db))
	if e.authz, err = forge.InjectType[*security.Authorizer](c); err != nil {
		return err
	}
	if e.verify, err = forge.InjectType[identity.RevalidateStream](c); err != nil {
		return err
	}
	if e.guard, err = forge.InjectType[identity.Guard](c); err != nil {
		return err
	}
	id, err := typeid.Generate("replica")
	if err != nil {
		return err
	}
	if e.bus, err = store.NewBus(e.cfg.Database.URL, id.String()); err != nil {
		return err
	}
	e.bus.Warn = func(err error) { e.Logger().Warn("realtime wake-up publication failed", forge.F("error", err.Error())) }
	if err = forge.ProvideValue(c, store.WakeSource(e.bus.Watch)); err != nil {
		return fmt.Errorf("provide realtime wake source: %w", err)
	}
	if err = forge.ProvideValue(c, e.store); err != nil {
		return fmt.Errorf("provide realtime store: %w", err)
	}
	if err = forge.ProvideValue(c, store.SubscriptionCount(e.bus.Subscribers)); err != nil {
		return fmt.Errorf("provide realtime bus: %w", err)
	}
	return e.routes(app.Router())
}

func (e *Extension) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	if err := e.bus.Start(ctx); err != nil {
		cancel()
		return err
	}
	e.cancel = cancel
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := e.store.Prune(ctx, time.Now().Add(-store.Retention)); err != nil {
					e.Logger().Warn("realtime retention failed", forge.F("error", err.Error()))
				}
			}
		}
	}()
	e.MarkStarted()
	return nil
}

func (e *Extension) Stop(ctx context.Context) error {
	if e.cancel != nil {
		e.cancel()
		e.wg.Wait()
	}
	return e.bus.Stop(ctx)
}
