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
	a.printf("Saved reader credentials for %s to %s. Login expires %s. Cloud SQL still requires your own authorized connector identity.\n", id, path, credentials.ExpiresAt.UTC().Format("2006-01-02 15:04Z"))
	return nil
}
