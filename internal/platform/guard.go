package platform

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/ops"
)

// guardTimeout bounds the schema check at startup.
const guardTimeout = 30 * time.Second

// schemaGuard refuses to start against a database a newer Loomplane has
// migrated: this one would read and write a schema it doesn't know. It
// checks while registering, before the migrations run.
//
// In development it only warns. Branches share a local database, so one
// branch's migrations are routinely unknown to another's checkout.
type schemaGuard struct {
	*forge.BaseExtension
	dsn  string
	warn bool
}

func newSchemaGuard(cfg config.Config) *schemaGuard {
	return &schemaGuard{
		BaseExtension: forge.NewBaseExtension("loomplane-schema-guard", "0.1.0", "Refuses a database a newer Loomplane migrated"),
		dsn:           cfg.Database.URL,
		warn:          cfg.Environment == config.EnvDevelopment,
	}
}

// Register implements forge.Extension.
func (g *schemaGuard) Register(app forge.App) error {
	if err := g.BaseExtension.Register(app); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), guardTimeout)
	defer cancel()
	err := ops.CheckCompatible(ctx, g.dsn, migrations.All())
	if errors.Is(err, ops.ErrNewerSchema) && g.warn {
		g.Logger().Warn("starting anyway, because this is development", forge.F("error", err.Error()))
		return nil
	}
	return err
}

// Start implements forge.Extension.
func (g *schemaGuard) Start(context.Context) error {
	g.MarkStarted()
	return nil
}
