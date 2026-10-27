package ctl

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/xraph/forge/cli"
)

func (a *App) nativeCopyCredentials(ctx cli.CommandContext) (result error) {
	id, path := ctx.Arg(0), ctx.String("out")
	if id == "" || path == "" {
		return fmt.Errorf("provide a copy ID and --out for a new private credential file")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, file.Close())
		if result != nil {
			_ = os.Remove(path)
		}
	}()
	c, err := a.client()
	if err != nil {
		return err
	}
	credentials, err := c.NativeRecoveryCopyCredentials(ctxOf(ctx), id)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if writeErr := encoder.Encode(credentials); writeErr != nil { // #nosec G117 -- Explicit disclosure to the new owner-only file requested by the caller.
		return writeErr
	}
	a.printf("Saved reader credentials for %s to %s. Login expires %s.\n", id, path, credentials.ExpiresAt.UTC().Format("2006-01-02 15:04Z"))
	switch {
	case credentials.PrivateServiceConnect:
		a.printf("Use your own authorized Cloud SQL connector identity with PSC selected (Cloud SQL Auth Proxy --psc) from the reviewed recovery network.\n")
	case credentials.Provider == "cloud-sql":
		a.printf("Cloud SQL requires your own authorized connector identity.\n")
	case credentials.Provider == "rds":
		a.printf("Connect over the reviewed private route with RDS CA verification.\n")
	}
	return nil
}
