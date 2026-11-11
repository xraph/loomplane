package identity

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xraph/grove/observability"

	"github.com/xraph/loomplane/internal/currentauthority"
)

// prepareCurrentAuthority qualifies both handles after migrations and bootstrap.
// Unsupported profiles stay unavailable while identity uses the main store.
func (e *Extension) prepareCurrentAuthority(ctx context.Context) {
	config, err := pgxpool.ParseConfig(e.cfg.Database.URL)
	if err != nil {
		e.Logger().Warn("Current authority is unavailable")
		return
	}
	telemetry := observability.NewTelemetry("postgresql", observability.WithStatusCode(func(err error) string {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return pgErr.Code
		}
		return ""
	}), observability.WithBenign(func(err error) bool { return errors.Is(err, pgx.ErrNoRows) }))
	telemetry.SetConnection(config.ConnConfig.Database, config.ConnConfig.Host, int(config.ConnConfig.Port))
	telemetry.SetMetrics(e.App().Metrics())
	report, err := currentauthority.InstallBuiltInProfiles(ctx, e.authority, e.pg, e.warden, e.authz, e.cfg.Database.URL, e.cfg.Identity.AppID, e.cfg.Identity.RequireEmailVerification, telemetry, e.App().Metrics())
	if err != nil {
		e.Logger().Warn("Current authority qualification failed")
		return
	}
	if report.Ordinary != currentauthority.ProfileReady {
		e.Logger().Warn("Ordinary authority is unavailable")
	}
	if report.Fleet != currentauthority.ProfileReady {
		e.Logger().Warn("Fleet authority is unavailable")
	}
}
