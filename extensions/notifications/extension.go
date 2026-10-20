// Package notifications composes delivery before identity and exposes the
// authenticated notification API after identity has installed its guards.
package notifications

import (
	"context"
	"fmt"

	"github.com/xraph/chronicle"
	"github.com/xraph/forge"
	"github.com/xraph/grove"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/security"
)

const DeliveryName = "loomplane-notification-delivery"
const Name = "loomplane-notifications"

type DeliveryExtension struct {
	*forge.BaseExtension
	cfg config.Config
	svc *notifications.Service
}

func NewDelivery(cfg config.Config) *DeliveryExtension {
	return &DeliveryExtension{BaseExtension: forge.NewBaseExtension(DeliveryName, "0.1.0", "Account mail delivery"), cfg: cfg}
}
func (e *DeliveryExtension) Dependencies() []string { return []string{"grove"} }
func (e *DeliveryExtension) Register(app forge.App) error {
	if err := e.BaseExtension.Register(app); err != nil {
		return err
	}
	db, err := forge.InjectType[*grove.DB](app.Container())
	if err != nil {
		return err
	}
	e.svc, err = notifications.New(e.cfg, db)
	if err != nil {
		return err
	}
	return forge.ProvideValue(app.Container(), e.svc)
}
func (e *DeliveryExtension) Start(ctx context.Context) error {
	if err := e.svc.Prepare(ctx); err != nil {
		return fmt.Errorf("prepare account email: %w", err)
	}
	e.MarkStarted()
	return nil
}

type Extension struct {
	*forge.BaseExtension
	cfg config.Config
	svc *notifications.Service
}

func New(cfg config.Config) *Extension {
	return &Extension{BaseExtension: forge.NewBaseExtension(Name, "0.1.0", "Deployment notifications and signed webhooks"), cfg: cfg}
}
func (e *Extension) Dependencies() []string { return []string{identity.Name, DeliveryName} }
func (e *Extension) Register(app forge.App) error {
	if err := e.BaseExtension.Register(app); err != nil {
		return err
	}
	var err error
	c := app.Container()
	if e.svc, err = forge.InjectType[*notifications.Service](c); err != nil {
		return err
	}
	authz, err := forge.InjectType[*security.Authorizer](c)
	if err != nil {
		return err
	}
	e.svc.SetAuthorizer(authz)
	audit, err := forge.InjectType[*chronicle.Chronicle](c)
	if err != nil {
		return err
	}
	e.svc.SetAudit(func(ctx context.Context, p security.Principal, action, id string) {
		if auditErr := audit.Info(ctx, action, "notification", id).Category("notifications").AppID(e.cfg.Identity.AppID).TenantID(p.OrgID).UserID(p.UserID).Outcome("success").Record(); auditErr != nil {
			e.Logger().Error("record notification audit event", forge.F("action", action), forge.F("error", "audit storage unavailable"))
		}
	})
	guard, err := forge.InjectType[identity.Guard](c)
	if err != nil {
		return err
	}
	return e.routes(app.Router(), guard)
}
func (e *Extension) Start(ctx context.Context) error {
	if err := e.svc.Run(ctx); err != nil {
		return err
	}
	e.MarkStarted()
	return nil
}
func (e *Extension) Stop(ctx context.Context) error   { return e.svc.Shutdown(ctx) }
func (e *Extension) Health(ctx context.Context) error { return e.svc.Health(ctx) }
