package ops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"

	"github.com/xraph/authsome/rbac"
	"github.com/xraph/chronicle"

	"github.com/xraph/loomplane/internal/platformadmin"
)

// grantDeps is what grant-platform-admin opens. The tests swap it for
// fakes.
type grantDeps struct {
	open      func(ctx context.Context, dsn, appID string) (platformadmin.UserFinder, rbac.Store, func(), error)
	openAudit func(ctx context.Context, dsn string) (*chronicle.Chronicle, func(), error)
}

var liveGrant = grantDeps{open: platformadmin.Open, openAudit: platformadmin.OpenAudit}

// grantPlatformAdmin makes the user with email a platform owner, records
// the grant in the platform stream and tells out what it did. The audit
// trail is opened first, so a grant is never made where it can't be
// recorded.
func grantPlatformAdmin(ctx context.Context, s Settings, email string, out io.Writer, d grantDeps) error {
	trail, closeAudit, err := d.openAudit(ctx, s.DatabaseURL)
	if err != nil {
		return err
	}
	defer closeAudit()
	users, roles, closeDB, err := d.open(ctx, s.DatabaseURL, s.AuthAppID)
	if err != nil {
		return err
	}
	defer closeDB()
	userID, granted, err := platformadmin.Grant(ctx, users, roles, s.AuthAppID, email)
	if errors.Is(err, platformadmin.ErrNoUser) {
		return fmt.Errorf("no user with email %s", email)
	}
	if err != nil {
		return err
	}
	if !granted {
		_, _ = fmt.Fprintf(out, "%s is already a platform admin.\n", email)
		return nil
	}
	if err := platformadmin.RecordGrant(ctx, trail, s.AuthAppID, platformadmin.CLIActor, userID, operator()); err != nil {
		return fmt.Errorf("granted platform-owner to %s, but %w", email, err)
	}
	_, _ = fmt.Fprintf(out, "Granted platform-owner to %s.\n", email)
	return nil
}

// operator is who ran the command, as far as the machine can tell: the
// operating system account and the host.
func operator() map[string]string {
	meta := map[string]string{}
	if u, err := user.Current(); err == nil {
		meta["os_user"] = u.Username
	}
	if host, err := os.Hostname(); err == nil {
		meta["host"] = host
	}
	return meta
}
